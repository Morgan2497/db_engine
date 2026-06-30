//go:build unix

package db0105

import (
	"os"
	"path"
	"syscall"
)

// syncDir forces the OS to flush the directory metadata to the physical disk.
func syncDir(file string) error {
	flags := os.O_RDONLY | syscall.O_DIRECTORY 
	dirfd, err := syscall.Open(path.Dir(file), flags, 0o644)
	if err != nil {
		return err 
	}
	defer syscall.Close(dirfd)
	return syscall.Fsync(dirfd)
}

func createFileSync(file string) (*os.File, error) {
	fp, err := os.OpenFile(file, os.O_RDWR | os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err = syncDir(path.Base(file)); err != nil {
		_ = fp.Close() // to avoid the memory leak.
		return nil, err
	}
	return fp, err 
}
