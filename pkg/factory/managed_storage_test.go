package factory //nolint:testpackage // The storage boundary must exercise the actual private openers and SQLite connections.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedStateByteBudget(t *testing.T) {
	if dir := os.Getenv("ORO_FACTORY_STORAGE_CRASH_DIR"); dir != "" {
		runManagedStorageCrashChild(t, dir)
		return
	}
	t.Run("fresh reopen and delivery writes", testManagedStorageOpeners)
	t.Run("admission and scope authority use rollback mode", testManagedStorageAdmission)
	t.Run("initialized state cannot reset", testManagedStorageNoReset)
	t.Run("missing objective directory cannot reset", testManagedStorageMissingDirectory)
	t.Run("root initialization authority fails closed", testManagedStorageRootAuthority)
	t.Run("missing root reservation cannot adopt local state", testManagedStorageMissingReservation)
	t.Run("pinned reader causes bounded contention", testManagedStoragePinnedReader)
	t.Run("legacy store retains WAL", testManagedStorageLegacy)
	t.Run("physical page and rollback journal bounds", testManagedStoragePhysicalLimit)
	t.Run("interrupted on-disk commit rolls back", testManagedStorageCrash)
	t.Run("marker window and incompatible state fail closed", testManagedStorageInvalidState)
	t.Run("failed journal commit releases ownership", testManagedStorageJournalRollback)
	t.Run("failed admission commit stays unpublished", testManagedStorageAdmissionRollback)
}

func managedStorageFixture(t *testing.T) (string, *factoryStore) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	return dir, store
}

func assertManagedStorageConfig(t *testing.T, conn *sql.Conn) {
	t.Helper()
	for pragma, want := range map[string]string{
		"journal_mode": "delete", "synchronous": "2", "locking_mode": "normal",
		"page_size": "4096", "max_page_count": "16384", "temp_store": "2", "cache_spill": "0", "busy_timeout": "1000",
	} {
		var got string
		if err := conn.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("managed %s = %q, %v; want %s", pragma, got, err, want)
		}
	}
}

func testManagedStorageOpeners(t *testing.T) {
	t.Helper()
	dir, store := managedStorageFixture(t)
	assertManagedStorageConfig(t, store.conn)
	state := testSnapshot()
	if err := store.save(t.Context(), state, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openManagedFactoryStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	assertManagedStorageConfig(t, reopened.conn)
	state, err = reopened.load(t.Context())
	if err != nil || state.Revision != 1 {
		t.Fatalf("managed snapshot reopen = %+v, %v", state, err)
	}
	state.State = "running"
	if err := reopened.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	assertManagedPhysicalBounds(t, dir)
}

func testManagedStorageAdmission(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	j := openJournalFixture(t, root, spec.ID)
	assertManagedStorageConfig(t, j.conn)
	if _, _, err := j.beginGeneration(t.Context(), "storage-supervisor", time.Now()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, spec.ID)
	store, err := openManagedFactoryStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	assertManagedStorageConfig(t, store.conn)
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	assertManagedPhysicalBounds(t, dir)
}

func testManagedStorageNoReset(t *testing.T) {
	t.Helper()
	dir, store := managedStorageFixture(t)
	if err := store.save(t.Context(), testSnapshot(), 0); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedFactoryStore(t.Context(), dir); err == nil {
		t.Fatal("missing initialized database silently reset")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed reopen recreated the database: %v", err)
	}
	for _, contents := range []string{"", "not a SQLite database"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openManagedFactoryStore(t.Context(), dir); err == nil {
			t.Fatalf("unexpected database contents %q silently initialized", contents)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != contents {
			t.Fatalf("rejected database was rewritten: %q, %v", got, err)
		}
	}
}

func testManagedStorageMissingDirectory(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, spec.ID)
	store, err := openManagedFactoryStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.State, state.Attempts = "running", 1
	if err := store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, root, spec.ID)
	if _, _, err := journal.beginGeneration(t.Context(), "spent-supervisor", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".saved"); err != nil {
		t.Fatal(err)
	}
	if status, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatalf("missing admitted directory reset spent state: state=%s attempts=%d", status.State, status.Attempts)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("missing initialized objective created a fresh database: %v", err)
	}
	if state, err := readSnapshot(t.Context(), dir+".saved"); err != nil || state.Attempts != 1 || state.State != "running" {
		t.Fatalf("spent original state changed: %+v, %v", state, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir+".saved", "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if state, err := loadManagedSupervision(t.Context(), db); err != nil || state.Generation != 1 || state.SupervisorToken != "spent-supervisor" {
		t.Fatalf("spent original ownership changed: %+v, %v", state, err)
	}
}

