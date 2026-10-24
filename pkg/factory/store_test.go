package factory //nolint:testpackage // Durable state transitions are private lifecycle seams.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testSnapshot() snapshot {
	created := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	return snapshot{
		Spec: contractSpec(), PolicyHash: "policy", PolicyDigests: map[string]string{"worker": "digest"},
		CreatedAt: created, Deadline: created.Add(5 * time.Minute), State: "pending",
	}
}

func TestFactoryStore(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "objective")
	if _, err := readSnapshot(ctx, dir); err == nil {
		t.Fatal("missing read succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("missing read created filesystem state")
	}
	store, err := openStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var synchronous int
	if err := store.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("synchronous = %d, %v", synchronous, err)
	}
	if _, err := store.load(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing snapshot = %v", err)
	}
	want := testSnapshot()
	if err := store.save(ctx, want, 0); err != nil {
		t.Fatal(err)
	}
	want.Revision = 1
	assertSnapshot(t, store, want)
	if err := store.save(ctx, want, 0); err == nil {
		t.Fatal("stale insert succeeded")
	}
	want.State, want.Attempts = "running", 1
	if err := store.save(ctx, want, 1); err != nil {
		t.Fatal(err)
	}
	want.Revision = 2
	assertSnapshot(t, store, want)
	if err := store.save(ctx, want, 1); err == nil {
		t.Fatal("stale update succeeded")
	}
	var events int
	if err := store.conn.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&events); err != nil || events != 2 {
		t.Fatalf("events = %d, %v", events, err)
	}
	if _, err := store.conn.ExecContext(ctx, "CREATE TRIGGER reject_events BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'injected event failure'); END"); err != nil {
		t.Fatal(err)
	}
	next := want
	next.State = "candidate"
	if err := store.save(ctx, next, 2); err == nil {
		t.Fatal("event failure succeeded")
	}
	assertSnapshot(t, store, want)
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	assertSnapshot(t, reopened, want)
	got, err := readSnapshot(ctx, dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only snapshot = %#v, %v", got, err)
	}
}

func assertSnapshot(t *testing.T, store *factoryStore, want snapshot) {
	t.Helper()
	got, err := store.load(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mismatch: got %#v, want %#v, error %v", got, want, err)
	}
}

func TestStoreFailures(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(ctx, file); err == nil {
		t.Fatal("invalid directory opened")
	}
	store, err := openStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(ctx, testSnapshot(), -1); err == nil {
		t.Fatal("negative revision accepted")
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(ctx); err == nil {
		t.Fatal("closed store loaded")
	}
	if err := store.save(ctx, testSnapshot(), 0); err == nil {
		t.Fatal("closed store saved")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := openStore(cancelled, t.TempDir()); err == nil {
		t.Fatal("cancelled open succeeded")
	}
}

func TestDurableAttemptBudget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	state := testSnapshot()
	deadline := state.Deadline
	store, err := openStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(ctx, state, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		store, err = openStore(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		state, err = store.load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state.CandidateSHA, state.BaseSHA, state.Worktree = "old-candidate", "old-base", "old-worktree"
		state.Acceptance, state.Review = &evidence{}, &evidence{}
		state.Feedback = "repair the observed failure"
		next := reserveAttempt(state, state.CreatedAt.Add(time.Duration(attempt)*time.Second))
		if !next.Deadline.Equal(deadline) {
			t.Fatal("reservation moved deadline")
		}
		if attempt <= 3 {
			assertReservedAttempt(t, next, attempt, state.Feedback)
		} else if next.State != "failed" || next.Attempts != 3 || next.Reason == "" {
			t.Fatalf("fourth reservation = %#v", next)
		}
		if err := store.save(ctx, next, state.Revision); err != nil {
			t.Fatal(err)
		}
		if err := store.close(); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := readSnapshot(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || failed.Attempts != 3 || !failed.Deadline.Equal(deadline) {
		t.Fatalf("terminal budget not durable: %#v", failed)
	}
	if got := reserveAttempt(failed, failed.CreatedAt); !reflect.DeepEqual(got, failed) {
		t.Fatal("failed budget reopened")
	}
	for _, status := range []string{"pending", "running", "candidate", "accepted", "approved"} {
		state := testSnapshot()
		state.State, state.Attempts = status, 1
		got := reserveAttempt(state, state.Deadline)
		if got.State != "failed" || got.Attempts != 1 || got.Reason == "" || !got.Deadline.Equal(state.Deadline) {
			t.Fatalf("deadline did not fail closed: %#v", got)
		}
	}
	completed := testSnapshot()
	completed.State = "completed"
	if got := reserveAttempt(completed, completed.Deadline.Add(time.Hour)); !reflect.DeepEqual(got, completed) {
		t.Fatal("completed objective reopened")
	}
}

func assertReservedAttempt(t *testing.T, next snapshot, attempt int, feedback string) {
	t.Helper()
	if next.State != "running" || next.Attempts != attempt {
		t.Fatalf("reservation %d = %#v", attempt, next)
	}
	if next.CandidateSHA != "" || next.BaseSHA != "" || next.Worktree != "" || next.Acceptance != nil || next.Review != nil {
		t.Fatal("stale attempt authority retained")
	}
	if next.Feedback != feedback {
		t.Fatal("repair feedback lost")
	}
}
