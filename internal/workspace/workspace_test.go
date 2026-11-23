package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agentrail/internal/protocol"
)

func TestResolveWritePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	_, err = manager.ResolveWritePath(filepath.Join("..", "outside.txt"))
	if err == nil {
		t.Fatalf("expected traversal to be rejected")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePathDenied {
		t.Fatalf("expected path_denied, got %v", err)
	}
}

func TestResolveWritePathPreservesLeadingSpace(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}
	resolved, err := manager.ResolveWritePath(" leading.txt")
	if err != nil {
		t.Fatalf("ResolveWritePath: %v", err)
	}
	if filepath.Base(resolved) != " leading.txt" {
		t.Fatalf("path was trimmed: %q", resolved)
	}
}

func TestResolveWritePathRejectsDeniedDirectories(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	for _, input := range []string{".agentrail/cache/file.tmp", ".git/config", "node_modules/pkg/index.js"} {
		_, err := manager.ResolveWritePath(input)
		if err == nil {
			t.Fatalf("expected path to be denied for %s", input)
		}
		te, ok := protocol.AsToolError(err)
		if !ok || te.Code != protocol.CodePathDenied {
			t.Fatalf("expected path_denied for %s, got %v", input, err)
		}
	}
}

func TestResolveReadPathOutsideWorkspaceNeedsOptIn(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = manager.ResolveReadPath(outside, false)
	if err == nil {
		t.Fatalf("expected outside read to be denied")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePathDenied {
		t.Fatalf("expected path_denied, got %v", err)
	}

	resolved, err := manager.ResolveReadPath(outside, true)
	if err != nil {
		t.Fatalf("expected opt-in outside read to succeed, got %v", err)
	}
	if resolved == "" {
		t.Fatalf("expected resolved path")
	}
}

func TestDisplayPathUsesForwardSlashes(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	inside := filepath.Join(root, "nested", "file.txt")
	if got := manager.DisplayPath(inside); got != "nested/file.txt" {
		t.Fatalf("expected workspace-relative slash path, got %q", got)
	}
}

func TestDisplayPathOutsideWorkspaceUsesAbsoluteSlashPath(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	got := manager.DisplayPath(outside)
	if strings.Contains(got, `\`) {
		t.Fatalf("expected slash separators, got %q", got)
	}
	if runtime.GOOS == "windows" {
		if len(got) < 3 || got[1] != ':' || strings.ToUpper(got[:1]) != got[:1] {
			t.Fatalf("expected uppercase drive absolute path, got %q", got)
		}
	}
}

func TestResolveReadPathRejectsActualWindowsSystemDirectoryAcrossDrives(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific system directory policy")
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		t.Skip("SystemRoot is unavailable")
	}
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	_, err = manager.ResolveReadPath(filepath.Join(systemRoot, "win.ini"), true)
	if err == nil {
		t.Fatal("expected actual Windows system directory to be denied")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePathDenied {
		t.Fatalf("expected path_denied, got %v", err)
	}
}

func TestResolveReadPathRejectsExtendedAndDeviceWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific path prefixes")
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		t.Skip("SystemRoot is unavailable")
	}
	manager, err := NewManagerFromRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	for _, candidate := range []string{
		`\\?\` + filepath.Join(systemRoot, "win.ini"),
		`\\.\NUL`,
		`\??\` + filepath.Join(systemRoot, "win.ini"),
	} {
		_, err := manager.ResolveReadPath(candidate, true)
		if err == nil {
			t.Fatalf("expected %q to be denied", candidate)
		}
		te, ok := protocol.AsToolError(err)
		if !ok || te.Code != protocol.CodePathDenied {
			t.Fatalf("expected path_denied for %q, got %v", candidate, err)
		}
	}
}

func TestSystemDirsForPathIncludesDriveFallbacks(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific system directory policy")
	}
	t.Setenv("SystemRoot", "")
	t.Setenv("windir", "")
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramW6432", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("ProgramData", "")
	t.Setenv("SystemDrive", "C:")

	dirs := systemDirsForPath(`Z:\workspace`)
	want := map[string]bool{
		`z:\windows`:             false,
		`z:\program files`:       false,
		`z:\program files (x86)`: false,
		`z:\programdata`:         false,
		`c:\windows`:             false,
		`c:\programdata`:         false,
	}
	for _, dir := range dirs {
		lower := strings.ToLower(dir)
		if _, ok := want[lower]; ok {
			want[lower] = true
		}
	}
	for dir, found := range want {
		if !found {
			t.Fatalf("expected fallback system dir %q in %v", dir, dirs)
		}
	}
}

func TestIsSystemDeniedStillProtectsSystemDirsUnderDriveRootWorkspace(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific system directory policy")
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		t.Skip("SystemRoot is unavailable")
	}
	driveRoot := filepath.VolumeName(systemRoot) + string(os.PathSeparator)
	manager := &Manager{
		Root:              driveRoot,
		rootWithSep:       driveRoot,
		rootLower:         strings.ToLower(driveRoot),
		rootWithSepLower:  strings.ToLower(driveRoot),
		systemDirectories: systemDirsForPath(driveRoot),
	}
	if !manager.isSystemDenied(filepath.Join(systemRoot, "win.ini")) {
		t.Fatal("expected system directory under drive-root workspace to remain denied")
	}
}

func TestIsDeniedPathIgnoresWorkspaceAncestors(t *testing.T) {
	base := t.TempDir()
	wsRoot := filepath.Join(base, "node_modules", "my-workspace")
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manager, err := NewManagerFromRoot(wsRoot)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}
	if _, err := manager.ResolveWritePath("src/app.go"); err != nil {
		t.Fatalf("expected in-workspace path under ancestor node_modules to be allowed, got %v", err)
	}
	if _, err := manager.ResolveWritePath("node_modules/pkg/index.js"); err == nil {
		t.Fatal("expected nested node_modules inside workspace to remain denied")
	}
}