func testManagedStorageMissingReservation(t *testing.T) {
	t.Helper()
	dir, store := managedStorageFixture(t)
	if err := store.save(t.Context(), testSnapshot(), 0); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(dir), ".managed", filepath.Base(dir)+".state.init")
	if err := os.Rename(marker, marker+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedFactoryStore(t.Context(), dir); err == nil {
		t.Fatal("local database adopted without its original root reservation")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("failed reopen replaced root reservation: %v", err)
	}
	if state, err := readSnapshot(t.Context(), dir); err != nil || state.Revision != 1 {
		t.Fatalf("unadopted database changed: %+v, %v", state, err)
	}
}

func testManagedStorageRootAuthority(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"root-marker-only", "partial-root-marker", "symlink-root-marker", "symlink-namespace"} {
		t.Run(kind, func(t *testing.T) {
			dir := managedStorageRootAuthorityFixture(t, kind)
			if _, err := openManagedFactoryStore(t.Context(), dir); err == nil {
				t.Fatalf("%s permitted a fresh database", kind)
			}
			if _, err := os.Stat(filepath.Join(dir, "state.db")); !os.IsNotExist(err) {
				t.Fatalf("rejected initialization created a database: %v", err)
			}
		})
	}
}

func managedStorageRootAuthorityFixture(t *testing.T, kind string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, namespace := filepath.Join(root, "objective"), filepath.Join(root, ".managed")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if kind == "symlink-namespace" {
		if err := os.Symlink(t.TempDir(), namespace); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if err := os.Mkdir(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(namespace, "objective.state.init")
	contents := managedStateInitMarker
	if kind == "partial-root-marker" {
		contents = "partial"
	}
	if kind == "symlink-root-marker" {
		target := filepath.Join(root, "other-marker")
		if err := os.WriteFile(target, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, marker); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if err := os.WriteFile(marker, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testManagedStoragePinnedReader(t *testing.T) {
	t.Helper()
	dir, store := managedStorageFixture(t)
	state := testSnapshot()
	if err := store.save(t.Context(), state, 0); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var revision int
	if err := reader.QueryRowContext(t.Context(), "SELECT revision FROM snapshots WHERE id=1").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		started := time.Now()
		err := store.save(ctx, state, 1)
		cancel()
		if err == nil || time.Since(started) > 1500*time.Millisecond {
			t.Fatalf("pinned-reader write was unbounded or committed: elapsed=%v err=%v", time.Since(started), err)
		}
		assertManagedPhysicalBounds(t, dir)
		var actual int
		if err := reader.QueryRowContext(t.Context(), "SELECT revision FROM snapshots WHERE id=1").Scan(&actual); err != nil || actual != 1 {
			t.Fatalf("failed commit published a transition: revision=%d err=%v", actual, err)
		}
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.save(t.Context(), state, 1); err != nil {
		t.Fatalf("write did not recover after reader release: %v", err)
	}
}

func fillManagedStorage(t *testing.T, store *factoryStore, rows int) {
	t.Helper()
	if err := store.save(t.Context(), testSnapshot(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.ExecContext(t.Context(), "CREATE TABLE storage_payload(id INTEGER PRIMARY KEY, marker TEXT NOT NULL, body BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	tx, err := store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for id := range rows {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO storage_payload VALUES(?, 'committed', zeroblob(1048576))", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := commitManagedTransaction(store.conn, tx); err != nil {
		t.Fatal(err)
	}
}

func testManagedStoragePhysicalLimit(t *testing.T) {
	t.Helper()
	dir, store := managedStorageFixture(t)
	fillManagedStorage(t, store, 60)
	assertManagedPhysicalBounds(t, dir)
	tx, err := store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "UPDATE storage_payload SET marker='uncommitted', body=randomblob(length(body))"); err != nil {
		t.Fatal(err)
	}
	journal, err := os.Stat(filepath.Join(dir, "state.db-journal"))
	if err != nil || journal.Size() < 32<<20 {
		t.Fatalf("large real rollback journal was not exercised: %v, %v", journal, err)
	}
	assertManagedPhysicalBounds(t, dir)
	t.Logf("live rollback journal=%d bytes with 60 MiB committed payload", journal.Size())
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.ExecContext(t.Context(), "INSERT INTO storage_payload VALUES(100, 'overflow', zeroblob(8388608))"); err == nil {
		t.Fatal("physical database exceeded its 64 MiB data cap")
	}
	var count int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM storage_payload WHERE marker='committed'").Scan(&count); err != nil || count != 60 {
		t.Fatalf("failed oversized transaction damaged committed data: count=%d err=%v", count, err)
	}
	assertManagedPhysicalBounds(t, dir)
}

func runManagedStorageCrashChild(t *testing.T, dir string) {
	t.Helper()
	store, err := openManagedFactoryStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	assertManagedStorageConfig(t, store.conn)
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "UPDATE storage_payload SET marker='uncommitted', body=randomblob(length(body))"); err != nil {
		t.Fatal(err)
	}
	state.Revision, state.State = 2, "running"
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(t.Context(), tx, body, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO events(revision,state,body) VALUES(2,'running',?)", body); err != nil {
		t.Fatal(err)
	}
	fmt.Println("DIRTY")
	var goSignal [1]byte
	if _, err := io.ReadFull(os.Stdin, goSignal[:]); err != nil {
		t.Fatal(err)
	}
	if err := commitManagedTransaction(store.conn, tx); err != nil {
		t.Fatal(err)
	}
}

func testManagedStorageCrash(t *testing.T) {
	t.Helper()
	for attempt := 1; attempt <= 3; attempt++ {
		dir, store := managedStorageFixture(t)
		fillManagedStorage(t, store, 16)
		if err := store.close(); err != nil {
			t.Fatal(err)
		}
		interrupted := killDuringManagedCommit(t, dir)
		reopened, err := openManagedFactoryStore(t.Context(), dir)
		if err != nil {
			t.Fatal(err)
		}
		state, err := reopened.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var changed int
		if err := reopened.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM storage_payload WHERE marker='uncommitted'").Scan(&changed); err != nil {
			t.Fatal(err)
		}
		if interrupted && (state.Revision != 1 || changed != 0) {
			t.Fatalf("interrupted commit failed atomic rollback: revision=%d changed=%d", state.Revision, changed)
		}
		if !interrupted && (state.Revision != 2 || changed != 16) {
			t.Fatalf("winning commit was not fully atomic: revision=%d changed=%d", state.Revision, changed)
		}
		if err := reopened.close(); err != nil {
			t.Fatal(err)
		}
		assertManagedPhysicalBounds(t, dir)
		t.Logf("commit interruption attempt=%d retained_hot_journal=%v recovered_revision=%d changed_rows=%d", attempt, interrupted, state.Revision, changed)
		if interrupted {
			return
		}
	}
	t.Fatal("no interrupted on-disk commit observed in 3 bounded attempts")
}

func killDuringManagedCommit(t *testing.T, dir string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedStateByteBudget$", "-test.count=1")
	command.Env = append(os.Environ(), "ORO_FACTORY_STORAGE_CRASH_DIR="+dir)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "DIRTY\n" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("crash child barrier = %q, %v; context=%v stderr=%s", ready, err, ctx.Err(), stderr.String())
	}
	file, err := os.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before := make([]byte, 100)
	if _, err := file.ReadAt(before, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte{'G'}); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	changed := waitManagedDiskChange(t, file, before, dir)
	if changed {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	hot := managedHotJournal(t, dir)
	if ctx.Err() != nil || (hot && (!changed || waitErr == nil)) {
		t.Fatalf("no valid interrupted-commit evidence: changed=%v hot=%v wait=%v context=%v stderr=%s", changed, hot, waitErr, ctx.Err(), stderr.String())
	}
	assertManagedPhysicalBounds(t, dir)
	return hot
}

func pinManagedReader(t *testing.T, dir, query string) *sql.Tx {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reader, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Rollback() })
	var count int
	if err := reader.QueryRowContext(t.Context(), query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return reader
}

func testManagedStorageJournalRollback(t *testing.T) {
	t.Helper()
	root, admission, store := journalFixture(t)
	insertJournalFixture(t, store, admission)
	j := openJournalFixture(t, root, admission.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, admission.ObjectiveID)
	reader := pinManagedReader(t, dir, "SELECT count(*) FROM managed_reservations")
	started := time.Now()
	if fresh, err := j.reserveBudget(t.Context(), gen, "host", "blocked-host", time.Now()); err == nil || fresh || time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("journal commit contention = fresh:%v err:%v elapsed:%v", fresh, err, time.Since(started))
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE token='blocked-host'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed journal commit spent a reservation: count=%d err=%v", count, err)
	}
	if fresh, err := j.reserveBudget(t.Context(), gen, "host", "blocked-host", time.Now()); err != nil || !fresh {
		t.Fatalf("clean journal retry = %v, %v", fresh, err)
	}
	assertManagedPhysicalBounds(t, dir)
}

func testManagedStorageAdmissionRollback(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	dir := filepath.Join(root, spec.ID)
	var reader *sql.Tx
	hooks := managedAdmissionHooks{beforeCommit: func(*sql.Tx) error {
		reader = pinManagedReader(t, dir, "SELECT count(*) FROM snapshots")
		return nil
	}}
	if _, err := admitManaged(t.Context(), root, spec, reservation, hooks); err == nil {
		t.Fatal("pinned reader did not reject the admission commit")
	}
	if reader == nil {
		t.Fatal("admission did not reach its real commit boundary")
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(t.Context(), dir); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed admission published a snapshot: %v", err)
	}
	if _, err := OpenScopeAuthority(t.Context(), root, spec.ID); err == nil {
		t.Fatal("failed admission published ownership authority")
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatalf("bounded admission retry after lock release failed: %v", err)
	}
	assertManagedPhysicalBounds(t, dir)
}

func waitManagedDiskChange(t *testing.T, file *os.File, before []byte, dir string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	header := make([]byte, len(before))
	for time.Now().Before(deadline) {
		if _, err := file.ReadAt(header, 0); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(header, before) {
			return true
		}
		if _, err := os.Stat(filepath.Join(dir, "state.db-journal")); os.IsNotExist(err) {
			return false
		}
		time.Sleep(100 * time.Microsecond)
	}
	t.Fatal("commit did not reach a bounded on-disk observation")
	return false
}

func managedHotJournal(t *testing.T, dir string) bool {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, "state.db-journal"))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var header [8]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(header[:], []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7})
}

