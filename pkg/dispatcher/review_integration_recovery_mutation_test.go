//nolint:testpackage // white-box assertions for durable review integration recovery
package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/merge"
	"oro/pkg/protocol"
)

type reviewIntegrationRecoveryMutationRunner struct {
	calls [][]string
	run   func(context.Context, string, ...string) ([]byte, error)
}

func (r *reviewIntegrationRecoveryMutationRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.run == nil {
		return nil, nil
	}
	return r.run(ctx, name, args...)
}

type reviewIntegrationRecoveryMutationBeadStore struct {
	*beadstore.SQLiteStore
	showFn  func(context.Context, string) (*protocol.Bead, error)
	closeFn func(context.Context, string, string) error
	findFn  func(context.Context, string, string) ([]protocol.Bead, error)
}

type reviewIntegrationRecoveryMutationGitRunner struct{}

func (reviewIntegrationRecoveryMutationGitRunner) Run(
	context.Context,
	string,
	...string,
) (string, string, error) {
	return "", "", errors.New("unexpected merge command")
}

func reviewIntegrationRecoveryMutationExitOne(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("construct exit-one error: %v", err)
	}
	return err
}

func (s *reviewIntegrationRecoveryMutationBeadStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	if s.showFn != nil {
		return s.showFn(ctx, id)
	}
	return s.SQLiteStore.Show(ctx, id)
}

func (s *reviewIntegrationRecoveryMutationBeadStore) Close(ctx context.Context, id, reason string) error {
	if s.closeFn != nil {
		return s.closeFn(ctx, id, reason)
	}
	return s.SQLiteStore.Close(ctx, id, reason)
}

func (s *reviewIntegrationRecoveryMutationBeadStore) FindByParentAndTag(
	ctx context.Context,
	parentID, tag string,
) ([]protocol.Bead, error) {
	if s.findFn != nil {
		return s.findFn(ctx, parentID, tag)
	}
	return s.SQLiteStore.FindByParentAndTag(ctx, parentID, tag)
}

func newReviewIntegrationRecoveryMutationDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("initialize dispatcher schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(context.Background(), db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	return db
}

func insertReviewIntegrationRecoveryMutationAssignment(
	t *testing.T,
	db *sql.DB,
	beadID, status string,
) int64 {
	t.Helper()

	result, err := db.Exec(`
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, 'mutation-worker', '/tmp/mutation-worktree', ?)`, beadID, status)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	return id
}

func insertReviewIntegrationRecoveryMutationCheckpoint(
	t *testing.T,
	db *sql.DB,
	key, beadID string,
	assignmentID int64,
	state ReviewCheckpointState,
	step string,
) *ReviewIntegrationCheckpoint {
	t.Helper()

	result, err := db.Exec(`
INSERT INTO review_checkpoints (
  checkpoint_key, bead_id, origin_assignment_id, current_assignment_id, worker_id,
  worktree, branch, target_branch, head_sha, target_sha, acceptance_hash,
  qg_script_hash, qg_mode, review_policy_hash, triage_revision, ready_attempt, state,
  integration_target_before_sha, integration_approved_head_sha,
  integration_observed_target_sha, integration_step
) VALUES (?, ?, ?, NULL, 'mutation-worker', '/worktree', 'agent/bead', 'main',
          'approved', 'base', 'acceptance', 'qg-script', 'full', 'policy',
          'triage', 'attempt', ?, 'base', 'approved', 'current', ?)`,
		key, beadID, assignmentID, state, step)
	if err != nil {
		t.Fatalf("insert review checkpoint: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("checkpoint id: %v", err)
	}
	return &ReviewIntegrationCheckpoint{
		ReviewCheckpoint: ReviewCheckpoint{ID: id, CheckpointInput: CheckpointInput{
			CheckpointKey: key, BeadID: beadID, OriginAssignmentID: assignmentID,
			WorkerID: "mutation-worker", Worktree: "/worktree", Branch: "agent/bead",
			TargetBranch: "main", HeadSHA: "approved", TargetSHA: "base", State: state,
		}},
		IntegrationTargetBeforeSHA:   "base",
		IntegrationApprovedHeadSHA:   "approved",
		IntegrationObservedTargetSHA: "current",
		IntegrationStep:              step,
	}
}

func newReviewIntegrationRecoveryMutationDispatcher(
	t *testing.T,
) (*Dispatcher, *reviewIntegrationRecoveryMutationBeadStore, *ReviewCheckpointStore) {
	t.Helper()
	db := newReviewIntegrationRecoveryMutationDB(t)
	beads := &reviewIntegrationRecoveryMutationBeadStore{SQLiteStore: beadstore.NewSQLiteStore(db)}
	d := &Dispatcher{db: db, beads: beads, repoRoot: "/repo"}
	d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{})
	return d, beads, NewReviewCheckpointStore(db)
}

