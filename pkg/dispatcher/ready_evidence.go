package dispatcher

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"oro/pkg/protocol"
)

const readyEvidenceAttempt = "1.json"

type durableReadyIdentity struct {
	assignmentID int64
	beadID       string
	workerID     string
	worktree     string
	evidenceRoot string
	targetSHA    string
	targetBranch string
}

func (d *Dispatcher) acceptReadyEvidence(ctx context.Context, workerID string, ready *protocol.ReadyForReviewPayload) (durableReadyIdentity, bool) {
	if legacyReadyEvidenceIdentity(ready) {
		return d.validateLegacyReadyEvidence(ctx, workerID, ready)
	}
	identity, evidence, evidenceSHA, ok := d.validateReadyEvidence(ctx, workerID, ready)
	if !ok {
		return durableReadyIdentity{}, false
	}
	branch := protocol.BranchPrefix + identity.beadID
	headSHA, err := d.worktrees.BranchHead(ctx, branch)
	if err != nil || strings.TrimSpace(headSHA) == "" {
		return durableReadyIdentity{}, false
	}
	_, acceptance, _ := d.lookupBeadDetail(ctx, identity.beadID, workerID)
	checkpointInput := CheckpointInput{
		CheckpointKey:       fmt.Sprintf("qg-evidence:%s:%d", identity.beadID, ready.AssignmentID),
		BeadID:              identity.beadID,
		OriginAssignmentID:  ready.AssignmentID,
		CurrentAssignmentID: ready.AssignmentID,
		WorkerID:            workerID,
		Worktree:            identity.worktree,
		Branch:              branch,
		TargetBranch:        identity.targetBranch,
		HeadSHA:             strings.TrimSpace(headSHA),
		TargetSHA:           identity.targetSHA,
		AcceptanceHash:      readyEvidenceHash([]byte(acceptance)),
		QGScriptHash:        readyEvidenceHash([]byte("worker-qg-evidence-v1")),
		QGMode:              "worker",
		ReviewPolicyHash:    readyEvidenceHash([]byte("default-review-policy")),
		TriageRevision:      "ready-evidence-v1",
		ReadyAttempt:        "1",
		State:               ReviewCheckpointStateQGPassed,
	}
	checkpoint, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, checkpointInput)
	if err != nil || checkpoint.CheckpointInput != checkpointInput {
		return durableReadyIdentity{}, false
	}
	result, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET qg_evidence_path = ?, qg_evidence_sha256 = ?, updated_at = datetime('now')
WHERE id = ? AND state = ?
  AND (qg_evidence_path IS NULL OR qg_evidence_path = ?)
  AND (qg_evidence_sha256 IS NULL OR qg_evidence_sha256 = ?)`,
		evidence.QGEvidencePath, evidenceSHA, checkpoint.ID, ReviewCheckpointStateQGPassed,
		evidence.QGEvidencePath, evidenceSHA)
	if err != nil {
		return durableReadyIdentity{}, false
	}
	rows, err := result.RowsAffected()
	return identity, err == nil && rows == 1
}

func legacyReadyEvidenceIdentity(ready *protocol.ReadyForReviewPayload) bool {
	return ready != nil && ready.AssignmentID == 0 && ready.Worktree == "" &&
		ready.QGEvidencePath == "" && ready.TargetSHA == ""
}

func (d *Dispatcher) validateLegacyReadyEvidence(
	ctx context.Context,
	workerID string,
	ready *protocol.ReadyForReviewPayload,
) (durableReadyIdentity, bool) {
	if !legacyReadyEvidenceIdentity(ready) || ready.WorkerID != workerID || ready.BeadID == "" || d.db == nil {
		return durableReadyIdentity{}, false
	}
	d.mu.Lock()
	w := d.workers[workerID]
	if w == nil || w.state != protocol.WorkerBusy || w.assignmentID <= 0 ||
		w.beadID != ready.BeadID || w.worktree == "" {
		d.mu.Unlock()
		return durableReadyIdentity{}, false
	}
	identity := durableReadyIdentity{
		assignmentID: w.assignmentID,
		beadID:       w.beadID,
		workerID:     workerID,
		worktree:     w.worktree,
		targetBranch: w.targetBranch,
	}
	d.mu.Unlock()

	var durable durableReadyIdentity
	durable.assignmentID = identity.assignmentID
	if err := d.db.QueryRowContext(ctx, `
