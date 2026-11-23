//go:build windows

package writemod

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL                 = syscall.NewLazyDLL("kernel32.dll")
	kernel32ReplaceFileW        = kernel32DLL.NewProc("ReplaceFileW")
	kernel32GetFinalPathByHandle = kernel32DLL.NewProc("GetFinalPathNameByHandleW")
)

const errorInvalidHandle = syscall.Errno(6)

func renameInDirRoot(dirRoot *os.Root, tmpName, dstName string) error {
	err := dirRoot.Rename(tmpName, dstName)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errorInvalidHandle) {
		return err
	}

	// Some Windows filesystem minifilters/sandboxes reject NtSetInformationFile
	// with a RootDirectory handle (ERROR_INVALID_HANDLE). Resolve the canonical
	// path directly from our already-validated dirRoot handle and use ReplaceFileW
	// / os.Rename on that verified directory.
	realDir, dirErr := finalPathForRoot(dirRoot)
	if dirErr != nil {
		return err
	}
	if info, statErr := dirRoot.Lstat(dstName); statErr == nil {
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return err
	}
	return replaceFile(filepath.Join(realDir, tmpName), filepath.Join(realDir, dstName))
}

func finalPathForRoot(dirRoot *os.Root) (string, error) {
	dirFile, err := dirRoot.Open(".")
	if err != nil {
		return "", err
	}
	defer dirFile.Close()

	buf := make([]uint16, 260)
	for {
		n, _, callErr := kernel32GetFinalPathByHandle.Call(
			dirFile.Fd(),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			0, // FILE_NAME_NORMALIZED | VOLUME_NAME_DOS
		)
		if n == 0 {
			return "", callErr
		}
		if int(n) < len(buf) {
			resolved := syscall.UTF16ToString(buf[:n])
			resolved = strings.TrimPrefix(resolved, `\\?\`)
			return resolved, nil
		}
		buf = make([]uint16, n+1)
	}
}

func replaceFile(src, dst string) error {
	if _, err := os.Lstat(dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.Rename(src, dst)
		}
		return err
	}

	dstPtr, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	srcPtr, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	r1, _, callErr := kernel32ReplaceFileW.Call(
		uintptr(unsafe.Pointer(dstPtr)),
		uintptr(unsafe.Pointer(srcPtr)),
		0,
		0,
		0,
		0,
	)
	if r1 == 0 {
		return callErr
	}
	return nil
}