func TestReviewIntegrationRecoveryMutationCompleteCheckpointAssignment(t *testing.T) {
	ctx := context.Background()

	t.Run("requires exact positive assignment identity", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		d := &Dispatcher{db: db}
		for _, assignmentID := range []int64{0, -1} {
			err := d.completeCheckpointAssignment(ctx, assignmentID, "bead-a")
			if err == nil || !strings.Contains(err.Error(), "missing exact assignment identity") {
				t.Fatalf("assignment %d error = %v, want exact-identity error", assignmentID, err)
			}
		}
	})

	t.Run("requires bead identity", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		d := &Dispatcher{db: db}
		err := d.completeCheckpointAssignment(ctx, 1, "")
		if err == nil || !strings.Contains(err.Error(), "missing exact assignment identity") {
			t.Fatalf("error = %v, want exact-identity error", err)
		}
	})

	for _, status := range []string{"active", "requeued"} {
		t.Run("completes "+status+" assignment", func(t *testing.T) {
			db := newReviewIntegrationRecoveryMutationDB(t)
			id := insertReviewIntegrationRecoveryMutationAssignment(t, db, "bead-a", status)
			d := &Dispatcher{db: db}
			if err := d.completeCheckpointAssignment(ctx, id, "bead-a"); err != nil {
				t.Fatalf("complete assignment: %v", err)
			}
			var gotStatus string
			var completedAt sql.NullString
			if err := db.QueryRow(`SELECT status, completed_at FROM assignments WHERE id=?`, id).
				Scan(&gotStatus, &completedAt); err != nil {
				t.Fatalf("load assignment: %v", err)
			}
			if gotStatus != "completed" || !completedAt.Valid || completedAt.String == "" {
				t.Fatalf("assignment = status %q completed_at %#v, want durable completion", gotStatus, completedAt)
			}
		})
	}

	t.Run("accepts exact already-completed replay", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		id := insertReviewIntegrationRecoveryMutationAssignment(t, db, "bead-a", "completed")
		d := &Dispatcher{db: db}
		if err := d.completeCheckpointAssignment(ctx, id, "bead-a"); err != nil {
			t.Fatalf("idempotent completion: %v", err)
		}
	})

	t.Run("rejects missing assignment", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		d := &Dispatcher{db: db}
		err := d.completeCheckpointAssignment(ctx, 4242, "bead-a")
		if err == nil || !strings.Contains(err.Error(), "assignment 4242 not found") {
			t.Fatalf("error = %v, want not-found identity error", err)
		}
	})

	t.Run("rejects assignment owned by another bead", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		id := insertReviewIntegrationRecoveryMutationAssignment(t, db, "bead-other", "active")
		d := &Dispatcher{db: db}
		err := d.completeCheckpointAssignment(ctx, id, "bead-a")
		if err == nil || !strings.Contains(err.Error(), `owns bead "bead-other" in status "active"`) {
			t.Fatalf("error = %v, want ownership mismatch", err)
		}
	})

	t.Run("rejects exact assignment in a non-completable state", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		id := insertReviewIntegrationRecoveryMutationAssignment(t, db, "bead-a", "failed")
		d := &Dispatcher{db: db}
		err := d.completeCheckpointAssignment(ctx, id, "bead-a")
		if err == nil || !strings.Contains(err.Error(), `owns bead "bead-a" in status "failed"`) {
			t.Fatalf("error = %v, want status mismatch", err)
		}
	})

	t.Run("propagates update failure", func(t *testing.T) {
		db := newReviewIntegrationRecoveryMutationDB(t)
		if _, err := db.Exec(`DROP TABLE assignments`); err != nil {
			t.Fatalf("drop assignments: %v", err)
		}
		d := &Dispatcher{db: db}
		err := d.completeCheckpointAssignment(ctx, 1, "bead-a")
		if err == nil || !strings.Contains(err.Error(), "complete checkpoint assignment") {
			t.Fatalf("error = %v, want contextual update failure", err)
		}
	})
}

func TestReviewIntegrationRecoveryMutationReferenceResolution(t *testing.T) {
	ctx := context.Background()

	t.Run("ref requires workdir", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{}
		d := &Dispatcher{}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationRefSHA(ctx, "", "HEAD"); err == nil || !strings.Contains(err.Error(), "missing workdir or ref") {
			t.Fatalf("error = %v, want missing identity", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner calls = %v, want none", runner.calls)
		}
	})

	t.Run("ref requires name", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{}
		d := &Dispatcher{}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationRefSHA(ctx, "/repo", ""); err == nil || !strings.Contains(err.Error(), "missing workdir or ref") {
			t.Fatalf("error = %v, want missing identity", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner calls = %v, want none", runner.calls)
		}
	})

	t.Run("ref propagates command failure", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("rev-parse failed")
		}}
		d := &Dispatcher{}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationRefSHA(ctx, "/repo", "HEAD"); err == nil ||
			!strings.Contains(err.Error(), "resolve HEAD in /repo: rev-parse failed") {
			t.Fatalf("error = %v, want contextual command failure", err)
		}
	})

	t.Run("ref rejects empty object identity", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(" \n"), nil
		}}
		d := &Dispatcher{}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationRefSHA(ctx, "/repo", "HEAD"); err == nil || !strings.Contains(err.Error(), "empty object ID") {
			t.Fatalf("error = %v, want empty-object error", err)
		}
	})

	t.Run("ref returns trimmed identity using exact git command", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(" abc123\n"), nil
		}}
		d := &Dispatcher{}
		d.setCommandRunner(runner)
		sha, err := d.reviewIntegrationRefSHA(ctx, "/repo", "HEAD^{commit}")
		if err != nil || sha != "abc123" {
			t.Fatalf("sha/error = %q/%v, want abc123/nil", sha, err)
		}
		want := []string{"git", "-C", "/repo", "rev-parse", "HEAD^{commit}"}
		if len(runner.calls) != 1 || strings.Join(runner.calls[0], "|") != strings.Join(want, "|") {
			t.Fatalf("runner calls = %v, want %v", runner.calls, want)
		}
	})

	t.Run("target requires repository", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{}
		d := &Dispatcher{}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationTargetSHA(ctx, "main"); err == nil || !strings.Contains(err.Error(), "missing repository or target branch") {
			t.Fatalf("error = %v, want missing identity", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner calls = %v, want none", runner.calls)
		}
	})

	t.Run("target requires branch", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationTargetSHA(ctx, ""); err == nil || !strings.Contains(err.Error(), "missing repository or target branch") {
			t.Fatalf("error = %v, want missing identity", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner calls = %v, want none", runner.calls)
		}
	})

	t.Run("target propagates command failure", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("target failed")
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationTargetSHA(ctx, "main"); err == nil || !strings.Contains(err.Error(), "resolve integration target main: target failed") {
			t.Fatalf("error = %v, want contextual target failure", err)
		}
	})

	t.Run("target rejects empty identity", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("\n"), nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		if _, err := d.reviewIntegrationTargetSHA(ctx, "main"); err == nil || !strings.Contains(err.Error(), "empty object ID") {
			t.Fatalf("error = %v, want empty-object error", err)
		}
	})

	t.Run("target returns trimmed identity using exact git command", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(" target-sha \n"), nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		sha, err := d.reviewIntegrationTargetSHA(ctx, "release")
		if err != nil || sha != "target-sha" {
			t.Fatalf("sha/error = %q/%v, want target-sha/nil", sha, err)
		}
		want := []string{"git", "-C", "/repo", "rev-parse", "release^{commit}"}
		if len(runner.calls) != 1 || strings.Join(runner.calls[0], "|") != strings.Join(want, "|") {
			t.Fatalf("runner calls = %v, want %v", runner.calls, want)
		}
	})
}

