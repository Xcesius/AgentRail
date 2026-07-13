//go:build !windows

package writemod

import "os"

func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}
