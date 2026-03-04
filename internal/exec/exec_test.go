package execmod

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentrail/internal/protocol"
)

func TestRunExecPreservesArgvWithoutShell(t *testing.T) {
	argv := []string{os.Args[0], "-test.run=TestHelperProcess", "--", "alpha beta", "1&2", "$(nope)"}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_HELPER_PROCESS": "1"})

	result, err := Run(Options{Argv: argv, Env: envMap})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%s", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "alpha beta|1&2|$(nope)") {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

func TestRunExecTimeout(t *testing.T) {
	argv := []string{os.Args[0], "-test.run=TestHelperProcess", "--", "sleep-ms=200"}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_HELPER_PROCESS": "1"})

	result, err := Run(Options{Argv: argv, Env: envMap, TimeoutMS: 50})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeTimeout {
		t.Fatalf("expected timeout code, got %v", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("expected timeout exit code -1, got %d", result.ExitCode)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
			break
		}
	}
	if index == -1 || index+1 >= len(os.Args) {
		fmt.Fprint(os.Stderr, "missing helper args")
		os.Exit(2)
	}

	helperArgs := os.Args[index+1:]
	for _, arg := range helperArgs {
		if strings.HasPrefix(arg, "sleep-ms=") {
			msRaw := strings.TrimPrefix(arg, "sleep-ms=")
			ms, _ := strconv.Atoi(msRaw)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			fmt.Print("slept")
			os.Exit(0)
		}
	}

	fmt.Print(strings.Join(helperArgs, "|"))
	os.Exit(0)
}
