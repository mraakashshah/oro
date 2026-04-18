// Package dbutil provides shared SQLite helpers for oro commands.
package dbutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the sqlite driver
)

// ResolveSqliteVecLibPath returns the path to the sqlite-vec shared library.
// It honours the ORO_SQLITE_VEC_LIB environment variable; when the variable is
// empty or unset it falls back to ~/.oro/lib/sqlite-vec.dylib. An error is
// returned if the resolved path does not exist on disk.
//
//oro:testonly — wired into production by subsequent sqlite-vec load bead (oro-p545)
func ResolveSqliteVecLibPath() (string, error) {
	path := os.Getenv("ORO_SQLITE_VEC_LIB")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		path = filepath.Join(home, ".oro", "lib", "sqlite-vec.dylib")
	}
	if _, err := os.Stat(path); err != nil { //nolint:gosec // G703: path sourced from ORO_SQLITE_VEC_LIB env var or well-known default; traversal risk accepted by design
		return "", fmt.Errorf("sqlite-vec extension not found at %s: %w", path, err)
	}
	return path, nil
}

// OpenDB opens a SQLite database at path with WAL journal mode, a 5-second
// busy timeout, and a verified ping. The parent directory is created if absent.
func OpenDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create dir for %s: %w", path, err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	ctx := context.Background()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode on %s: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout on %s: %w", path, err)
	}

	return db, nil
}
