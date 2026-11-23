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

	startCh := make(chan struct{})
	var stop atomic.Bool
	var junctionSwaps atomic.Int64
	var writeSuccesses atomic.Int64
	var writeErrors atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		<-startCh
		for !stop.Load() {
			_ = os.RemoveAll(subDir)
			cmd := osexec.Command("cmd", "/c", "mklink", "/J", subDir, outside)
			if err := cmd.Run(); err == nil {
				junctionSwaps.Add(1)
			}
			_ = os.Remove(subDir)
			_ = os.MkdirAll(subDir, 0o755)
		}
	})

	close(startCh)
	for i := range 80 {
		if _, err := WriteFileAtomicInRoot(root, "sub/target.txt", []byte(fmt.Sprintf("payload-%d\n", i)), false); err == nil {
			writeSuccesses.Add(1)
		} else {
			writeErrors.Add(1)
		}
	}
	stop.Store(true)
	wg.Wait()

	if junctionSwaps.Load() == 0 {
		t.Fatalf("expected at least one junction swap during concurrent race (writes ok=%d err=%d)", writeSuccesses.Load(), writeErrors.Load())
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("ReadDir(outside): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("concurrent junction swap leaked files outside workspace: %+v", entries)
	}
}

func TestRenameInDirRootFallbackEnforcesRootContainment(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	subDir := filepath.Join(rootDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	rootHandle, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot(root): %v", err)
	}
	defer rootHandle.Close()

	// 1. Forced fallback inside root must succeed for create and replace.
	inSubHandle, err := rootHandle.OpenRoot("sub")
	if err != nil {
		t.Fatalf("OpenRoot(sub): %v", err)
	}
	defer inSubHandle.Close()

	if err := os.WriteFile(filepath.Join(subDir, ".tmp1"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("WriteFile(.tmp1): %v", err)
	}
	if err := renameInDirRootFallback(rootHandle, inSubHandle, ".tmp1", "target.txt", errorInvalidHandle); err != nil {
		t.Fatalf("expected in-root fallback create to succeed, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, ".tmp2"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("WriteFile(.tmp2): %v", err)
	}
	if err := renameInDirRootFallback(rootHandle, inSubHandle, ".tmp2", "target.txt", errorInvalidHandle); err != nil {
		t.Fatalf("expected in-root fallback replace to succeed, got %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(subDir, "target.txt")); err != nil || string(got) != "v2" {
		t.Fatalf("unexpected fallback target content: got=%q err=%v", string(got), err)
	}

	// 2. Forced fallback with a dirRoot outside root must be rejected without writing outside.
	outsideHandle, err := os.OpenRoot(outsideDir)
	if err != nil {
		t.Fatalf("OpenRoot(outside): %v", err)
	}
	defer outsideHandle.Close()

	if err := os.WriteFile(filepath.Join(outsideDir, ".tmp-out"), []byte("escape"), 0o644); err != nil {
		t.Fatalf("WriteFile(.tmp-out): %v", err)
	}
	if err := renameInDirRootFallback(rootHandle, outsideHandle, ".tmp-out", "escaped.txt", errorInvalidHandle); err == nil {
		t.Fatal("expected out-of-root fallback to fail containment check")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out-of-root fallback unexpectedly created escaped.txt: %v", err)
	}
}

func TestStripWindowsExtendedPrefixHandlesUNCAndDrivePaths(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`\\?\C:\Users\dev\repo`, `C:\Users\dev\repo`},
		{`\\?\UNC\fileserver\share\repo`, `\\fileserver\share\repo`},
		{`\\?\unc\fileserver\share\repo`, `\\fileserver\share\repo`},
		{`C:\plain\path`, `C:\plain\path`},
	}
	for _, tc := range cases {
		if got := stripWindowsExtendedPrefix(tc.in); got != tc.want {
			t.Fatalf("stripWindowsExtendedPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
		{`\\server\share\repo\sub`, `\\server\share\repo`, true},
		{`\\server\share\other`, `\\server\share\repo`, false},
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
