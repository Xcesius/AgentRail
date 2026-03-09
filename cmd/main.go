package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	execmod "agentrail/internal/exec"
	filesmod "agentrail/internal/files"
	patchmod "agentrail/internal/patch"
	"agentrail/internal/protocol"
	readmod "agentrail/internal/read"
	searchmod "agentrail/internal/search"
	"agentrail/internal/workspace"
	writemod "agentrail/internal/write"
)

const maxStdinBytes = 64 * 1024 * 1024

type globalOptions struct {
	ForceJSON    bool
	AllowOutside bool
	Args         []string
}

type filesCLIOptions struct {
	Limit  int
	Cursor string
}

type patchCLIOptions struct {
	Atomic bool
}

func main() {
	manager, err := workspace.NewManager()
	if err != nil {
		payload := protocol.GetErrorPayload(err, protocol.CodeWorkspaceRequired)
		writeAndExit(protocol.FailureWithDetails("init", payload.Code, payload.Message, payload.Details, nil))
		return
	}
	if warning := manager.WarningMessage(); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	resp := run(manager, os.Args[1:])
	writeAndExit(resp)
}

func run(manager *workspace.Manager, args []string) map[string]any {
	globals, err := parseGlobalFlags(args)
	if err != nil {
		return failure("cli", err, nil)
	}

	if globals.ForceJSON {
		if len(globals.Args) != 0 {
			return protocol.FailureWithDetails("json", protocol.CodeInvalidRequest, "--json does not accept CLI subcommands", protocol.ErrorDetails{"field": "--json", "reason": "unexpected_cli_args"}, nil)
		}
		payload, readErr := readStdin(true)
		if readErr != nil {
			return failure("json", readErr, nil)
		}
		return handleJSON(manager, globals.AllowOutside, payload)
	}

	if len(globals.Args) == 0 {
		if !stdinHasData() {
			return protocol.FailureWithDetails("cli", protocol.CodeInvalidRequest, "missing command", protocol.ErrorDetails{"field": "action", "reason": "required"}, nil)
		}
		payload, readErr := readStdin(true)
		if readErr != nil {
			return failure("json", readErr, nil)
		}
		return handleJSON(manager, globals.AllowOutside, payload)
	}

	return handleCLI(manager, globals)
}

