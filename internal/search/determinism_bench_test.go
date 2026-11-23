package searchmod

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
)

func buildTestCorpus(tb testing.TB, numFiles, linesPerFile int) (string, *workspace.Manager) {
	tb.Helper()
	root := tb.TempDir()
	for i := 0; i < numFiles; i++ {
		ext := ".txt"
		if i%5 == 0 {
			ext = ".go"
		}
		dir := filepath.Join(root, fmt.Sprintf("pkg-%02d", i%10))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatalf("MkdirAll: %v", err)
		}
		var b strings.Builder
		for line := 1; line <= linesPerFile; line++ {
			if (i+line)%17 == 0 {
				fmt.Fprintf(&b, "line %d: Grüße NEEDLE_TOKEN alpha needle_token\n", line)
			} else {
				fmt.Fprintf(&b, "line %d: ordinary source code payload without match\n", line)
			}
		}
		path := filepath.Join(dir, fmt.Sprintf("file-%03d%s", i, ext))
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			tb.Fatalf("WriteFile: %v", err)
		}
	}
	// Include a binary file to verify skipping under concurrency.
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte{'N', 'E', 'E', 'D', 'L', 'E', 0, 1, 2}, 0o644); err != nil {
		tb.Fatalf("WriteFile(blob): %v", err)
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		tb.Fatalf("NewManagerFromRoot: %v", err)
	}
	return root, manager
}

func TestDeterministicParallelSearchIdenticalAcrossWorkerCounts(t *testing.T) {
	root, manager := buildTestCorpus(t, 60, 40)

	configs := []struct {
		name string
		opts Options
	}{
		{"literal_ci", Options{Query: "needle_token", Root: root, CaseSensitive: false, Deterministic: true}},
		{"literal_cs", Options{Query: "NEEDLE_TOKEN", Root: root, CaseSensitive: true, Deterministic: true}},
		{"regex", Options{Query: `needle_[a-z]+`, Root: root, Regex: true, CaseSensitive: false, Deterministic: true}},
		{"limited", Options{Query: "needle_token", Root: root, Limit: 23, Deterministic: true}},
		{"glob_recursive", Options{Query: "needle_token", Root: root, Glob: "**/*.go", Deterministic: true}},
	}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			baseline, err := searchWithWorkerCount(context.Background(), manager, cfg.opts, 1)
			if err != nil {
				t.Fatalf("baseline search failed: %v", err)
			}
			if len(baseline) == 0 {
				t.Fatalf("expected non-empty baseline matches for %s", cfg.name)
			}
			wantJSON, err := json.Marshal(baseline)
			if err != nil {
				t.Fatalf("Marshal baseline: %v", err)
			}

			for _, workers := range []int{1, 2, 4, 8} {
				for rep := 0; rep < 8; rep++ {
					got, err := searchWithWorkerCount(context.Background(), manager, cfg.opts, workers)
					if err != nil {
						t.Fatalf("workers=%d rep=%d err=%v", workers, rep, err)
					}
					gotJSON, err := json.Marshal(got)
					if err != nil {
						t.Fatalf("Marshal got: %v", err)
					}
					if string(gotJSON) != string(wantJSON) {
						t.Fatalf("non-deterministic output for workers=%d rep=%d", workers, rep)
					}
				}
			}
		})
	}
}

func TestDeterministicParallelSearchCancellationReturnsErrorNotPartial(t *testing.T) {
	root, manager := buildTestCorpus(t, 40, 30)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, workers := range []int{1, 2, 4, 8} {
		matches, err := searchWithWorkerCount(ctx, manager, Options{
			Query:         "needle_token",
			Root:          root,
			Deterministic: true,
		}, workers)
		if err == nil {
			t.Fatalf("workers=%d: expected cancellation error", workers)
		}
		te, ok := protocol.AsToolError(err)
		if !ok || te.Code != protocol.CodeSearchError {
			t.Fatalf("workers=%d: expected search_error, got %v", workers, err)
		}
		if len(matches) != 0 {
			t.Fatalf("workers=%d: expected nil/empty matches on cancel, got %d", workers, len(matches))
		}
	}
}

func BenchmarkSearchDeterministicLiteral(b *testing.B) {
	root, manager := buildTestCorpus(b, 200, 100)
	opts := Options{Query: "needle_token", Root: root, Deterministic: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches, err := Search(context.Background(), manager, opts)
		if err != nil || len(matches) == 0 {
			b.Fatalf("Search: len=%d err=%v", len(matches), err)
		}
	}
}

func BenchmarkSearchDeterministicGlob(b *testing.B) {
	root, manager := buildTestCorpus(b, 200, 100)
	opts := Options{Query: "needle_token", Root: root, Glob: "**/*.go", Deterministic: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches, err := Search(context.Background(), manager, opts)
		if err != nil || len(matches) == 0 {
			b.Fatalf("Search: len=%d err=%v", len(matches), err)
		}
	}
}
