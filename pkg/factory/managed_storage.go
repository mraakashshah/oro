package factory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	managedJournalMaxDatabaseBytes int64 = 64 << 20
	managedDatabasePageSize              = 4096
	managedStateInitMarker               = "oro-managed-state-v1;page=4096;data=67108864;allowance=268435456\n"
	// ManagedStateStorageBytes is the host reservation for one managed database
	// and its transient rollback journal. It excludes other objective artifacts.
	// DELETE mode permits one rollback journal; each original page appears at
	// most once, with an 8-byte record overhead. With cache spilling disabled,
	// the 16,384-page database and journal fit comfortably within this allowance.
	// See https://sqlite.org/fileformat2.html#the_rollback_journal.
	ManagedStateStorageBytes int64 = 256 << 20
)

// openManagedFactoryStore is the required constructor for managed admission and
// future managed delivery snapshot writers. Legacy openStore retains WAL mode.
func openManagedFactoryStore(ctx context.Context, dir string) (*factoryStore, error) {
	store, fresh, err := openManagedConnection(ctx, dir, true)
	if err != nil {
		return nil, err
	}
	if fresh {
		err = store.initialize(ctx)
	} else {
		err = verifyManagedTables(ctx, store.conn)
	}
	if err != nil {
		_ = store.close()
		return nil, err
	}
	return store, nil
}

func openManagedConnection(ctx context.Context, dir string, create bool) (*factoryStore, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("open managed storage: %w", err)
	}
	fresh, err := prepareManagedDatabase(dir, create)
	if err != nil {
		return nil, false, err
	}
	query := url.Values{"mode": {"rw"}, "_txlock": {"immediate"}, "_pragma": {"busy_timeout(1000)"}}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(dir, "state.db"), RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, false, fmt.Errorf("open managed SQLite connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, false, fmt.Errorf("acquire managed storage connection: %w", err)
	}
	store := &factoryStore{db: db, conn: conn, managed: true}
	if err := configureManagedStorage(ctx, conn, fresh); err != nil {
		_ = store.close()
		return nil, false, err
	}
	return store, fresh, nil
}

func prepareManagedDatabase(dir string, create bool) (bool, error) {
	if !managedAbsolutePath(dir) {
		return false, errors.New("managed database directory must be absolute")
	}
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil || actual != dir {
		return false, errors.New("managed database directory must exist and be canonical")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, fmt.Errorf("open managed database directory: %w", err)
	}
	defer root.Close()
	if err := validateManagedStateFiles(root); err != nil {
		return false, err
	}
	namespace, err := openManagedInitializationRoot(dir, create)
	if err != nil {
		return false, err
	}
	defer namespace.Close()
	err = readManagedInitializationMarker(namespace, filepath.Base(dir)+".state.init")
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return false, errors.New("managed database has no durable initialization marker")
		}
		return initializeManagedDatabase(dir, root, namespace)
	}
	if err != nil {
		return false, err
	}
	if err := readManagedInitializationMarker(root, "state.init"); err != nil {
		return false, fmt.Errorf("reserved managed initialization is unavailable; refusing reset: %w", err)
	}
	info, err := root.Lstat("state.db")
	if err != nil {
		return false, fmt.Errorf("initialized managed database is unavailable; refusing reset: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < 100 || info.Size() > managedJournalMaxDatabaseBytes {
		return false, errors.New("initialized managed database is empty, unsafe or exceeds its byte cap")
	}
	return false, nil
}

func openManagedInitializationRoot(dir string, create bool) (*os.Root, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("open managed initialization parent: %w", err)
	}
	defer parent.Close()
	if create {
		if err := parent.Mkdir(".managed", 0o700); err != nil && !os.IsExist(err) {
			return nil, fmt.Errorf("create managed initialization namespace: %w", err)
		}
	}
	info, err := parent.Lstat(".managed")
	if err != nil {
		return nil, fmt.Errorf("inspect managed initialization namespace: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("managed initialization namespace must be a directory and cannot be a symlink")
	}
	if create {
		if err := syncManagedDirectory(parent.Name()); err != nil {
			return nil, err
		}
	}
	root, err := parent.OpenRoot(".managed")
	if err != nil {
		return nil, fmt.Errorf("open managed initialization namespace: %w", err)
	}
	return root, nil
}

func readManagedInitializationMarker(root *os.Root, name string) error {
	file, err := openManagedRootRegular(root, name, int64(len(managedStateInitMarker)), false)
	if err != nil {
		return fmt.Errorf("open managed initialization marker: %w", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, int64(len(managedStateInitMarker)+1)))
	if err != nil || string(contents) != managedStateInitMarker {
		return errors.New("managed database initialization marker is incomplete or incompatible")
	}
	return nil
}

