package workspace

import (
	"os"
	"path/filepath"
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
