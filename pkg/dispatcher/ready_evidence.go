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
	beadID       string
	workerID     string
	worktree     string
	evidenceRoot string
	targetSHA    string
	targetBranch string
}

func (d *Dispatcher) acceptReadyEvidence(ctx context.Context, workerID string, ready *protocol.ReadyForReviewPayload) bool {
	if legacyReadyEvidenceIdentity(ready) {
		return !d.productionReadyEvidenceRequired(ctx, workerID, ready.BeadID)
	}
	identity, evidence, evidenceSHA, ok := d.validateReadyEvidence(ctx, workerID, ready)
	if !ok {
		return false
	}
	branch := protocol.BranchPrefix + identity.beadID
	headSHA, err := d.worktrees.BranchHead(ctx, branch)
	if err != nil || strings.TrimSpace(headSHA) == "" {
		return false
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
		return false
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
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

func legacyReadyEvidenceIdentity(ready *protocol.ReadyForReviewPayload) bool {
	return ready != nil && ready.AssignmentID == 0 && ready.Worktree == "" &&
		ready.QGEvidencePath == "" && ready.TargetSHA == ""
}

func (d *Dispatcher) productionReadyEvidenceRequired(ctx context.Context, workerID, beadID string) bool {
	if d.db == nil {
		return false
	}
	d.mu.Lock()
	assignmentID := int64(0)
	if w := d.workers[workerID]; w != nil && w.beadID == beadID {
		assignmentID = w.assignmentID
	}
	d.mu.Unlock()
	if assignmentID <= 0 {
		return false
	}
	var count int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM assignments
WHERE id = ? AND bead_id = ? AND status = 'active'
  AND (qg_evidence_dir <> '' OR target_sha <> '')`, assignmentID, beadID).Scan(&count); err != nil {
		return true
	}
	return count > 0
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
	if err != nil || filepath.Clean(ready.QGEvidencePath) != wantPath {
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
	var identity durableReadyIdentity
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
	return identity.beadID != "" && ready.BeadID == identity.beadID && ready.WorkerID == workerID &&
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