func handleJSON(manager *workspace.Manager, allowOutsideFlag bool, payload []byte) map[string]any {
	req, err := protocol.ParseRequest(payload)
	if err != nil {
		return failure("json", err, nil)
	}

	requestID := strings.TrimSpace(req.RequestID)
	respond := func(resp map[string]any) map[string]any {
		if requestID != "" {
			resp["request_id"] = requestID
		}
		return resp
	}

	action := strings.ToLower(req.Action)
	allowOutside := allowOutsideFlag || req.AllowOutsideWorkspace

	switch action {
	case "schema":
		return respond(schemaResponse(req.Target))
	case "files":
		base := req.Path
		if base == "" {
			base = "."
		}
		root, resolveErr := manager.ResolveDirPath(base, allowOutside)
		if resolveErr != nil {
			return respond(failure(action, resolveErr, nil))
		}
		page, listErr := filesmod.ListFilesPage(root, manager, req.Limit, req.Cursor)
		if listErr != nil {
			return respond(failure(action, listErr, nil))
		}
		return respond(protocol.Success(action, map[string]any{"paths": page.Paths, "has_more": page.HasMore, "next_cursor": page.NextCursor}))
	case "search":
		base := req.Path
		if base == "" {
			base = "."
		}
		root, resolveErr := manager.ResolveDirPath(base, allowOutside)
		if resolveErr != nil {
			return respond(failure(action, resolveErr, nil))
		}
		deterministic := true
		if req.Deterministic != nil {
			deterministic = *req.Deterministic
		}
		matches, searchErr := searchmod.Search(context.Background(), manager, searchmod.Options{
			Query:         req.Query,
			Root:          root,
			CaseSensitive: req.CaseSensitive,
			Regex:         req.Regex,
			Glob:          req.Glob,
			Limit:         req.Limit,
			MaxFileBytes:  req.MaxFileBytes,
			Deterministic: deterministic,
		})
		if searchErr != nil {
			return respond(failure(action, searchErr, nil))
		}
		return respond(protocol.Success(action, map[string]any{"matches": matches}))
	case "read":
		if strings.TrimSpace(req.Path) == "" {
			return respond(protocol.FailureWithDetails(action, protocol.CodeInvalidRequest, "path is required", protocol.ErrorDetails{"field": "path", "reason": "required"}, nil))
		}
		resolved, resolveErr := manager.ResolveReadPath(req.Path, allowOutside)
		if resolveErr != nil {
			return respond(failure(action, resolveErr, nil))
		}
		result, readErr := readmod.ReadFile(resolved, readmod.Options{
			DisplayPath: manager.DisplayPath(resolved),
			StartLine:   req.StartLine,
			EndLine:     req.EndLine,
			MaxBytes:    req.MaxBytes,
		})
		if readErr != nil {
			return respond(failure(action, readErr, nil))
		}
		fields := map[string]any{
			"path":            manager.DisplayPath(resolved),
			"content":         result.Content,
			"file_token":      result.FileToken,
			"start_line":      result.StartLine,
			"end_line":        result.EndLine,
			"truncated":       result.Truncated,
			"has_more":        result.HasMore,
			"next_start_line": result.NextStartLine,
		}
		return respond(protocol.Success(action, fields))
	case "write":
		if strings.TrimSpace(req.Path) == "" {
			return respond(protocol.FailureWithDetails(action, protocol.CodeInvalidRequest, "path is required", protocol.ErrorDetails{"field": "path", "reason": "required"}, nil))
		}
		if req.Content == nil {
			return respond(protocol.FailureWithDetails(action, protocol.CodeInvalidRequest, "content is required in JSON mode", protocol.ErrorDetails{"field": "content", "reason": "required"}, nil))
		}
		resolved, resolveErr := manager.ResolveWritePath(req.Path)
		if resolveErr != nil {
			return respond(failure(action, resolveErr, nil))
		}
		written, writeErr := writemod.WriteFileAtomic(resolved, []byte(*req.Content), req.CreateDirs)
		if writeErr != nil {
			return respond(failure(action, writeErr, nil))
		}
		return respond(protocol.Success(action, map[string]any{
			"path":          manager.DisplayPath(resolved),
			"bytes_written": written,
		}))
	case "patch":
		if req.Diff == nil {
			return respond(protocol.FailureWithDetails(action, protocol.CodeInvalidRequest, "diff is required in JSON mode", protocol.ErrorDetails{"field": "diff", "reason": "required"}, patchBaseFields()))
		}
		applyResult, patchErr := patchmod.Apply(manager, *req.Diff, patchmod.Options{Atomic: req.Atomic, ExpectedFileTokens: req.ExpectedFileTokens})
		fields := map[string]any{
			"repository_state": applyResult.RepositoryState,
			"files_changed":    applyResult.FilesChanged,
			"hunks_applied":    applyResult.HunksApplied,
			"results":          applyResult.Results,
		}
		if patchErr != nil {
			return respond(failure(action, patchErr, fields))
		}
		return respond(protocol.Success(action, fields))
	case "exec":
		cwd, cwdErr := manager.ResolveExecCWD(req.CWD)
		if cwdErr != nil {
			return respond(failure(action, cwdErr, nil))
		}
		maxOutputBytes, outputErr := resolveMaxOutputBytes(req.MaxOutputBytes)
		if outputErr != nil {
			return respond(failure(action, outputErr, nil))
		}
		res, execErr := execmod.Run(execmod.Options{
			Argv:           req.Argv,
			CWD:            cwd,
			Env:            req.Env,
			TimeoutMS:      req.TimeoutMS,
			MaxOutputBytes: maxOutputBytes,
		})
		fields := map[string]any{
			"exit_code":        res.ExitCode,
			"stdout":           res.Stdout,
			"stderr":           res.Stderr,
			"stdout_truncated": res.StdoutTruncated,
			"stderr_truncated": res.StderrTruncated,
			"output_bytes":     res.OutputBytes,
			"timing_ms":        res.TimingMS,
		}
		if execErr != nil {
			return respond(failure(action, execErr, fields))
		}
		return respond(protocol.Success(action, fields))
	default:
		return respond(protocol.FailureWithDetails(action, protocol.CodeInvalidRequest, "unknown action", protocol.ErrorDetails{"field": "action", "reason": "unknown_action"}, nil))
	}
}

