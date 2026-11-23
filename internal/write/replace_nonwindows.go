//go:build !windows

package writemod

import "os"

func renameInDirRoot(_ *os.Root, dirRoot *os.Root, tmpName, dstName string) error {
	return dirRoot.Rename(tmpName, dstName)
}

func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}
