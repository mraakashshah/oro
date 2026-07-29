package dispatcher

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ArtifactRef identifies immutable feedback content by its SHA-256 digest.
type ArtifactRef struct{ SHA256 string }

// QGRetryContext records the exact persisted QG failure a replacement worker
// must receive when the original worker is lost before retry delivery.
type QGRetryContext struct {
	OccurrenceID string
	HeadSHA      string
	Attempt      int
	FeedbackRef  *ArtifactRef
}

func (d *Dispatcher) rememberQGRetryContext(workerID string, rec QGFailureRecord, attempt int) {
	if workerID == "" || rec.ID == "" || attempt <= 0 {
		return
	}
	d.mu.Lock()
	worktree := ""
	if worker := d.workers[workerID]; worker != nil {
		worktree = worker.worktree
	}
	d.mu.Unlock()
	if worktree == "" {
		return
	}
	head, err := d.commandRunner().Run(context.Background(), "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) == "" {
		return
	}
	retry := QGRetryContext{OccurrenceID: rec.ID, HeadSHA: strings.TrimSpace(string(head)), Attempt: attempt, FeedbackRef: &ArtifactRef{SHA256: rec.OutputHash}}
	d.mu.Lock()
	d.pendingQGRetries[workerID] = retry
	d.mu.Unlock()
}

func (d *Dispatcher) hasPendingQGRetry(workerID, beadID string, attempt int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	retry, ok := d.pendingQGRetries[workerID]
	if ok && retry.Attempt == attempt {
		worker := d.workers[workerID]
		return worker == nil || worker.beadID == beadID
	}
	handoff := d.pendingHandoffs[beadID]
	return handoff != nil && handoff.attempt == attempt
}

func (d *Dispatcher) loadQGRetryFeedback(ctx context.Context, retry QGRetryContext, worktree string) (string, error) {
	if d.db == nil || retry.OccurrenceID == "" || retry.HeadSHA == "" || retry.Attempt <= 0 || retry.FeedbackRef == nil || retry.FeedbackRef.SHA256 == "" {
		return "", fmt.Errorf("invalid qg retry context")
	}
	head, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != retry.HeadSHA {
		return "", fmt.Errorf("qg retry head mismatch")
	}
	var output, outputHash string
	if err := d.db.QueryRowContext(ctx, `SELECT raw_output, output_hash FROM qg_failure_occurrences WHERE id=?`, retry.OccurrenceID).Scan(&output, &outputHash); err != nil {
		return "", fmt.Errorf("load qg occurrence: %w", err)
	}
	if output == "" || outputHash != retry.FeedbackRef.SHA256 || hashQGFailureOutput(output) != outputHash {
		return "", fmt.Errorf("qg occurrence feedback hash mismatch")
	}
	return output, nil
}

func (d *Dispatcher) restoreQGRetryHandoff(ctx context.Context, workerID, beadID string, assignmentID int64, retry QGRetryContext, snapshot workerAssignmentSnapshot) error {
	feedback, err := d.loadQGRetryFeedback(ctx, retry, snapshot.worktree)
	if err != nil {
		return fmt.Errorf("load qg retry feedback: %w", err)
	}
	if snapshot.execution.AssignmentID == 0 {
		snapshot.execution = workerExecutionContext(assignmentID, false, filepath.Base(d.cfg.RepoRoot))
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.pendingHandoffs[beadID]; exists {
		return fmt.Errorf("qg retry handoff already pending for bead %q", beadID)
	}
	d.pendingHandoffs[beadID] = &pendingHandoff{
		assignmentID: assignmentID,
		execution:    snapshot.execution,
		beadID:       beadID,
		epicID:       snapshot.epicID,
		worktree:     snapshot.worktree,
		baseBranch:   snapshot.baseBranch,
		targetBranch: snapshot.targetBranch,
		runtime:      snapshot.runtime,
		model:        snapshot.model,
		reasoning:    snapshot.reasoning,
		feedback:     feedback,
		attempt:      retry.Attempt,
	}
	delete(d.pendingQGRetries, workerID)
	return nil
}
