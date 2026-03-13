package patchmod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentrail/internal/workspace"
)

func TestBuildFilePatchExistingFileAppliesCleanly(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("old\nvalue\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	generated, err := BuildFilePatch(manager, "sample.txt", "new\nvalue\n", "")
	if err != nil {
		t.Fatalf("BuildFilePatch: %v", err)
	}
	if !generated.Changed {
		t.Fatalf("expected changed patch, got %+v", generated)
	}
	result, err := Apply(manager, generated.Diff, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.RepositoryState != RepositoryStateChanged {
		t.Fatalf("expected changed repository state, got %+v", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(updated) != "new\nvalue\n" {
		t.Fatalf("expected updated file, got %q", string(updated))
	}
}

func TestBuildFilePatchNewFileAppliesCleanly(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	generated, err := BuildFilePatch(manager, "created.txt", "created\n", "")
	if err != nil {
		t.Fatalf("BuildFilePatch: %v", err)
	}
	if !generated.Changed {
		t.Fatalf("expected changed patch, got %+v", generated)
	}
	result, err := Apply(manager, generated.Diff, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.RepositoryState != RepositoryStateChanged {
		t.Fatalf("expected changed repository state, got %+v", result)
	}
	updated, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(updated) != "created\n" {
		t.Fatalf("expected created file, got %q", string(updated))
	}
}

func TestBuildFilePatchUnchangedReturnsEmptyDiff(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("same\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	generated, err := BuildFilePatch(manager, "sample.txt", "same\n", "")
	if err != nil {
		t.Fatalf("BuildFilePatch: %v", err)
	}
	if generated.Changed {
		t.Fatalf("expected unchanged patch, got %+v", generated)
	}
	if generated.Diff != "" {
		t.Fatalf("expected empty diff, got %q", generated.Diff)
	}
	if generated.FileToken == "" {
		t.Fatalf("expected file token, got %+v", generated)
	}
}

func TestBuildFilePatchEmitsExactLineCounts(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	generated, err := BuildFilePatch(manager, "sample.txt", "three\n", "")
	if err != nil {
		t.Fatalf("BuildFilePatch: %v", err)
	}
	if !strings.Contains(generated.Diff, "@@ -1,2 +1,1 @@") {
		t.Fatalf("expected exact hunk header, got %q", generated.Diff)
	}
}

func TestBuildFilePatchSupportsTrailingNewlineChanges(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("line"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	generated, err := BuildFilePatch(manager, "sample.txt", "line\n", "")
	if err != nil {
		t.Fatalf("BuildFilePatch: %v", err)
	}
	if _, err := Apply(manager, generated.Diff, Options{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(updated) != "line\n" {
		t.Fatalf("expected trailing newline to be added, got %q", string(updated))
	}

	generated, err = BuildFilePatch(manager, "sample.txt", "line", "")
	if err != nil {
		t.Fatalf("BuildFilePatch second pass: %v", err)
	}
	if _, err := Apply(manager, generated.Diff, Options{}); err != nil {
		t.Fatalf("Apply second pass: %v", err)
	}
	updated, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile second pass: %v", err)
	}
	if string(updated) != "line" {
		t.Fatalf("expected trailing newline to be removed, got %q", string(updated))
	}
}
