package dispatcher //nolint:testpackage // white-box mutation tests exercise private checkpoint identity and transition helpers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func reviewCheckpointAuthoritativeInput() CheckpointInput {
	return CheckpointInput{
		CheckpointKey:      "checkpoint-71",
		BeadID:             "oro-71",
		OriginAssignmentID: 17,
		Worktree:           "/tmp/oro-71",
		Branch:             "worker/oro-71",
		TargetBranch:       "main",
		HeadSHA:            "head-71",
		TargetSHA:          "target-71",
		AcceptanceHash:     "acceptance-71",
		QGScriptHash:       "qg-script-71",
		QGMode:             "full",
		ReviewPolicyHash:   "review-policy-71",
		TriageRevision:     "triage-71",
		ReadyAttempt:       "ready-71",
		OpsRunID:           117,
		State:              ReviewCheckpointStateApproved,
	}
}

func TestReviewCheckpointAuthoritativeSurvivorMutationIdentityValidation(t *testing.T) {
	valid := ReviewCheckpoint{
		ID:              71,
		CheckpointInput: reviewCheckpointAuthoritativeInput(),
	}

	if err := validateOpsRunCheckpointIdentity(valid, 117, "oro-71"); err != nil {
		t.Fatalf("validate exact identity: %v", err)
	}

	tests := []struct {
		name       string
		mutate     func(*ReviewCheckpoint)
		wantDetail string
	}{
		{
			name: "ops run mismatch",
			mutate: func(checkpoint *ReviewCheckpoint) {
				checkpoint.OpsRunID++
			},
			wantDetail: "identity is ops",
		},
		{
			name: "integrated is terminal",
			mutate: func(checkpoint *ReviewCheckpoint) {
				checkpoint.State = ReviewCheckpointStateIntegrated
			},
			wantDetail: "linked terminal checkpoint",
		},
		{
			name: "superseded is terminal",
			mutate: func(checkpoint *ReviewCheckpoint) {
				checkpoint.State = ReviewCheckpointStateSuperseded
			},
			wantDetail: "linked terminal checkpoint",
		},
		{
			name: "immutable identity is required",
			mutate: func(checkpoint *ReviewCheckpoint) {
				checkpoint.QGScriptHash = ""
			},
			wantDetail: "invalid immutable identity",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkpoint := valid
			tt.mutate(&checkpoint)
			err := validateOpsRunCheckpointIdentity(checkpoint, 117, "oro-71")
			if !errors.Is(err, ErrCheckpointOwnershipCorrupt) {
				t.Fatalf("error = %v, want ErrCheckpointOwnershipCorrupt", err)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error = %q, want detail %q", err, tt.wantDetail)
			}
		})
	}
}

func TestReviewCheckpointAuthoritativeSurvivorMutationTransitionFailureContracts(t *testing.T) {
	ctx := context.Background()
	transitions := []struct {
		name string
		call func(*ReviewCheckpointStore) error
	}{
		{
			name: "advance",
			call: func(store *ReviewCheckpointStore) error {
				return store.AdvanceIntegrationStep(ctx, 71, "bead_closed")
			},
		},
		{
			name: "observe",
			call: func(store *ReviewCheckpointStore) error {
				return store.ObserveIntegration(ctx, 71, "observed-sha")
			},
		},
		{
			name: "promote manual",
			call: func(store *ReviewCheckpointStore) error {
				return store.PromoteManualIntegration(ctx, 71, "observed-sha")
			},
		},
		{
			name: "complete",
			call: func(store *ReviewCheckpointStore) error {
				return store.CompleteIntegration(ctx, 71)
			},
		},
	}

	closedDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := closedDB.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	for _, transition := range transitions {
		t.Run(transition.name, func(t *testing.T) {
			for _, store := range []*ReviewCheckpointStore{nil, {}} {
				if err := transition.call(store); err == nil {
					t.Fatal("nil store or database error = nil")
				}
			}

			err := transition.call(&ReviewCheckpointStore{db: closedDB})
			if err == nil {
				t.Fatal("closed database error = nil")
			}
			if errors.Is(err, ErrCheckpointConflict) {
				t.Fatalf("closed database error = %v, want execution error before row-count conflict", err)
			}
		})
	}
}

type reviewCheckpointAuthoritativeExecutor struct {
	execErr error
	queryDB *sql.DB
}

func (executor reviewCheckpointAuthoritativeExecutor) ExecContext(
	context.Context,
	string,
	...any,
) (sql.Result, error) {
	return reviewCheckpointAuthoritativeResult{rows: 1}, executor.execErr
}