func handleCLI(manager *workspace.Manager, globals globalOptions) map[string]any {
	cmd := strings.ToLower(globals.Args[0])
	args := globals.Args[1:]

	switch cmd {
	case "schema":
		if len(args) != 1 {
			return protocol.FailureWithDetails(cmd, protocol.CodeInvalidRequest, "schema requires <target>", protocol.ErrorDetails{"field": "target", "reason": "required"}, nil)
		}
		return schemaResponse(args[0])
	case "files":
		options, parseErr := parseFilesCLIArgs(args)
		if parseErr != nil {
			return failure(cmd, parseErr, nil)
		}
		page, err := filesmod.ListFilesPage(manager.Root, manager, options.Limit, options.Cursor)
		if err != nil {
			return failure(cmd, err, nil)
		}
		return protocol.Success(cmd, map[string]any{"paths": page.Paths, "has_more": page.HasMore, "next_cursor": page.NextCursor})
	case "search":
		if len(args) < 1 {
			return protocol.FailureWithDetails(cmd, protocol.CodeInvalidRequest, "search requires <query>", protocol.ErrorDetails{"field": "query", "reason": "required"}, nil)
		}
		matches, err := searchmod.Search(context.Background(), manager, searchmod.Options{
			Query:         args[0],
			Root:          manager.Root,
			CaseSensitive: false,
			Regex:         false,
			Deterministic: true,
		})
		if err != nil {
			return failure(cmd, err, nil)
		}
		return protocol.Success(cmd, map[string]any{"matches": matches})
	case "read":
		if len(args) != 1 {
			return protocol.FailureWithDetails(cmd, protocol.CodeInvalidRequest, "read requires <path>", protocol.ErrorDetails{"field": "path", "reason": "required"}, nil)
		}
		resolved, err := manager.ResolveReadPath(args[0], globals.AllowOutside)
		if err != nil {
			return failure(cmd, err, nil)
		}
		result, readErr := readmod.ReadFile(resolved, readmod.Options{DisplayPath: manager.DisplayPath(resolved)})
		if readErr != nil {
			return failure(cmd, readErr, nil)
		}
		return protocol.Success(cmd, map[string]any{
			"path":            manager.DisplayPath(resolved),
			"content":         result.Content,
			"file_token":      result.FileToken,
			"start_line":      result.StartLine,
			"end_line":        result.EndLine,
			"truncated":       result.Truncated,
			"has_more":        result.HasMore,
			"next_start_line": result.NextStartLine,
		})
	case "write":
		if len(args) != 1 {
			return protocol.FailureWithDetails(cmd, protocol.CodeInvalidRequest, "write requires <path>", protocol.ErrorDetails{"field": "path", "reason": "required"}, nil)
		}
		content, readErr := readStdin(true)
		if readErr != nil {
			return failure(cmd, readErr, nil)
		}
		resolved, err := manager.ResolveWritePath(args[0])
		if err != nil {
			return failure(cmd, err, nil)
		}
		written, writeErr := writemod.WriteFileAtomic(resolved, content, false)
		if writeErr != nil {
			return failure(cmd, writeErr, nil)
		}
		return protocol.Success(cmd, map[string]any{
			"path":          manager.DisplayPath(resolved),
			"bytes_written": written,
		})
	case "patch":
		patchOpts, parseErr := parsePatchCLIArgs(args)
		if parseErr != nil {
			return failure(cmd, parseErr, patchBaseFields())
		}
		diff, readErr := readStdin(true)
		if readErr != nil {
			return failure(cmd, readErr, patchBaseFields())
		}
		applyResult, patchErr := patchmod.Apply(manager, string(diff), patchmod.Options{Atomic: patchOpts.Atomic})
		fields := map[string]any{
			"repository_state": applyResult.RepositoryState,
			"files_changed":    applyResult.FilesChanged,
			"hunks_applied":    applyResult.HunksApplied,
			"results":          applyResult.Results,
		}
		if patchErr != nil {
			return failure(cmd, patchErr, fields)
		}
		return protocol.Success(cmd, fields)
	case "exec":
		execOpts, parseErr := parseExecCLIArgs(args)
		if parseErr != nil {
			return failure(cmd, parseErr, nil)
		}
		cwd, cwdErr := manager.ResolveExecCWD(execOpts.CWD)
		if cwdErr != nil {
			return failure(cmd, cwdErr, nil)
		}
		execOpts.CWD = cwd
		result, runErr := execmod.Run(execOpts)
		fields := map[string]any{
			"exit_code":        result.ExitCode,
			"stdout":           result.Stdout,
			"stderr":           result.Stderr,
			"stdout_truncated": result.StdoutTruncated,
			"stderr_truncated": result.StderrTruncated,
			"output_bytes":     result.OutputBytes,
			"timing_ms":        result.TimingMS,
		}
		if runErr != nil {
			return failure(cmd, runErr, fields)
		}
		return protocol.Success(cmd, fields)
	default:
		return protocol.FailureWithDetails("cli", protocol.CodeInvalidRequest, "unknown command", protocol.ErrorDetails{"field": "action", "reason": "unknown_command"}, nil)
	}
}

