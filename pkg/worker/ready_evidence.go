package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"oro/pkg/evidencefs"
	"oro/pkg/protocol"
)

const qgEvidenceAttempt = "1.json"

type qgEvidenceOptions struct {
	RunID      string
	HeadSHA    string
	ScriptHash string
	Output     []byte
	StartedAt  time.Time
	FinishedAt time.Time
}

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

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func canonicalQGEvidencePath(root, beadID string, assignmentID int64) (string, error) {
	if !filepath.IsAbs(root) || !safeEvidenceComponent(beadID) || assignmentID <= 0 {
		return "", errors.New("invalid canonical QG evidence identity")
	}
	return filepath.Join(filepath.Clean(root), beadID, strconv.FormatInt(assignmentID, 10), qgEvidenceAttempt), nil
}

func (w *Worker) buildQGEvidence(opts qgEvidenceOptions) (protocol.QGEvidence, error) {
	w.mu.Lock()
	assign := protocol.AssignPayload{
		BeadID:        w.beadID,
		AssignmentID:  w.assignmentID,
		QGEvidenceDir: w.qgEvidenceDir,
		TargetSHA:     w.targetSHA,
	}
	workerID := w.ID
	targetBranch := w.targetBranch
	w.mu.Unlock()
	if err := validateAssignmentEvidenceIdentity(&assign); err != nil {
		return protocol.QGEvidence{}, err
	}
	if targetBranch == "" {
		return protocol.QGEvidence{}, errors.New("target branch is required")
	}
	evidence := protocol.QGEvidence{
		RunID:        opts.RunID,
		AssignmentID: assign.AssignmentID,
		BeadID:       assign.BeadID,
		WorkerID:     workerID,
		HeadSHA:      opts.HeadSHA,
		TargetBranch: targetBranch,
		TargetSHA:    assign.TargetSHA,
		ScriptHash:   opts.ScriptHash,
		Mode:         "worker",
		Passed:       true,
		StartedAt:    opts.StartedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt:   opts.FinishedAt.UTC().Format(time.RFC3339Nano),
	}
	evidence.OutputHash = sha256Hex(opts.Output)
	if err := evidence.Validate(); err != nil {
		return protocol.QGEvidence{}, fmt.Errorf("validate QG evidence: %w", err)
	}
	return evidence, nil
}

func (w *Worker) writeQGEvidence(evidence protocol.QGEvidence) (protocol.QGEvidenceRef, error) {
	w.mu.Lock()
	assign := protocol.AssignPayload{
		BeadID:        w.beadID,
		AssignmentID:  w.assignmentID,
		QGEvidenceDir: w.qgEvidenceDir,
		TargetSHA:     w.targetSHA,
	}
	evidenceRoot := w.qgEvidenceDir
	workerID := w.ID
	targetBranch := w.targetBranch
	w.mu.Unlock()
	if err := validateAssignmentEvidenceIdentity(&assign); err != nil {
		return protocol.QGEvidenceRef{}, err
	}
	if targetBranch == "" {
		return protocol.QGEvidenceRef{}, errors.New("target branch is required")
	}
	if evidence.AssignmentID != assign.AssignmentID || evidence.BeadID != assign.BeadID ||
		evidence.WorkerID != workerID || evidence.TargetBranch != targetBranch || evidence.TargetSHA != assign.TargetSHA {
		return protocol.QGEvidenceRef{}, errors.New("QG evidence does not match worker assignment")
	}
	if err := evidence.Validate(); err != nil {
		return protocol.QGEvidenceRef{}, fmt.Errorf("validate QG evidence: %w", err)
	}

	path, err := canonicalQGEvidencePath(evidenceRoot, evidence.BeadID, evidence.AssignmentID)
	if err != nil {
		return protocol.QGEvidenceRef{}, err
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return protocol.QGEvidenceRef{}, fmt.Errorf("marshal QG evidence: %w", err)
	}
	if err := evidencefs.WriteFile(evidenceRoot,
		[]string{evidence.BeadID, strconv.FormatInt(evidence.AssignmentID, 10)}, qgEvidenceAttempt, data); err != nil {
		return protocol.QGEvidenceRef{}, fmt.Errorf("publish QG evidence: %w", err)
	}
	ref := protocol.QGEvidenceRef{RunID: evidence.RunID, Path: path, SHA256: sha256Hex(data)}
	if err := ref.Validate(); err != nil {
		return protocol.QGEvidenceRef{}, fmt.Errorf("validate QG evidence reference: %w", err)
	}

	w.mu.Lock()
	w.qgEvidencePath = path
	evidenceCopy := evidence
	refCopy := ref
	w.qgEvidence = &evidenceCopy
	w.qgEvidenceRef = &refCopy
	w.mu.Unlock()
	return ref, nil
}