func TestReviewIntegrationRecoveryMutationCloseIntegratedBeadOnce(t *testing.T) {
	ctx := context.Background()
	newDispatcher := func(t *testing.T) (*Dispatcher, *reviewIntegrationRecoveryMutationBeadStore) {
		t.Helper()
		db := newReviewIntegrationRecoveryMutationDB(t)
		store := &reviewIntegrationRecoveryMutationBeadStore{SQLiteStore: beadstore.NewSQLiteStore(db)}
		return &Dispatcher{db: db, beads: store}, store
	}
	createBead := func(t *testing.T, store *reviewIntegrationRecoveryMutationBeadStore, id, beadType string) {
		t.Helper()
		if _, err := store.Create(ctx, beadstore.CreateParams{ID: id, Title: id, Type: beadType, Status: "in_progress"}); err != nil {
			t.Fatalf("create bead: %v", err)
		}
	}

	t.Run("requires bead identity", func(t *testing.T) {
		d, _ := newDispatcher(t)
		if err := d.closeIntegratedBeadOnce(ctx, "", "target"); err == nil || !strings.Contains(err.Error(), "missing integrated bead") {
			t.Fatalf("error = %v, want missing identity", err)
		}
	})

	t.Run("requires observed target identity", func(t *testing.T) {
		d, _ := newDispatcher(t)
		if err := d.closeIntegratedBeadOnce(ctx, "bead", ""); err == nil || !strings.Contains(err.Error(), "missing integrated bead") {
			t.Fatalf("error = %v, want missing identity", err)
		}
	})

	t.Run("propagates bead observation failure", func(t *testing.T) {
		d, store := newDispatcher(t)
		store.showFn = func(context.Context, string) (*protocol.Bead, error) { return nil, errors.New("show failed") }
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err == nil || !strings.Contains(err.Error(), "observe integrated bead before close: show failed") {
			t.Fatalf("error = %v, want observation failure", err)
		}
	})

	t.Run("rejects missing bead", func(t *testing.T) {
		d, store := newDispatcher(t)
		store.showFn = func(context.Context, string) (*protocol.Bead, error) { return nil, nil }
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err == nil || !strings.Contains(err.Error(), "bead bead not found") {
			t.Fatalf("error = %v, want not-found error", err)
		}
	})

	t.Run("rejects conflicting closed reason", func(t *testing.T) {
		d, store := newDispatcher(t)
		store.showFn = func(context.Context, string) (*protocol.Bead, error) {
			return &protocol.Bead{ID: "bead", Type: "task", Status: "closed", CloseReason: "Merged: other"}, nil
		}
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err == nil || !strings.Contains(err.Error(), `reason "Merged: other", want "Merged: target"`) {
			t.Fatalf("error = %v, want close-reason conflict", err)
		}
	})

	t.Run("accepts exact closed replay without closing again", func(t *testing.T) {
		d, store := newDispatcher(t)
		closeCalls := 0
		store.showFn = func(context.Context, string) (*protocol.Bead, error) {
			return &protocol.Bead{ID: "bead", Type: "task", Status: "closed", CloseReason: "Merged: target"}, nil
		}
		store.closeFn = func(context.Context, string, string) error { closeCalls++; return nil }
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err != nil {
			t.Fatalf("closed replay: %v", err)
		}
		if closeCalls != 0 {
			t.Fatalf("close calls = %d, want zero", closeCalls)
		}
	})

	t.Run("closes open bead with exact merge reason", func(t *testing.T) {
		d, store := newDispatcher(t)
		createBead(t, store, "bead", "task")
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err != nil {
			t.Fatalf("close integrated bead: %v", err)
		}
		bead, err := store.Show(ctx, "bead")
		if err != nil || bead == nil || bead.Status != "closed" || bead.CloseReason != "Merged: target" {
			t.Fatalf("bead/error = %#v/%v, want exact durable close", bead, err)
		}
	})

	t.Run("records nonfatal child sweep failure on closed replay", func(t *testing.T) {
		d, store := newDispatcher(t)
		store.showFn = func(context.Context, string) (*protocol.Bead, error) {
			return &protocol.Bead{ID: "bead", Type: "research", Status: "closed", CloseReason: "Merged: target"}, nil
		}
		store.findFn = func(context.Context, string, string) ([]protocol.Bead, error) {
			return nil, errors.New("find children failed")
		}
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err != nil {
			t.Fatalf("closed replay: %v", err)
		}
		var events int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='close_bead_sweep_failed' AND bead_id='bead'`).Scan(&events); err != nil {
			t.Fatalf("count sweep events: %v", err)
		}
		if events != 1 {
			t.Fatalf("sweep failure events = %d, want 1", events)
		}
	})

	t.Run("propagates native close failure", func(t *testing.T) {
		d, store := newDispatcher(t)
		store.showFn = func(context.Context, string) (*protocol.Bead, error) {
			return &protocol.Bead{ID: "bead", Type: "task", Status: "in_progress"}, nil
		}
		store.closeFn = func(context.Context, string, string) error { return errors.New("close failed") }
		if err := d.closeIntegratedBeadOnce(ctx, "bead", "target"); err == nil || !strings.Contains(err.Error(), "Store.Close(bead): close failed") {
			t.Fatalf("error = %v, want close failure", err)
		}
	})
}

func TestReviewIntegrationRecoveryMutationAncestryAndProof(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		older string
		newer string
	}{
		{name: "missing older", newer: "new"},
		{name: "missing newer", older: "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &reviewIntegrationRecoveryMutationRunner{}
			d := &Dispatcher{repoRoot: "/repo"}
			d.setCommandRunner(runner)
			ok, err := d.reviewIntegrationAncestor(ctx, tc.older, tc.newer)
			if ok || err == nil || !strings.Contains(err.Error(), "missing ancestry identity") {
				t.Fatalf("result = %v/%v, want missing-identity failure", ok, err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("runner calls = %v, want none", runner.calls)
			}
		})
	}

	t.Run("ancestor returns true on git success", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationAncestor(ctx, "old", "new")
		if err != nil || !ok {
			t.Fatalf("result = %v/%v, want true/nil", ok, err)
		}
		want := []string{"git", "-C", "/repo", "merge-base", "--is-ancestor", "old", "new"}
		if len(runner.calls) != 1 || strings.Join(runner.calls[0], "|") != strings.Join(want, "|") {
			t.Fatalf("runner calls = %v, want %v", runner.calls, want)
		}
	})

	t.Run("ancestor returns false for exact git exit one", func(t *testing.T) {
		exitOne := reviewIntegrationRecoveryMutationExitOne(t)
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, exitOne
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationAncestor(ctx, "old", "new")
		if err != nil || ok {
			t.Fatalf("result = %v/%v, want false/nil", ok, err)
		}
	})

	t.Run("ancestor propagates non-ancestry failure", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("git unavailable")
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationAncestor(ctx, "old", "new")
		if ok || err == nil || !strings.Contains(err.Error(), "prove integration ancestry old..new: git unavailable") {
			t.Fatalf("result = %v/%v, want contextual failure", ok, err)
		}
	})

	t.Run("proof requires approved head ancestry", func(t *testing.T) {
		exitOne := reviewIntegrationRecoveryMutationExitOne(t)
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, exitOne
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationProof(ctx, "base", "approved", "current")
		if err != nil || ok || len(runner.calls) != 1 {
			t.Fatalf("result/calls = %v/%v/%v, want false/nil/one", ok, err, runner.calls)
		}
	})

	t.Run("proof requires target ancestry", func(t *testing.T) {
		exitOne := reviewIntegrationRecoveryMutationExitOne(t)
		calls := 0
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			if calls == 2 {
				return nil, exitOne
			}
			return nil, nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationProof(ctx, "base", "approved", "current")
		if err != nil || ok || len(runner.calls) != 2 {
			t.Fatalf("result/calls = %v/%v/%v, want false/nil/two", ok, err, runner.calls)
		}
	})

	t.Run("proof propagates ancestry error", func(t *testing.T) {
		calls := 0
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			if calls == 2 {
				return nil, errors.New("second ancestry failed")
			}
			return nil, nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationProof(ctx, "base", "approved", "current")
		if ok || err == nil || !strings.Contains(err.Error(), "second ancestry failed") {
			t.Fatalf("result = %v/%v, want propagated failure", ok, err)
		}
	})

	t.Run("proof accepts both ancestry relations", func(t *testing.T) {
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		ok, err := d.reviewIntegrationProof(ctx, "base", "approved", "current")
		if err != nil || !ok || len(runner.calls) != 2 {
			t.Fatalf("result/calls = %v/%v/%v, want true/nil/two", ok, err, runner.calls)
		}
	})
}

func TestReviewIntegrationRecoveryMutationApprovedSourceAndRetry(t *testing.T) {
	ctx := context.Background()
	checkpoint := &ReviewIntegrationCheckpoint{ReviewCheckpoint: ReviewCheckpoint{CheckpointInput: CheckpointInput{
		BeadID: "bead", Worktree: "/worktree", Branch: "agent/bead", TargetBranch: "main",
	}}, IntegrationTargetBeforeSHA: "base", IntegrationApprovedHeadSHA: "approved"}

	verify := func(t *testing.T, outputs ...any) error {
		t.Helper()
		call := 0
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			out := outputs[call]
			call++
			if err, ok := out.(error); ok {
				return nil, err
			}
			return []byte(out.(string)), nil
		}}
		d := &Dispatcher{repoRoot: "/repo"}
		d.setCommandRunner(runner)
		return d.verifyApprovedIntegrationSource(ctx, checkpoint, "approved")
	}

	t.Run("branch resolution failure", func(t *testing.T) {
		err := verify(t, errors.New("branch lookup failed"))
		if err == nil || !strings.Contains(err.Error(), "resolve approved branch agent/bead") ||
			!strings.Contains(err.Error(), "branch lookup failed") {
			t.Fatalf("error = %v, want branch resolution failure", err)
		}
	})

	t.Run("branch mismatch", func(t *testing.T) {
		err := verify(t, "other")
		if err == nil || !strings.Contains(err.Error(), "branch agent/bead is other, approved approved") {
			t.Fatalf("error = %v, want branch mismatch", err)
		}
	})

	t.Run("worktree resolution failure", func(t *testing.T) {
		err := verify(t, "approved", errors.New("worktree lookup failed"))
		if err == nil || !strings.Contains(err.Error(), "resolve approved worktree HEAD") ||
			!strings.Contains(err.Error(), "worktree lookup failed") {
			t.Fatalf("error = %v, want worktree resolution failure", err)
		}
	})

	t.Run("worktree mismatch", func(t *testing.T) {
		err := verify(t, "approved", "other")
		if err == nil || !strings.Contains(err.Error(), "worktree HEAD is other, approved approved") {
			t.Fatalf("error = %v, want worktree mismatch", err)
		}
	})

	t.Run("exact source accepted", func(t *testing.T) {
		if err := verify(t, "approved", "approved"); err != nil {
			t.Fatalf("verify exact source: %v", err)
		}
	})

	t.Run("retry requires merge coordinator", func(t *testing.T) {
		d := &Dispatcher{}
		if err := d.retryReviewIntegrationMerge(ctx, checkpoint); err == nil || !strings.Contains(err.Error(), "merge coordinator is unavailable") {
			t.Fatalf("error = %v, want coordinator failure", err)
		}
	})

	newRetryDispatcher := func(run func(context.Context, string, ...string) ([]byte, error)) *Dispatcher {
		d := &Dispatcher{repoRoot: "/repo", merger: merge.NewCoordinator(reviewIntegrationRecoveryMutationGitRunner{})}
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: run})
		return d
	}

	t.Run("retry propagates target resolution failure", func(t *testing.T) {
		d := newRetryDispatcher(func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("target lookup failed")
		})
		if err := d.retryReviewIntegrationMerge(ctx, checkpoint); err == nil || !strings.Contains(err.Error(), "target lookup failed") {
			t.Fatalf("error = %v, want target failure", err)
		}
	})

	t.Run("retry rejects target movement", func(t *testing.T) {
		d := newRetryDispatcher(func(context.Context, string, ...string) ([]byte, error) {
			return []byte("moved"), nil
		})
		if err := d.retryReviewIntegrationMerge(ctx, checkpoint); err == nil || !strings.Contains(err.Error(), "approved target moved from base to moved") {
			t.Fatalf("error = %v, want target movement", err)
		}
	})

	t.Run("retry rejects source movement", func(t *testing.T) {
		calls := 0
		d := newRetryDispatcher(func(context.Context, string, ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("base"), nil
			}
			return nil, errors.New("source lookup failed")
		})
		if err := d.retryReviewIntegrationMerge(ctx, checkpoint); err == nil || !strings.Contains(err.Error(), "approved source moved before merge") {
			t.Fatalf("error = %v, want source failure", err)
		}
	})
}

func TestReviewIntegrationRecoveryMutationPrepareAndReconcile(t *testing.T) {
	ctx := context.Background()
	exitOne := reviewIntegrationRecoveryMutationExitOne(t)

	newApproved := func(t *testing.T) (*Dispatcher, *ReviewCheckpointStore, *ReviewIntegrationCheckpoint) {
		t.Helper()
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(
			t, d.db, strings.ReplaceAll(t.Name(), "/", "-"), "bead", 1, ReviewCheckpointStateApproved, "")
		checkpoint.IntegrationTargetBeforeSHA = ""
		checkpoint.IntegrationApprovedHeadSHA = ""
		checkpoint.IntegrationObservedTargetSHA = ""
		return d, store, checkpoint
	}

	t.Run("prepare blocks proof error", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("ancestry failed")
		}})
		if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, "base", "approved"); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		var summary string
		if err := d.db.QueryRow(`SELECT summary FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&summary); err != nil {
			t.Fatalf("load blocker: %v", err)
		}
		if !strings.Contains(summary, "cannot prove approved head") {
			t.Fatalf("summary = %q, want proof blocker", summary)
		}
	})

	t.Run("prepare blocks moved target without proof", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 2 && args[2] == "merge-base" {
				return nil, exitOne
			}
			return []byte("approved"), nil
		}})
		if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, "moved", "approved"); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		var summary string
		if err := d.db.QueryRow(`SELECT summary FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&summary); err != nil {
			t.Fatalf("load blocker: %v", err)
		}
		if summary != "approved target moved before integration intent" {
			t.Fatalf("summary = %q, want exact target-moved blocker", summary)
		}
	})

	t.Run("prepare permits unchanged target when approved head is not yet merged", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		calls := 0
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return nil, exitOne
			}
			return []byte("approved"), nil
		}})
		if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, "base", "approved"); err != nil {
			t.Fatalf("prepare unchanged target: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateIntegrating || checkpoint.IntegrationStep != integrationStepIntent {
			t.Fatalf("checkpoint = %q/%q, want integrating intent", checkpoint.State, checkpoint.IntegrationStep)
		}
	})

	t.Run("prepare blocks moved approved source", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 2 && args[2] == "merge-base" {
				return nil, exitOne
			}
			return []byte("moved"), nil
		}})
		if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, "base", "approved"); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
	})

	for _, manual := range []bool{false, true} {
		t.Run("prepare persists intent manual="+string(rune('0'+boolToIntReviewIntegrationMutation(manual))), func(t *testing.T) {
			d, store, checkpoint := newApproved(t)
			d.cfg.ManualIntegration = manual
			d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
				return nil, nil
			}})
			if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, "current", "approved"); err != nil {
				t.Fatalf("prepare: %v", err)
			}
			wantState := ReviewCheckpointStateIntegrating
			if manual {
				wantState = ReviewCheckpointStateManualIntegrationPending
			}
			if checkpoint.State != wantState || checkpoint.IntegrationStep != integrationStepIntent ||
				checkpoint.IntegrationTargetBeforeSHA != "base" || checkpoint.IntegrationApprovedHeadSHA != "approved" {
				t.Fatalf("checkpoint = %#v, want durable %q intent", checkpoint, wantState)
			}
			var state, before, head, step string
			if err := d.db.QueryRow(`SELECT state, integration_target_before_sha, integration_approved_head_sha, integration_step FROM review_checkpoints WHERE id=?`, checkpoint.ID).
				Scan(&state, &before, &head, &step); err != nil {
				t.Fatalf("load intent: %v", err)
			}
			if state != string(wantState) || before != "base" || head != "approved" || step != integrationStepIntent {
				t.Fatalf("durable intent = %q/%q/%q/%q", state, before, head, step)
			}
		})
	}

	t.Run("prepare propagates begin conflict", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		checkpoint.ID = 99999
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		}})
		if err := d.prepareApprovedReviewIntegration(ctx, store, checkpoint, "current", "approved"); err == nil {
			t.Fatal("prepare error = nil, want begin conflict")
		}
	})

	t.Run("reconcile blocks target observation failure", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("target unavailable")
		}})
		if err := d.reconcileReviewIntegration(ctx, store, checkpoint); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		var summary string
		if err := d.db.QueryRow(`SELECT summary FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&summary); err != nil {
			t.Fatalf("load blocker: %v", err)
		}
		if !strings.Contains(summary, "cannot observe integration target main") || !strings.Contains(summary, "target unavailable") {
			t.Fatalf("summary = %q, want target observation blocker", summary)
		}
	})

	for _, missing := range []string{"target", "head"} {
		t.Run("reconcile blocks missing "+missing+" intent identity", func(t *testing.T) {
			d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
			checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(
				t, d.db, strings.ReplaceAll(t.Name(), "/", "-"), "bead", 1, ReviewCheckpointStateIntegrating, integrationStepIntent)
			if missing == "target" {
				checkpoint.IntegrationTargetBeforeSHA = ""
			} else {
				checkpoint.IntegrationApprovedHeadSHA = ""
				checkpoint.HeadSHA = ""
			}
			d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("current"), nil
			}})
			if err := d.reconcileReviewIntegration(ctx, store, checkpoint); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if checkpoint.State != ReviewCheckpointStateBlocked {
				t.Fatalf("state = %q, want blocked", checkpoint.State)
			}
		})
	}

	t.Run("reconcile propagates approved preparation failure", func(t *testing.T) {
		d, store, checkpoint := newApproved(t)
		checkpoint.ID = 99999
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("current"), nil
		}})
		if err := d.reconcileReviewIntegration(ctx, store, checkpoint); err == nil {
			t.Fatal("reconcile error = nil, want preparation conflict")
		}
	})
}

