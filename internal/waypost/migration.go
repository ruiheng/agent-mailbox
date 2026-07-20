package waypost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	legacyStateDirSuffix   = "ai-agent/mailbox"
	legacyDatabaseFilename = "mailbox.db"

	migrationMarkerSuffix         = ".waypost-migration"
	migrationCopiedMarkerSuffix   = ".copied"
	migrationCopiedMarkerContents = "copied\n"
)

type MigrationResult struct {
	Source      string
	Destination string
}

type migrationMarker struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// MigrateLegacyState moves one prior state directory into the current Waypost
// location. sourceOverride is required for a non-default legacy location.
func MigrateLegacyState(destinationOverride, sourceOverride string) (MigrationResult, error) {
	return migrateLegacyState(destinationOverride, sourceOverride, os.Rename)
}

func migrateLegacyState(destinationOverride, sourceOverride string, renameDirectory func(string, string) error) (MigrationResult, error) {
	destination, err := resolveStateDir(destinationOverride)
	if err != nil {
		return MigrationResult{}, err
	}
	source, err := resolveLegacyStateDir(sourceOverride)
	if err != nil {
		return MigrationResult{}, err
	}
	if err := validateMigrationPaths(source, destination); err != nil {
		return MigrationResult{}, err
	}

	sourceExists, err := stateDirectoryExists(source, "legacy")
	if err != nil {
		return MigrationResult{}, err
	}
	destinationExists, err := stateDirectoryExists(destination, "current")
	if err != nil {
		return MigrationResult{}, err
	}

	marker, markerExists, err := readMigrationMarker(destination)
	if err != nil {
		return MigrationResult{}, err
	}
	if markerExists {
		if err := validateMigrationMarker(marker, source, destination); err != nil {
			return MigrationResult{}, err
		}
		return resumeMigration(source, destination, sourceExists, destinationExists, renameDirectory)
	}

	if destinationExists {
		return recoverMovedLegacyState(source, destination, sourceExists)
	}
	if !sourceExists {
		return MigrationResult{}, fmt.Errorf("legacy state directory %q does not exist", source)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return MigrationResult{}, fmt.Errorf("create current state parent directory: %w", err)
	}
	marker = newMigrationMarker(source, destination)
	if err := writeMigrationMarker(destination, marker); err != nil {
		return MigrationResult{}, err
	}

	return continueMigration(source, destination, renameDirectory)
}

func resumeMigration(source, destination string, sourceExists, destinationExists bool, renameDirectory func(string, string) error) (MigrationResult, error) {
	if sourceExists && !destinationExists {
		return continueMigration(source, destination, renameDirectory)
	}
	if !sourceExists && destinationExists {
		return finishMigration(source, destination)
	}
	if sourceExists && destinationExists {
		copyCompleted, err := migrationCopyCompleted(destination)
		if err != nil {
			return MigrationResult{}, err
		}
		if copyCompleted {
			if err := os.RemoveAll(source); err != nil {
				return MigrationResult{}, fmt.Errorf("remove migrated legacy state %q: %w", source, err)
			}
			return finishMigration(source, destination)
		}
		if err := os.RemoveAll(destination); err != nil {
			return MigrationResult{}, fmt.Errorf("remove incomplete copied state %q: %w", destination, err)
		}
		return continueMigration(source, destination, renameDirectory)
	}
	return MigrationResult{}, fmt.Errorf("incomplete migration from %q to %q lost both source and destination", source, destination)
}

func continueMigration(source, destination string, renameDirectory func(string, string) error) (MigrationResult, error) {
	if err := renameDirectory(source, destination); err == nil {
		return finishMigration(source, destination)
	} else if !errors.Is(err, syscall.EXDEV) {
		return MigrationResult{}, fmt.Errorf("move legacy state directory from %q to %q: %w", source, destination, err)
	}

	if err := copyStateDirectory(source, destination); err != nil {
		return MigrationResult{}, fmt.Errorf("copy legacy state directory from %q to %q: %w", source, destination, err)
	}
	if err := writeMigrationCopiedMarker(destination); err != nil {
		return MigrationResult{}, err
	}
	if err := os.RemoveAll(source); err != nil {
		return MigrationResult{}, fmt.Errorf("remove migrated legacy state %q: %w", source, err)
	}
	return finishMigration(source, destination)
}