func parseGlobalFlags(args []string) (globalOptions, error) {
	out := globalOptions{Args: args}
	remaining := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			out.ForceJSON = true
			continue
		}
		if a == "--allow-outside-workspace" {
			out.AllowOutside = true
			continue
		}
		remaining = append(remaining, args[i:]...)
		break
	}
	if len(remaining) == 0 && len(args) > 0 {
		remaining = []string{}
	}
	out.Args = remaining
	return out, nil
}

func parseFilesCLIArgs(args []string) (filesCLIOptions, error) {
	var options filesCLIOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			i++
			if i >= len(args) {
				return filesCLIOptions{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "--limit requires a value", protocol.ErrorDetails{"field": "limit", "reason": "required"})
			}
			value, err := strconv.Atoi(args[i])
			if err != nil {
				return filesCLIOptions{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid limit value", protocol.ErrorDetails{"field": "limit", "reason": "invalid_type"})
			}
			options.Limit = value
		case "--cursor":
			i++
			if i >= len(args) {
				return filesCLIOptions{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "--cursor requires a value", protocol.ErrorDetails{"field": "cursor", "reason": "required"})
			}
			options.Cursor = args[i]
		default:
			return filesCLIOptions{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "files does not accept positional arguments", protocol.ErrorDetails{"field": "files", "reason": "unexpected_argument"})
		}
	}
	return options, nil
}

func parsePatchCLIArgs(args []string) (patchCLIOptions, error) {
	var options patchCLIOptions
	for _, arg := range args {
		if arg == "--atomic" {
			options.Atomic = true
			continue
		}
		return patchCLIOptions{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "patch does not accept positional arguments", protocol.ErrorDetails{"field": "patch", "reason": "unexpected_argument"})
	}
	return options, nil
}

func parseExecCLIArgs(args []string) (execmod.Options, error) {
	var options execmod.Options
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		switch a {
		case "--cwd":
			i++
			if i >= len(args) {
				return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "--cwd requires a value", protocol.ErrorDetails{"field": "cwd", "reason": "required"})
			}
			options.CWD = args[i]
		case "--timeout-ms":
			i++
			if i >= len(args) {
				return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "--timeout-ms requires a value", protocol.ErrorDetails{"field": "timeout_ms", "reason": "required"})
			}
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 0 {
				return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid timeout value", protocol.ErrorDetails{"field": "timeout_ms", "reason": "invalid_value"})
			}
			options.TimeoutMS = value
		case "--max-output-bytes":
			i++
			if i >= len(args) {
				return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "--max-output-bytes requires a value", protocol.ErrorDetails{"field": "max_output_bytes", "reason": "required"})
			}
			value, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil {
				return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid max_output_bytes", protocol.ErrorDetails{"field": "max_output_bytes", "reason": "invalid_type"})
			}
			if value <= 0 || value > execmod.HardMaxOutputBytes {
				return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid max_output_bytes", protocol.ErrorDetails{"field": "max_output_bytes", "reason": "invalid_value"})
			}
			options.MaxOutputBytes = value
		default:
			return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "exec arguments must follow --", protocol.ErrorDetails{"field": "argv", "reason": "missing_separator"})
		}
		i++
	}
	if i >= len(args) {
		return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "exec requires -- <argv...>", protocol.ErrorDetails{"field": "argv", "reason": "required"})
	}
	options.Argv = args[i:]
	if len(options.Argv) == 0 {
		return execmod.Options{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "exec argv must not be empty", protocol.ErrorDetails{"field": "argv", "reason": "required"})
	}
	return options, nil
}

