package waypost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMigrateLegacyStateMovesDirectoryAndDatabaseFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")

	if err := os.MkdirAll(filepath.Join(source, blobsDirName), 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	files := map[string]string{
		legacyDatabaseFilename:              "database",
		legacyDatabaseFilename + "-wal":     "wal",
		legacyDatabaseFilename + "-shm":     "shm",
		legacyDatabaseFilename + "-journal": "journal",
		filepath.Join(blobsDirName, "blob"): "body",
	}
	for path, contents := range files {
		if err := os.WriteFile(filepath.Join(source, path), []byte(contents), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	result, err := MigrateLegacyState(destination, source)
	if err != nil {
		t.Fatalf("MigrateLegacyState() error = %v", err)
	}
	if result.Source != source || result.Destination != destination {
		t.Fatalf("MigrateLegacyState() result = %+v, want source=%q destination=%q", result, source, destination)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source after migration = %v, want not exist", err)
	}

	for suffix, want := range map[string]string{
		"":         "database",
		"-wal":     "wal",
		"-shm":     "shm",
		"-journal": "journal",
	} {
		got, err := os.ReadFile(filepath.Join(destination, databaseFilename+suffix))
		if err != nil {
			t.Fatalf("ReadFile(current database%q) error = %v", suffix, err)
		}
		if string(got) != want {
			t.Fatalf("current database%q = %q, want %q", suffix, got, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(destination, legacyDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy database after migration = %v, want not exist", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, blobsDirName, "blob")); err != nil || string(got) != "body" {
		t.Fatalf("migrated blob = %q, %v; want body, nil", got, err)
	}
}

func TestMigrateLegacyStateRejectsExistingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("MkdirAll(destination) error = %v", err)
	}

	_, err := MigrateLegacyState(destination, source)
	if err == nil {
		t.Fatal("MigrateLegacyState() error = nil, want existing destination error")
	}
	if _, err := os.Lstat(source); err != nil {
		t.Fatalf("legacy source after rejected migration = %v, want preserved", err)
	}
}

func TestMigrateLegacyStateRejectsNestedDestinationWithoutChangingSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(source, "waypost-state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}

	_, err := MigrateLegacyState(destination, source)
	if err == nil {
		t.Fatal("MigrateLegacyState() error = nil, want overlapping paths error")
	}
	if !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("MigrateLegacyState() error = %q, want overlapping paths error", err)
	}
	if got, err := os.ReadFile(filepath.Join(source, legacyDatabaseFilename)); err != nil || string(got) != "database" {
		t.Fatalf("ReadFile(legacy database) = %q, %v; want database, nil", got, err)
	}
	if _, err := os.Lstat(filepath.Join(source, databaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current database after rejected migration = %v, want not exist", err)
	}
	if _, err := os.Lstat(migrationMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration marker after rejected migration = %v, want not exist", err)
	}
}

func TestMigrateLegacyStatePreservesSourceWhenMoveFails(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}
	moveErr := errors.New("move denied")

	_, err := migrateLegacyState(destination, source, func(string, string) error {
		return moveErr
	})
	if !errors.Is(err, moveErr) {
		t.Fatalf("migrateLegacyState() error = %v, want move error", err)
	}
	if got, err := os.ReadFile(filepath.Join(source, legacyDatabaseFilename)); err != nil || string(got) != "database" {
		t.Fatalf("ReadFile(legacy database) = %q, %v; want database, nil", got, err)
	}
	if _, err := os.Lstat(filepath.Join(source, databaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current database in source after failed move = %v, want not exist", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination after failed move = %v, want not exist", err)
	}
	if _, err := os.Lstat(migrationMarkerPath(destination)); err != nil {
		t.Fatalf("migration marker after failed move = %v, want preserved", err)
	}

	if _, err := MigrateLegacyState(destination, source); err != nil {
		t.Fatalf("MigrateLegacyState() resume error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, databaseFilename)); err != nil || string(got) != "database" {
		t.Fatalf("ReadFile(current database) = %q, %v; want database, nil", got, err)
	}
}

func TestMigrateLegacyStateResumesAfterLegacyDirectoryWasMoved(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("MkdirAll(destination) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, legacyDatabaseFilename+"-wal"), []byte("wal"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database wal) error = %v", err)
	}

	result, err := MigrateLegacyState(destination, source)
	if err != nil {
		t.Fatalf("MigrateLegacyState() error = %v", err)
	}
	if result.Source != source || result.Destination != destination {
		t.Fatalf("MigrateLegacyState() result = %+v, want source=%q destination=%q", result, source, destination)
	}
	if got, err := os.ReadFile(filepath.Join(destination, databaseFilename)); err != nil || string(got) != "database" {
		t.Fatalf("ReadFile(current database) = %q, %v; want database, nil", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, databaseFilename+"-wal")); err != nil || string(got) != "wal" {
		t.Fatalf("ReadFile(current database wal) = %q, %v; want wal, nil", got, err)
	}
	if _, err := os.Lstat(filepath.Join(destination, legacyDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy database after resumed migration = %v, want not exist", err)
	}
}

func TestOpenRuntimeRejectsIncompleteLegacyMigration(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(stateDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}

	_, err := OpenRuntime(context.Background(), stateDir)
	if err == nil {
		t.Fatal("OpenRuntime() error = nil, want incomplete migration error")
	}
	if !strings.Contains(err.Error(), "legacy database files") {
		t.Fatalf("OpenRuntime() error = %q, want legacy migration guidance", err)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, databaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current database after rejected OpenRuntime = %v, want not exist", err)
	}
}

func TestOpenRuntimeRequiresMigrationBeforeCreatingDefaultState(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("WAYPOST_STATE_DIR", "")
	legacyStateDir := filepath.Join(stateHome, legacyStateDirSuffix)
	currentStateDir := filepath.Join(stateHome, defaultStateDirSuffix)
	if err := os.MkdirAll(legacyStateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(legacyStateDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyStateDir, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}

	_, err := OpenRuntime(context.Background(), "")
	if err == nil {
		t.Fatal("OpenRuntime() error = nil, want migration-required error")
	}
	if !strings.Contains(err.Error(), "waypost migrate") {
		t.Fatalf("OpenRuntime() error = %q, want migration guidance", err)
	}
	if _, err := os.Lstat(currentStateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current state after rejected OpenRuntime = %v, want not exist", err)
	}
}

func TestOpenRuntimeRejectsMigrationBeforeCreatingState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	stateDir := filepath.Join(root, "waypost-state")
	marker := newMigrationMarker(source, stateDir)
	if err := writeMigrationMarker(stateDir, marker); err != nil {
		t.Fatalf("writeMigrationMarker() error = %v", err)
	}

	_, err := OpenRuntime(context.Background(), stateDir)
	if err == nil {
		t.Fatal("OpenRuntime() error = nil, want incomplete migration error")
	}
	if !strings.Contains(err.Error(), "incomplete migration") {
		t.Fatalf("OpenRuntime() error = %q, want incomplete migration guidance", err)
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory after rejected OpenRuntime = %v, want not exist", err)
	}
}

func TestMigrateLegacyStateResumesPreparedMigrationAfterMove(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("MkdirAll(destination) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, databaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(current database) error = %v", err)
	}
	if err := writeMigrationMarker(destination, newMigrationMarker(source, destination)); err != nil {
		t.Fatalf("writeMigrationMarker() error = %v", err)
	}

	if _, err := MigrateLegacyState(destination, source); err != nil {
		t.Fatalf("MigrateLegacyState() error = %v", err)
	}
	if _, err := os.Lstat(migrationMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration marker after resumed moved migration = %v, want not exist", err)
	}
}

func TestMigrateLegacyStateCopiesAcrossFilesystems(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(filepath.Join(source, blobsDirName), 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, blobsDirName, "blob"), []byte("body"), 0o600); err != nil {
		t.Fatalf("WriteFile(blob) error = %v", err)
	}

	result, err := migrateLegacyState(destination, source, func(oldPath, newPath string) error {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EXDEV}
	})
	if err != nil {
		t.Fatalf("migrateLegacyState() error = %v", err)
	}
	if result.Source != source || result.Destination != destination {
		t.Fatalf("migrateLegacyState() result = %+v, want source=%q destination=%q", result, source, destination)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source after cross-filesystem migration = %v, want not exist", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, databaseFilename)); err != nil || string(got) != "database" {
		t.Fatalf("ReadFile(current database) = %q, %v; want database, nil", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, blobsDirName, "blob")); err != nil || string(got) != "body" {
		t.Fatalf("ReadFile(blob) = %q, %v; want body, nil", got, err)
	}
	if _, err := os.Lstat(migrationMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration marker after cross-filesystem migration = %v, want not exist", err)
	}
	if _, err := os.Lstat(migrationCopiedMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration copy marker after cross-filesystem migration = %v, want not exist", err)
	}
}

