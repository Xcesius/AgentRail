package filesmod

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agentrail/internal/workspace"
)

func BenchmarkFilesPaginationFullSweep(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 300; i++ {
		path := filepath.Join(root, fmt.Sprintf("dir-%02d", i%15), fmt.Sprintf("file-%03d.txt", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			b.Fatalf("WriteFile: %v", err)
		}
	}
	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		b.Fatalf("NewManagerFromRoot: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cursor := ""
		total := 0
		for {
			page, err := ListFilesPage(root, manager, 25, cursor)
			if err != nil {
				b.Fatalf("ListFilesPage: %v", err)
			}
			total += len(page.Paths)
			if !page.HasMore {
				break
			}
			cursor = page.NextCursor
		}
		if total != 300 {
			b.Fatalf("expected 300 files, got %d", total)
		}
	}
}
