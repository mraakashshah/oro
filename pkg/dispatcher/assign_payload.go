package dispatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/protocol"
)

// maxWorkerProgramSize is the maximum number of bytes read from worker-program.md.
// Content exceeding this limit is truncated with a warning logged.
const maxWorkerProgramSize = 32 * 1024

// gitLogTimeout is the maximum time allowed for the git log command.
const gitLogTimeout = 2 * time.Second

// buildAssignPayload assembles an AssignPayload for a worker from beads.Show
// and filesystem sources. It is the single source of truth for payload
// construction, replacing the ad-hoc inline literals scattered across assignBead,
// qgRetryWithReservation, and handleReviewRejection.
//
// Edges:
//   - beads.Show error → log warning, leave Title/Description/AC empty.
//   - git log timeout (2s) → empty GitLog.
//   - worker-program.md missing → empty WorkerProgram (no warning).
//   - worker-program.md >32KB → truncate with log warning.
//   - isEpicDecomp=true → GitLog and WorkerProgram are always empty.
func (d *Dispatcher) buildAssignPayload(ctx context.Context, w *trackedWorker, attempt int, feedback, memCtx string) *protocol.AssignPayload {
	p := &protocol.AssignPayload{
		BeadID:              w.beadID,
		Worktree:            w.worktree,
		Model:               w.model,
		Attempt:             attempt,
		Feedback:            feedback,
		MemoryContext:       memCtx,
		IsEpicDecomposition: w.isEpicDecomp,
		ProjectRoot:         d.cfg.RepoRoot,
		TargetBranch:        w.targetBranch,
	}

	// Populate metadata from beads.Show.
	detail, err := d.beads.Show(ctx, w.beadID)
	if err != nil {
		_ = d.logEvent(ctx, "build_assign_payload_show_failed", "dispatcher", w.beadID, w.id,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		// Title, Description, AcceptanceCriteria remain empty.
	} else if detail != nil {
		p.Title = detail.Title
		p.Description = detail.Description
		p.AcceptanceCriteria = detail.AcceptanceCriteria
	}

	// Epic decomposition workers don't need git history or the worker program.
	if w.isEpicDecomp {
		return p
	}

	// Git log — 2s hard timeout to keep assignment latency bounded.
	gitCtx, gitCancel := context.WithTimeout(ctx, gitLogTimeout)
	defer gitCancel()
	gitOut, gitErr := d.shutdownRunner.Run(gitCtx, "git", "log", "--oneline", "-20")
	if gitErr == nil {
		p.GitLog = strings.TrimSpace(string(gitOut))
	}
	// On timeout or error GitLog stays empty.

	// worker-program.md — optional file that provides project-specific guidance.
	wpPath := filepath.Join(d.cfg.RepoRoot, "worker-program.md")
	wpData, wpErr := os.ReadFile(wpPath) //nolint:gosec // path derived from trusted config
	if wpErr == nil {
		if len(wpData) > maxWorkerProgramSize {
			_ = d.logEvent(ctx, "worker_program_truncated", "dispatcher", w.beadID, w.id,
				fmt.Sprintf(`{"original_size":%d,"truncated_to":%d}`, len(wpData), maxWorkerProgramSize))
			wpData = wpData[:maxWorkerProgramSize]
		}
		p.WorkerProgram = string(wpData)
	}
	// Missing file: WorkerProgram stays empty.

	return p
}
