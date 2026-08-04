package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"oro/pkg/evidencefs"
	"oro/pkg/protocol"
)

const qgEvidenceAttempt = "1.json"

func validateAssignmentEvidenceIdentity(assign *protocol.AssignPayload) error {
	if assign.AssignmentID <= 0 || assign.QGEvidenceDir == "" || assign.TargetSHA == "" {
		return errors.New("assignment ID, evidence directory, and target SHA are required")
	}
	if !filepath.IsAbs(assign.QGEvidenceDir) {
		return errors.New("evidence directory must be absolute")
	}
	if !safeEvidenceComponent(assign.BeadID) {
		return errors.New("bead ID must be a safe evidence path component")
	}
	return nil
}

func safeEvidenceComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value
}

func canonicalQGEvidencePath(root, beadID string, assignmentID int64) (string, error) {
	if !filepath.IsAbs(root) || !safeEvidenceComponent(beadID) || assignmentID <= 0 {
		return "", errors.New("invalid canonical QG evidence identity")
	}
	return filepath.Join(filepath.Clean(root), beadID, strconv.FormatInt(assignmentID, 10), qgEvidenceAttempt), nil
}

func (w *Worker) writeQGEvidence() error {
	w.mu.Lock()
	ready := protocol.ReadyForReviewPayload{
		BeadID:       w.beadID,
		WorkerID:     w.ID,
		AssignmentID: w.assignmentID,
		Worktree:     w.worktree,
		TargetSHA:    w.targetSHA,
	}
	evidenceRoot := w.qgEvidenceDir
	w.mu.Unlock()

	path, err := canonicalQGEvidencePath(evidenceRoot, ready.BeadID, ready.AssignmentID)
	if err != nil {
		return err
	}
	ready.QGEvidencePath = path
	if err := ready.Validate(); err != nil {
		return fmt.Errorf("validate QG evidence: %w", err)
	}
	data, err := json.Marshal(ready)
	if err != nil {
		return fmt.Errorf("marshal QG evidence: %w", err)
	}
	if err := evidencefs.WriteFile(evidenceRoot,
		[]string{ready.BeadID, strconv.FormatInt(ready.AssignmentID, 10)}, qgEvidenceAttempt, data); err != nil {
		return fmt.Errorf("publish QG evidence: %w", err)
	}

	w.mu.Lock()
	w.qgEvidencePath = path
	w.mu.Unlock()
	return nil
}
