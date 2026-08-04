//nolint:testpackage // The interleaving must observe SQLiteStore's internal writer admission.
package beadstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"

	modernsqlite "modernc.org/sqlite"
)

func TestSQLiteStoreLegacyReconnectWriterDoesNotInvertOrdinaryUpdate(t *testing.T) {
	t.Parallel()
	ordinaryAtFirstWrite := make(chan struct{})
	allowOrdinaryWrite := make(chan struct{})
	var gateOnce sync.Once
	db := openGatedSQLiteStoreDB(t, func(ctx context.Context, query string, args []driver.NamedValue) {
		if !isBeadUpdateFor(query, args, "ordinary-update") {
			return
		}
		gateOnce.Do(func() { close(ordinaryAtFirstWrite) })
		select {
		case <-allowOrdinaryWrite:
		case <-ctx.Done():
		}
	})
	store := NewSQLiteStore(db)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, bead := range []CreateParams{
		{ID: "ordinary-update", Title: "ordinary", Status: "open"},
		{ID: "legacy-reconnect", Title: "legacy", Status: "in_progress"},
	} {
		if _, err := store.Create(ctx, bead); err != nil {
			t.Fatalf("create %s: %v", bead.ID, err)
		}
	}

	newTitle := "ordinary update reached SQLite"
	ordinaryDone := make(chan error, 1)
	go func() {
		ordinaryDone <- store.Update(ctx, "ordinary-update", UpdateParams{Title: &newTitle})
	}()
	select {
	case <-ordinaryAtFirstWrite:
	case <-ctx.Done():
		t.Fatalf("ordinary Update did not reach its first SQLite write while holding writeMu: %v", ctx.Err())
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open legacy reconnect connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("reserve legacy reconnect SQLite writer: %v", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()

	legacyStarted := make(chan struct{})
	legacyDone := make(chan error, 1)
	go func() {
		close(legacyStarted)
		updated, updateErr := store.UpdateStatusIfConn(ctx, conn, "legacy-reconnect", "in_progress", "open")
		if updateErr != nil {
			legacyDone <- updateErr
			return
		}
		if !updated {
			legacyDone <- errors.New("legacy reconnect bead transition did not update one row")
			return
		}
		if _, insertErr := conn.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('legacy-reconnect', 'legacy-worker', '/tmp/legacy-worktree', 'active')`); insertErr != nil {
			legacyDone <- fmt.Errorf("persist legacy ASSIGN: %w", insertErr)
			return
		}
		if _, commitErr := conn.ExecContext(ctx, `COMMIT`); commitErr != nil {
			legacyDone <- fmt.Errorf("commit legacy reconnect: %w", commitErr)
			return
		}
		legacyDone <- nil
	}()
	<-legacyStarted
	close(allowOrdinaryWrite)

	if err := <-legacyDone; err != nil {
		t.Fatalf("writeMu/SQLite inversion prevented legacy ASSIGN: %v", err)
	}
	if err := <-ordinaryDone; err != nil {
		t.Fatalf("ordinary Update after legacy reconnect: %v", err)
	}
	var assignments int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM assignments
WHERE bead_id='legacy-reconnect' AND worker_id='legacy-worker' AND status='active'`).Scan(&assignments); err != nil {
		t.Fatalf("count durable legacy ASSIGN: %v", err)
	}
	if assignments != 1 {
		t.Fatalf("durable legacy ASSIGN rows = %d, want 1", assignments)
	}
}

var gatedSQLiteDriverID atomic.Uint64

func openGatedSQLiteStoreDB(
	t *testing.T,
	beforeExec func(context.Context, string, []driver.NamedValue),
) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("oro-sqlite-write-gate-%d", gatedSQLiteDriverID.Add(1))
	sql.Register(driverName, &gatedSQLiteDriver{
		base:       &modernsqlite.Driver{},
		beforeExec: beforeExec,
	})
	db, err := sql.Open(driverName, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open gated SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), protocol.SchemaDDL); err != nil {
		t.Fatalf("migrate dispatcher schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("migrate native bead schema: %v", err)
	}
	return db
}

func isBeadUpdateFor(query string, args []driver.NamedValue, beadID string) bool {
	if !strings.HasPrefix(strings.TrimSpace(query), "UPDATE beads SET") || len(args) == 0 {
		return false
	}
	gotID, ok := args[len(args)-1].Value.(string)
	return ok && gotID == beadID
}

type gatedSQLiteDriver struct {
	base       driver.Driver
	beforeExec func(context.Context, string, []driver.NamedValue)
}

func (d *gatedSQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("modernc SQLite connection does not support ExecContext")
	}
	if _, err := execer.ExecContext(context.Background(), `PRAGMA busy_timeout=5000`, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set SQLite busy timeout: %w", err)
	}
	return &gatedSQLiteConn{Conn: conn, beforeExec: d.beforeExec}, nil
}

type gatedSQLiteConn struct {
	driver.Conn
	beforeExec func(context.Context, string, []driver.NamedValue)
}

func (c *gatedSQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if conn, ok := c.Conn.(driver.ConnBeginTx); ok {
		return conn.BeginTx(ctx, opts)
	}
	return nil, errors.New("modernc SQLite connection does not support BeginTx")
}

func (c *gatedSQLiteConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	c.beforeExec(ctx, query, args)
	conn, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return conn.ExecContext(ctx, query, args)
}

func (c *gatedSQLiteConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	conn, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return conn.QueryContext(ctx, query, args)
}

func (c *gatedSQLiteConn) CheckNamedValue(value *driver.NamedValue) error {
	if conn, ok := c.Conn.(driver.NamedValueChecker); ok {
		return conn.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *gatedSQLiteConn) Ping(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.Pinger); ok {
		return conn.Ping(ctx)
	}
	return nil
}

func (c *gatedSQLiteConn) ResetSession(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.SessionResetter); ok {
		return conn.ResetSession(ctx)
	}
	return nil
}

func (c *gatedSQLiteConn) IsValid() bool {
	if conn, ok := c.Conn.(driver.Validator); ok {
		return conn.IsValid()
	}
	return true
}
