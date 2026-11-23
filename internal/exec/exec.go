package execmod

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentrail/internal/protocol"
)

const (
	DefaultMaxOutputBytes int64 = 262144
	HardMaxOutputBytes    int64 = 4194304
	maxTimeoutMS          int64 = int64(^uint64(0)>>1) / int64(time.Millisecond)
)

type Options struct {
	Argv           []string
	CWD            string
	WorkspaceRoot  string
	Env            json.RawMessage
	TimeoutMS      int
	MaxOutputBytes int64
}

type Result struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	OutputBytes     int64  `json:"output_bytes"`
	TimingMS        int64  `json:"timing_ms"`
}

func Run(options Options) (Result, error) {
	if len(options.Argv) == 0 {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "argv must not be empty", protocol.ErrorDetails{"field": "argv", "reason": "required"})
	}
	if options.TimeoutMS < 0 {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "timeout_ms must be >= 0", protocol.ErrorDetails{"field": "timeout_ms", "reason": "negative"})
	}
	if int64(options.TimeoutMS) > maxTimeoutMS {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "timeout_ms exceeds supported duration", protocol.ErrorDetails{"field": "timeout_ms", "reason": "too_large", "maximum": maxTimeoutMS})
	}
	if options.MaxOutputBytes < 0 || options.MaxOutputBytes > HardMaxOutputBytes {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid max_output_bytes", protocol.ErrorDetails{"field": "max_output_bytes", "reason": "invalid_value"})
	}

	maxOutputBytes := options.MaxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}

	cmd := osexec.Command(options.Argv[0], options.Argv[1:]...)
	if options.CWD != "" {
		cmd.Dir = options.CWD
	}

	env, err := parseEnv(options.Env, options.WorkspaceRoot)
	if err != nil {
		return Result{}, err
	}
	if env != nil {
		cmd.Env = env
	}

	job, err := newJobObject()
	if err != nil {
		return Result{}, protocol.ErrDetails(protocol.CodeExecFailed, "failed to initialize process job", execErrorDetails(options, 0, false))
	}
	if job != nil {
		defer job.Close()
		prepareCommandForJob(cmd)
	}

	capture := newSharedOutputCapture(maxOutputBytes)
	cmd.Stdout = capture.stdoutWriter()
	cmd.Stderr = capture.stderrWriter()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, protocol.ErrDetails(protocol.CodeExecFailed, "failed to start process", execErrorDetails(options, 0, false))
	}
	if job != nil {
		if err := job.AssignAndResume(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			res := capture.result(time.Since(start).Milliseconds(), -1)
			return res, protocol.ErrDetails(protocol.CodeExecFailed, "failed to assign process to job", execErrorDetails(options, res.OutputBytes, false))
		}
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	timedOut := false
	processTreeKilled := false
	var runErr error
	if options.TimeoutMS > 0 {
		timer := time.NewTimer(time.Duration(options.TimeoutMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case runErr = <-waitCh:
		case <-timer.C:
			timedOut = true
			if job != nil {
				if killErr := job.CloseAndKill(); killErr == nil {
					processTreeKilled = true
				}
			} else if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			runErr = <-waitCh
		}
	} else {
		runErr = <-waitCh
	}

	timingMS := time.Since(start).Milliseconds()
	result := capture.result(timingMS, 0)

	if timedOut {
		result.ExitCode = -1
		return result, protocol.ErrDetails(protocol.CodeTimeout, "process timed out", execErrorDetails(options, result.OutputBytes, processTreeKilled))
	}

	if runErr != nil {
		if exitErr, ok := errors.AsType[*osexec.ExitError](runErr); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, protocol.ErrDetails(protocol.CodeExecFailed, "failed while waiting for process", execErrorDetails(options, result.OutputBytes, processTreeKilled))
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, nil
}

func execErrorDetails(options Options, outputBytes int64, processTreeKilled bool) protocol.ErrorDetails {
	argv0 := ""
	if len(options.Argv) > 0 {
		argv0 = options.Argv[0]
	}
	return protocol.ErrorDetails{
		"argv0":               argv0,
		"cwd":                 options.CWD,
		"timeout_ms":          options.TimeoutMS,
		"output_bytes":        outputBytes,
		"process_tree_killed": processTreeKilled,
	}
}

type envEntry struct {
	key   string
	value string
}

type envTable struct {
	entries map[string]envEntry
}

func newEnvTable(capacity int) *envTable {
	return &envTable{entries: make(map[string]envEntry, capacity)}
}

func envLookupKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func (t *envTable) set(key, value string) {
	norm := envLookupKey(key)
	if existing, ok := t.entries[norm]; ok {
		t.entries[norm] = envEntry{key: existing.key, value: value}
		return
	}
	t.entries[norm] = envEntry{key: key, value: value}
}

func (t *envTable) toSlice() []string {
	if len(t.entries) == 0 {
		return []string{}
	}
	items := make([]envEntry, 0, len(t.entries))
	for _, entry := range t.entries {
		items = append(items, entry)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].key < items[j].key
	})
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.key+"="+item.value)
	}
	return result
}

