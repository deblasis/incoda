//go:build unix

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// syncDir fsyncs the directory holding path, so the rename that put path
// in place survives a crash. Some filesystems refuse to fsync a directory
// (EINVAL, ENOTSUP); there is nothing more to do on those, so that is not
// an error.
func syncDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		_ = d.Close()
		return err
	}
	return d.Close()
}
