package mailbox

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultStateDirSuffix = "ai-agent/mailbox"
	databaseFilename      = "mailbox.db"
	blobsDirName          = "blobs"
)

type Runtime struct {
	stateDir string
	dbPath   string
	blobDir  string
	db       *sql.DB
	readDB   *sql.DB
	claimDB  *sql.DB
	store    *Store
}

func OpenRuntime(ctx context.Context, overrideStateDir string) (*Runtime, error) {
	stateDir, err := resolveStateDir(overrideStateDir)
	if err != nil {
		return nil, err
	}
	if err := ensureDir(stateDir); err != nil {
		return nil, err
	}

	blobDir := filepath.Join(stateDir, blobsDirName)
	if err := ensureDir(blobDir); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(stateDir, databaseFilename)
	db, err := openDatabase(ctx, dbPath, "immediate", 5000)
	if err != nil {
		return nil, err
	}
	if err := initSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	readDB, err := openDatabase(ctx, dbPath, "", 5000)
	if err != nil {
		db.Close()
		return nil, err
	}
	claimDB, err := openDatabase(ctx, dbPath, "immediate", 0)
	if err != nil {
		readDB.Close()
		db.Close()
		return nil, err
	}

	store := NewStore(readDB, db, claimDB, blobDir)

	return &Runtime{
		stateDir: stateDir,
		dbPath:   dbPath,
		blobDir:  blobDir,
		db:       db,
		readDB:   readDB,
		claimDB:  claimDB,
		store:    store,
	}, nil
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	if r.readDB != nil {
		if err := r.readDB.Close(); err != nil && r.claimDB == nil && r.db == nil {
			return err
		}
	}
	if r.claimDB != nil {
		if err := r.claimDB.Close(); err != nil && r.db == nil {
			return err
		}
	}
	if r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *Runtime) Store() *Store {
	return r.store
}

func (r *Runtime) DB() *sql.DB {
	return r.db
}

func (r *Runtime) StateDir() string {
	return r.stateDir
}

func (r *Runtime) BlobDir() string {
	return r.blobDir
}

func (r *Runtime) DBPath() string {
	return r.dbPath
}

func resolveStateDir(override string) (string, error) {
	if override != "" {
		return filepath.Clean(override), nil
	}
	if value := os.Getenv("MAILBOX_STATE_DIR"); value != "" {
		return filepath.Clean(value), nil
	}
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return filepath.Join(value, defaultStateDirSuffix), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, ".local", "state", defaultStateDirSuffix), nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create directory %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("chmod directory %q: %w", path, err)
	}
	return nil
}

func openDatabase(ctx context.Context, path, txLock string, busyTimeoutMS int) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=%d&_foreign_keys=on", filepath.ToSlash(path), busyTimeoutMS)
	if txLock != "" {
		dsn += "&_txlock=" + txLock
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	return db, nil
}
