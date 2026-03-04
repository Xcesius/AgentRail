package patchmod

import (
	"os"
	"path/filepath"
	"testing"

	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
)

func TestPatchContextMismatch(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	diff := "--- a/file.txt\n+++ b/file.txt\n@@ -1,2 +1,2 @@\n-HELLO\n+hello\n world\n"
	result, err := Apply(manager, diff)
	if err == nil {
		t.Fatalf("expected patch failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].OK {
		t.Fatalf("expected one failed file result, got %+v", result.Results)
	}
}
