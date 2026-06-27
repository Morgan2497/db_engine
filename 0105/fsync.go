package kv 

import (
	"os"
	"path"
	"syscall"
)

// syncDir forces the OS to flush the directory metadata to the physical disk.
// This ensures that if we create a new file, its existence is permanently recorded.

func syncDir(file string) error {
	// O_RDONLY: this flag is used to open() system call to specify that a file should be
	// opened for reading only. 
	// O_DIRECTORY: this flag tells the kernel that the target path must be a directory 
	// it it is a regular file, the call fails with ENOTDIR.
	// Return a single integer a bitmask of flags. 
	flags := os.O_RDONLY | syscall.O_DIRECTORY 
	
	dirfd, err := syscall.Open(path.Dir(file), flags, 0o644)
	if err != nil {
		return err 
	}
	defer syscall.Close(dirfd) // schedules a function call to be executed immediately before the surrounding function returns.
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

