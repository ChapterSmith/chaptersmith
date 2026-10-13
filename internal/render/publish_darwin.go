package render

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func publishNoReplace(stagedPath, destinationPath string) error {
	if e := unix.RenamexNp(stagedPath, destinationPath, unix.RENAME_EXCL); e != nil {
		return fmt.Errorf("atomic no-replace publication failed (filesystem must support RENAME_EXCL): %w", e)
	}
	return nil
}
