package workspace

import (
	"path/filepath"
	"testing"
)

func BenchmarkWorkspaceDisplayAndDeny(b *testing.B) {
	root := b.TempDir()
	manager, err := NewManagerFromRoot(root)
	if err != nil {
		b.Fatalf("NewManagerFromRoot: %v", err)
	}
	sample := filepath.Join(root, "internal", "search", "search.go")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = manager.DisplayPath(sample)
		_ = manager.IsDeniedPath(sample)
	}
}
