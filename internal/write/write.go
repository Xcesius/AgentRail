package writemod

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"agentrail/internal/protocol"
)

func WriteFileAtomic(path string, content []byte, createDirs bool) (int, error) {
	dir := filepath.Dir(path)
	if createDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to create parent directory")
		}
	} else {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return 0, protocol.Err(protocol.CodeNotFound, "parent directory not found")
		}
	}

	perm := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to inspect target file")
	}

	tmp, err := os.CreateTemp(dir, ".agentrail-*")
	if err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to create temp file")
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to write temp file")
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to set temp file permissions")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to sync temp file")
	}
	if err := tmp.Close(); err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to close temp file")
	}

	if err := replaceFile(tmpPath, path); err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, fmt.Sprintf("unable to replace target file: %v", err))
	}
	cleanup = false

	syncDirBestEffort(dir)
	return len(content), nil
}

func replaceFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Rename(src, dst)
	}
	return os.Rename(src, dst)
}

func syncDirBestEffort(dir string) {
	h, err := os.Open(dir)
	if err != nil {
		return
	}
	defer h.Close()
	_ = h.Sync()
}