func validateManagedStateFiles(root *os.Root) error {
	for _, name := range []string{"state.init", "state.db", "state.db-journal", "state.db-wal", "state.db-shm"} {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect managed %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed %s must be regular and cannot be a symlink", name)
		}
	}
	return nil
}

func initializeManagedDatabase(dir string, root, namespace *os.Root) (bool, error) {
	for _, name := range []string{"state.init", "state.db", "state.db-journal", "state.db-wal", "state.db-shm"} {
		if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return false, errors.New("existing database state without initialization marker cannot be adopted")
		}
	}
	// Reserve outside the objective first: losing the whole objective directory
	// must not erase the evidence that its database may contain spent budgets.
	if err := persistManagedInitializationMarker(namespace, filepath.Base(dir)+".state.init"); err != nil {
		return false, err
	}
	if err := persistManagedInitializationMarker(root, "state.init"); err != nil {
		return false, err
	}
	// The marker precedes creation. A crash or deletion leaving it without a
	// valid database fails closed instead of recreating spent budgets.
	file, err := root.OpenFile("state.db", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("exclusively create managed database: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close new managed database: %w", err)
	}
	return true, nil
}

func persistManagedInitializationMarker(root *os.Root, name string) error {
	marker, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("exclusively reserve managed database initialization: %w", err)
	}
	_, writeErr := marker.WriteString(managedStateInitMarker)
	if err := errors.Join(writeErr, marker.Sync(), marker.Close()); err != nil {
		return fmt.Errorf("persist managed initialization marker: %w", err)
	}
	return syncManagedDirectory(root.Name())
}

func verifyManagedTables(ctx context.Context, conn *sql.Conn) error {
	for _, statement := range []string{"SELECT revision,body FROM snapshots LIMIT 0", "SELECT revision,state,body FROM events LIMIT 0"} {
		rows, err := conn.QueryContext(ctx, statement)
		if err != nil {
			return fmt.Errorf("managed database schema is missing or corrupt: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("verify managed database schema: %w", err)
		}
	}
	return nil
}

func configureManagedStorage(ctx context.Context, conn *sql.Conn, fresh bool) error {
	if fresh {
		if _, err := conn.ExecContext(ctx, "PRAGMA page_size=4096; PRAGMA journal_mode=DELETE"); err != nil {
			return fmt.Errorf("initialize managed rollback storage: %w", err)
		}
	}
	for _, statement := range []string{
		"PRAGMA synchronous=FULL", "PRAGMA locking_mode=NORMAL", "PRAGMA temp_store=MEMORY", "PRAGMA cache_spill=OFF", "PRAGMA busy_timeout=1000",
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure managed rollback storage: %w", err)
		}
	}
	if err := verifyManagedStoragePragmas(ctx, conn); err != nil {
		return err
	}
	var pages int64
	if err := conn.QueryRowContext(ctx, "PRAGMA max_page_count=16384").Scan(&pages); err != nil {
		return fmt.Errorf("bound managed database pages: %w", err)
	}
	if pages != managedJournalMaxDatabaseBytes/managedDatabasePageSize {
		return errors.New("managed database already exceeds its page budget")
	}
	return nil
}

func verifyManagedStoragePragmas(ctx context.Context, conn *sql.Conn) error {
	for _, setting := range [][2]string{
		{"journal_mode", "delete"},
		{"synchronous", "2"},
		{"page_size", "4096"},
		{"locking_mode", "normal"},
		{"temp_store", "2"},
		{"cache_spill", "0"},
		{"busy_timeout", "1000"},
	} {
		var value string
		if err := conn.QueryRowContext(ctx, "PRAGMA "+setting[0]).Scan(&value); err != nil {
			return fmt.Errorf("verify managed %s: %w", setting[0], err)
		}
		if value != setting[1] {
			return fmt.Errorf("managed storage requires %s=%s, got %s", setting[0], setting[1], value)
		}
	}
	return nil
}

// commitManagedTransaction preserves failure and clears a failed SQLite COMMIT.
// modernc's driver can leave the SQL transaction active after SQLITE_BUSY while
// database/sql has already marked its Tx done, making Tx.Rollback ineffective.
func commitManagedTransaction(conn *sql.Conn, tx *sql.Tx) error {
	err := tx.Commit()
	if err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
		closeErr := conn.Close()
		return fmt.Errorf("managed commit failed; connection closed after uncertain rollback: %w",
			errors.Join(err, rollbackErr, closeErr))
	}
	return fmt.Errorf("managed commit failed and was rolled back: %w", err)
}