SELECT bead_id, worker_id, worktree, qg_evidence_dir, target_sha
FROM assignments
WHERE id = ? AND status = 'active'`, identity.assignmentID).Scan(
		&durable.beadID, &durable.workerID, &durable.worktree, &durable.evidenceRoot, &durable.targetSHA,
	); err != nil {
		return durableReadyIdentity{}, false
	}
	if durable.beadID != identity.beadID || durable.workerID != identity.workerID ||
		durable.worktree != identity.worktree || durable.evidenceRoot != "" || durable.targetSHA != "" {
		return durableReadyIdentity{}, false
	}
	durable.targetBranch = identity.targetBranch
	if durable.targetBranch == "" {
		durable.targetBranch = d.cfg.DefaultBranch
	}
	return durable, true
}

func (d *Dispatcher) validateReadyEvidence(
	ctx context.Context,
	workerID string,
	ready *protocol.ReadyForReviewPayload,
) (durableReadyIdentity, protocol.ReadyForReviewPayload, string, bool) {
	if ready == nil || ready.Validate() != nil || ready.WorkerID != workerID || d.db == nil {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, "", false
	}
	identity, err := d.loadDurableReadyIdentity(ctx, ready.AssignmentID, workerID)
	if err != nil || !readyMatchesDurableIdentity(workerID, ready, identity) {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, "", false
	}
	wantPath, err := canonicalReadyEvidencePath(identity.evidenceRoot, identity.beadID, ready.AssignmentID)
	if err != nil || ready.QGEvidencePath != wantPath {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, "", false
	}
	data, err := os.ReadFile(wantPath) //nolint:gosec // exact path is derived from the durable assignment identity.
	if err != nil {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, "", false
	}
	var evidence protocol.ReadyForReviewPayload
	if json.Unmarshal(data, &evidence) != nil || evidence.Validate() != nil || evidence != *ready {
		return durableReadyIdentity{}, protocol.ReadyForReviewPayload{}, "", false
	}
	return identity, evidence, readyEvidenceHash(data), true
}

func (d *Dispatcher) loadDurableReadyIdentity(ctx context.Context, assignmentID int64, workerID string) (durableReadyIdentity, error) {
	identity := durableReadyIdentity{assignmentID: assignmentID}
	err := d.db.QueryRowContext(ctx, `
SELECT bead_id, worker_id, worktree, qg_evidence_dir, target_sha
FROM assignments
WHERE id = ? AND status = 'active'`, assignmentID).Scan(
		&identity.beadID, &identity.workerID, &identity.worktree, &identity.evidenceRoot, &identity.targetSHA,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return durableReadyIdentity{}, errors.New("active assignment evidence identity not found")
		}
		return durableReadyIdentity{}, fmt.Errorf("load assignment evidence identity: %w", err)
	}
	d.mu.Lock()
	w := d.workers[workerID]
	if w != nil && w.assignmentID == assignmentID {
		identity.targetBranch = w.targetBranch
	}
	d.mu.Unlock()
	if w == nil || w.assignmentID != assignmentID {
		return durableReadyIdentity{}, errors.New("worker does not own active assignment")
	}
	if identity.targetBranch == "" {
		identity.targetBranch = d.cfg.DefaultBranch
	}
	return identity, nil
}

func readyMatchesDurableIdentity(workerID string, ready *protocol.ReadyForReviewPayload, identity durableReadyIdentity) bool {
	return identity.assignmentID == ready.AssignmentID && identity.beadID != "" && ready.BeadID == identity.beadID &&
		identity.workerID == workerID && ready.WorkerID == workerID &&
		ready.Worktree == identity.worktree &&
		identity.evidenceRoot != "" && ready.TargetSHA == identity.targetSHA
}

func canonicalReadyEvidencePath(root, beadID string, assignmentID int64) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || beadID == "" || beadID == "." || beadID == ".." ||
		filepath.Base(beadID) != beadID || assignmentID <= 0 {
		return "", errors.New("invalid durable QG evidence identity")
	}
	return filepath.Join(root, beadID, strconv.FormatInt(assignmentID, 10), readyEvidenceAttempt), nil
}

func readyEvidenceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