func parseEnv(raw json.RawMessage, workspaceRoot string) ([]string, error) {
	if len(raw) == 0 {
		base := envToTable(os.Environ())
		if err := applyWorkspaceEnvDefaults(base, workspaceRoot); err != nil {
			return nil, err
		}
		return base.toSlice(), nil
	}

	var mapEnv map[string]string
	if err := json.Unmarshal(raw, &mapEnv); err == nil {
		base := envToTable(os.Environ())
		if err := applyWorkspaceEnvDefaults(base, workspaceRoot); err != nil {
			return nil, err
		}
		for key, value := range mapEnv {
			if err := validateEnvEntry(key, value); err != nil {
				return nil, err
			}
			base.set(key, value)
		}
		return base.toSlice(), nil
	}

	var listEnv []string
	if err := json.Unmarshal(raw, &listEnv); err == nil {
		if len(listEnv) == 0 {
			return []string{}, nil
		}
		table := newEnvTable(len(listEnv))
		for _, item := range listEnv {
			idx := strings.IndexByte(item, '=')
			if idx <= 0 || strings.IndexByte(item, 0) >= 0 {
				return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "env list entries must contain '='", protocol.ErrorDetails{"field": "env", "reason": "invalid_entry"})
			}
			if err := validateEnvEntry(item[:idx], item[idx+1:]); err != nil {
				return nil, err
			}
			if runtime.GOOS == "windows" {
				table.set(item[:idx], item[idx+1:])
			}
		}
		if runtime.GOOS == "windows" {
			return table.toSlice(), nil
		}
		return listEnv, nil
	}

	return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "env must be an object or array", protocol.ErrorDetails{"field": "env", "reason": "invalid_type"})
}

func applyWorkspaceEnvDefaults(env *envTable, workspaceRoot string) error {
	root := workspaceRoot
	if root == "" {
		return nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return protocol.ErrDetails(protocol.CodeExecFailed, "failed to resolve workspace runtime root", protocol.ErrorDetails{"path": filepath.ToSlash(workspaceRoot)})
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return protocol.ErrDetails(protocol.CodeExecFailed, "failed to open workspace runtime root", protocol.ErrorDetails{"path": filepath.ToSlash(root)})
	}
	defer rootHandle.Close()

	type envDir struct {
		key string
		rel string
	}
	defaults := []envDir{
		{"AGENTRAIL_RUNTIME_DIR", ".agentrail"},
		{"TMP", filepath.Join(".agentrail", "tmp")},
		{"TEMP", filepath.Join(".agentrail", "tmp")},
		{"TMPDIR", filepath.Join(".agentrail", "tmp")},
		{"GOCACHE", filepath.Join(".agentrail", "cache", "go-build")},
		{"GOTMPDIR", filepath.Join(".agentrail", "tmp", "go")},
		{"APPDATA", filepath.Join(".agentrail", "appdata", "roaming")},
		{"LOCALAPPDATA", filepath.Join(".agentrail", "appdata", "local")},
		{"XDG_CACHE_HOME", filepath.Join(".agentrail", "cache", "xdg")},
		{"npm_config_cache", filepath.Join(".agentrail", "cache", "npm")},
		{"PIP_CACHE_DIR", filepath.Join(".agentrail", "cache", "pip")},
		{"CARGO_HOME", filepath.Join(".agentrail", "cache", "cargo-home")},
		{"CARGO_TARGET_DIR", filepath.Join(".agentrail", "cache", "cargo-target")},
	}
	created := make(map[string]struct{}, len(defaults))
	for _, item := range defaults {
		fullDir := filepath.Join(root, item.rel)
		if _, done := created[item.rel]; !done {
			if err := ensureWorkspaceRuntimeDir(rootHandle, item.rel, fullDir); err != nil {
				return err
			}
			created[item.rel] = struct{}{}
		}
		env.set(item.key, fullDir)
	}
	return nil
}

