package execmod

import (
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
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

func TestRunRejectsNegativeTimeout(t *testing.T) {
	_, err := Run(Options{Argv: []string{os.Args[0]}, TimeoutMS: -1})
	if err == nil {
		t.Fatal("expected negative timeout to fail")
	}
}

func TestParseEnvObjectOverridesCaseInsensitivelyOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows environment names are case-insensitive")
	}
	t.Setenv("AgentRail_Case_Key", "old")
	raw, _ := json.Marshal(map[string]string{"AGENTRAIL_CASE_KEY": "new"})
	env, err := parseEnv(raw, t.TempDir())
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}
	matches := 0
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "agentrail_case_key") {
			matches++
			if parts[1] != "new" {
				t.Fatalf("expected override value, got %q", entry)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one case-insensitive environment key, got %d", matches)
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
	if te.Details["timeout_ms"] != 50 {
		t.Fatalf("expected timeout details, got %+v", te.Details)
	}
	if te.Details["process_tree_killed"] != true {
		t.Fatalf("expected process_tree_killed=true, got %+v", te.Details)
	}
}

func TestRunExecStdoutOutputBudget(t *testing.T) {
	argv := []string{os.Args[0], "-test.run=TestHelperProcess", "--", "stdout-bytes=32"}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_HELPER_PROCESS": "1"})

	result, err := Run(Options{Argv: argv, Env: envMap, MaxOutputBytes: 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OutputBytes != 10 {
		t.Fatalf("expected 10 captured bytes, got %d", result.OutputBytes)
	}
	if len(result.Stdout) != 10 {
		t.Fatalf("expected 10 stdout bytes, got %d", len(result.Stdout))
	}
	if !result.StdoutTruncated {
		t.Fatalf("expected stdout truncation, got %+v", result)
	}
	if result.StderrTruncated {
		t.Fatalf("did not expect stderr truncation, got %+v", result)
	}
}

func TestRunExecCombinedOutputBudget(t *testing.T) {
	argv := []string{os.Args[0], "-test.run=TestHelperProcess", "--", "stdout-bytes=8", "stderr-bytes=8"}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_HELPER_PROCESS": "1"})

	result, err := Run(Options{Argv: argv, Env: envMap, MaxOutputBytes: 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OutputBytes != 10 {
		t.Fatalf("expected 10 captured bytes, got %d", result.OutputBytes)
	}
	if len(result.Stdout)+len(result.Stderr) != 10 {
		t.Fatalf("expected combined capture of 10 bytes, got %+v", result)
	}
	if !result.StdoutTruncated && !result.StderrTruncated {
		t.Fatalf("expected at least one stream to be truncated, got %+v", result)
	}
}

func TestParseEnvUsesWorkspaceRuntimeDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(t.TempDir(), "host-appdata"))
	t.Setenv("GOCACHE", filepath.Join(t.TempDir(), "host-gocache"))

	env, err := parseEnv(nil, root)
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}
	values := envToMap(env)

	wantAppData := filepath.Join(root, ".agentrail", "appdata", "roaming")
	wantGoCache := filepath.Join(root, ".agentrail", "cache", "go-build")
	if values["APPDATA"] != wantAppData {
		t.Fatalf("expected APPDATA=%q, got %q", wantAppData, values["APPDATA"])
	}
	if values["GOCACHE"] != wantGoCache {
		t.Fatalf("expected GOCACHE=%q, got %q", wantGoCache, values["GOCACHE"])
	}
	if _, err := os.Stat(wantGoCache); err != nil {
		t.Fatalf("expected runtime directory to be created: %v", err)
	}
}

func TestParseEnvHonorsCallerObjectOverrides(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(t.TempDir(), "caller-cache")
	raw, _ := json.Marshal(map[string]string{"GOCACHE": override})

	env, err := parseEnv(raw, root)
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}
	values := envToMap(env)
	if values["GOCACHE"] != override {
		t.Fatalf("expected caller GOCACHE override %q, got %q", override, values["GOCACHE"])
	}
	if values["TMP"] != filepath.Join(root, ".agentrail", "tmp") {
		t.Fatalf("expected workspace TMP default, got %q", values["TMP"])
	}
}

func TestParseEnvArrayRemainsFullReplacement(t *testing.T) {
	root := t.TempDir()
	raw, _ := json.Marshal([]string{"ONLY=value"})

	env, err := parseEnv(raw, root)
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}
	if len(env) != 1 || env[0] != "ONLY=value" {
		t.Fatalf("expected exact replacement env, got %+v", env)
	}
}

func TestParseEnvRejectsRuntimeSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".agentrail")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := parseEnv(nil, root)
	if err == nil {
		t.Fatalf("expected runtime symlink escape to fail")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeExecFailed {
		t.Fatalf("expected exec_failed, got %v", err)
	}
}

func TestRunExecTimeoutKillsDescendantTree(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific Job Object behavior")
	}

	markerDir := t.TempDir()
	markerPath := filepath.Join(markerDir, "marker.txt")
	argv := []string{os.Args[0], "-test.run=TestHelperProcess", "--", "spawn-marker=" + markerPath}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_HELPER_PROCESS": "1"})

	_, err := Run(Options{Argv: argv, Env: envMap, TimeoutMS: 100})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	time.Sleep(700 * time.Millisecond)
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected descendant process to be terminated before writing marker, got %v", statErr)
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
		if strings.HasPrefix(arg, "write-marker-after=") {
			parts := strings.SplitN(strings.TrimPrefix(arg, "write-marker-after="), ":", 2)
			if len(parts) != 2 {
				fmt.Fprint(os.Stderr, "invalid write-marker-after")
				os.Exit(2)
			}
			ms, _ := strconv.Atoi(parts[0])
			time.Sleep(time.Duration(ms) * time.Millisecond)
			if err := os.WriteFile(parts[1], []byte("marker"), 0o644); err != nil {
				fmt.Fprint(os.Stderr, err.Error())
				os.Exit(2)
			}
			os.Exit(0)
		}
		if strings.HasPrefix(arg, "spawn-marker=") {
			markerPath := strings.TrimPrefix(arg, "spawn-marker=")
			child := osexec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "write-marker-after=300:"+markerPath)
			child.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
			if err := child.Start(); err != nil {
				fmt.Fprint(os.Stderr, err.Error())
				os.Exit(2)
			}
			time.Sleep(time.Second)
			os.Exit(0)
		}
	}
	for _, arg := range helperArgs {
		if strings.HasPrefix(arg, "stdout-bytes=") {
			n, _ := strconv.Atoi(strings.TrimPrefix(arg, "stdout-bytes="))
			fmt.Fprint(os.Stdout, strings.Repeat("o", n))
		}
		if strings.HasPrefix(arg, "stderr-bytes=") {
			n, _ := strconv.Atoi(strings.TrimPrefix(arg, "stderr-bytes="))
			fmt.Fprint(os.Stderr, strings.Repeat("e", n))
		}
	}
	if len(helperArgs) == 1 && (strings.HasPrefix(helperArgs[0], "stdout-bytes=") || strings.HasPrefix(helperArgs[0], "stderr-bytes=")) {
		os.Exit(0)
	}
	if len(helperArgs) == 2 {
		allOutputArgs := true
		for _, arg := range helperArgs {
			if !strings.HasPrefix(arg, "stdout-bytes=") && !strings.HasPrefix(arg, "stderr-bytes=") {
				allOutputArgs = false
			}
		}
		if allOutputArgs {
			os.Exit(0)
		}
	}

	fmt.Print(strings.Join(helperArgs, "|"))
	os.Exit(0)
}
