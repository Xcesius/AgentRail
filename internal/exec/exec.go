package execmod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	osexec "os/exec"
	"sort"
	"strings"
	"time"

	"agentrail/internal/protocol"
)

type Options struct {
	Argv      []string
	CWD       string
	Env       json.RawMessage
	TimeoutMS int
}

type Result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimingMS int64  `json:"timing_ms"`
}

func Run(options Options) (Result, error) {
	if len(options.Argv) == 0 {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "argv must not be empty")
	}

	ctx := context.Background()
	cancel := func() {}
	if options.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(options.TimeoutMS)*time.Millisecond)
	}
	defer cancel()

	cmd := osexec.CommandContext(ctx, options.Argv[0], options.Argv[1:]...)
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

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	timing := time.Since(start).Milliseconds()

	result := Result{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		TimingMS: timing,
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.ExitCode = -1
		return result, protocol.Err(protocol.CodeTimeout, "process timed out")
	}

	if runErr != nil {
		var exitErr *osexec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, protocol.Err(protocol.CodeExecFailed, "failed to start process")
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
				return nil, protocol.Err(protocol.CodeInvalidRequest, "env list entries must contain '='")
			}
		}
		return listEnv, nil
	}

	return nil, protocol.Err(protocol.CodeInvalidRequest, "env must be an object or array")
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