func ensureWorkspaceRuntimeDir(rootHandle *os.Root, relative, fullDir string) error {
	if info, err := rootHandle.Stat(relative); err == nil && info.IsDir() {
		return nil
	}
	if err := rootHandle.MkdirAll(relative, 0o755); err != nil {
		return protocol.ErrDetails(protocol.CodeExecFailed, "failed to initialize workspace runtime directory", protocol.ErrorDetails{"path": filepath.ToSlash(fullDir)})
	}
	return nil
}

func envToTable(entries []string) *envTable {
	table := newEnvTable(len(entries))
	for _, entry := range entries {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			continue
		}
		table.set(entry[:idx], entry[idx+1:])
	}
	return table
}

func envToMap(entries []string) map[string]string {
	table := envToTable(entries)
	result := make(map[string]string, len(table.entries))
	for _, entry := range table.entries {
		result[entry.key] = entry.value
	}
	return result
}

func validateEnvEntry(key, value string) error {
	if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
		return protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid environment entry", protocol.ErrorDetails{"field": "env", "reason": "invalid_entry"})
	}
	return nil
}

type sharedOutputCapture struct {
	limit           int64
	captured        int64
	stdout          bytes.Buffer
	stderr          bytes.Buffer
	stdoutTruncated bool
	stderrTruncated bool
	mu              sync.Mutex
}

type streamCaptureWriter struct {
	capture       *sharedOutputCapture
	stdout        bool
	drainFastPath atomic.Bool
}

func newSharedOutputCapture(limit int64) *sharedOutputCapture {
	return &sharedOutputCapture{limit: limit}
}

func (c *sharedOutputCapture) stdoutWriter() io.Writer {
	return &streamCaptureWriter{capture: c, stdout: true}
}

func (c *sharedOutputCapture) stderrWriter() io.Writer {
	return &streamCaptureWriter{capture: c, stdout: false}
}

func (w *streamCaptureWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || w.drainFastPath.Load() {
		return len(p), nil
	}

	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()

	remaining := w.capture.limit - w.capture.captured
	keep := len(p)
	if remaining <= 0 {
		keep = 0
	} else if int64(keep) > remaining {
		keep = int(remaining)
	}

	if keep > 0 {
		if w.stdout {
			_, _ = w.capture.stdout.Write(p[:keep])
		} else {
			_, _ = w.capture.stderr.Write(p[:keep])
		}
		w.capture.captured += int64(keep)
	}
	if keep < len(p) {
		if w.stdout {
			w.capture.stdoutTruncated = true
		} else {
			w.capture.stderrTruncated = true
		}
		w.drainFastPath.Store(true)
	}
	return len(p), nil
}

func (c *sharedOutputCapture) result(timingMS int64, exitCode int) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Result{
		ExitCode:        exitCode,
		Stdout:          c.stdout.String(),
		Stderr:          c.stderr.String(),
		StdoutTruncated: c.stdoutTruncated,
		StderrTruncated: c.stderrTruncated,
		OutputBytes:     c.captured,
		TimingMS:        timingMS,
	}
}
