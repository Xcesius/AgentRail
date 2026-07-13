//go:build windows

package writemod

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32ReplaceFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")
)

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
