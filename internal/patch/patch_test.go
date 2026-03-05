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

func TestDeletePatchRequiresTrulyEmptyResult(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("line\n \n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	diff := "--- a/file.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-line\n"
	result, err := Apply(manager, diff)
	if err == nil {
		t.Fatalf("expected delete patch failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].OK {
		t.Fatalf("expected failed file result, got %+v", result.Results)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected file to remain after failed delete, got %v", statErr)
	}
}

func TestDeletePatchRemovesFileWhenResultIsEmpty(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	diff := "--- a/file.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-line\n"
	result, err := Apply(manager, diff)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Results) != 1 || !result.Results[0].OK {
		t.Fatalf("expected successful file result, got %+v", result.Results)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected file to be deleted, got %v", statErr)
	}
}

func TestPatchReportsPartialApplyAcrossFiles(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	onePath := filepath.Join(root, "one.txt")
	twoPath := filepath.Join(root, "two.txt")
	if err := os.WriteFile(onePath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(one.txt): %v", err)
	}
	if err := os.WriteFile(twoPath, []byte("other\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(two.txt): %v", err)
	}

	diff := "--- a/one.txt\n+++ b/one.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n--- a/two.txt\n+++ b/two.txt\n@@ -1,1 +1,1 @@\n-missing\n+new\n"
	result, err := Apply(manager, diff)
	if err == nil {
		t.Fatalf("expected partial-apply failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected two file results, got %+v", result.Results)
	}
	if !result.Results[0].OK || result.Results[1].OK {
		t.Fatalf("unexpected per-file results: %+v", result.Results)
	}
	if len(result.FilesChanged) != 1 || result.FilesChanged[0] != "one.txt" {
		t.Fatalf("unexpected files_changed: %+v", result.FilesChanged)
	}
	if result.HunksApplied != 1 {
		t.Fatalf("expected one applied hunk, got %d", result.HunksApplied)
	}
	updated, readErr := os.ReadFile(onePath)
	if readErr != nil {
		t.Fatalf("ReadFile(one.txt): %v", readErr)
	}
	if string(updated) != "new\n" {
		t.Fatalf("expected first file to remain patched, got %q", string(updated))
	}
}
