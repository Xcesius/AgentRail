package searchmod

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentrail/internal/workspace"
)

func TestSearchFindsUTF8TextAndSortsDeterministically(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	files := map[string]string{
		"z.txt": "needle later\n",
		"a.txt": "Grüße needle\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	matches, err := Search(context.Background(), manager, Options{
		Query:         "needle",
		Root:          root,
		Deterministic: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].Path != "a.txt" || matches[1].Path != "z.txt" {
		t.Fatalf("unexpected match order: %+v", matches)
	}
}

func TestSearchSkipsBinaryFilesAndHonorsLimit(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle one\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("needle two\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'n', 0, 'x'}, 0o644); err != nil {
		t.Fatalf("WriteFile(binary.bin): %v", err)
	}

	matches, err := Search(context.Background(), manager, Options{
		Query:         "needle",
		Root:          root,
		Limit:         1,
		Deterministic: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Path != "a.txt" {
		t.Fatalf("expected deterministic first match from a.txt, got %+v", matches[0])
	}
}
