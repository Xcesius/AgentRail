package patchmod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
)

func FuzzPatchParse(f *testing.F) {
	seeds := []string{
		"",
		"--- a/f.txt\n+++ b/f.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n",
		"--- a/f.sql\n+++ b/f.sql\n@@ -1,2 +1,2 @@\n--- comment\n+-- updated\n SELECT 1;\n",
		"--- a/f.txt\n+++ b/f.txt\n@@ -1,9223372036854775806 +1,9223372036854775806 @@\n",
		"--- /dev/null\n+++ /dev/null\n@@ -0,0 +0,0 @@\n",
		"--- a/f.txt\n+++ b/f.txt\n@@ -1,1 +1,1 @@\n-no-nl\n\\ No newline at end of file\n+with-nl\n",
		"diff --git a/x b/y\nrename from x\nrename to y\n",
		"--- a/u.txt\r\n+++ b/u.txt\r\n@@ -1,1 +1,2 @@\r\n こんにちは\r\n+🚀\r\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, diff string) {
		parsed, err := Parse(diff)
		if err != nil {
			return
		}
		for _, fp := range parsed.Files {
			for _, h := range fp.Hunks {
				if cap(h.Lines) > 1024 && len(h.Lines) < 512 {
					t.Fatalf("disproportionate hunk slice capacity: len=%d cap=%d", len(h.Lines), cap(h.Lines))
				}
			}
		}
	})
}

func FuzzBuildAndApplyRoundTrip(f *testing.F) {
	type seed struct{ orig, desired string }
	seeds := []seed{
		{"", "hello\n"},
		{"hello\n", ""},
		{"a\nb\nc\n", "a\nB\nc\n"},
		{"-- old\nSELECT 1;\n", "-- new\nSELECT 1;\n"},
		{"no-newline", "no-newline\n"},
		{"with-newline\n", "with-newline"},
		{"line1\nline2", "line1\nline2\nline3"},
		{"Grüße\n世界\n🚀", "Grüße\n世界\n✨\n"},
		{"a\n\nb\n", "a\n\nc\n"},
		{"--- header\n@@ fake @@\ndiff --git a b\n", "replaced\n"},
		{"0\n", "\n0"},
		{"a\nb\nc\n", "a\nb"},
		{"0\n0", "0"},
		{"0", "0\n0"},
	}
	for _, s := range seeds {
		f.Add(s.orig, s.desired)
	}
	f.Fuzz(func(t *testing.T, rawOrig, rawDesired string) {
		// Normalize LF and strip NUL/CR since unified diff text represents LF text lines.
		orig := strings.ReplaceAll(strings.ReplaceAll(rawOrig, "\x00", ""), "\r", "")
		desired := strings.ReplaceAll(strings.ReplaceAll(rawDesired, "\x00", ""), "\r", "")
		if orig == desired {
			return
		}

		diff := buildUnifiedDiff("target.txt", orig, true, desired)
		parsed, err := Parse(diff)
		if err != nil {
			t.Fatalf("Parse failed on generated diff:\norig=%q\ndesired=%q\ndiff=%q\nerr=%v", orig, desired, diff, err)
		}
		if len(parsed.Files) != 1 {
			t.Fatalf("expected 1 file patch, got %d", len(parsed.Files))
		}
		got, _, err := applyToContent(orig, parsed.Files[0])
		if err != nil {
			t.Fatalf("applyToContent failed:\norig=%q\ndesired=%q\ndiff=%q\nerr=%v", orig, desired, diff, err)
		}
		if got != desired {
			t.Fatalf("round-trip mismatch:\norig=%q\ndesired=%q\ngot=%q\ndiff=%q", orig, desired, got, diff)
		}
	})
}

func TestAtomicMultiFileRollbackMixedOpsRestoresExactState(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	modPath := filepath.Join(root, "mod.txt")
	delPath := filepath.Join(root, "del.txt")
	failPath := filepath.Join(root, "fail.txt")
	if err := os.WriteFile(modPath, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(mod): %v", err)
	}
	if err := os.WriteFile(delPath, []byte("gone\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(del): %v", err)
	}
	if err := os.WriteFile(failPath, []byte("stay\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(fail): %v", err)
	}

	origWrite := writeFileAtomicInRoot
	defer func() { writeFileAtomicInRoot = origWrite }()
	writeFileAtomicInRoot = func(rootPath, relPath string, data []byte, createDirs bool) (int, error) {
		if relPath == "fail.txt" {
			return 0, protocol.Err(protocol.CodePatchFailed, "injected commit failure")
		}
		return origWrite(rootPath, relPath, data, createDirs)
	}

	diff := strings.Join([]string{
		"--- /dev/null",
		"+++ b/newdir/nested/created.txt",
		"@@ -0,0 +1,1 @@",
		"+created",
		"--- a/mod.txt",
		"+++ b/mod.txt",
		"@@ -1,1 +1,1 @@",
		"-before",
		"+after",
		"--- a/del.txt",
		"+++ /dev/null",
		"@@ -1,1 +0,0 @@",
		"-gone",
		"--- a/fail.txt",
		"+++ b/fail.txt",
		"@@ -1,1 +1,1 @@",
		"-stay",
		"+changed",
		"",
	}, "\n")

	res, err := Apply(manager, diff, Options{Atomic: true})
	if err == nil {
		t.Fatal("expected atomic commit failure")
	}
	te, ok := protocol.AsToolError(err)
	if !ok || te.Code != protocol.CodeCommitFailed {
		t.Fatalf("expected commit_failed, got %v", err)
	}
	if res.RepositoryState != RepositoryStateUnchanged || len(res.FilesChanged) != 0 || res.HunksApplied != 0 {
		t.Fatalf("expected clean unchanged metadata after rollback, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "newdir")); !os.IsNotExist(err) {
		t.Fatalf("expected newly created empty parent directory to be cleaned up on rollback, got %v", err)
	}
	if got, _ := os.ReadFile(modPath); string(got) != "before\n" {
		t.Fatalf("mod.txt not restored: %q", string(got))
	}
	if got, _ := os.ReadFile(delPath); string(got) != "gone\n" {
		t.Fatalf("del.txt not restored: %q", string(got))
	}
	if got, _ := os.ReadFile(failPath); string(got) != "stay\n" {
		t.Fatalf("fail.txt unexpectedly mutated: %q", string(got))
	}
}

func BenchmarkPatchParseMultiHunk(b *testing.B) {
	var diff strings.Builder
	diff.WriteString("--- a/big.txt\n+++ b/big.txt\n")
	for i := 1; i <= 200; i++ {
		diff.WriteString("@@ -1,2 +1,2 @@\n-old line\n+new line\n context\n")
	}
	payload := diff.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(payload); err != nil {
			b.Fatalf("Parse: %v", err)
		}
	}
}

func BenchmarkBuildUnifiedDiffLargeFile(b *testing.B) {
	var orig, updated strings.Builder
	for i := 1; i <= 2000; i++ {
		if i == 1000 {
			orig.WriteString("-- old comment\n")
			updated.WriteString("-- new comment\n")
		} else {
			orig.WriteString("unchanged source code line\n")
			updated.WriteString("unchanged source code line\n")
		}
	}
	origStr := orig.String()
	updStr := updated.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildUnifiedDiff("large.sql", origStr, true, updStr)
	}
}