func boolToIntReviewIntegrationMutation(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestReviewIntegrationRecoveryMutationFinalize(t *testing.T) {
	ctx := context.Background()
	createBead := func(t *testing.T, store *reviewIntegrationRecoveryMutationBeadStore, id string) {
		t.Helper()
		if _, err := store.Create(ctx, beadstore.CreateParams{ID: id, Title: id, Type: "task", Status: "in_progress"}); err != nil {
			t.Fatalf("create bead: %v", err)
		}
	}

	t.Run("rejects intent without observed proof", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-intent", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepIntent)
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "without observed merge proof") {
			t.Fatalf("error = %v, want proof error", err)
		}
	})

	t.Run("finalizes all durable side effects using origin assignment fallback", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		createBead(t, beads, "bead")
		assignmentID := insertReviewIntegrationRecoveryMutationAssignment(t, d.db, "bead", "requeued")
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-success", "bead", assignmentID, ReviewCheckpointStateIntegrating, integrationStepMergeObserved)
		checkpoint.CurrentAssignmentID = 0
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		var assignmentStatus, checkpointState, step string
		if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
			t.Fatalf("load assignment: %v", err)
		}
		if err := d.db.QueryRow(`SELECT state, integration_step FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&checkpointState, &step); err != nil {
			t.Fatalf("load checkpoint: %v", err)
		}
		bead, err := beads.Show(ctx, "bead")
		if err != nil {
			t.Fatalf("load bead: %v", err)
		}
		var events int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='review_integration_reconciled' AND bead_id='bead'`).Scan(&events); err != nil {
			t.Fatalf("count reconcile events: %v", err)
		}
		if assignmentStatus != "completed" || checkpointState != "integrated" || step != integrationStepIntegrated ||
			bead == nil || bead.Status != "closed" || bead.CloseReason != "Merged: current" || events != 1 ||
			checkpoint.State != ReviewCheckpointStateIntegrated || checkpoint.IntegrationStep != integrationStepIntegrated {
			t.Fatalf("final state = assignment %q checkpoint %q/%q bead %#v events %d memory %q/%q",
				assignmentStatus, checkpointState, step, bead, events, checkpoint.State, checkpoint.IntegrationStep)
		}
	})

	t.Run("propagates assignment completion failure", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-assignment-error", "bead", 99999, ReviewCheckpointStateIntegrating, integrationStepMergeObserved)
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want assignment error", err)
		}
		if checkpoint.IntegrationStep != integrationStepMergeObserved {
			t.Fatalf("step = %q, want unchanged merge_observed", checkpoint.IntegrationStep)
		}
	})

	t.Run("does not close bead when assignment step cannot advance", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		createBead(t, beads, "bead")
		assignmentID := insertReviewIntegrationRecoveryMutationAssignment(t, d.db, "bead", "active")
		checkpoint := &ReviewIntegrationCheckpoint{ReviewCheckpoint: ReviewCheckpoint{ID: 99999, CheckpointInput: CheckpointInput{
			BeadID: "bead", OriginAssignmentID: assignmentID, State: ReviewCheckpointStateIntegrating,
		}}, IntegrationObservedTargetSHA: "current", IntegrationStep: integrationStepMergeObserved}
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "advance review integration") {
			t.Fatalf("error = %v, want advance failure", err)
		}
		bead, err := beads.Show(ctx, "bead")
		if err != nil || bead == nil || bead.Status == "closed" {
			t.Fatalf("bead/error = %#v/%v, want still open", bead, err)
		}
	})

	t.Run("propagates close failure before advancing", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-close-error", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepAssignmentCompleted)
		beads.showFn = func(context.Context, string) (*protocol.Bead, error) {
			return &protocol.Bead{ID: "bead", Type: "task", Status: "in_progress"}, nil
		}
		beads.closeFn = func(context.Context, string, string) error { return errors.New("close failed") }
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "close integrated bead bead") {
			t.Fatalf("error = %v, want close failure", err)
		}
		if checkpoint.IntegrationStep != integrationStepAssignmentCompleted {
			t.Fatalf("step = %q, want assignment_completed", checkpoint.IntegrationStep)
		}
	})

	t.Run("records assignment step in memory before close attempt", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		assignmentID := insertReviewIntegrationRecoveryMutationAssignment(t, d.db, "bead", "active")
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-intermediate-assignment", "bead", assignmentID, ReviewCheckpointStateIntegrating, integrationStepMergeObserved)
		beads.showFn = func(context.Context, string) (*protocol.Bead, error) {
			return &protocol.Bead{ID: "bead", Type: "task", Status: "in_progress"}, nil
		}
		beads.closeFn = func(context.Context, string, string) error { return errors.New("close failed") }
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "close failed") {
			t.Fatalf("error = %v, want close failure", err)
		}
		if checkpoint.IntegrationStep != integrationStepAssignmentCompleted {
			t.Fatalf("step = %q, want assignment_completed", checkpoint.IntegrationStep)
		}
	})

	t.Run("propagates bead-step persistence failure", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		createBead(t, beads, "bead")
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-step-error", "bead", 1, ReviewCheckpointStateBlocked, integrationStepAssignmentCompleted)
		checkpoint.State = ReviewCheckpointStateIntegrating
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "advance review integration") {
			t.Fatalf("error = %v, want step persistence failure", err)
		}
	})

	t.Run("propagates terminal persistence failure", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := &ReviewIntegrationCheckpoint{ReviewCheckpoint: ReviewCheckpoint{ID: 99999, CheckpointInput: CheckpointInput{
			BeadID: "bead", State: ReviewCheckpointStateIntegrating,
		}}, IntegrationStep: integrationStepBeadClosed}
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "complete review integration") {
			t.Fatalf("error = %v, want terminal persistence failure", err)
		}
	})

	t.Run("records bead-close step in memory before terminal persistence", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		if _, err := beads.Create(ctx, beadstore.CreateParams{ID: "bead", Title: "bead", Type: "task", Status: "in_progress"}); err != nil {
			t.Fatalf("create bead: %v", err)
		}
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-intermediate-bead", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepAssignmentCompleted)
		if _, err := d.db.Exec(`
CREATE TRIGGER fail_review_integration_completion
BEFORE UPDATE OF state ON review_checkpoints
WHEN NEW.state='integrated'
BEGIN SELECT RAISE(ABORT, 'terminal persistence failed'); END`); err != nil {
			t.Fatalf("create completion trigger: %v", err)
		}
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err == nil || !strings.Contains(err.Error(), "terminal persistence failed") {
			t.Fatalf("error = %v, want terminal persistence failure", err)
		}
		if checkpoint.IntegrationStep != integrationStepBeadClosed {
			t.Fatalf("step = %q, want bead_closed", checkpoint.IntegrationStep)
		}
	})

	t.Run("integrated replay is a no-op", func(t *testing.T) {
		checkpoint := &ReviewIntegrationCheckpoint{IntegrationStep: integrationStepIntegrated}
		if err := (&Dispatcher{}).finalizeReviewIntegration(ctx, nil, checkpoint); err != nil {
			t.Fatalf("integrated replay: %v", err)
		}
	})

	t.Run("unknown step becomes one durable block", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "finalize-unknown", "bead", 1, ReviewCheckpointStateIntegrating, "mystery")
		if err := d.finalizeReviewIntegration(ctx, store, checkpoint); err != nil {
			t.Fatalf("finalize unknown step: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked || checkpoint.IntegrationStep != "blocked" {
			t.Fatalf("checkpoint = %q/%q, want blocked", checkpoint.State, checkpoint.IntegrationStep)
		}
	})
}

