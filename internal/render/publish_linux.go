package render

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func publishNoReplace(stagedPath, destinationPath string) error {
	if e := unix.Renameat2(unix.AT_FDCWD, stagedPath, unix.AT_FDCWD, destinationPath, unix.RENAME_NOREPLACE); e != nil {
		return fmt.Errorf("atomic no-replace publication failed (filesystem must support RENAME_NOREPLACE): %w", e)
	}
	return nil
}
