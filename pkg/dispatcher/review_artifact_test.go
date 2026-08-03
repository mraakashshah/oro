//nolint:testpackage // The acceptance proof exercises the dispatcher persistence boundary.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/reviewcontract"
)

func TestReviewArtifactAndFindingOverflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "checkpoints.sqlite")
	artifactDir := filepath.Join(root, "review-recovery")
	store := openReviewCheckpointStore(ctx, t, dbPath)
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-overflow"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}

	t.Run("inline stays inline and bounded", func(t *testing.T) {
		findings := []reviewcontract.Finding{reviewOverflowFinding("inline", "small")}
		recovery, err := prepareReviewRecovery(artifactDir, checkpoint.ID, "rejected-head", "acceptance", 1, findings)
		if err != nil {
			t.Fatalf("prepare inline recovery: %v", err)
		}
		if recovery.FindingsRef != nil {
			t.Fatalf("inline recovery reference = %#v, want nil", recovery.FindingsRef)
		}
		if !reflect.DeepEqual(recovery.Findings, findings) {
			t.Fatalf("inline findings changed: got %#v want %#v", recovery.Findings, findings)
		}
		assertReviewRecoveryPayloadBounded(t, recovery)
	})

	findings := []reviewcontract.Finding{
		reviewOverflowFinding("critical-overflow", strings.Repeat("lossless-detail-", 20_000)),
		reviewOverflowFinding("important-overflow", strings.Repeat("exact-required-action-", 8_000)),
	}
	recovery, err := prepareReviewRecovery(artifactDir, checkpoint.ID, "rejected-head", "acceptance", 2, findings)
	if err != nil {
		t.Fatalf("prepare overflow recovery: %v", err)
	}
	if recovery.FindingsRef == nil {
		t.Fatal("overflow recovery reference is nil")
	}
	if recovery.Findings != nil {
		t.Fatalf("overflow inline findings = %#v, want nil", recovery.Findings)
	}
	assertReviewRecoveryPayloadBounded(t, recovery)

	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findings, recovery.FindingsRef); err != nil {
		t.Fatalf("commit rejected findings and reference: %v", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close store before restart: %v", err)
	}

	restarted := openReviewCheckpointStore(ctx, t, dbPath)
	loadedRecovery, err := restarted.LoadReviewRecovery(ctx, checkpoint.ID)
	if err != nil {
		t.Fatalf("reload review recovery: %v", err)
	}
	if loadedRecovery.Findings != nil {
		t.Fatalf("restart regenerated partial inline findings: %#v", loadedRecovery.Findings)
	}
	if !reflect.DeepEqual(loadedRecovery.FindingsRef, recovery.FindingsRef) {
		t.Fatalf("restart reference = %#v, want exact %#v", loadedRecovery.FindingsRef, recovery.FindingsRef)
	}
	reloadedFindings, err := LoadRecoveryArtifact(*loadedRecovery.FindingsRef)
	if err != nil {
		t.Fatalf("load persisted recovery artifact: %v", err)
	}
	if !reflect.DeepEqual(reloadedFindings, findings) {
		t.Fatalf("reloaded findings changed: got %#v want %#v", reloadedFindings, findings)
	}
	artifactInfo, err := os.Stat(recovery.FindingsRef.Path)
	if err != nil {
		t.Fatalf("stat recovery artifact: %v", err)
	}
	if artifactInfo.Mode().Perm() != recoveryArtifactFileMode {
		t.Fatalf("artifact mode = %o, want %o", artifactInfo.Mode().Perm(), recoveryArtifactFileMode)
	}

	t.Run("canonical payload builder preserves the exact bounded reference", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		beadSource.shown["oro-overflow"] = &protocol.BeadDetail{Title: "overflow correction"}
		d.shutdownRunner = &mockCommandRunner{}
		payload := d.buildAssignPayload(ctx, &trackedWorker{
			beadID:   "oro-overflow",
			worktree: "/tmp/oro-overflow",
		}, 2, "bounded compatibility summary", "", WorkerExecutionContext{
			AssignmentID:   19,
			Generation:     1,
			ActorRole:      "execution_worker",
			Project:        "oro",
			ReviewRecovery: &loadedRecovery,
		})
		if payload.ReviewRecovery == nil || !reflect.DeepEqual(payload.ReviewRecovery.FindingsRef, recovery.FindingsRef) {
			t.Fatalf("built recovery = %#v, want exact reference %#v", payload.ReviewRecovery, recovery.FindingsRef)
		}
		assertReviewRecoveryPayloadBounded(t, *payload.ReviewRecovery)
	})

	t.Run("rejected retention and mismatched replacement are fail closed", func(t *testing.T) {
		prunable, err := restarted.ListPrunableArtifacts(ctx, time.Now().Add(24*time.Hour))
		if err != nil {
			t.Fatalf("list prunable artifacts: %v", err)
		}
		if len(prunable) != 0 {
			t.Fatalf("rejected recovery artifact is prunable: %#v", prunable)
		}

		mismatched := append([]reviewcontract.Finding(nil), findings...)
		mismatched[0].Detail += " changed"
		if err := restarted.SaveRejectedFindings(ctx, checkpoint.ID, mismatched, recovery.FindingsRef); err == nil {
			t.Fatal("mismatched findings committed against existing artifact reference")
		}
		stillLoaded, err := restarted.LoadReviewRecovery(ctx, checkpoint.ID)
		if err != nil {
			t.Fatalf("reload after rejected replacement: %v", err)
		}
		if !reflect.DeepEqual(stillLoaded.FindingsRef, recovery.FindingsRef) {
			t.Fatalf("reference after rejected replacement = %#v, want %#v", stillLoaded.FindingsRef, recovery.FindingsRef)
		}
	})

	t.Run("idempotent concurrent persistence keeps one exact artifact", func(t *testing.T) {
		const writers = 8
		refs := make([]ReviewRecoveryArtifactRef, writers)
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				refs[index], errs[index] = PersistRecoveryArtifact(artifactDir, checkpoint.ID, findings)
			}(i)
		}
		wg.Wait()
		for i := range writers {
			if errs[i] != nil {
				t.Fatalf("writer %d: %v", i, errs[i])
			}
			if !reflect.DeepEqual(refs[i], *recovery.FindingsRef) {
				t.Fatalf("writer %d ref = %#v, want %#v", i, refs[i], *recovery.FindingsRef)
			}
		}
		entries, err := os.ReadDir(artifactDir)
		if err != nil {
			t.Fatalf("read artifact directory: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("artifact files = %d, want one durable identity", len(entries))
		}
	})

	t.Run("missing or corrupt artifact fails typed without partial findings", func(t *testing.T) {
		missing := *recovery.FindingsRef
		missing.Path = filepath.Join(artifactDir, "missing.json")
		got, err := LoadRecoveryArtifact(missing)
		assertTypedRecoveryFailure(t, got, err, RecoveryArtifactMissing)

		movedPath := recovery.FindingsRef.Path + ".moved"
		if err := os.Rename(recovery.FindingsRef.Path, movedPath); err != nil {
			t.Fatalf("move recovery artifact: %v", err)
		}
		loaded, err := restarted.LoadReviewRecovery(ctx, checkpoint.ID)
		assertTypedStoredRecoveryFailure(t, loaded, err, RecoveryArtifactMissing)
		if err := os.Rename(movedPath, recovery.FindingsRef.Path); err != nil {
			t.Fatalf("restore recovery artifact: %v", err)
		}

		if err := os.WriteFile(recovery.FindingsRef.Path, []byte("corrupt"), 0o600); err != nil {
			t.Fatalf("corrupt recovery artifact: %v", err)
		}
		got, err = LoadRecoveryArtifact(*recovery.FindingsRef)
		assertTypedRecoveryFailure(t, got, err, RecoveryArtifactCorrupt)
		loaded, err = restarted.LoadReviewRecovery(ctx, checkpoint.ID)
		assertTypedStoredRecoveryFailure(t, loaded, err, RecoveryArtifactCorrupt)
	})
}

