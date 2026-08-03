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
		recovery, err := PrepareReviewRecovery(artifactDir, checkpoint.ID, "rejected-head", "acceptance", 1, findings)
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
	recovery, err := PrepareReviewRecovery(artifactDir, checkpoint.ID, "rejected-head", "acceptance", 2, findings)
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

		if err := os.WriteFile(recovery.FindingsRef.Path, []byte("corrupt"), 0o600); err != nil {
			t.Fatalf("corrupt recovery artifact: %v", err)
		}
		got, err = LoadRecoveryArtifact(*recovery.FindingsRef)
		assertTypedRecoveryFailure(t, got, err, RecoveryArtifactCorrupt)
	})
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
			BeadID:        "oro-overflow",
			Worktree:      "/tmp/oro-overflow",
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
