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
	"time"

	"agentrail/internal/protocol"
)

const (
	DefaultMaxOutputBytes int64 = 262144
	HardMaxOutputBytes    int64 = 4194304
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
	if len(env) > 0 {
		cmd.Env = env
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, protocol.ErrDetails(protocol.CodeExecFailed, "failed to create stdout pipe", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD})
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, protocol.ErrDetails(protocol.CodeExecFailed, "failed to create stderr pipe", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD})
	}

	job, err := newJobObject()
	if err != nil {
		return Result{}, protocol.ErrDetails(protocol.CodeExecFailed, "failed to initialize process job", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD})
	}
	if job != nil {
		defer job.Close()
	}

	capture := newSharedOutputCapture(maxOutputBytes)
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(capture.stdoutWriter(), stdoutPipe)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(capture.stderrWriter(), stderrPipe)
	}()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, protocol.ErrDetails(protocol.CodeExecFailed, "failed to start process", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD})
	}
	if job != nil {
		if err := job.Assign(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			copyWG.Wait()
			return capture.result(time.Since(start).Milliseconds(), -1), protocol.ErrDetails(protocol.CodeExecFailed, "failed to assign process to job", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD})
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

	copyWG.Wait()
	timingMS := time.Since(start).Milliseconds()
	result := capture.result(timingMS, 0)

	if timedOut {
		result.ExitCode = -1
		return result, protocol.ErrDetails(protocol.CodeTimeout, "process timed out", protocol.ErrorDetails{
			"argv0":               options.Argv[0],
			"cwd":                 options.CWD,
			"timeout_ms":          options.TimeoutMS,
			"output_bytes":        result.OutputBytes,
			"process_tree_killed": processTreeKilled,
		})
	}

	if runErr != nil {
		var exitErr *osexec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, protocol.ErrDetails(protocol.CodeExecFailed, "failed while waiting for process", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD, "output_bytes": result.OutputBytes})
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, nil
}

func parseEnv(raw json.RawMessage, workspaceRoot string) ([]string, error) {
	if len(raw) == 0 {
		base := envToMap(os.Environ())
		if err := applyWorkspaceEnvDefaults(base, workspaceRoot); err != nil {
			return nil, err
		}
		return mapToEnv(base), nil
	}

	var mapEnv map[string]string
	if err := json.Unmarshal(raw, &mapEnv); err == nil {
		base := envToMap(os.Environ())
		if err := applyWorkspaceEnvDefaults(base, workspaceRoot); err != nil {
			return nil, err
		}
		for key, value := range mapEnv {
			if err := validateEnvEntry(key, value); err != nil {
				return nil, err
			}
			setEnvValue(base, key, value)
		}
		return mapToEnv(base), nil
	}

	var listEnv []string
	if err := json.Unmarshal(raw, &listEnv); err == nil {
		windowsValues := map[string]string{}
		for _, item := range listEnv {
			idx := strings.IndexByte(item, '=')
			if idx <= 0 || strings.IndexByte(item, 0) >= 0 {
				return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "env list entries must contain '='", protocol.ErrorDetails{"field": "env", "reason": "invalid_entry"})
			}
			if err := validateEnvEntry(item[:idx], item[idx+1:]); err != nil {
				return nil, err
			}
			if runtime.GOOS == "windows" {
				setEnvValue(windowsValues, item[:idx], item[idx+1:])
			}
		}
		if runtime.GOOS == "windows" {
			return mapToEnv(windowsValues), nil
		}
		return listEnv, nil
	}

	return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "env must be an object or array", protocol.ErrorDetails{"field": "env", "reason": "invalid_type"})
}

func applyWorkspaceEnvDefaults(env map[string]string, workspaceRoot string) error {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return protocol.ErrDetails(protocol.CodeExecFailed, "failed to resolve workspace runtime root", protocol.ErrorDetails{"path": filepath.ToSlash(workspaceRoot)})
	}

	dirs := map[string]string{
		"AGENTRAIL_RUNTIME_DIR": filepath.Join(root, ".agentrail"),
		"TMP":                   filepath.Join(root, ".agentrail", "tmp"),
		"TEMP":                  filepath.Join(root, ".agentrail", "tmp"),
		"TMPDIR":                filepath.Join(root, ".agentrail", "tmp"),
		"GOCACHE":               filepath.Join(root, ".agentrail", "cache", "go-build"),
		"GOTMPDIR":              filepath.Join(root, ".agentrail", "tmp", "go"),
		"APPDATA":               filepath.Join(root, ".agentrail", "appdata", "roaming"),
		"LOCALAPPDATA":          filepath.Join(root, ".agentrail", "appdata", "local"),
		"XDG_CACHE_HOME":        filepath.Join(root, ".agentrail", "cache", "xdg"),
		"npm_config_cache":      filepath.Join(root, ".agentrail", "cache", "npm"),
		"PIP_CACHE_DIR":         filepath.Join(root, ".agentrail", "cache", "pip"),
		"CARGO_HOME":            filepath.Join(root, ".agentrail", "cache", "cargo-home"),
		"CARGO_TARGET_DIR":      filepath.Join(root, ".agentrail", "cache", "cargo-target"),
	}
	for key, dir := range dirs {
		if err := ensureWorkspaceRuntimeDir(root, dir); err != nil {
			return err
		}
		setEnvValue(env, key, dir)
	}
	return nil
}

func ensureWorkspaceRuntimeDir(workspaceRoot, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return protocol.ErrDetails(protocol.CodeExecFailed, "failed to initialize workspace runtime directory", protocol.ErrorDetails{"path": filepath.ToSlash(dir)})
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return protocol.ErrDetails(protocol.CodeExecFailed, "failed to inspect workspace runtime directory", protocol.ErrorDetails{"path": filepath.ToSlash(dir)})
	}
	if !pathWithin(resolved, workspaceRoot) {
		return protocol.ErrDetails(protocol.CodeExecFailed, "workspace runtime directory escapes workspace", protocol.ErrorDetails{
			"path":     filepath.ToSlash(dir),
			"resolved": filepath.ToSlash(resolved),
		})
	}
	return nil
}

func pathWithin(path, parent string) bool {
	path = filepath.Clean(path)
	parent = filepath.Clean(parent)
	if runtime.GOOS == "windows" {
		if strings.EqualFold(path, parent) {
			return true
		}
	} else if path == parent {
		return true
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func envToMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			continue
		}
		setEnvValue(result, entry[:idx], entry[idx+1:])
	}
	return result
}

func setEnvValue(values map[string]string, key, value string) {
	if runtime.GOOS == "windows" {
		for existing := range values {
			if strings.EqualFold(existing, key) {
				values[existing] = value
				return
			}
		}
	}
	values[key] = value
}

func validateEnvEntry(key, value string) error {
	if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
		return protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid environment entry", protocol.ErrorDetails{"field": "env", "reason": "invalid_entry"})
	}
	return nil
}

func mapToEnv(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
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
	capture *sharedOutputCapture
	stdout  bool
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
