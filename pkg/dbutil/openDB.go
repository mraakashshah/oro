// Package dbutil provides shared SQLite helpers for oro commands.
package dbutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the sqlite driver
)

const (
	sqliteBusyCode       = 5
	sqliteOpenTimeout    = 5 * time.Second
	sqliteBusyRetryDelay = 10 * time.Millisecond
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

	dsn, err := withBusyTimeout(path)
	if err != nil {
		return nil, fmt.Errorf("configure sqlite %s: %w", path, err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), sqliteOpenTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}

	if err := retrySQLiteBusy(ctx, func() error {
		_, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL")
		if err != nil {
			return fmt.Errorf("execute WAL pragma: %w", err)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode on %s: %w", path, err)
	}

	return db, nil
}

func retrySQLiteBusy(ctx context.Context, operation func() error) error {
	for {
		err := operation()
		if err == nil || !isSQLiteBusy(err) {
			return err
		}

		timer := time.NewTimer(sqliteBusyRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("retry SQLite busy operation: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func isSQLiteBusy(err error) bool {
	var sqliteErr interface{ Code() int }
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteBusyCode
}

// withBusyTimeout configures modernc SQLite's per-connection busy handler.
// Driver pragmas run while each physical connection is created, before Ping or
// any journal and schema work can encounter a lock.
func withBusyTimeout(path string) (string, error) {
	base := path
	fragment := ""
	if strings.HasPrefix(path, "file:") {
		base, fragment, _ = strings.Cut(path, "#")
	}
	base, rawQuery, _ := strings.Cut(base, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse query parameters: %w", err)
	}

	pragmas := query["_pragma"][:0]
	for _, pragma := range query["_pragma"] {
		if !isBusyTimeoutPragma(pragma) {
			pragmas = append(pragmas, pragma)
		}
	}
	query["_pragma"] = append(pragmas, "busy_timeout(5000)")

	dsn := base + "?" + query.Encode()
	if fragment != "" {
		dsn += "#" + fragment
	}
	return dsn, nil
}

func isBusyTimeoutPragma(pragma string) bool {
	name := strings.TrimSpace(strings.ToLower(pragma))
	if end := strings.IndexAny(name, "=( \t\r\n"); end >= 0 {
		name = name[:end]
	}
	return name == "busy_timeout"
}
