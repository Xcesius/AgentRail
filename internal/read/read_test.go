package readmod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentrail/internal/protocol"
)

func TestReadRangeReportsContinuation(t *testing.T) {
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
	if !result.HasMore || result.NextStartLine != 4 {
		t.Fatalf("expected continuation at line 4, got has_more=%v next_start_line=%d", result.HasMore, result.NextStartLine)
	}
}

func TestReadCRLFMaxBytesStopsOnLineBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.txt")
	content := "one\r\ntwo\r\nthree\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	limit := int64(len("one\r\ntwo\r\n"))
	result, err := ReadFile(path, Options{StartLine: 1, MaxBytes: limit})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if result.Content != "one\r\ntwo\r\n" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if !result.Truncated {
		t.Fatalf("expected truncation at the byte cap")
	}
	if !result.HasMore || result.NextStartLine != 3 {
		t.Fatalf("expected continuation at line 3, got has_more=%v next_start_line=%d", result.HasMore, result.NextStartLine)
	}
	if result.EndLine != 2 {
		t.Fatalf("expected end_line 2, got %d", result.EndLine)
	}
}

func TestReadExactMaxBytesIsNotTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.txt")
	content := "123\r\n456\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := ReadFile(path, Options{StartLine: 1, MaxBytes: int64(len(content))})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if result.Truncated {
		t.Fatalf("expected exact-size read to avoid truncation")
	}
	if result.HasMore || result.NextStartLine != 0 {
		t.Fatalf("expected no continuation, got has_more=%v next_start_line=%d", result.HasMore, result.NextStartLine)
	}
	if result.Content != content {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestReadTooLargeWhenFirstSelectedLineExceedsMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large-line.txt")
	content := "skip\nselected line is too long\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ReadFile(path, Options{StartLine: 2, MaxBytes: 8})
	if err == nil {
		t.Fatalf("expected too_large error")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeTooLarge {
		t.Fatalf("expected too_large, got %v", err)
	}
}

func TestReadLargeLineOver64KiB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.txt")
	longLine := strings.Repeat("a", 70*1024) + "\n"
	content := longLine + "tail\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := ReadFile(path, Options{StartLine: 1, MaxBytes: int64(len(content))})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if result.Content != content {
		t.Fatalf("unexpected content length: got %d want %d", len(result.Content), len(content))
	}
	if result.Truncated || result.HasMore || result.NextStartLine != 0 {
		t.Fatalf("expected a complete page, got truncated=%v has_more=%v next_start_line=%d", result.Truncated, result.HasMore, result.NextStartLine)
	}
}

func TestReadAcceptsUTF8Text(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf8.txt")
	content := "Grüße 世界\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := ReadFile(path, Options{})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if result.Content != content {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestReadRejectsUTF16LEBOMAsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf16.txt")
	data := []byte{0xFF, 0xFE, 0x68, 0x00, 0x69, 0x00, 0x0A, 0x00}
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

func TestReadStartLineBeyondEOFReturnsEmptyPage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.txt")
	content := "line1\nline2\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := ReadFile(path, Options{StartLine: 5, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected empty content, got %q", result.Content)
	}
	if result.StartLine != 5 || result.EndLine != 4 {
		t.Fatalf("unexpected line range: %d-%d", result.StartLine, result.EndLine)
	}
	if result.Truncated || result.HasMore || result.NextStartLine != 0 {
		t.Fatalf("expected no continuation, got truncated=%v has_more=%v next_start_line=%d", result.Truncated, result.HasMore, result.NextStartLine)
	}
}
