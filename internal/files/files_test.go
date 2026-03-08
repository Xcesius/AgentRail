package filesmod

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
)

func TestListFilesPagePagination(t *testing.T) {
	root := t.TempDir()
	for rel := range map[string]string{
		"a.txt":        "a",
		"b.txt":        "b",
		"nested/c.txt": "c",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", rel, err)
		}
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	page1, err := ListFilesPage(root, manager, 2, "")
	if err != nil {
		t.Fatalf("ListFilesPage page1: %v", err)
	}
	if !reflect.DeepEqual(page1.Paths, []string{"a.txt", "b.txt"}) {
		t.Fatalf("unexpected first page: %+v", page1)
	}
	if !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("expected first page continuation, got %+v", page1)
	}

	page2, err := ListFilesPage(root, manager, 2, page1.NextCursor)
	if err != nil {
		t.Fatalf("ListFilesPage page2: %v", err)
	}
	if !reflect.DeepEqual(page2.Paths, []string{"nested/c.txt"}) {
		t.Fatalf("unexpected second page: %+v", page2)
	}
	if page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("expected final page, got %+v", page2)
	}
}

func TestListFilesPageRejectsInvalidCursor(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	_, err = ListFilesPage(root, manager, 1, "not-a-cursor")
	if err == nil {
		t.Fatalf("expected invalid cursor error")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeCursorInvalid {
		t.Fatalf("expected cursor_invalid, got %v", err)
	}
	if te.Details["field"] != "cursor" {
		t.Fatalf("expected cursor field details, got %+v", te.Details)
	}
}

func TestListFilesPageRejectsStaleCursor(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.txt", "b.txt", "c.txt"} {
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", rel, err)
		}
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	page1, err := ListFilesPage(root, manager, 2, "")
	if err != nil {
		t.Fatalf("ListFilesPage page1: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	_, err = ListFilesPage(root, manager, 2, page1.NextCursor)
	if err == nil {
		t.Fatalf("expected stale cursor error")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeCursorStale {
		t.Fatalf("expected cursor_stale, got %v", err)
	}
	if te.Details["field"] != "cursor" {
		t.Fatalf("expected cursor field details, got %+v", te.Details)
	}
}

func TestListFilesPageRejectsRootMismatchedCursor(t *testing.T) {
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	for _, item := range []struct{ root, name string }{{rootOne, "a.txt"}, {rootOne, "b.txt"}, {rootTwo, "a.txt"}, {rootTwo, "b.txt"}} {
		path := filepath.Join(item.root, item.name)
		if err := os.WriteFile(path, []byte(item.name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", item.name, err)
		}
	}

	managerOne, err := workspace.NewManagerFromRoot(rootOne)
	if err != nil {
		t.Fatalf("NewManagerFromRoot(rootOne): %v", err)
	}
	managerTwo, err := workspace.NewManagerFromRoot(rootTwo)
	if err != nil {
		t.Fatalf("NewManagerFromRoot(rootTwo): %v", err)
	}

	page, err := ListFilesPage(rootOne, managerOne, 1, "")
	if err != nil {
		t.Fatalf("ListFilesPage page1: %v", err)
	}
	_, err = ListFilesPage(rootTwo, managerTwo, 1, page.NextCursor)
	if err == nil {
		t.Fatalf("expected root mismatch cursor error")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeCursorInvalid {
		t.Fatalf("expected cursor_invalid, got %v", err)
	}
	if te.Details["reason"] != "root_mismatch" {
		t.Fatalf("expected root_mismatch detail, got %+v", te.Details)
	}
}

func TestListFilesCursorEncodesCanonicalRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	page, err := ListFilesPage(root, manager, 1, "")
	if err != nil {
		t.Fatalf("ListFilesPage: %v", err)
	}
	payload, err := decodeCursor(page.NextCursor)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if payload.Root != canonicalCursorRoot(root) {
		t.Fatalf("expected canonical cursor root %q, got %q", canonicalCursorRoot(root), payload.Root)
	}
}
