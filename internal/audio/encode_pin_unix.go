//go:build !windows

package audio

import (
	"os"
	"syscall"
)

// pinFile keeps a file's inode allocated until release is called, even after
// every other handle is closed and the name is removed. Without it a
// replacement created at the same path can receive the same inode number, and
// would then be indistinguishable from the file this process created.
func pinFile(file *os.File) (release func()) {
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return func() {}
	}
	return func() { syscall.Close(fd) }
}