func TestReviewRecoveryArtifactRetentionRemovesUnreferencedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	artifactDir := filepath.Join(root, "review-recovery")
	store := openReviewCheckpointStore(ctx, t, filepath.Join(root, "checkpoints.sqlite"))
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-artifact-retention"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}

	findingsA := []reviewcontract.Finding{reviewOverflowFinding("A", "committed first")}
	refA, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsA)
	if err != nil {
		t.Fatalf("persist A: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findingsA, &refA); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	if err := store.CompareAndSwap(ctx, checkpoint.ID, ReviewCheckpointStateRejected, ReviewCheckpointStateReviewRunning); err != nil {
		t.Fatalf("start replacement review: %v", err)
	}

	findingsB := []reviewcontract.Finding{reviewOverflowFinding("B", "durable replacement")}
	refB, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsB)
	if err != nil {
		t.Fatalf("persist B: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findingsB, &refB); err != nil {
		t.Fatalf("commit B: %v", err)
	}

	findingsC := []reviewcontract.Finding{reviewOverflowFinding("C", "database write loses CAS")}
	refC, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsC)
	if err != nil {
		t.Fatalf("persist C: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findingsC, &refC); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("commit C error = %v, want ErrCheckpointConflict", err)
	}

	// A fresh unreferenced contender may still be between file persistence and
	// its DB commit. The retention grace window must protect it from this sweep.
	findingsFresh := []reviewcontract.Finding{reviewOverflowFinding("fresh", "concurrent contender")}
	refFresh, err := PersistRecoveryArtifact(artifactDir, checkpoint.ID, findingsFresh)
	if err != nil {
		t.Fatalf("persist fresh contender: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{refA.Path, refB.Path, refC.Path} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age artifact %s: %v", path, err)
		}
	}

	d := &Dispatcher{
		db:                        store.db,
		nowFunc:                   time.Now,
		reviewArtifactRetention:   time.Hour,
		reviewRecoveryArtifactDir: artifactDir,
	}
	d.pruneReviewArtifacts(ctx)

	for name, path := range map[string]string{"A": refA.Path, "C": refC.Path} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unreferenced artifact %s stat error = %v, want not exist", name, err)
		}
	}
	for name, path := range map[string]string{"B": refB.Path, "fresh": refFresh.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained artifact %s stat: %v", name, err)
		}
	}
}