func testManagedStorageInvalidState(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"marker-only", "wrong-page-size", "unexpected-schema"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state.init"), []byte(managedStateInitMarker), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind != "marker-only" {
				createIncompatibleManagedFixture(t, dir, kind)
			}
			if _, err := openManagedFactoryStore(t.Context(), dir); err == nil {
				t.Fatalf("%s was silently initialized or rewritten", kind)
			}
		})
	}
}

func createIncompatibleManagedFixture(t *testing.T, dir, kind string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statement := "CREATE TABLE unrelated(id INTEGER)"
	if kind == "wrong-page-size" {
		statement = "PRAGMA page_size=8192; CREATE TABLE unrelated(id INTEGER)"
	}
	if _, err := db.ExecContext(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
}

func testManagedStorageLegacy(t *testing.T) {
	t.Helper()
	store, err := openStore(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var mode string
	if err := store.conn.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("legacy journal mode changed: %q, %v", mode, err)
	}
}

func assertManagedPhysicalBounds(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "state.db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name() == "state.db-wal" || entry.Name() == "state.db-shm" {
			t.Fatalf("managed write created WAL authority: %s (%d bytes)", entry.Name(), info.Size())
		}
		if entry.Name() == "state.db" && info.Size() > 64<<20 {
			t.Fatalf("managed data exceeded 64 MiB: %d", info.Size())
		}
		total += info.Size()
	}
	if total >= 256<<20 {
		t.Fatalf("managed database/transient files exceeded 256 MiB: %d", total)
	}
}
