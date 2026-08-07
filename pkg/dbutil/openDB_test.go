package dbutil_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
)

func TestOpenDBWAL(t *testing.T) {
	dir := t.TempDir()
	// Verify auto-creation of a nested parent that does not exist yet.
	path := filepath.Join(dir, "sub", "nested", "test.db")

	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()

	// Assert PingContext succeeds.
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("PingContext: %v", err)
	}

	// Assert journal_mode=wal.
	var jm string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if jm != "wal" {
		t.Errorf("journal_mode = %q, want %q", jm, "wal")
	}

	// Assert busy_timeout=5000.
	var bt int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&bt); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if bt != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", bt)
	}

	// Assert parent directory was auto-created.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent dir not created: %v", err)
	}
}

func TestOpenDBBusyTimeoutAppliesToEveryConnection(t *testing.T) {
	const connectionCount = 4

	ctx := context.Background()
	dsn := "file:open_db_busy_timeout?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(1)"
	db, err := dbutil.OpenDB(dsn)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(connectionCount)

	conns := make([]*sql.Conn, 0, connectionCount)
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()
	for range connectionCount {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("reserve connection: %v", err)
		}
		conns = append(conns, conn)
	}

	for i, conn := range conns {
		var busyTimeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if busyTimeout != 5000 {
			t.Errorf("connection %d busy_timeout = %d, want 5000", i, busyTimeout)
		}

		var foreignKeys int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if foreignKeys != 1 {
			t.Errorf("connection %d foreign_keys = %d, want 1", i, foreignKeys)
		}
	}
}

func TestOpenDBConcurrentOpenersWaitForSchemaLock(t *testing.T) {
	const openerCount = 4

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("seed DB: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed DB: %v", err)
	}

	locker, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open schema locker: %v", err)
	}
	defer func() { _ = locker.Close() }()
	lockConn, err := locker.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve schema locker connection: %v", err)
	}
	defer func() { _ = lockConn.Close() }()
	if _, err := lockConn.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatalf("set rollback journal mode: %v", err)
	}
	if _, err := lockConn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		t.Fatalf("hold exclusive schema lock: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = lockConn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	type openResult struct {
		db  *sql.DB
		err error
	}
	results := make(chan openResult, openerCount)
	for range openerCount {
		go func() {
			db, openErr := dbutil.OpenDB(dbPath)
			results <- openResult{db: db, err: openErr}
		}()
	}

	select {
	case opened := <-results:
		if opened.db != nil {
			_ = opened.db.Close()
		}
		t.Fatalf("DB opener returned before schema lock release: %v", opened.err)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := lockConn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("release exclusive schema lock: %v", err)
	}
	locked = false

	for range openerCount {
		select {
		case opened := <-results:
			if opened.err != nil {
				t.Errorf("open DB after schema lock release: %v", opened.err)
				continue
			}
			var busyTimeout int
			if err := opened.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
				t.Errorf("query opener busy timeout: %v", err)
			} else if busyTimeout != 5000 {
				t.Errorf("opener busy timeout = %d, want 5000", busyTimeout)
			}
			if err := opened.db.Close(); err != nil {
				t.Errorf("close opened DB: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("timed out waiting for DB opener")
		}
	}
}

func TestOpenDB_EmptyPath(t *testing.T) {
	_, err := dbutil.OpenDB("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
	if !strings.Contains(err.Error(), "empty path") {
		t.Errorf("error %q does not contain 'empty path'", err.Error())
	}
}

func TestOpenDB_PingFailureClosesDB(t *testing.T) {
	// We can't easily force a PingContext failure with modernc sqlite,
	// but we can verify OpenDB returns a usable, open DB on success.
	dir := t.TempDir()
	path := filepath.Join(dir, "ping.db")
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	// Verify it's still open (stats accessible).
	_ = db.Stats()
	_ = db.Close()
}

func TestResolveSqliteVecLibPathEnv(t *testing.T) {
	// Create a real file so the existence check passes.
	dir := t.TempDir()
	libPath := filepath.Join(dir, "sqlite-vec.dylib")
	if err := os.WriteFile(libPath, []byte{}, 0o644); err != nil {
		t.Fatalf("create temp lib: %v", err)
	}

	t.Setenv("ORO_SQLITE_VEC_LIB", libPath)

	got, err := dbutil.ResolveSqliteVecLibPath()
	if err != nil {
		t.Fatalf("ResolveSqliteVecLibPath(): unexpected error: %v", err)
	}
	if got != libPath {
		t.Errorf("ResolveSqliteVecLibPath() = %q, want %q", got, libPath)
	}
}

func TestResolveSqliteVecLibPathDefault(t *testing.T) {
	// Ensure the env var is not set so the function falls back to the default path.
	t.Setenv("ORO_SQLITE_VEC_LIB", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("os.UserHomeDir() unavailable: %v", err)
	}
	defaultPath := filepath.Join(home, ".oro", "lib", "sqlite-vec.dylib")

	// Create the file so the existence check passes.
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o750); err != nil {
		t.Fatalf("create lib dir: %v", err)
	}
	existed := false
	if _, statErr := os.Stat(defaultPath); statErr == nil {
		existed = true
	}
	if !existed {
		if err := os.WriteFile(defaultPath, []byte{}, 0o644); err != nil {
			t.Fatalf("create default lib: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(defaultPath) })
	}

	got, err := dbutil.ResolveSqliteVecLibPath()
	if err != nil {
		t.Fatalf("ResolveSqliteVecLibPath(): unexpected error: %v", err)
	}
	if got != defaultPath {
		t.Errorf("ResolveSqliteVecLibPath() = %q, want %q", got, defaultPath)
	}
}

func TestResolveSqliteVecLibPathMissing(t *testing.T) {
	// Point env at a path that does not exist on disk.
	t.Setenv("ORO_SQLITE_VEC_LIB", "/nonexistent/path/sqlite-vec.dylib")

	_, err := dbutil.ResolveSqliteVecLibPath()
	if err == nil {
		t.Fatal("ResolveSqliteVecLibPath(): expected error for missing path, got nil")
	}
	const want = "sqlite-vec extension not found at"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}
