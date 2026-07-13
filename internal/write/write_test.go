package writemod

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestChmodModePreservesSpecialPermissionBits(t *testing.T) {
	mode := fs.FileMode(0o751) | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if got := chmodMode(mode); got != mode {
		t.Fatalf("chmodMode=%v want %v", got, mode)
	}
}

func TestWriteFileAtomicReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := WriteFileAtomic(path, []byte("new"), false); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("unexpected replacement: data=%q err=%v", data, err)
	}
}

func TestReplaceFailurePreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(dst, []byte("original"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := replaceFile(filepath.Join(dir, "missing-source.txt"), dst); err == nil {
		t.Fatal("expected replacement to fail")
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "original" {
		t.Fatalf("failed replacement damaged destination: data=%q err=%v", data, err)
	}
}

func TestWriteFileAtomicInRootCreatesNestedFile(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteFileAtomicInRoot(root, "nested/file.txt", []byte("content"), true); err != nil {
		t.Fatalf("WriteFileAtomicInRoot: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "nested", "file.txt"))
	if err != nil || string(data) != "content" {
		t.Fatalf("unexpected rooted write: data=%q err=%v", data, err)
	}
}

func TestWriteFileAtomicInRootRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("unable to create directory symlink: %v", err)
	}

	if _, err := WriteFileAtomicInRoot(root, "outside-link/escaped.txt", []byte("escape"), false); err == nil {
		t.Fatal("expected rooted write through escaping symlink to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rooted write escaped workspace: %v", err)
	}
}
