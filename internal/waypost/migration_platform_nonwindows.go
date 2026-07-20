//go:build !windows

package waypost

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func isCrossDeviceRenameError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

func syncMigrationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q: %w", path, err)
	}
	defer directory.Close()

	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}

func retainLegacySourceAfterCopy() bool {
	return false
}

func keepMigrationRecoveryMarker() bool {
	return false
}

func rebuildCopiedStateOnResume() bool {
	return false
}

func renameMigrationFile(oldPath, newPath string, _ bool) error {
	return os.Rename(oldPath, newPath)
}