func finishMigration(source, destination string) (MigrationResult, error) {
	if err := renameLegacyDatabaseFiles(destination); err != nil {
		return MigrationResult{}, fmt.Errorf("finish legacy database rename: %w", err)
	}
	if err := removeMigrationMarkers(destination); err != nil {
		return MigrationResult{}, err
	}
	return MigrationResult{Source: source, Destination: destination}, nil
}

func recoverMovedLegacyState(source, destination string, sourceExists bool) (MigrationResult, error) {
	if sourceExists {
		return MigrationResult{}, fmt.Errorf("current state directory %q already exists", destination)
	}

	hasLegacyFiles, err := hasLegacyDatabaseFiles(destination)
	if err != nil {
		return MigrationResult{}, err
	}
	if !hasLegacyFiles {
		return MigrationResult{}, fmt.Errorf("current state directory %q already exists", destination)
	}
	if err := renameLegacyDatabaseFiles(destination); err != nil {
		return MigrationResult{}, fmt.Errorf("finish legacy database rename: %w", err)
	}
	return MigrationResult{Source: source, Destination: destination}, nil
}

func stateDirectoryExists(path, kind string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s state directory %q: %w", kind, path, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s state path %q is not a directory", kind, path)
	}
	return true, nil
}

func ensureDefaultLegacyStateMigrated(overrideStateDir, stateDir string) error {
	if overrideStateDir != "" || os.Getenv("WAYPOST_STATE_DIR") != "" {
		return nil
	}

	stateExists, err := stateDirectoryExists(stateDir, "current")
	if err != nil {
		return err
	}
	if stateExists {
		return nil
	}

	legacyStateDir, err := resolveLegacyStateDir("")
	if err != nil {
		return err
	}
	legacyExists, err := stateDirectoryExists(legacyStateDir, "legacy")
	if err != nil {
		return err
	}
	if !legacyExists {
		return nil
	}

	return fmt.Errorf("legacy state directory %q exists; run `waypost migrate` before using the default state directory %q", legacyStateDir, stateDir)
}

func validateMigrationPaths(source, destination string) error {
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve legacy state directory %q: %w", source, err)
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve current state directory %q: %w", destination, err)
	}

	overlaps, err := migrationPathsOverlap(sourcePath, destinationPath)
	if err != nil {
		return err
	}
	if overlaps {
		return fmt.Errorf("legacy and current state directories must not overlap: %q and %q", source, destination)
	}
	return nil
}

func migrationPathsOverlap(first, second string) (bool, error) {
	firstContainsSecond, err := migrationPathContains(first, second)
	if err != nil {
		return false, err
	}
	if firstContainsSecond {
		return true, nil
	}
	return migrationPathContains(second, first)
}

func migrationPathContains(parent, child string) (bool, error) {
	relativePath, err := filepath.Rel(parent, child)
	if err != nil {
		return false, fmt.Errorf("compare migration paths %q and %q: %w", parent, child, err)
	}
	return filepath.IsLocal(relativePath), nil
}

func resolveLegacyStateDir(override string) (string, error) {
	if override != "" {
		return filepath.Clean(override), nil
	}
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return filepath.Join(value, legacyStateDirSuffix), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, ".local", "state", legacyStateDirSuffix), nil
}

func newMigrationMarker(source, destination string) migrationMarker {
	return migrationMarker{
		Source:      source,
		Destination: destination,
	}
}

func migrationMarkerPath(destination string) string {
	return destination + migrationMarkerSuffix
}

func migrationCopiedMarkerPath(destination string) string {
	return migrationMarkerPath(destination) + migrationCopiedMarkerSuffix
}

func readMigrationMarker(destination string) (migrationMarker, bool, error) {
	path := migrationMarkerPath(destination)
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return migrationMarker{}, false, nil
	}
	if err != nil {
		return migrationMarker{}, false, fmt.Errorf("read migration marker %q: %w", path, err)
	}

	var marker migrationMarker
	if err := json.Unmarshal(contents, &marker); err != nil {
		return migrationMarker{}, false, fmt.Errorf("read migration marker %q: %w", path, err)
	}
	return marker, true, nil
}

func validateMigrationMarker(marker migrationMarker, source, destination string) error {
	if marker.Source != source || marker.Destination != destination {
		return fmt.Errorf("migration marker for %q does not match requested source %q and destination %q", migrationMarkerPath(destination), source, destination)
	}
	return nil
}

func writeMigrationMarker(destination string, marker migrationMarker) error {
	contents, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode migration marker: %w", err)
	}
	return writeMigrationFile(migrationMarkerPath(destination), contents)
}

