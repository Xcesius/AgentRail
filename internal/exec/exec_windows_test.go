//go:build windows

package execmod

import (
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const createBreakawayFromJob = 0x01000000

func TestRunExecTimeoutKillsGrandchildTree(t *testing.T) {
	markerDir := t.TempDir()
	markerPath := filepath.Join(markerDir, "grandchild-marker.txt")
	argv := []string{os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "spawn-grandchild=" + markerPath}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_WIN_HELPER_PROCESS": "1"})

	_, err := Run(Options{Argv: argv, Env: envMap, TimeoutMS: 180})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	time.Sleep(800 * time.Millisecond)
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected grandchild process to be terminated before writing marker, got %v", statErr)
	}
}

func TestRunExecBlocksBreakawayFromJob(t *testing.T) {
	markerDir := t.TempDir()
	markerPath := filepath.Join(markerDir, "breakaway-marker.txt")
	argv := []string{os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "spawn-breakaway=" + markerPath}
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_WIN_HELPER_PROCESS": "1"})

	res, err := Run(Options{Argv: argv, Env: envMap, TimeoutMS: 180})
	// Either the breakaway attempt itself fails with Access Denied (exit 0 with "breakaway_denied")
	// or the child times out and the job kills the entire tree. In neither case may marker exist.
	if err == nil && !strings.Contains(res.Stdout, "breakaway_denied") {
		t.Fatalf("unexpected non-timeout success without breakaway denial: %+v", res)
	}
	time.Sleep(700 * time.Millisecond)
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("child broke away from job and wrote marker: %v", statErr)
	}
}

func TestRunExecFastExitStressCapturesOutputReliably(t *testing.T) {
	envMap, _ := json.Marshal(map[string]string{"GO_WANT_WIN_HELPER_PROCESS": "1"})
	for i := range 15 {
		token := fmt.Sprintf("fast-output-%02d", i)
		argv := []string{os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "fast-print=" + token}
		res, err := Run(Options{Argv: argv, Env: envMap})
		if err != nil {
			t.Fatalf("iteration %d failed: %v", i, err)
		}
		if res.ExitCode != 0 || res.Stdout != token {
			t.Fatalf("iteration %d lost/corrupted output: exit=%d stdout=%q want=%q", i, res.ExitCode, res.Stdout, token)
		}
	}
}

func TestResumeProcessThreadsWin32FallbackWorks(t *testing.T) {
	cmd := osexec.Command(os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "fast-print=resumed")
	cmd.Env = append(os.Environ(), "GO_WANT_WIN_HELPER_PROCESS=1")
	prepareCommandForJob(cmd)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := resumeProcessThreadsWin32(uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("resumeProcessThreadsWin32: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.String() != "resumed" {
		t.Fatalf("expected resumed output, got %q", out.String())
	}
}

func TestWindowsHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WIN_HELPER_PROCESS") != "1" {
		return
	}
	idx := -1
	for i, arg := range os.Args {
		if arg == "--" {
			idx = i
			break
		}
	}
	if idx == -1 || idx+1 >= len(os.Args) {
		os.Exit(2)
	}
	arg := os.Args[idx+1]
	switch {
	case strings.HasPrefix(arg, "fast-print="):
		fmt.Print(strings.TrimPrefix(arg, "fast-print="))
		os.Exit(0)
	case strings.HasPrefix(arg, "spawn-grandchild="):
		marker := strings.TrimPrefix(arg, "spawn-grandchild=")
		child := osexec.Command(os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "spawn-leaf="+marker)
		child.Env = append(os.Environ(), "GO_WANT_WIN_HELPER_PROCESS=1")
		if err := child.Start(); err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			os.Exit(2)
		}
		time.Sleep(2 * time.Second)
		os.Exit(0)
	case strings.HasPrefix(arg, "spawn-leaf="):
		marker := strings.TrimPrefix(arg, "spawn-leaf=")
		grandchild := osexec.Command(os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "write-delayed="+marker)
		grandchild.Env = append(os.Environ(), "GO_WANT_WIN_HELPER_PROCESS=1")
		if err := grandchild.Start(); err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			os.Exit(2)
		}
		time.Sleep(2 * time.Second)
		os.Exit(0)
	case strings.HasPrefix(arg, "spawn-breakaway="):
		marker := strings.TrimPrefix(arg, "spawn-breakaway=")
		child := osexec.Command(os.Args[0], "-test.run=TestWindowsHelperProcess", "--", "write-delayed="+marker)
		child.Env = append(os.Environ(), "GO_WANT_WIN_HELPER_PROCESS=1")
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createBreakawayFromJob}
		if err := child.Start(); err != nil {
			fmt.Print("breakaway_denied")
			os.Exit(0)
		}
		time.Sleep(2 * time.Second)
		os.Exit(0)
	case strings.HasPrefix(arg, "write-delayed="):
		marker := strings.TrimPrefix(arg, "write-delayed=")
		time.Sleep(450 * time.Millisecond)
		_ = os.WriteFile(marker, []byte("escaped"), 0o644)
		os.Exit(0)
	}
	os.Exit(2)
}
