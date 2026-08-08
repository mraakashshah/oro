//nolint:testpackage // fault injection must construct SQLiteStore with a wrapped database driver
package beadstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"oro/pkg/protocol"

	modernsqlite "modernc.org/sqlite"
)

type removeDependencyFaultMode int32

const (
	removeDependencyNoFault removeDependencyFaultMode = iota
	removeDependencyBeginFault
	removeDependencyExecFault
	removeDependencyRowsAffectedFault
	removeDependencyCommitFault
)

var (
	errRemoveDependencyBegin        = errors.New("injected remove dependency begin failure")
	errRemoveDependencyExec         = errors.New("injected remove dependency exec failure")
	errRemoveDependencyRowsAffected = errors.New("injected remove dependency rows-affected failure")
	errRemoveDependencyCommit       = errors.New("injected remove dependency commit failure")
	removeDependencyFaultDriverID   atomic.Uint64
)

func TestSQLiteRemoveDependencyPropagatesTransactionFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        removeDependencyFaultMode
		wantErr     error
		wantMessage string
	}{
		{name: "begin", mode: removeDependencyBeginFault, wantErr: errRemoveDependencyBegin, wantMessage: "begin remove dependency transaction"},
		{name: "exec", mode: removeDependencyExecFault, wantErr: errRemoveDependencyExec, wantMessage: "remove dependency oro-dependent -> oro-blocker"},
		{name: "rows affected", mode: removeDependencyRowsAffectedFault, wantErr: errRemoveDependencyRowsAffected, wantMessage: "count dependency removal oro-dependent -> oro-blocker"},
		{name: "commit", mode: removeDependencyCommitFault, wantErr: errRemoveDependencyCommit, wantMessage: "commit remove dependency oro-dependent -> oro-blocker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, faults := newRemoveDependencyFaultStore(t)
			mustCreate(t, store, CreateParams{ID: "oro-dependent", Title: "dependent"})
			mustCreate(t, store, CreateParams{ID: "oro-blocker", Title: "blocker"})
			if err := store.AddDependency(t.Context(), "oro-dependent", "oro-blocker", "blocks"); err != nil {
				t.Fatalf("seed dependency: %v", err)
			}

			faults.Store(int32(tc.mode))
			err := store.RemoveDependency(t.Context(), "oro-dependent", "oro-blocker")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RemoveDependency error = %v, want wrapped %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("RemoveDependency error = %q, want operation %q", err, tc.wantMessage)
			}
			faults.Store(int32(removeDependencyNoFault))
			if got := beadDepsCount(t, store.db); got != 1 {
				t.Fatalf("dependency count after failed removal = %d, want rolled back 1", got)
			}
		})
	}
}

func newRemoveDependencyFaultStore(t *testing.T) (*SQLiteStore, *atomic.Int32) {
	t.Helper()
	faults := &atomic.Int32{}
	driverName := fmt.Sprintf("oro-remove-dependency-fault-%d", removeDependencyFaultDriverID.Add(1))
	sql.Register(driverName, &removeDependencyFaultDriver{base: &modernsqlite.Driver{}, faults: faults})
	db, err := sql.Open(driverName, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open fault-injected SQLite database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), protocol.SchemaDDL); err != nil {
		t.Fatalf("migrate runtime schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	return NewSQLiteStore(db), faults
}

type removeDependencyFaultDriver struct {
	base   driver.Driver
	faults *atomic.Int32
}

func (d *removeDependencyFaultDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &removeDependencyFaultConn{Conn: conn, faults: d.faults}, nil
}

type removeDependencyFaultConn struct {
	driver.Conn
	faults *atomic.Int32
}

func (c *removeDependencyFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if removeDependencyFaultMode(c.faults.Load()) == removeDependencyBeginFault {
		return nil, errRemoveDependencyBegin
	}
	conn, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("modernc SQLite connection does not support BeginTx")
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &removeDependencyFaultTx{Tx: tx, faults: c.faults}, nil
}

func (c *removeDependencyFaultConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if strings.HasPrefix(strings.TrimSpace(query), "DELETE FROM bead_deps") {
		switch removeDependencyFaultMode(c.faults.Load()) {
		case removeDependencyExecFault:
			return nil, errRemoveDependencyExec
		case removeDependencyRowsAffectedFault:
			result, err := execer.ExecContext(ctx, query, args)
			return removeDependencyFaultResult{Result: result}, err
		}
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *removeDependencyFaultConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func (c *removeDependencyFaultConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *removeDependencyFaultConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *removeDependencyFaultConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *removeDependencyFaultConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type removeDependencyFaultTx struct {
	driver.Tx
	faults *atomic.Int32
}

func (tx *removeDependencyFaultTx) Commit() error {
	if removeDependencyFaultMode(tx.faults.Load()) == removeDependencyCommitFault {
		_ = tx.Rollback()
		return errRemoveDependencyCommit
	}
	return tx.Tx.Commit()
}

type removeDependencyFaultResult struct {
	driver.Result
}

func (removeDependencyFaultResult) RowsAffected() (int64, error) {
	return 0, errRemoveDependencyRowsAffected
}
