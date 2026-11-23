//go:build windows

package writemod

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func createJunction(t *testing.T, linkPath, targetPath string) {
	t.Helper()
	cmd := osexec.Command("cmd", "/c", "mklink", "/J", linkPath, targetPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("NTFS junction creation unavailable in this environment: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

func TestWriteFileAtomicInRootRejectsNTFSJunctionEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	junction := filepath.Join(root, "outside-junction")
	createJunction(t, junction, outside)

	if _, err := WriteFileAtomicInRoot(root, "outside-junction/escaped.txt", []byte("escape"), false); err == nil {
		t.Fatal("expected rooted write through NTFS junction to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rooted write escaped workspace through NTFS junction: %v", err)
	}
}

func TestWriteFileAtomicInRootConcurrentJunctionSwapNeverEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// Verify junction creation works in this environment before claiming race coverage.
	probe := filepath.Join(root, "probe-junction")
	createJunction(t, probe, outside)
	_ = os.Remove(probe)

	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			_ = os.RemoveAll(subDir)
			cmd := osexec.Command("cmd", "/c", "mklink", "/J", subDir, outside)
			_ = cmd.Run()
			_ = os.Remove(subDir)
			_ = os.MkdirAll(subDir, 0o755)
		}
	}()

	for i := 0; i < 60; i++ {
		_, _ = WriteFileAtomicInRoot(root, "sub/target.txt", []byte(fmt.Sprintf("payload-%d\n", i)), false)
	}
	stop.Store(true)
	wg.Wait()

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("ReadDir(outside): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("concurrent junction swap leaked files outside workspace: %+v", entries)
	}
}

func TestIsPathWithinWindowsRoot(t *testing.T) {
	cases := []struct {
		dir, root string
		want      bool
	}{
		{`C:\ws`, `C:\ws`, true},
		{`c:\WS\sub\dir`, `C:\ws`, true},
		{`C:\ws-other\sub`, `C:\ws`, false},
		{`C:\outside`, `C:\ws`, false},
		{`C:\sub`, `C:\`, true},
		{`D:\ws\sub`, `C:\ws`, false},
	}
	for _, tc := range cases {
		if got := isPathWithinWindowsRoot(tc.dir, tc.root); got != tc.want {
			t.Fatalf("isPathWithinWindowsRoot(%q, %q) = %v, want %v", tc.dir, tc.root, got, tc.want)
		}
	}
}

func TestWriteFileAtomicInRootReadOnlyTargetLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "readonly.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o666) })

	_, err := WriteFileAtomicInRoot(root, "readonly.txt", []byte("updated\n"), false)
	// On Windows, replacing a read-only file via ReplaceFileW/renameat fails with access denied.
	// Either way: no temp files may leak, and if it fails, original bytes must be preserved.
	if err != nil {
		got, readErr := os.ReadFile(target)
		if readErr != nil || string(got) != "original\n" {
			t.Fatalf("failed write on read-only target corrupted destination: got=%q err=%v", string(got), readErr)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".agentrail-") {
			t.Fatalf("leaked temp file after read-only write attempt: %s", entry.Name())
		}
	}
}
