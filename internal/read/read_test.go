package readmod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentrail/internal/protocol"
)

func TestReadPartialLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	content := "line1\nline2\nline3\nline4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := ReadFile(path, Options{StartLine: 2, EndLine: 3, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if result.Content != "line2\nline3\n" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if result.StartLine != 2 || result.EndLine != 3 {
		t.Fatalf("unexpected line range: %d-%d", result.StartLine, result.EndLine)
	}
	if result.Truncated {
		t.Fatalf("expected non-truncated result")
	}
}

func TestReadMaxBytesTruncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	content := strings.Repeat("abcdef\n", 500)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := ReadFile(path, Options{StartLine: 1, MaxBytes: 64})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !result.Truncated {
		t.Fatalf("expected truncation")
	}
	if len(result.Content) != 64 {
		t.Fatalf("expected 64 bytes, got %d", len(result.Content))
	}
}

func TestReadRejectsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	data := []byte{'a', 'b', 0, 'c'}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ReadFile(path, Options{})
	if err == nil {
		t.Fatalf("expected binary file error")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeBinaryFile {
		t.Fatalf("expected binary_file, got %v", err)
	}
}
