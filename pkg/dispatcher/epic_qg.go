package dispatcher

import (
	"context"
	"fmt"
	"oro/pkg/beadstore"
	"oro/pkg/protocol"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// checkEpicQG creates a temporary worktree from epicBranch, runs the local
// quality gate against it with mutation testing disabled unless configured, and cleans up
// the worktree on completion. It returns true when the gate passes and
// tryCloseEpic should proceed to completeEpicClose. On failure or error it
// handles logging/escalation and returns false.
func (d *Dispatcher) checkEpicQG(ctx context.Context, epicID, workerID, epicBranch, targetBranch string) bool {
	if err := d.observeStorageController(ctx); err != nil || !d.storageAdmissionAllowed() {
		return false
	}
	wtID := d.epicQGWorktreeID(epicID)
	worktree, _, err := d.worktrees.Create(ctx, wtID, epicBranch)
	if err != nil {
		_ = d.logEvent(ctx, "epic_qg_worktree_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, err.Error()))
		return d.handleEpicQGInfraFailure(ctx, epicID, workerID, epicBranch, err)
	}
	defer func() { _ = d.worktrees.Remove(context.Background(), worktree) }()

	passed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, d.qgMutationBase(targetBranch))
	if qgErr != nil {
		_ = d.logEvent(ctx, "epic_qg_error", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, qgErr.Error()))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, epicID, "epic QG error", qgErr.Error()), epicID, workerID)
		return d.handleEpicQGInfraFailure(ctx, epicID, workerID, epicBranch, qgErr)
	}
	if !passed {
		return d.handleEpicQGFailure(ctx, epicID, workerID, epicBranch, qgOutput)
	}
	return true
}

func (d *Dispatcher) epicQGWorktreeID(epicID string) string {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(atomic.AddUint64(&d.epicQGWorktreeSeq, 1), 36)
	maxPrefixLen := 63 - len("-qg-") - len(suffix)
	if maxPrefixLen < 1 {
		maxPrefixLen = 1
	}
	prefix := epicID
	if len(prefix) > maxPrefixLen {
		prefix = strings.TrimRight(prefix[:maxPrefixLen], "-._")
	}
	if prefix == "" {
		prefix = "q"
	}
	return prefix + "-qg-" + suffix
}

// handleEpicQGFailure classifies a QG failure on an epic branch and takes the
// appropriate action:
//   - systemic/flaky → record or reuse the infra incident; no epic-specific fix bead.
//   - deterministic/unknown → create one targeted fix bead per (epic, fingerprint);
//     subsequent calls with the same fingerprint are no-ops to prevent duplicates.
//
// Always returns false (the epic remains open until QG passes); the bool
// return preserves the symmetry with checkEpicQG's other branches so the
// caller can keep its `return d.handleEpicQGFailure(...)` form.
func (d *Dispatcher) handleEpicQGFailure(ctx context.Context, epicID, workerID, epicBranch, qgOutput string) bool { //nolint:unparam // bool mirrors checkEpicQG's success/failure return so the call site stays a single-line return
	fp, summary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	rec := QGFailureRecord{
		BeadID:      epicID,
		WorkerID:    workerID,
		Component:   "epic",
		Fingerprint: fp,
		Summary:     summary,
		Output:      qgOutput,
	}
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{})

	_ = d.logEvent(ctx, "epic_qg_failed", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"output":%q,"fingerprint":%q,"class":%q,"decision":%q}`, qgOutput, fp, cls.Class, cls.Decision))

	// Systemic or flaky: record/reuse an infra incident; no epic-specific fix bead.
	if cls.Decision == QGFailureDecisionCreateOrReuseInfra || cls.Decision == QGFailureDecisionBackoffRetry {
		_, _ = d.createOrReuseQGInfraIncident(ctx, rec, cls)
		return false
	}

	// Impossible acceptance/state failures need the existing epic state fixed;
	// creating another epic child repeats the missing-AC loop.
	if cls.Decision == QGFailureDecisionBumpOriginal {
		return false
	}

	// Deterministic or unknown: one fix bead per (epic, fingerprint); skip if already created.
	if !d.epicFixBeadExists(ctx, epicID, fp) {
		beads, err := CreateBeadGraph(ctx, d.beads, epicID, []beadstore.CreateParams{{
			Title:              fmt.Sprintf("P0: Fix QG failures on %s", epicBranch),
			Type:               "bug",
			Priority:           0,
			Description:        fmt.Sprintf("Epic %s QG failed on branch %s.\n\nQG output:\n%s", epicID, epicBranch, qgOutput),
			AcceptanceCriteria: epicQGFixAcceptance(epicID, epicBranch),
		}})
		if err == nil && len(beads) > 0 {
			d.recordEpicFixBead(ctx, epicID, fp, beads[0].ID)
		}
	}
	return false
}

func (d *Dispatcher) epicFixBeadExists(ctx context.Context, epicID, fingerprint string) bool {
	if d.db == nil {
		return false
	}
	var count int
	_ = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM qg_epic_fix_beads WHERE epic_id=? AND fingerprint=?`,
		epicID, fingerprint).Scan(&count)
	return count > 0
}

func (d *Dispatcher) recordEpicFixBead(ctx context.Context, epicID, fingerprint, beadID string) {
	if d.db == nil {
		return
	}
	_, _ = d.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO qg_epic_fix_beads (epic_id, fingerprint, bead_id) VALUES (?, ?, ?)`,
		epicID, fingerprint, beadID)
}

// handleEpicQGInfraFailure classifies an infrastructure error (worktree create
// failure or QG runner error) as systemic/transient/unknown, records an infra
// incident via the standard QG failure store, and returns false. It never
// creates a direct epic child fix task.
func (d *Dispatcher) handleEpicQGInfraFailure(ctx context.Context, epicID, workerID, epicBranch string, err error) bool { //nolint:unparam // always false: infra errors never allow epic close to proceed
	errText := err.Error()
	fingerprint, summary := FingerprintQGFailure(errText, QGFingerprintOptions{})
	rec := QGFailureRecord{
		BeadID:      epicID,
		WorkerID:    workerID,
		Component:   "dispatcher",
		Output:      errText,
		Fingerprint: fingerprint,
		Summary:     summary,
	}
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{})
	cls.Decision = QGFailureDecisionCreateOrReuseInfra

	incident, incErr := d.createOrReuseQGInfraIncident(ctx, rec, cls)
	if incErr != nil {
		_ = d.logEvent(ctx, "epic_qg_infra_record_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, incErr.Error()))
		return false
	}
	_ = d.logEvent(ctx, "qg_infra_incident_reused", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"incident_id":%d,"class":%q,"fingerprint":%q}`, incident.ID, cls.Class, fingerprint))
	return false
}

func epicQGFixAcceptance(epicID, epicBranch string) string {
	return fmt.Sprintf("Test: epic QG failure for %s | Cmd: git branch --list %s | grep -q '^..%s$' && ORO_QG_CONTEXT=local ./scripts/quality_gate.sh | Assert: quality gate passes on %s without creating another missing-AC child task.\nRead: scripts/quality_gate.sh, docs/runbooks/beadstore-recovery.md\nEdges: reproduce the failing QG on %s before changing code; do not close %s directly; fix the underlying QG failure, then let the dispatcher retry epic auto-close.",
		epicID, epicBranch, epicBranch, epicBranch, epicBranch, epicID)
}
