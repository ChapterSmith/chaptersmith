package analysiscache

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Relative NT opens keep all access anchored to the checked directory handle.
// OPEN_REPARSE_POINT opens the entry itself; the caller rejects all reparse points.
func openChild(parent *os.File, name string, write, create, directory bool) (*os.File, error) {
	n, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: n, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	access := uint32(windows.FILE_GENERIC_READ)
	if write {
		access |= windows.FILE_GENERIC_WRITE
	}
	disposition := uint32(windows.FILE_OPEN)
	if create {
		disposition = windows.FILE_CREATE
	}
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var h windows.Handle
	err = windows.NtCreateFile(&h, access, &attrs, &windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition, options, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func isReparse(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