func resolveMaxOutputBytes(raw *int64) (int64, error) {
	if raw == nil {
		return 0, nil
	}
	if *raw <= 0 || *raw > execmod.HardMaxOutputBytes {
		return 0, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid max_output_bytes", protocol.ErrorDetails{"field": "max_output_bytes", "reason": "invalid_value"})
	}
	return *raw, nil
}

func failure(action string, err error, fields map[string]any) map[string]any {
	payload := protocol.GetErrorPayload(err, protocol.CodeInvalidRequest)
	return protocol.FailureWithDetails(action, payload.Code, payload.Message, payload.Details, fields)
}

func patchBaseFields() map[string]any {
	return map[string]any{
		"repository_state": patchmod.RepositoryStateUnchanged,
		"files_changed":    []string{},
		"hunks_applied":    0,
		"results":          []patchmod.FileResult{},
	}
}

func schemaResponse(target string) map[string]any {
	normalized := strings.ToLower(strings.TrimSpace(target))
	if normalized == "" {
		return protocol.FailureWithDetails("schema", protocol.CodeInvalidRequest, "target is required", protocol.ErrorDetails{"field": "target", "reason": "required"}, nil)
	}

	switch normalized {
	case "patch":
		return protocol.Success("schema", patchSchemaFields())
	default:
		return protocol.FailureWithDetails("schema", protocol.CodeInvalidRequest, "unknown schema target", protocol.ErrorDetails{"field": "target", "reason": "unknown_target"}, nil)
	}
}

func patchSchemaFields() map[string]any {
	return map[string]any{
		"target": "patch",
		"request_schema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action", "diff"},
			"properties": map[string]any{
				"action": map[string]any{
					"type":  "string",
					"const": "patch",
				},
				"diff": map[string]any{
					"type":        "string",
					"format":      "unified_diff",
					"description": "Full unified diff including ---/+++ file headers before @@ hunks.",
				},
				"atomic": map[string]any{
					"type":    "boolean",
					"default": false,
				},
				"expected_file_tokens": map[string]any{
					"type": "object",
					"additionalProperties": map[string]any{
						"type":    "string",
						"pattern": "^sha256:[0-9a-f]{64}$",
					},
					"description": "Map of canonical workspace-relative paths to current file tokens.",
				},
			},
		},
		"examples": []map[string]any{
			{
				"description": "Single-file update",
				"request": map[string]any{
					"action": "patch",
					"diff": `--- a/sample.txt
+++ b/sample.txt
@@ -1,1 +1,1 @@
-old
+new
`,
				},
			},
			{
				"description": "Atomic multi-file update with file tokens",
				"request": map[string]any{
					"action": "patch",
					"atomic": true,
					"expected_file_tokens": map[string]any{
						"one.txt": "sha256:<64 lowercase hex>",
						"two.txt": "sha256:<64 lowercase hex>",
					},
					"diff": `--- a/one.txt
+++ b/one.txt
@@ -1,1 +1,1 @@
-old
+new
--- a/two.txt
+++ b/two.txt
@@ -1,1 +1,1 @@
-old
+new
`,
				},
			},
		},
		"notes": []string{
			"AgentRail JSON patch endpoint accepts unified diff text in diff only.",
			"The diff must include ---/+++ file headers before any @@ hunks.",
			"Fields such as mode, patch, old_string, and new_string are not part of the AgentRail CLI JSON contract.",
		},
	}
}

func readStdin(required bool) ([]byte, error) {
	if !stdinHasData() {
		if required {
			return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "stdin is required", protocol.ErrorDetails{"field": "stdin", "reason": "required"})
		}
		return nil, nil
	}
	reader := io.LimitReader(os.Stdin, maxStdinBytes)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to read stdin")
	}
	if len(data) >= maxStdinBytes {
		return nil, protocol.ErrDetails(protocol.CodeTooLarge, "stdin payload exceeds limit", protocol.ErrorDetails{"field": "stdin", "limit_bytes": maxStdinBytes, "actual_bytes": len(data)})
	}
	return data, nil
}

func stdinHasData() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) == 0
}

func writeAndExit(resp map[string]any) {
	if err := protocol.WriteJSON(os.Stdout, resp); err != nil {
		fmt.Fprintln(os.Stderr, "failed to encode response:", err)
		os.Exit(1)
	}
	ok, _ := resp["ok"].(bool)
	if !ok {
		os.Exit(1)
	}
}
