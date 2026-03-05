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

func main() {
	manager, err := workspace.NewManager()
	if err != nil {
		code, message := protocol.GetCodeAndMessage(err, protocol.CodeWorkspaceRequired)
		writeAndExit(protocol.Failure("init", code, message, nil))
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
		code, message := protocol.GetCodeAndMessage(err, protocol.CodeInvalidRequest)
		return protocol.Failure("cli", code, message, nil)
	}

	if globals.ForceJSON {
		if len(globals.Args) != 0 {
			return protocol.Failure("json", protocol.CodeInvalidRequest, "--json does not accept CLI subcommands", nil)
		}
		payload, readErr := readStdin(true)
		if readErr != nil {
			code, message := protocol.GetCodeAndMessage(readErr, protocol.CodeInvalidRequest)
			return protocol.Failure("json", code, message, nil)
		}
		return handleJSON(manager, globals.AllowOutside, payload)
	}

	if len(globals.Args) == 0 {
		if !stdinHasData() {
			return protocol.Failure("cli", protocol.CodeInvalidRequest, "missing command", nil)
		}
		payload, readErr := readStdin(true)
		if readErr != nil {
			code, message := protocol.GetCodeAndMessage(readErr, protocol.CodeInvalidRequest)
			return protocol.Failure("json", code, message, nil)
		}
		return handleJSON(manager, globals.AllowOutside, payload)
	}

	return handleCLI(manager, globals)
}

func handleJSON(manager *workspace.Manager, allowOutsideFlag bool, payload []byte) map[string]any {
	req, err := protocol.ParseRequest(payload)
	if err != nil {
		code, message := protocol.GetCodeAndMessage(err, protocol.CodeInvalidRequest)
		return protocol.Failure("json", code, message, nil)
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
	case "files":
		base := req.Path
		if base == "" {
			base = "."
		}
		root, resolveErr := manager.ResolveDirPath(base, allowOutside)
		if resolveErr != nil {
			return respond(failure(action, resolveErr, nil))
		}
		paths, listErr := filesmod.ListFiles(root, manager)
		if listErr != nil {
			return respond(failure(action, listErr, nil))
		}
		return respond(protocol.Success(action, map[string]any{"paths": paths}))
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
			return respond(protocol.Failure(action, protocol.CodeInvalidRequest, "path is required", nil))
		}
		resolved, resolveErr := manager.ResolveReadPath(req.Path, allowOutside)
		if resolveErr != nil {
			return respond(failure(action, resolveErr, nil))
		}
		result, readErr := readmod.ReadFile(resolved, readmod.Options{
			StartLine: req.StartLine,
			EndLine:   req.EndLine,
			MaxBytes:  req.MaxBytes,
		})
		if readErr != nil {
			return respond(failure(action, readErr, nil))
		}
		fields := map[string]any{
			"path":            manager.RelativePath(resolved),
			"content":         result.Content,
			"start_line":      result.StartLine,
			"end_line":        result.EndLine,
			"truncated":       result.Truncated,
			"has_more":        result.HasMore,
			"next_start_line": result.NextStartLine,
		}
		return respond(protocol.Success(action, fields))
	case "write":
		if strings.TrimSpace(req.Path) == "" {
			return respond(protocol.Failure(action, protocol.CodeInvalidRequest, "path is required", nil))
		}
		if req.Content == nil {
			return respond(protocol.Failure(action, protocol.CodeInvalidRequest, "content is required in JSON mode", nil))
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
			"path":          manager.RelativePath(resolved),
			"bytes_written": written,
		}))
	case "patch":
		if req.Diff == nil {
			return respond(protocol.Failure(action, protocol.CodeInvalidRequest, "diff is required in JSON mode", nil))
		}
		applyResult, patchErr := patchmod.Apply(manager, *req.Diff)
		fields := map[string]any{
			"files_changed": applyResult.FilesChanged,
			"hunks_applied": applyResult.HunksApplied,
			"results":       applyResult.Results,
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
		res, execErr := execmod.Run(execmod.Options{
			Argv:      req.Argv,
			CWD:       cwd,
			Env:       req.Env,
			TimeoutMS: req.TimeoutMS,
		})
		fields := map[string]any{
			"exit_code": res.ExitCode,
			"stdout":    res.Stdout,
			"stderr":    res.Stderr,
			"timing_ms": res.TimingMS,
		}
		if execErr != nil {
			return respond(failure(action, execErr, fields))
		}
		return respond(protocol.Success(action, fields))
	default:
		return respond(protocol.Failure(action, protocol.CodeInvalidRequest, "unknown action", nil))
	}
}

