package mailbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"testing"
)

const closeErrorDriverName = "mailbox-close-error"

var (
	registerCloseErrorDriver sync.Once
	closeErrorMu             sync.Mutex
	closeErrors              = map[string]error{}
)

type closeErrorDriver struct{}

func (closeErrorDriver) Open(name string) (driver.Conn, error) {
	closeErrorMu.Lock()
	err := closeErrors[name]
	closeErrorMu.Unlock()
	return closeErrorConn{err: err}, nil
}

type closeErrorConn struct {
	err error
}

func (c closeErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}

func (c closeErrorConn) Close() error {
	return c.err
}

func (c closeErrorConn) Begin() (driver.Tx, error) {
	return nil, errors.New("begin unsupported")
}

func TestRuntimeCloseJoinsAllDatabaseCloseErrors(t *testing.T) {
	registerCloseErrorDriver.Do(func() {
		sql.Register(closeErrorDriverName, closeErrorDriver{})
	})

	readErr := errors.New("read close")
	claimErr := errors.New("claim close")
	writeErr := errors.New("write close")

	runtime := &Runtime{
		readDB:  openCloseErrorDB(t, "read", readErr),
		claimDB: openCloseErrorDB(t, "claim", claimErr),
		db:      openCloseErrorDB(t, "write", writeErr),
	}

	err := runtime.Close()
	for _, want := range []error{readErr, claimErr, writeErr} {
		if !errors.Is(err, want) {
			t.Fatalf("Runtime.Close() error = %v, want joined error containing %v", err, want)
		}
	}
}

func openCloseErrorDB(t *testing.T, name string, closeErr error) *sql.DB {
	t.Helper()

	dsn := fmt.Sprintf("%s/%s", t.Name(), name)
	closeErrorMu.Lock()
	closeErrors[dsn] = closeErr
	closeErrorMu.Unlock()
	t.Cleanup(func() {
		closeErrorMu.Lock()
		delete(closeErrors, dsn)
		closeErrorMu.Unlock()
	})

	db, err := sql.Open(closeErrorDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", dsn, err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext(%q) error = %v", dsn, err)
	}
	return db
}
