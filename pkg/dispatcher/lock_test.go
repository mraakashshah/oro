package dispatcher //nolint:testpackage // white-box tests exercise unexported lock helpers.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestPIDLockSameStateDBRejectsSecondDispatcherRun(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")

	d1Socket := shortPIDLockSocketPath(t, "d1")
	d1, db1 := newPIDLockTestDispatcher(t, dbPath, d1Socket)
	defer db1.Close()
	ctx1, cancel1 := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- d1.Run(ctx1)
	}()
	t.Cleanup(func() {
		cancel1()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("first dispatcher Run() returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("first dispatcher did not stop")
		}
	})

	waitFor(t, func() bool {
		d1.mu.Lock()
		defer d1.mu.Unlock()
		return d1.listener != nil
	}, 2*time.Second)

	d2Socket := shortPIDLockSocketPath(t, "d2")
	d2, db2 := newPIDLockTestDispatcher(t, dbPath, d2Socket)
	defer db2.Close()
	if err := d2.Run(context.Background()); !errors.Is(err, ErrLocked) {
		t.Fatalf("second dispatcher Run() error = %v, want ErrLocked", err)
	}
}

func shortPIDLockSocketPath(t *testing.T, name string) string {
	t.Helper()
	path := fmt.Sprintf("/tmp/oro-pidlock-%d-%s.sock", time.Now().UnixNano(), name)
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func TestPIDLockDifferentStateDBsDoNotConflict(t *testing.T) {
	tmp := t.TempDir()
	first, err := acquirePIDLock(filepath.Join(tmp, "first.db"))
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer first.release()

	second, err := acquirePIDLock(filepath.Join(tmp, "second.db"))
	if err != nil {
		t.Fatalf("acquire second lock in same directory: %v", err)
	}
	defer second.release()
}

func TestPIDLockConcurrentAcquisitionAllowsOnlyOneOwner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	start := make(chan struct{})
	errCh := make(chan error, 2)
	releaseCh := make(chan *pidLock, 2)

	for range 2 {
		go func() {
			<-start
			lock, err := acquirePIDLock(dbPath)
			if err == nil {
				releaseCh <- lock
			}
			errCh <- err
		}()
	}
	close(start)

	var locked, acquired int
	for range 2 {
		err := <-errCh
		switch {
		case err == nil:
			acquired++
		case errors.Is(err, ErrLocked):
			locked++
		default:
			t.Fatalf("acquire error = %v, want nil or ErrLocked", err)
		}
	}
	for range acquired {
		lock := <-releaseCh
		if err := lock.release(); err != nil {
			t.Fatalf("release lock: %v", err)
		}
	}
	if acquired != 1 || locked != 1 {
		t.Fatalf("acquired=%d locked=%d, want exactly one owner and one ErrLocked", acquired, locked)
	}
}

func TestPIDLockSymlinkedStateDBUsesCanonicalLock(t *testing.T) {
	tmp := t.TempDir()
	realDB := filepath.Join(tmp, "real.db")
	if err := os.WriteFile(realDB, nil, 0o600); err != nil {
		t.Fatalf("write real db: %v", err)
	}
	linkDB := filepath.Join(tmp, "link.db")
	if err := os.Symlink(realDB, linkDB); err != nil {
		t.Fatalf("symlink db: %v", err)
	}

	lock, err := acquirePIDLock(realDB)
	if err != nil {
		t.Fatalf("acquire real db lock: %v", err)
	}
	defer lock.release()

	if _, err := acquirePIDLock(linkDB); !errors.Is(err, ErrLocked) {
		t.Fatalf("acquire symlinked db lock error = %v, want ErrLocked", err)
	}
}

func TestPIDLockStaleDeadPIDIsReclaimed(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	lockPath := dbPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	lock, err := acquirePIDLock(dbPath)
	if err != nil {
		t.Fatalf("acquire after dead PID lock: %v", err)
	}
	defer lock.release()

	got, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read reclaimed lock: %v", err)
	}
	want := strconv.Itoa(os.Getpid()) + "\n"
	if string(got) != want {
		t.Fatalf("lock file was not replaced with current PID, got %q, want %q", got, want)
	}
}

func TestPIDLockOldLivePIDLockIsReclaimed(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	lockPath := dbPath + ".lock"
	oldPID := strconv.Itoa(os.Getpid()) + "\n"
	if err := os.WriteFile(lockPath, []byte(oldPID), 0o600); err != nil {
		t.Fatalf("write old lock: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age old lock: %v", err)
	}

	lock, err := acquirePIDLock(dbPath)
	if err != nil {
		t.Fatalf("acquire after old live PID lock: %v", err)
	}
	defer lock.release()
}

func TestPIDLockRefreshKeepsOwnedLockCurrent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	lock, err := acquirePIDLock(dbPath)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer lock.release()

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lock.path, old, old); err != nil {
		t.Fatalf("age owned lock: %v", err)
	}
	if err := lock.refresh(); err != nil {
		t.Fatalf("refresh owned lock: %v", err)
	}

	if _, err := acquirePIDLock(dbPath); !errors.Is(err, ErrLocked) {
		t.Fatalf("acquire refreshed lock error = %v, want ErrLocked", err)
	}
}

func newPIDLockTestDispatcher(t *testing.T, dbPath, socketPath string) (*Dispatcher, *sql.DB) {
	t.Helper()
	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	repoRoot := t.TempDir()
	beadsDir := filepath.Join(repoRoot, protocol.BeadsDir)
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}

	cfg := Config{
		SocketPath:       socketPath,
		DBPath:           dbPath,
		RepoRoot:         repoRoot,
		BeadsDir:         beadsDir,
		MaxWorkers:       1,
		InitialWorkers:   1,
		HeartbeatTimeout: 100 * time.Millisecond,
		PollInterval:     20 * time.Millisecond,
		ShutdownTimeout:  100 * time.Millisecond,
	}
	d, err := New(cfg, db, merge.NewCoordinator(&mockGitRunner{}), ops.NewSpawner(&mockBatchSpawner{}),
		&mockBeadSource{shown: make(map[string]*protocol.BeadDetail)},
		&mockWorktreeManager{created: make(map[string]string)},
		&mockEscalator{}, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("New(): %v", err)
	}
	return d, db
}
