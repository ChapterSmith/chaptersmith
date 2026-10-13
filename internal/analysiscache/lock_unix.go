//go:build darwin || linux

package analysiscache

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openChild(parent *os.File, name string, write, create, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if write {
		flags = flags&^unix.O_RDONLY | unix.O_RDWR
	}
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func isReparse(os.FileInfo) bool { return false }
