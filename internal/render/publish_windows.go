package render

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func publishNoReplace(stagedPath, destinationPath string) error {
	from, e := windows.UTF16PtrFromString(stagedPath)
	if e != nil {
		return e
	}
	to, e := windows.UTF16PtrFromString(destinationPath)
	if e != nil {
		return e
	}
	if e = windows.MoveFileEx(from, to, 0); e != nil {
		return fmt.Errorf("atomic no-replace publication failed (filesystem must support MoveFileEx): %w", e)
	}
	return nil
}
