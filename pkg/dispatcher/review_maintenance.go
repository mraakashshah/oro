package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"oro/pkg/evidencefs"
)

const (
	defaultReviewArtifactRetention   = 7 * 24 * time.Hour
	defaultReviewMaintenanceInterval = time.Hour
)

// isReviewArtifactTerminal reports whether a checkpoint can release its
// artifacts after retention. Approval alone is intentionally not terminal:
// integration can still be pending or require operator action.
func isReviewArtifactTerminal(state ReviewCheckpointState) bool {
	return state == ReviewCheckpointStateIntegrated || state == ReviewCheckpointStateSuperseded
}

// reviewMaintenanceLoop periodically removes terminal review artifacts after
// their retention window. It runs once immediately so restarts do not defer a
// due cleanup until the next interval.
func (d *Dispatcher) reviewMaintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(d.reviewMaintenanceInterval)
	defer ticker.Stop()

	for {
		d.pruneReviewArtifacts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) pruneReviewArtifacts(ctx context.Context) {
	d.reviewArtifactPruneMu.Lock()
	defer d.reviewArtifactPruneMu.Unlock()

	store := NewReviewCheckpointStore(d.db)
	before := d.nowFunc().Add(-d.reviewArtifactRetention)
	artifacts, err := store.ListPrunableArtifacts(ctx, before)
	if err == nil {
		for _, artifact := range artifacts {
			if err := d.removeReviewArtifact(artifact); err != nil && !errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err := store.ClearPrunedArtifact(ctx, artifact.Path); err != nil {
				continue
			}
		}
	}
	d.pruneReviewEvidenceOrphans(ctx, before)
}

func (d *Dispatcher) removeReviewArtifact(artifact ArtifactRef) error {
	if !artifact.QGEvidence {
		if err := os.Remove(artifact.Path); err != nil {
			return fmt.Errorf("remove review artifact: %w", err)
		}
		return nil
	}
	root := filepath.Clean(d.cfg.ReviewEvidenceDir)
	path := filepath.Clean(artifact.Path)
	if artifact.Path != path || !filepath.IsAbs(root) {
		return errors.New("checkpoint QG evidence path is not canonical")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve checkpoint QG evidence path: %w", err)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[2] != readyEvidenceAttempt {
		return errors.New("checkpoint QG evidence path has invalid layout")
	}
	assignmentID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || assignmentID <= 0 || strconv.FormatInt(assignmentID, 10) != parts[1] {
		return errors.New("checkpoint QG evidence assignment is invalid")
	}
	want, err := canonicalReadyEvidencePath(root, parts[0], assignmentID)
	if err != nil || want != path {
		return errors.New("checkpoint QG evidence path is outside canonical root")
	}
	if err := evidencefs.RemoveFile(root, parts[:2], parts[2]); err != nil {
		return fmt.Errorf("remove checkpoint QG evidence: %w", err)
	}
	return nil
}

func (d *Dispatcher) pruneReviewEvidenceOrphans(ctx context.Context, olderThan time.Time) {
	root := filepath.Clean(d.cfg.ReviewEvidenceDir)
	if root == "." || !filepath.IsAbs(root) || d.db == nil {
		return
	}
	files, err := evidencefs.ListAssignmentFiles(root, readyEvidenceAttempt)
	if err != nil {
		return
	}
	live, err := d.loadLiveReviewEvidence(ctx, root)
	if err != nil {
		return
	}
	for _, file := range files {
		path := filepath.Join(root, file.BeadID, strconv.FormatInt(file.AssignmentID, 10), readyEvidenceAttempt)
		if !file.ModTime.Before(olderThan) || live[path] {
			continue
		}
		_ = evidencefs.RemoveFile(root,
			[]string{file.BeadID, strconv.FormatInt(file.AssignmentID, 10)}, readyEvidenceAttempt)
	}
}

func (d *Dispatcher) loadLiveReviewEvidence(ctx context.Context, root string) (map[string]bool, error) {
	live := make(map[string]bool)
	rows, err := d.db.QueryContext(ctx, `
SELECT id, bead_id
FROM assignments
	WHERE qg_evidence_dir=? AND status IN ('active','requeued','quarantined')`, root)
	if err != nil {
		return nil, fmt.Errorf("query live assignment evidence: %w", err)
	}
	for rows.Next() {
		var assignmentID int64
		var beadID string
		if err := rows.Scan(&assignmentID, &beadID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan live assignment evidence: %w", err)
		}
		path, pathErr := canonicalReadyEvidencePath(root, beadID, assignmentID)
		if pathErr == nil {
			live[path] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate live assignment evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close live assignment evidence: %w", err)
	}

	checkpointRows, err := d.db.QueryContext(ctx, `
SELECT qg_evidence_path
FROM review_checkpoints
	WHERE COALESCE(qg_evidence_path, '') <> ''`)
	if err != nil {
		return nil, fmt.Errorf("query checkpoint evidence: %w", err)
	}
	defer func() { _ = checkpointRows.Close() }()
	for checkpointRows.Next() {
		var path string
		if err := checkpointRows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan checkpoint evidence: %w", err)
		}
		live[filepath.Clean(path)] = true
	}
	if err := checkpointRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate checkpoint evidence: %w", err)
	}
	return live, nil
}
