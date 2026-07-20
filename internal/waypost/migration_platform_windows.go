//go:build windows

package waypost

import (
	"errors"
	"syscall"
	"unsafe"
)

const (
	windowsErrorNotSameDevice      = syscall.Errno(17)
	windowsMoveFileReplaceExisting = 0x1
	windowsMoveFileWriteThrough    = 0x8
)

var (
	windowsKernel32    = syscall.NewLazyDLL("kernel32.dll")
	windowsMoveFileExW = windowsKernel32.NewProc("MoveFileExW")
)

func isCrossDeviceRenameError(err error) bool {
	return errors.Is(err, syscall.EXDEV) || errors.Is(err, windowsErrorNotSameDevice)
}

func syncMigrationDirectory(string) error {
	// Windows does not support syncing a directory handle through os.File.Sync.
	// Each copied file and migration marker is synced, but directory-entry
	// replacement is not treated as crash-durable. Cross-volume migrations keep
	// their source and retain a recovery marker so a later migrate can rebuild
	// the destination from that source.
	return nil
}

func retainLegacySourceAfterCopy() bool {
	return true
}

func keepMigrationRecoveryMarker() bool {
	return true
}

func rebuildCopiedStateOnResume() bool {
	return true
}

func migrationRenameFlags(replace bool) uint32 {
	flags := uint32(windowsMoveFileWriteThrough)
	if replace {
		flags |= windowsMoveFileReplaceExisting
	}
	return flags
}

func renameMigrationFile(oldPath, newPath string, replace bool) error {
	oldPathPointer, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPathPointer, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}

	succeeded, _, callErr := windowsMoveFileExW.Call(
		uintptr(unsafe.Pointer(oldPathPointer)),
		uintptr(unsafe.Pointer(newPathPointer)),
		uintptr(migrationRenameFlags(replace)),
	)
	if succeeded != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return syscall.EINVAL
}