func (executor reviewCheckpointAuthoritativeExecutor) QueryRowContext(
	ctx context.Context,
	_ string,
	_ ...any,
) *sql.Row {
	if executor.queryDB == nil {
		panic("unexpected QueryRowContext")
	}
	return executor.queryDB.QueryRowContext(ctx, "SELECT 1 WHERE 0")
}

func TestReviewCheckpointAuthoritativeSurvivorMutationCreateHelpersPropagateFailures(t *testing.T) {
	ctx := context.Background()
	valid := reviewCheckpointAuthoritativeInput()
	for _, store := range []*ReviewCheckpointStore{nil, {}} {
		if _, err := store.CreateOrReuse(ctx, valid); err == nil {
			t.Fatal("CreateOrReuse nil store or database error = nil")
		}
	}

	invalid := reviewCheckpointAuthoritativeInput()
	invalid.CheckpointKey = ""
	panicExecutor := reviewCheckpointAuthoritativeExecutor{}

	if _, err := createOrReuseReviewCheckpoint(ctx, panicExecutor, invalid); err == nil {
		t.Fatal("create helper invalid input error = nil")
	}
	if _, retry, err := createOrReuseReviewCheckpointAttempt(ctx, panicExecutor, invalid); err == nil || retry {
		t.Fatalf("create attempt invalid input = retry %t, error %v", retry, err)
	}

	execErr := errors.New("insert unavailable")
	if _, retry, err := createOrReuseReviewCheckpointAttempt(ctx,
		reviewCheckpointAuthoritativeExecutor{execErr: execErr},
		reviewCheckpointAuthoritativeInput(),
	); !errors.Is(err, execErr) || retry {
		t.Fatalf("create attempt execution failure = retry %t, error %v, want wrapped %v", retry, err, execErr)
	}

	closedDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open closed-query database: %v", err)
	}
	if err := closedDB.Close(); err != nil {
		t.Fatalf("close query database: %v", err)
	}
	_, retry, err := createOrReuseReviewCheckpointAttempt(ctx,
		reviewCheckpointAuthoritativeExecutor{queryDB: closedDB},
		reviewCheckpointAuthoritativeInput(),
	)
	if err == nil || retry || !strings.Contains(err.Error(), "load active review checkpoint") {
		t.Fatalf("create attempt load failure = retry %t, error %v", retry, err)
	}

	emptyDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open empty-query database: %v", err)
	}
	t.Cleanup(func() { _ = emptyDB.Close() })
	if _, err := NewReviewCheckpointStore(emptyDB).CreateOrReuse(ctx, invalid); err == nil {
		t.Fatal("CreateOrReuse invalid input error = nil")
	}
	_, err = createOrReuseReviewCheckpoint(ctx,
		reviewCheckpointAuthoritativeExecutor{queryDB: emptyDB},
		reviewCheckpointAuthoritativeInput(),
	)
	if err == nil || !strings.Contains(err.Error(), "assignment side-effect admission is active") {
		t.Fatalf("create helper retry error = %v", err)
	}
}

type reviewCheckpointAuthoritativeResult struct {
	rows int64
	err  error
}

func (reviewCheckpointAuthoritativeResult) LastInsertId() (int64, error) { return 0, nil }

func (result reviewCheckpointAuthoritativeResult) RowsAffected() (int64, error) {
	return result.rows, result.err
}

func TestReviewCheckpointAuthoritativeSurvivorMutationRequiresExactlyOneRow(t *testing.T) {
	if err := requireOneCheckpointRow(reviewCheckpointAuthoritativeResult{rows: 1}, 19, "advance"); err != nil {
		t.Fatalf("one affected row: %v", err)
	}

	for _, rows := range []int64{0, 2} {
		err := requireOneCheckpointRow(reviewCheckpointAuthoritativeResult{rows: rows}, 19, "advance")
		if !errors.Is(err, ErrCheckpointConflict) {
			t.Fatalf("%d affected rows: error = %v, want ErrCheckpointConflict", rows, err)
		}
	}

	rowsErr := errors.New("rows affected unavailable")
	err := requireOneCheckpointRow(reviewCheckpointAuthoritativeResult{err: rowsErr}, 19, "advance")
	if !errors.Is(err, rowsErr) {
		t.Fatalf("RowsAffected error = %v, want wrapped %v", err, rowsErr)
	}
	if !strings.Contains(err.Error(), "count advance 19") {
		t.Fatalf("RowsAffected error = %q, want operation and checkpoint ID", err)
	}
}
