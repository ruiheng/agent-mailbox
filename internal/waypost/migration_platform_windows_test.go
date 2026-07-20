//go:build windows

package waypost

import (
	"os"
	"syscall"
	"testing"
)

func TestMigrationPathsOnDifferentWindowsVolumesDoNotOverlap(t *testing.T) {
	overlaps, err := migrationPathsOverlap("C:\\legacy-state", "D:\\waypost-state")
	if err != nil {
		t.Fatalf("migrationPathsOverlap() error = %v", err)
	}
	if overlaps {
		t.Fatal("migrationPathsOverlap() = true, want false for different volumes")
	}
}

func TestCrossDeviceRenameRecognizesWindowsNotSameDevice(t *testing.T) {
	err := &os.LinkError{
		Op:  "rename",
		Old: "C:\\legacy-state",
		New: "D:\\waypost-state",
		Err: syscall.Errno(17),
	}
	if !isCrossDeviceRenameError(err) {
		t.Fatalf("isCrossDeviceRenameError(%v) = false, want true", err)
	}
}

func TestWindowsCopiedMigrationRetainsSource(t *testing.T) {
	if !retainLegacySourceAfterCopy() {
		t.Fatal("retainLegacySourceAfterCopy() = false, want true")
	}
}

func TestMigrationRenameFlagsRequestWriteThrough(t *testing.T) {
	flags := migrationRenameFlags(false)
	if flags&windowsMoveFileWriteThrough == 0 {
		t.Fatalf("migrationRenameFlags(false) = %#x, want MOVEFILE_WRITE_THROUGH", flags)
	}
	if flags&windowsMoveFileReplaceExisting != 0 {
		t.Fatalf("migrationRenameFlags(false) = %#x, want no MOVEFILE_REPLACE_EXISTING", flags)
	}

	flags = migrationRenameFlags(true)
	if flags&windowsMoveFileWriteThrough == 0 || flags&windowsMoveFileReplaceExisting == 0 {
		t.Fatalf("migrationRenameFlags(true) = %#x, want MOVEFILE_WRITE_THROUGH | MOVEFILE_REPLACE_EXISTING", flags)
	}
}
