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

func TestResolveWritePathRejectsDeniedDirectories(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	for _, input := range []string{".git/config", "node_modules/pkg/index.js"} {
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