func handleCLI(manager *workspace.Manager, globals globalOptions) map[string]any {
	cmd := strings.ToLower(globals.Args[0])
	args := globals.Args[1:]

	switch cmd {
	case "files":
		if len(args) != 0 {
			return protocol.Failure(cmd, protocol.CodeInvalidRequest, "files does not accept positional arguments", nil)
		}
		paths, err := filesmod.ListFiles(manager.Root, manager)
		if err != nil {
			return failure(cmd, err, nil)
		}
		return protocol.Success(cmd, map[string]any{"paths": paths})
	case "search":
		if len(args) < 1 {
			return protocol.Failure(cmd, protocol.CodeInvalidRequest, "search requires <query>", nil)
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
			return protocol.Failure(cmd, protocol.CodeInvalidRequest, "read requires <path>", nil)
		}
		resolved, err := manager.ResolveReadPath(args[0], globals.AllowOutside)
		if err != nil {
			return failure(cmd, err, nil)
		}
		result, readErr := readmod.ReadFile(resolved, readmod.Options{})
		if readErr != nil {
			return failure(cmd, readErr, nil)
		}
		return protocol.Success(cmd, map[string]any{
			"path":            manager.RelativePath(resolved),
			"content":         result.Content,
			"start_line":      result.StartLine,
			"end_line":        result.EndLine,
			"truncated":       result.Truncated,
			"has_more":        result.HasMore,
			"next_start_line": result.NextStartLine,
		})
	case "write":
		if len(args) != 1 {
			return protocol.Failure(cmd, protocol.CodeInvalidRequest, "write requires <path>", nil)
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
			"path":          manager.RelativePath(resolved),
			"bytes_written": written,
		})
	case "patch":
		if len(args) != 0 {
			return protocol.Failure(cmd, protocol.CodeInvalidRequest, "patch does not accept positional arguments", nil)
		}
		diff, readErr := readStdin(true)
		if readErr != nil {
			return failure(cmd, readErr, nil)
		}
		applyResult, patchErr := patchmod.Apply(manager, string(diff))
		fields := map[string]any{
			"files_changed": applyResult.FilesChanged,
			"hunks_applied": applyResult.HunksApplied,
			"results":       applyResult.Results,
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
			"exit_code": result.ExitCode,
			"stdout":    result.Stdout,
			"stderr":    result.Stderr,
			"timing_ms": result.TimingMS,
		}
		if runErr != nil {
			return failure(cmd, runErr, fields)
		}
		return protocol.Success(cmd, fields)
	default:
		return protocol.Failure("cli", protocol.CodeInvalidRequest, "unknown command", nil)
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
				return execmod.Options{}, protocol.Err(protocol.CodeInvalidRequest, "--cwd requires a value")
			}
			options.CWD = args[i]
		case "--timeout-ms":
			i++
			if i >= len(args) {
				return execmod.Options{}, protocol.Err(protocol.CodeInvalidRequest, "--timeout-ms requires a value")
			}
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 0 {
				return execmod.Options{}, protocol.Err(protocol.CodeInvalidRequest, "invalid timeout value")
			}
			options.TimeoutMS = value
		default:
			return execmod.Options{}, protocol.Err(protocol.CodeInvalidRequest, "exec arguments must follow --")
		}
		i++
	}
	if i >= len(args) {
		return execmod.Options{}, protocol.Err(protocol.CodeInvalidRequest, "exec requires -- <argv...>")
	}
	options.Argv = args[i:]
	if len(options.Argv) == 0 {
		return execmod.Options{}, protocol.Err(protocol.CodeInvalidRequest, "exec argv must not be empty")
	}
	return options, nil
}

func failure(action string, err error, fields map[string]any) map[string]any {
	code, message := protocol.GetCodeAndMessage(err, protocol.CodeInvalidRequest)
	return protocol.Failure(action, code, message, fields)
}

func readStdin(required bool) ([]byte, error) {
	if !stdinHasData() {
		if required {
			return nil, protocol.Err(protocol.CodeInvalidRequest, "stdin is required")
		}
		return nil, nil
	}
	reader := io.LimitReader(os.Stdin, maxStdinBytes)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to read stdin")
	}
	if len(data) >= maxStdinBytes {
		return nil, protocol.Err(protocol.CodeTooLarge, "stdin payload exceeds limit")
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
