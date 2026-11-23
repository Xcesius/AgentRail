package searchmod

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentrail/internal/protocol"
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

func TestSearchRejectsInvalidGlob(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	_, err = Search(context.Background(), manager, Options{Query: "needle", Glob: "["})
	if err == nil {
		t.Fatal("expected invalid glob error")
	}
}

func TestSearchRejectsNegativeMaxFileBytes(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	_, err = Search(context.Background(), manager, Options{Query: "needle", MaxFileBytes: -1})
	if err == nil {
		t.Fatal("expected negative max_file_bytes error")
	}
}

func TestDeterministicLimitUsesDisplayPathOrderingAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}
	for name := range map[string]struct{}{"a/z.txt": {}, "a0.txt": {}} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	matches, err := Search(context.Background(), manager, Options{Query: "needle", Limit: 1, Deterministic: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Path != "a/z.txt" {
		t.Fatalf("expected display-path-first match, got %+v", matches)
	}
}

func TestSearchHugeLimitDoesNotPanic(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}
	limit := int(^uint(0) >> 1)
	matches, err := Search(context.Background(), manager, Options{Query: "needle", Limit: limit, Deterministic: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %+v", matches)
	}
}

func TestSearchRejectsFileSymlinkEscapeWithoutOptIn(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("external-needle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}
	_, err = Search(context.Background(), manager, Options{Query: "external-needle", Deterministic: true})
	if err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	toolErr, ok := protocol.AsToolError(err)
	if !ok || toolErr.Code != protocol.CodePathDenied {
		t.Fatalf("expected path_denied, got %v", err)
	}
}

func TestSearchGlobSupportsRecursiveDoubleStarAndBackslashes(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"top.go", "nested/sub/app.go", "nested/sub/app.txt"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte("needle\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	matches, err := Search(context.Background(), manager, Options{Query: "needle", Glob: "**/*.go", Deterministic: true})
	if err != nil {
		t.Fatalf("Search **/*.go: %v", err)
	}
	if len(matches) != 2 || matches[0].Path != "nested/sub/app.go" || matches[1].Path != "top.go" {
		t.Fatalf("unexpected **/*.go matches: %+v", matches)
	}

	matches, err = Search(context.Background(), manager, Options{Query: "needle", Glob: `nested\sub\*.go`, Deterministic: true})
	if err != nil {
		t.Fatalf("Search backslash glob: %v", err)
	}
	if len(matches) != 1 || matches[0].Path != "nested/sub/app.go" {
		t.Fatalf("unexpected backslash glob matches: %+v", matches)
	}
}

func TestSearchCaseInsensitiveUnicodeColumnOffsetIsExact(t *testing.T) {
	root := t.TempDir()
	// U+0130 ('İ') is 2 UTF-8 bytes, but bytes.ToLower expands it to 3 bytes.
	line := "İ needle\n"
	if err := os.WriteFile(filepath.Join(root, "uni.txt"), []byte(line), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	matches, err := Search(context.Background(), manager, Options{Query: "NEEDLE", CaseSensitive: false, Deterministic: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// 'İ' = 2 bytes, ' ' = 1 byte => 'needle' starts at byte index 3 (1-based col 4).
	if len(matches) != 1 || matches[0].Col != 4 {
		t.Fatalf("expected col=4, got %+v", matches)
	}
}

func TestBoundedDoesNotOverTruncateAfterInvalidUTF8Byte(t *testing.T) {
	// Single Latin-1 byte at index 2 followed by 600 ASCII chars.
	raw := "ab\xe9" + string(make([]byte, 600))
	buf := []byte(raw)
	for i := 3; i < len(buf); i++ {
		buf[i] = 'x'
	}
	got := bounded(string(buf), 512)
	if len(got) != 512 {
		t.Fatalf("expected bounded length 512, got %d", len(got))
	}
}

func TestSearchAllowsWhitespaceQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ws.txt"), []byte("a  b\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}
	matches, err := Search(context.Background(), manager, Options{Query: "  ", Deterministic: true})
	if err != nil {
		t.Fatalf("Search whitespace: %v", err)
	}
	if len(matches) != 1 || matches[0].Col != 2 {
		t.Fatalf("unexpected whitespace match: %+v", matches)
	}
}
