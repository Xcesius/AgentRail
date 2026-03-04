package filesmod

import (
	"io/fs"
	"path/filepath"
	"sort"

	"codex-tool/internal/protocol"
	"codex-tool/internal/workspace"
)

func ListFiles(root string, manager *workspace.Manager) ([]string, error) {
	paths, err := CollectAbsoluteFiles(root, manager)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		result = append(result, manager.RelativePath(path))
	}
	sort.Strings(result)
	return result, nil
}

func CollectAbsoluteFiles(root string, manager *workspace.Manager) ([]string, error) {
	files := make([]string, 0, 256)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && manager.ShouldSkipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if manager.IsDeniedPath(path) {
			return nil
		}
		files = append(files, filepath.Clean(path))
		return nil
	})
	if err != nil {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
	}
	sort.Strings(files)
	return files, nil
}