func writeMigrationFile(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create migration marker %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod migration marker %q: %w", path, err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write migration marker %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close migration marker %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("create migration marker %q: %w", path, err)
	}
	return nil
}

func writeMigrationCopiedMarker(destination string) error {
	path := migrationCopiedMarkerPath(destination)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("migration copy marker %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect migration copy marker %q: %w", path, err)
	}
	return writeMigrationFile(path, []byte(migrationCopiedMarkerContents))
}

func migrationCopyCompleted(destination string) (bool, error) {
	path := migrationCopiedMarkerPath(destination)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect migration copy marker %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("migration copy marker %q is not a regular file", path)
	}
	return true, nil
}

func removeMigrationMarkers(destination string) error {
	for _, path := range []string{migrationCopiedMarkerPath(destination), migrationMarkerPath(destination)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove migration marker %q: %w", path, err)
		}
	}
	return nil
}

func ensureMigrationComplete(stateDir string) error {
	marker, markerExists, err := readMigrationMarker(stateDir)
	if err != nil {
		return fmt.Errorf("inspect incomplete migration for state directory %q: %w", stateDir, err)
	}
	if markerExists {
		return fmt.Errorf("state directory %q has an incomplete migration from %q; rerun `waypost --state-dir %q migrate --from %q` before using it", stateDir, marker.Source, stateDir, marker.Source)
	}

	hasLegacyFiles, err := hasLegacyDatabaseFiles(stateDir)
	if err != nil {
		return err
	}
	if hasLegacyFiles {
		return fmt.Errorf("state directory %q still contains legacy database files; rerun `waypost --state-dir %q migrate` before using it", stateDir, stateDir)
	}
	return nil
}

func hasLegacyDatabaseFiles(stateDir string) (bool, error) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		path := filepath.Join(stateDir, legacyDatabaseFilename+suffix)
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect %q: %w", path, err)
		}
	}
	return false, nil
}

func renameLegacyDatabaseFiles(stateDir string) error {
	type rename struct {
		from string
		to   string
	}

	var renames []rename
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		from := filepath.Join(stateDir, legacyDatabaseFilename+suffix)
		_, fromErr := os.Lstat(from)
		fromExists := fromErr == nil
		if fromErr != nil && !errors.Is(fromErr, os.ErrNotExist) {
			return fmt.Errorf("inspect %q: %w", from, fromErr)
		}

		to := filepath.Join(stateDir, databaseFilename+suffix)
		_, toErr := os.Lstat(to)
		toExists := toErr == nil
		if toErr != nil && !errors.Is(toErr, os.ErrNotExist) {
			return fmt.Errorf("inspect %q: %w", to, toErr)
		}
		if fromExists && toExists {
			return fmt.Errorf("legacy and current database files both exist for suffix %q", suffix)
		}
		if fromExists {
			renames = append(renames, rename{from: from, to: to})
		}
	}

	for index, rename := range renames {
		if err := os.Rename(rename.from, rename.to); err != nil {
			for rollbackIndex := index - 1; rollbackIndex >= 0; rollbackIndex-- {
				_ = os.Rename(renames[rollbackIndex].to, renames[rollbackIndex].from)
			}
			return fmt.Errorf("rename %q to %q: %w", rename.from, rename.to, err)
		}
	}
	return nil
}

func copyStateDirectory(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect source directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path is not a directory")
	}
	if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod destination directory: %w", err)
	}

	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolve copied path %q: %w", path, err)
		}
		if relativePath == "." {
			return nil
		}
		target := filepath.Join(destination, relativePath)

		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect source directory %q: %w", path, err)
			}
			if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create destination directory %q: %w", target, err)
			}
			if err := os.Chmod(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("chmod destination directory %q: %w", target, err)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported source entry %q", path)
		}
		return copyStateFile(path, target, entry)
	})
}

func copyStateFile(source, destination string, entry fs.DirEntry) (err error) {
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("inspect source file %q: %w", source, err)
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", source, err)
	}
	defer func() {
		if closeErr := sourceFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close source file %q: %w", source, closeErr)
		}
	}()

	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create destination file %q: %w", destination, err)
	}
	defer func() {
		if closeErr := destinationFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close destination file %q: %w", destination, closeErr)
		}
	}()

	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		return fmt.Errorf("copy source file %q: %w", source, err)
	}
	if err := destinationFile.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod destination file %q: %w", destination, err)
	}
	if err := destinationFile.Sync(); err != nil {
		return fmt.Errorf("sync destination file %q: %w", destination, err)
	}
	return nil
}