func TestMigrateLegacyStateResumesCopiedStateCleanup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("MkdirAll(destination) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, databaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(current database) error = %v", err)
	}
	marker := newMigrationMarker(source, destination)
	if err := writeMigrationMarker(destination, marker); err != nil {
		t.Fatalf("writeMigrationMarker() error = %v", err)
	}
	if err := writeMigrationCopiedMarker(destination); err != nil {
		t.Fatalf("writeMigrationCopiedMarker() error = %v", err)
	}

	if _, err := MigrateLegacyState(destination, source); err != nil {
		t.Fatalf("MigrateLegacyState() error = %v", err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source after resumed copied migration = %v, want not exist", err)
	}
	if _, err := os.Lstat(migrationMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration marker after resumed copied migration = %v, want not exist", err)
	}
	if _, err := os.Lstat(migrationCopiedMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration copy marker after resumed copied migration = %v, want not exist", err)
	}
}

func TestMigrateLegacyStateRestartsIncompleteCrossFilesystemCopy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy-state")
	destination := filepath.Join(root, "waypost-state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) error = %v", err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("MkdirAll(destination) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, legacyDatabaseFilename), []byte("database"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacy database) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, "partial"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile(partial destination) error = %v", err)
	}
	if err := writeMigrationMarker(destination, newMigrationMarker(source, destination)); err != nil {
		t.Fatalf("writeMigrationMarker() error = %v", err)
	}

	if _, err := MigrateLegacyState(destination, source); err != nil {
		t.Fatalf("MigrateLegacyState() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, databaseFilename)); err != nil || string(got) != "database" {
		t.Fatalf("ReadFile(current database) = %q, %v; want database, nil", got, err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial destination after resumed migration = %v, want not exist", err)
	}
	if _, err := os.Lstat(migrationMarkerPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration marker after resumed copy = %v, want not exist", err)
	}
}