func reviewOverflowFinding(id, detail string) reviewcontract.Finding {
	return reviewcontract.Finding{
		ID:             id,
		Severity:       reviewcontract.SevImportant,
		Category:       "correctness",
		Title:          "preserve exact review finding",
		Detail:         detail,
		Evidence:       []reviewcontract.Evidence{{File: "pkg/dispatcher/review.go", LineStart: 1, LineEnd: 1}},
		Confidence:     99,
		Sources:        []string{"correctness"},
		Origin:         "review",
		ContractImpact: reviewcontract.ContractImplementationFix,
		RequiredAction: detail,
	}
}

func assertReviewRecoveryPayloadBounded(t *testing.T, recovery protocol.ReviewRecovery) {
	t.Helper()
	recoveryJSON, err := json.Marshal(recovery)
	if err != nil {
		t.Fatalf("marshal review recovery: %v", err)
	}
	if len(recoveryJSON) > maxReviewRecoveryInlineBytes {
		t.Fatalf("review recovery bytes = %d, want <= %d", len(recoveryJSON), maxReviewRecoveryInlineBytes)
	}
	messageJSON, err := json.Marshal(protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:         "oro-overflow",
			Worktree:       "/tmp/oro-overflow",
			ReviewRecovery: &recovery,
		},
	})
	if err != nil {
		t.Fatalf("marshal ASSIGN: %v", err)
	}
	if len(messageJSON) >= protocol.MaxMessageSize {
		t.Fatalf("ASSIGN bytes = %d, want < %d", len(messageJSON), protocol.MaxMessageSize)
	}
}

func assertTypedRecoveryFailure(t *testing.T, findings []reviewcontract.Finding, err error, kind RecoveryArtifactErrorKind) {
	t.Helper()
	if findings != nil {
		t.Fatalf("failed recovery returned partial findings: %#v", findings)
	}
	var typed *RecoveryArtifactError
	if !errors.As(err, &typed) {
		t.Fatalf("recovery error = %T %v, want *RecoveryArtifactError", err, err)
	}
	if typed.Kind != kind {
		t.Fatalf("recovery error kind = %q, want %q", typed.Kind, kind)
	}
}

func assertTypedStoredRecoveryFailure(t *testing.T, recovery protocol.ReviewRecovery, err error, kind RecoveryArtifactErrorKind) {
	t.Helper()
	if recovery.Findings != nil || recovery.FindingsRef != nil {
		t.Fatalf("failed stored recovery returned partial context: %#v", recovery)
	}
	var typed *RecoveryArtifactError
	if !errors.As(err, &typed) {
		t.Fatalf("stored recovery error = %T %v, want *RecoveryArtifactError", err, err)
	}
	if typed.Kind != kind {
		t.Fatalf("stored recovery error kind = %q, want %q", typed.Kind, kind)
	}
}
