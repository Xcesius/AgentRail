package patchmod

import (
	"os"
	"path/filepath"
	"strings"
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
	result, err := Apply(manager, diff, Options{})
	if err == nil {
		t.Fatalf("expected patch failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
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
	result, err := Apply(manager, diff, Options{})
	if err == nil {
		t.Fatalf("expected delete patch failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
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
	result, err := Apply(manager, diff, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.RepositoryState != RepositoryStateChanged {
		t.Fatalf("expected changed repository state, got %q", result.RepositoryState)
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
	result, err := Apply(manager, diff, Options{})
	if err == nil {
		t.Fatalf("expected partial-apply failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if result.RepositoryState != RepositoryStatePartiallyChange {
		t.Fatalf("expected partially_changed repository state, got %q", result.RepositoryState)
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

func TestPatchTokenMismatch(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	diff := "--- a/file.txt\n+++ b/file.txt\n@@ -1,1 +1,1 @@\n-hello\n+world\n"
	result, err := Apply(manager, diff, Options{ExpectedFileTokens: map[string]string{"file.txt": "sha256:deadbeef"}})
	if err == nil {
		t.Fatalf("expected token mismatch")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeTokenMismatch {
		t.Fatalf("expected token_mismatch, got %v", err)
	}
	if te.Details["path"] != "file.txt" || te.Details["expected_file_token"] != "sha256:deadbeef" {
		t.Fatalf("expected machine-readable token details, got %+v", te.Details)
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
	}
	if len(result.Results) != 1 || result.Results[0].ErrorCode != protocol.CodeTokenMismatch {
		t.Fatalf("expected token mismatch result, got %+v", result.Results)
	}
}

func TestPatchResolveFailureUsesCanonicalResultPath(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	expected := manager.DisplayPath(outside)
	diff := `--- a/../outside.txt
+++ b/../outside.txt
@@ -0,0 +1,1 @@
+hello
`
	result, err := Apply(manager, diff, Options{})
	if err == nil {
		t.Fatalf("expected path_denied failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePathDenied {
		t.Fatalf("expected path_denied, got %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Path != expected {
		t.Fatalf("expected canonical result path %q, got %+v", expected, result.Results)
	}
	if result.Results[0].ErrorDetails["path"] != expected {
		t.Fatalf("expected canonical error details, got %+v", result.Results[0].ErrorDetails)
	}
}

func TestAtomicPatchValidationFailurePerformsZeroWrites(t *testing.T) {
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
	result, err := Apply(manager, diff, Options{Atomic: true})
	if err == nil {
		t.Fatalf("expected atomic validation failure")
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
	}
	updated, readErr := os.ReadFile(onePath)
	if readErr != nil {
		t.Fatalf("ReadFile(one.txt): %v", readErr)
	}
	if string(updated) != "old\n" {
		t.Fatalf("expected first file to remain unchanged, got %q", string(updated))
	}
}

func TestPatchNoOpIsSuccessfulAndUnchanged(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	diff := "--- a/file.txt\n+++ b/file.txt\n@@ -1,1 +1,1 @@\n line\n"
	result, err := Apply(manager, diff, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
	}
	if len(result.FilesChanged) != 0 {
		t.Fatalf("expected no changed files, got %+v", result.FilesChanged)
	}
	if len(result.Results) != 1 || !result.Results[0].OK || result.Results[0].Changed {
		t.Fatalf("expected successful no-op result, got %+v", result.Results)
	}
	if result.HunksApplied != 1 {
		t.Fatalf("expected one accepted hunk, got %d", result.HunksApplied)
	}
}

func TestPatchHunkOnlyDiffExplainsMissingFileHeaders(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	diff := `@@ -1,1 +1,1 @@
-old
+new
`
	result, err := Apply(manager, diff, Options{})
	if err == nil {
		t.Fatalf("expected patch failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodePatchFailed {
		t.Fatalf("expected patch_failed, got %v", err)
	}
	if !strings.Contains(te.Message, "no file headers") {
		t.Fatalf("expected missing file headers message, got %q", te.Message)
	}
	if te.Details["field"] != "diff" || te.Details["reason"] != "missing_file_headers" {
		t.Fatalf("expected machine-readable diff details, got %+v", te.Details)
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
	}
}

func TestAtomicPatchCommitFailureWithRollbackReportsCommitFailed(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	onePath := filepath.Join(root, "one.txt")
	twoPath := filepath.Join(root, "two.txt")
	if err := os.WriteFile(onePath, []byte("old1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(one.txt): %v", err)
	}
	if err := os.WriteFile(twoPath, []byte("old2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(two.txt): %v", err)
	}

	originalWrite := writeFileAtomic
	defer func() { writeFileAtomic = originalWrite }()
	callCount := 0
	writeFileAtomic = func(path string, data []byte, createDirs bool) (int, error) {
		callCount++
		switch callCount {
		case 1:
			return originalWrite(path, data, createDirs)
		case 2:
			return 0, protocol.Err(protocol.CodePatchFailed, "simulated commit failure")
		default:
			return originalWrite(path, data, createDirs)
		}
	}

	diff := "--- a/one.txt\n+++ b/one.txt\n@@ -1,1 +1,1 @@\n-old1\n+new1\n--- a/two.txt\n+++ b/two.txt\n@@ -1,1 +1,1 @@\n-old2\n+new2\n"
	result, err := Apply(manager, diff, Options{Atomic: true})
	if err == nil {
		t.Fatalf("expected commit failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeCommitFailed {
		t.Fatalf("expected commit_failed, got %v", err)
	}
	if result.RepositoryState != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %q", result.RepositoryState)
	}
	if te.Details["repository_state"] != RepositoryStateUnchanged {
		t.Fatalf("expected unchanged details, got %+v", te.Details)
	}
	oneBytes, _ := os.ReadFile(onePath)
	twoBytes, _ := os.ReadFile(twoPath)
	if string(oneBytes) != "old1\n" || string(twoBytes) != "old2\n" {
		t.Fatalf("expected rollback to restore files, got one=%q two=%q", string(oneBytes), string(twoBytes))
	}
}

func TestAtomicPatchRollbackFailureReportsPartiallyChanged(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	onePath := filepath.Join(root, "one.txt")
	twoPath := filepath.Join(root, "two.txt")
	if err := os.WriteFile(onePath, []byte("old1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(one.txt): %v", err)
	}
	if err := os.WriteFile(twoPath, []byte("old2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(two.txt): %v", err)
	}

	originalWrite := writeFileAtomic
	defer func() { writeFileAtomic = originalWrite }()
	callCount := 0
	writeFileAtomic = func(path string, data []byte, createDirs bool) (int, error) {
		callCount++
		switch callCount {
		case 1:
			return originalWrite(path, data, createDirs)
		case 2:
			return 0, protocol.Err(protocol.CodePatchFailed, "simulated commit failure")
		case 3:
			return 0, protocol.Err(protocol.CodePatchFailed, "simulated rollback failure")
		default:
			return originalWrite(path, data, createDirs)
		}
	}

	diff := "--- a/one.txt\n+++ b/one.txt\n@@ -1,1 +1,1 @@\n-old1\n+new1\n--- a/two.txt\n+++ b/two.txt\n@@ -1,1 +1,1 @@\n-old2\n+new2\n"
	result, err := Apply(manager, diff, Options{Atomic: true})
	if err == nil {
		t.Fatalf("expected rollback failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeRollbackFailed {
		t.Fatalf("expected rollback_failed, got %v", err)
	}
	if result.RepositoryState != RepositoryStatePartiallyChange {
		t.Fatalf("expected partially_changed repository state, got %q", result.RepositoryState)
	}
	if te.Details["repository_state"] != RepositoryStatePartiallyChange {
		t.Fatalf("expected partially_changed details, got %+v", te.Details)
	}
	oneBytes, _ := os.ReadFile(onePath)
	twoBytes, _ := os.ReadFile(twoPath)
	if string(oneBytes) != "new1\n" || string(twoBytes) != "old2\n" {
		t.Fatalf("expected partial rollback state, got one=%q two=%q", string(oneBytes), string(twoBytes))
	}
}
