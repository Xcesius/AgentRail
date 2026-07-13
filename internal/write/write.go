package writemod

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"agentrail/internal/protocol"
)

func WriteFileAtomicInRoot(rootPath, relativePath string, content []byte, createDirs bool) (int, error) {
	return writeFileAtomicInRoot(rootPath, relativePath, content, createDirs, nil)
}

func WriteFileAtomicInRootWithMode(rootPath, relativePath string, content []byte, createDirs bool, mode fs.FileMode) (int, error) {
	return writeFileAtomicInRoot(rootPath, relativePath, content, createDirs, &mode)
}

func writeFileAtomicInRoot(rootPath, relativePath string, content []byte, createDirs bool, modeOverride *fs.FileMode) (int, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to open workspace root")
	}
	defer root.Close()

	relativePath = filepath.Clean(filepath.FromSlash(relativePath))
	dir := filepath.Dir(relativePath)
	if createDirs {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to create parent directory")
		}
	} else if info, err := root.Stat(dir); err != nil || !info.IsDir() {
		return 0, protocol.Err(protocol.CodeNotFound, "parent directory not found")
	}

	perm := fs.FileMode(0o644)
	if modeOverride != nil {
		perm = modeOverride.Perm()
	}
	if info, err := root.Stat(relativePath); err == nil {
		if info.IsDir() {
			return 0, protocol.Err(protocol.CodeInvalidRequest, "target path is a directory")
		}
		if modeOverride == nil {
			perm = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to inspect target file")
	}

	dirRoot, err := root.OpenRoot(dir)
	if err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to open parent directory")
	}
	defer dirRoot.Close()

	tmp, tmpName, err := createRootTemp(dirRoot)
	if err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, "unable to create temp file")
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = dirRoot.Remove(tmpName)
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
	if err := dirRoot.Rename(tmpName, filepath.Base(relativePath)); err != nil {
		return 0, protocol.Err(protocol.CodeInvalidRequest, fmt.Sprintf("unable to replace target file: %v", err))
	}
	cleanup = false
	syncRootBestEffort(dirRoot)
	return len(content), nil
}

func RemoveFileInRoot(rootPath, relativePath string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(filepath.Clean(filepath.FromSlash(relativePath)))
}

func createRootTemp(root *os.Root) (*os.File, string, error) {
	var random [8]byte
	for range 16 {
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := ".agentrail-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("unable to allocate unique temp filename")
}

func syncRootBestEffort(root *os.Root) {
	h, err := root.Open(".")
	if err != nil {
		return
	}
	defer h.Close()
	_ = h.Sync()
}

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

func syncDirBestEffort(dir string) {
	h, err := os.Open(dir)
	if err != nil {
		return
	}
	defer h.Close()
	_ = h.Sync()
}
