package execmod

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	osexec "os/exec"
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

	maxOutputBytes := options.MaxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}

	cmd := osexec.Command(options.Argv[0], options.Argv[1:]...)
	if options.CWD != "" {
		cmd.Dir = options.CWD
	}

	env, err := parseEnv(options.Env)
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
		return result, protocol.ErrDetails(protocol.CodeExecFailed, "failed to start process", protocol.ErrorDetails{"argv0": options.Argv[0], "cwd": options.CWD, "output_bytes": result.OutputBytes})
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, nil
}

func parseEnv(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var mapEnv map[string]string
	if err := json.Unmarshal(raw, &mapEnv); err == nil {
		base := envToMap(os.Environ())
		for key, value := range mapEnv {
			base[key] = value
		}
		return mapToEnv(base), nil
	}

	var listEnv []string
	if err := json.Unmarshal(raw, &listEnv); err == nil {
		for _, item := range listEnv {
			if !strings.Contains(item, "=") {
				return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "env list entries must contain '='", protocol.ErrorDetails{"field": "env", "reason": "invalid_entry"})
			}
		}
		return listEnv, nil
	}

	return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "env must be an object or array", protocol.ErrorDetails{"field": "env", "reason": "invalid_type"})
}

func envToMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			continue
		}
		result[entry[:idx]] = entry[idx+1:]
	}
	return result
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