func TestReviewIntegrationRecoveryMutationManualAndAutomatic(t *testing.T) {
	ctx := context.Background()
	exitOne := reviewIntegrationRecoveryMutationExitOne(t)
	allAncestors := func() *reviewIntegrationRecoveryMutationRunner {
		return &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		}}
	}
	checkpointSummary := func(t *testing.T, db *sql.DB, id int64) string {
		t.Helper()
		var summary string
		if err := db.QueryRow(`SELECT summary FROM review_checkpoints WHERE id=?`, id).Scan(&summary); err != nil {
			t.Fatalf("load checkpoint summary: %v", err)
		}
		return summary
	}

	t.Run("manual does nothing while target is unchanged", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "manual-unchanged", "bead", 1, ReviewCheckpointStateManualIntegrationPending, integrationStepIntent)
		checkpoint.IntegrationObservedTargetSHA = ""
		runner := &reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("must not inspect ancestry")
		}}
		d.setCommandRunner(runner)
		if err := d.reconcileManualReviewIntegration(ctx, store, checkpoint, "base", "approved"); err != nil {
			t.Fatalf("manual unchanged reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateManualIntegrationPending || checkpoint.IntegrationStep != integrationStepIntent || len(runner.calls) != 0 {
			t.Fatalf("checkpoint/calls = %q/%q/%v, want unchanged/no calls", checkpoint.State, checkpoint.IntegrationStep, runner.calls)
		}
	})

	t.Run("manual blocks ancestry error", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "manual-proof-error", "bead", 1, ReviewCheckpointStateManualIntegrationPending, integrationStepIntent)
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("proof failed")
		}})
		if err := d.reconcileManualReviewIntegration(ctx, store, checkpoint, "current", "approved"); err != nil {
			t.Fatalf("manual reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		if summary := checkpointSummary(t, d.db, checkpoint.ID); !strings.Contains(summary, "cannot observe manual integration proof") ||
			!strings.Contains(summary, "proof failed") {
			t.Fatalf("summary = %q, want proof-observation blocker", summary)
		}
	})

	t.Run("manual blocks target movement without proof", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "manual-no-proof", "bead", 1, ReviewCheckpointStateManualIntegrationPending, integrationStepIntent)
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, exitOne
		}})
		if err := d.reconcileManualReviewIntegration(ctx, store, checkpoint, "current", "approved"); err != nil {
			t.Fatalf("manual reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		if summary := checkpointSummary(t, d.db, checkpoint.ID); summary != "target moved without integration proof" {
			t.Fatalf("summary = %q, want target-without-proof blocker", summary)
		}
	})

	t.Run("manual propagates durable promotion conflict", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := &ReviewIntegrationCheckpoint{ReviewCheckpoint: ReviewCheckpoint{ID: 99999, CheckpointInput: CheckpointInput{
			BeadID: "bead", State: ReviewCheckpointStateManualIntegrationPending,
		}}, IntegrationTargetBeforeSHA: "base", IntegrationStep: integrationStepIntent}
		d.setCommandRunner(allAncestors())
		if err := d.reconcileManualReviewIntegration(ctx, store, checkpoint, "current", "approved"); err == nil ||
			!strings.Contains(err.Error(), "promote manual review integration") {
			t.Fatalf("manual reconcile error = %v, want promotion conflict", err)
		}
		if checkpoint.State != ReviewCheckpointStateManualIntegrationPending || checkpoint.IntegrationObservedTargetSHA != "" {
			t.Fatalf("checkpoint = %q/%q, want unchanged after promotion conflict", checkpoint.State, checkpoint.IntegrationObservedTargetSHA)
		}
	})

	t.Run("manual records proof in memory before resumable finalization", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "manual-promoted", "bead", 99999, ReviewCheckpointStateManualIntegrationPending, integrationStepIntent)
		checkpoint.IntegrationObservedTargetSHA = ""
		d.setCommandRunner(allAncestors())
		if err := d.reconcileManualReviewIntegration(ctx, store, checkpoint, "current", "approved"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("manual reconcile error = %v, want later assignment failure", err)
		}
		if checkpoint.State != ReviewCheckpointStateIntegrating || checkpoint.IntegrationObservedTargetSHA != "current" ||
			checkpoint.IntegrationStep != integrationStepMergeObserved {
			t.Fatalf("checkpoint = %q/%q/%q, want integrating/current/merge_observed",
				checkpoint.State, checkpoint.IntegrationObservedTargetSHA, checkpoint.IntegrationStep)
		}
	})

	t.Run("automatic blocks movement after recorded proof", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "automatic-moved-after-proof", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepMergeObserved)
		checkpoint.IntegrationObservedTargetSHA = "observed"
		if err := d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, "different", "approved"); err != nil {
			t.Fatalf("automatic reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		if summary := checkpointSummary(t, d.db, checkpoint.ID); summary != "target moved after recorded integration proof" {
			t.Fatalf("summary = %q, want moved-after-proof blocker", summary)
		}
	})

	t.Run("automatic blocks failed retry", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "automatic-retry-error", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepIntent)
		checkpoint.IntegrationObservedTargetSHA = ""
		d.merger = nil
		if err := d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, "base", "approved"); err != nil {
			t.Fatalf("automatic reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		if summary := checkpointSummary(t, d.db, checkpoint.ID); !strings.Contains(summary, "integration retry failed: merge coordinator is unavailable") {
			t.Fatalf("summary = %q, want retry blocker", summary)
		}
	})

	t.Run("automatic blocks target movement without proof", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "automatic-no-proof", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepIntent)
		checkpoint.IntegrationObservedTargetSHA = ""
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, exitOne
		}})
		if err := d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, "current", "approved"); err != nil {
			t.Fatalf("automatic reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		if summary := checkpointSummary(t, d.db, checkpoint.ID); summary != "target moved without integration proof" {
			t.Fatalf("summary = %q, want target-without-proof blocker", summary)
		}
	})

	t.Run("automatic blocks proof observation error", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "automatic-proof-error", "bead", 1, ReviewCheckpointStateIntegrating, integrationStepIntent)
		checkpoint.IntegrationObservedTargetSHA = ""
		d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("proof unavailable")
		}})
		if err := d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, "current", "approved"); err != nil {
			t.Fatalf("automatic reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateBlocked {
			t.Fatalf("state = %q, want blocked", checkpoint.State)
		}
		if summary := checkpointSummary(t, d.db, checkpoint.ID); !strings.Contains(summary, "cannot observe integration proof") ||
			!strings.Contains(summary, "proof unavailable") {
			t.Fatalf("summary = %q, want proof-observation blocker", summary)
		}
	})

	t.Run("automatic propagates durable proof observation conflict", func(t *testing.T) {
		d, _, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		checkpoint := &ReviewIntegrationCheckpoint{ReviewCheckpoint: ReviewCheckpoint{ID: 99999, CheckpointInput: CheckpointInput{
			BeadID: "bead", State: ReviewCheckpointStateIntegrating,
		}}, IntegrationTargetBeforeSHA: "base", IntegrationApprovedHeadSHA: "approved", IntegrationStep: integrationStepIntent}
		d.setCommandRunner(allAncestors())
		if err := d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, "current", "approved"); err == nil {
			t.Fatal("automatic reconcile error = nil, want observation conflict")
		}
		if checkpoint.IntegrationObservedTargetSHA != "" || checkpoint.IntegrationStep != integrationStepIntent {
			t.Fatalf("checkpoint = observed %q step %q, want unchanged", checkpoint.IntegrationObservedTargetSHA, checkpoint.IntegrationStep)
		}
	})

	t.Run("automatic persists proof and completes all side effects", func(t *testing.T) {
		d, beads, store := newReviewIntegrationRecoveryMutationDispatcher(t)
		if _, err := beads.Create(ctx, beadstore.CreateParams{ID: "bead", Title: "bead", Type: "task", Status: "in_progress"}); err != nil {
			t.Fatalf("create bead: %v", err)
		}
		assignmentID := insertReviewIntegrationRecoveryMutationAssignment(t, d.db, "bead", "active")
		checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(t, d.db, "automatic-success", "bead", assignmentID, ReviewCheckpointStateIntegrating, integrationStepIntent)
		checkpoint.IntegrationObservedTargetSHA = ""
		d.setCommandRunner(allAncestors())
		if err := d.reconcileAutomaticReviewIntegration(ctx, store, checkpoint, "current", "approved"); err != nil {
			t.Fatalf("automatic reconcile: %v", err)
		}
		if checkpoint.State != ReviewCheckpointStateIntegrated || checkpoint.IntegrationObservedTargetSHA != "current" ||
			checkpoint.IntegrationStep != integrationStepIntegrated {
			t.Fatalf("checkpoint = %q/%q/%q, want integrated/current/integrated",
				checkpoint.State, checkpoint.IntegrationObservedTargetSHA, checkpoint.IntegrationStep)
		}
	})
}

func TestReviewIntegrationRecoveryMutationStartupListFailure(t *testing.T) {
	d, _, _ := newReviewIntegrationRecoveryMutationDispatcher(t)
	if err := d.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	err := d.reconcileReviewIntegrationsOnStartup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("startup reconcile error = %v, want list failure", err)
	}
}

func TestReviewIntegrationRecoveryMutationStartupWrapsCheckpointFailure(t *testing.T) {
	ctx := context.Background()
	d, _, _ := newReviewIntegrationRecoveryMutationDispatcher(t)
	checkpoint := insertReviewIntegrationRecoveryMutationCheckpoint(
		t, d.db, "startup-checkpoint-error", "bead", 99999, ReviewCheckpointStateIntegrating, integrationStepMergeObserved)
	d.setCommandRunner(&reviewIntegrationRecoveryMutationRunner{run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("current"), nil
	}})
	err := d.reconcileReviewIntegrationsOnStartup(ctx)
	if err == nil || !strings.Contains(err.Error(), "reconcile review integration checkpoint") ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("startup reconcile error = %v, want checkpoint-scoped failure for %d", err, checkpoint.ID)
	}
}
