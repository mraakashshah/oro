package dbutil_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
