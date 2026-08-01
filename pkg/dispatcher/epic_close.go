package dispatcher

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// removeWorktreeAndClearTracking removes a worktree, deletes the agent branch,
// and clears the tracking entry. Safe to call after successful merge completion.
// Logs but does not return errors.
func (d *Dispatcher) removeWorktreeAndClearTracking(ctx context.Context, beadID, workerID, worktree, targetBranch string) {
	if err := d.worktrees.Remove(ctx, worktree); err != nil {
		_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	}

	// Unconditionally clear worktree tracking entry (oro-4mu1.2).
	// Delete even if Remove fails — the worktree path is stale regardless.
	d.mu.Lock()
	delete(d.worktreeByBead, beadID)
	d.mu.Unlock()

	// Best-effort branch cleanup — branch was merged, safe to delete.
	branch := protocol.BranchPrefix + beadID
	target := targetBranch
	if target == "" {
		target = d.cfg.DefaultBranch
	}
	if err := d.worktrees.DeleteBranchMergedInto(ctx, branch, target); err != nil {
		_ = d.logEvent(ctx, "branch_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	}
}

// autoCloseEpicIfComplete checks if the bead has a parent epic and
// auto-closes the epic if all children are completed. Runs in a goroutine.
func (d *Dispatcher) autoCloseEpicIfComplete(ctx context.Context, workerID, epicID string) {
	if epicID == "" {
		return
	}

	d.safeGo(func() { d.tryCloseEpic(ctx, epicID, workerID) })
}

// tryCloseEpic checks if all children of the epic are closed. If so, it runs
// the epic's Cmd: acceptance test (if present) before closing. A passing test
// closes the epic normally; a failing test spawns a diagnostic agent to create
// fix beads instead of closing. Epics without a Cmd: fall back to count-based
// close with a warning logged.

// resolveEpicTargetBranch returns the epic's target branch from metadata,
// falling back to defaultBranch.
func resolveEpicTargetBranch(metadata map[string]any, defaultBranch string) string {
	if s, _ := metadata[MetaBranch].(string); s != "" {
		return s
	}
	return defaultBranch
}

// epicMergeIsFailed reports whether the epic's FF-merge previously failed.
// Caller must not hold d.mu.
func (d *Dispatcher) epicMergeIsFailed(epicID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.epicMergeFailed[epicID]
}

// tryBeginEpicClose reserves the epic's close path. Caller must invoke the
// returned release function exactly once when it returns true.
func (d *Dispatcher) tryBeginEpicClose(epicID string) (reserved bool, release func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.epicMergeFailed, epicID)
	if d.epicCloseInFlight == nil {
		d.epicCloseInFlight = make(map[string]bool)
	}
	if d.epicCloseInFlight[epicID] {
		return false, nil
	}
	d.epicCloseInFlight[epicID] = true
	return true, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		delete(d.epicCloseInFlight, epicID)
	}
}

func (d *Dispatcher) tryCloseEpic(ctx context.Context, epicID, workerID string) {
	allClosed, err := d.beads.AllChildrenClosed(ctx, epicID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_auto_close_check_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	if !allClosed {
		if d.epicMergeIsFailed(epicID) {
			_ = d.logEvent(ctx, "epic_close_skipped_merge_failed", "dispatcher", epicID, workerID, "")
		}
		return
	}
	reserved, release := d.tryBeginEpicClose(epicID)
	if !reserved {
		return
	}
	defer release()

	detail, ok := d.fetchEpicCloseDetail(ctx, epicID, workerID)
	if !ok {
		return
	}
	if strings.EqualFold(detail.Status, "closed") {
		return
	}
	d.closeEpicAfterAcceptance(ctx, detail, epicID, workerID)
}

func (d *Dispatcher) closeEpicAfterAcceptance(ctx context.Context, detail *protocol.BeadDetail, epicID, workerID string) {
	targetBranch := resolveEpicTargetBranch(detail.Metadata, d.cfg.DefaultBranch)

	cmd, ok := d.parseEpicAcceptanceCmd(ctx, "epic_acceptance_parse_error", epicID, workerID, detail.AcceptanceCriteria)
	if !ok {
		return
	}
	if cmd == "" {
		// No executable acceptance test: warn and fall back to count-based close.
		_ = d.logEvent(ctx, "epic_no_acceptance_cmd", "dispatcher", epicID, workerID,
			`{"warning":"epic has no Cmd: acceptance test; falling back to count-based close"}`)
		d.completeEpicClose(ctx, epicID, workerID, "All children completed (no acceptance test)", targetBranch)
		return
	}

	// Run the acceptance test.
	output, passed, runErr := d.acceptance.Run(ctx, cmd)
	if runErr != nil {
		_ = d.logEvent(ctx, "epic_acceptance_run_error", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"cmd":%q,"error":%q}`, cmd, runErr.Error()))
		passed = false
	}

	if passed {
		_ = d.logEvent(ctx, "epic_acceptance_passed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"cmd":%q}`, cmd))
		epicBranch := protocol.EpicBranchPrefix + epicID
		if !d.checkEpicQG(ctx, epicID, workerID, epicBranch, targetBranch) {
			return
		}
		d.completeEpicClose(ctx, epicID, workerID, "Acceptance test passed", targetBranch)
		return
	}

	// Acceptance test failed: spawn a diagnostic agent to create fix beads.
	// Do NOT close the epic — it will be retried when the fix beads complete.
	_ = d.logEvent(ctx, "epic_acceptance_failed", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"cmd":%q,"output":%q}`, cmd, output))
	d.ops.DiagnoseEpicFailure(ctx, ops.EpicFixOpts{
		EpicID: epicID,
		AC:     detail.AcceptanceCriteria,
		Cmd:    cmd,
		Output: output,
	})
}

func (d *Dispatcher) fetchEpicCloseDetail(ctx context.Context, epicID, workerID string) (*protocol.Bead, bool) {
	// Fetch the epic's acceptance criteria to look for an executable Cmd:.
	detail, showErr := d.beads.Show(ctx, epicID)
	if showErr != nil {
		_ = d.logEvent(ctx, "epic_ac_fetch_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, showErr.Error()))
		// Fall back to count-based close so a transient Show error doesn't block.
		// Use DefaultBranch since we have no detail metadata to inspect.
		d.completeEpicClose(ctx, epicID, workerID, "All children completed (AC fetch failed)", d.cfg.DefaultBranch)
		return nil, false
	}
	if detail == nil {
		_ = d.logEvent(ctx, "epic_ac_fetch_failed", "dispatcher", epicID, workerID,
			`{"error":"show returned nil epic"}`)
		d.completeEpicClose(ctx, epicID, workerID, "All children completed (AC fetch failed)", d.cfg.DefaultBranch)
		return nil, false
	}
	return detail, true
}

// ffMergeEpicBranch merges the epic branch into targetBranch and deletes it.
// When targetBranch equals cfg.DefaultBranch (the HEAD branch), it uses
// MergeFFOnly (git merge --ff-only) so the working tree is updated. For any
// other target it uses UpdateBranchRef (git update-ref), which advances the
// ref without requiring it to be checked out. Returns nil if the branch does
// not exist (no-op) or if the merge succeeds. Returns an error if the merge
// fails; in that case a rebase child bead is created so the epic will be
// retried when the rebase completes.

func (d *Dispatcher) advanceTargetToEpic(ctx context.Context, epicBranch, targetBranch string) error {
	if targetBranch == d.cfg.DefaultBranch {
		if _, err := d.worktrees.MergeFFOnly(ctx, epicBranch, d.repoRoot); err != nil {
			return fmt.Errorf("ff-only merge %s into %s: %w", epicBranch, targetBranch, err)
		}
		return nil
	}
	if err := d.worktrees.UpdateBranchRef(ctx, targetBranch, epicBranch); err != nil {
		return fmt.Errorf("advance %s to %s: %w", targetBranch, epicBranch, err)
	}
	return nil
}

// recoverEpicDivergence handles a failed close-time fast-forward of the epic
// branch. It first attempts a deterministic preserve merge and retries the ff;
// only on a content conflict, an operational error, or a worktree manager that
// does not implement epicMergePreserver does it fall back to creating an LLM
// rebase child. Returns nil when the ff ultimately succeeds.
func (d *Dispatcher) recoverEpicDivergence(ctx context.Context, epicID, workerID, epicBranch, targetBranch string, cause error) error {
	wrapped := fmt.Errorf("ff merge %s to %s: %w", epicBranch, targetBranch, cause)
	_ = d.logEvent(ctx, "epic_ff_merge_failed", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, wrapped.Error()))

	if d.tryDeterministicEpicRebase(ctx, epicID, workerID, epicBranch, targetBranch) {
		if retryErr := d.advanceTargetToEpic(ctx, epicBranch, targetBranch); retryErr == nil {
			_ = d.logEvent(ctx, "epic_deterministic_rebase_recovered", "dispatcher", epicID, workerID,
				fmt.Sprintf(`{"branch":%q,"target":%q}`, epicBranch, targetBranch))
			return nil
		}
	}

	if _, ensureErr := d.ensureEpicRebaseChild(ctx, epicID, epicBranch, targetBranch, wrapped.Error()); ensureErr != nil {
		_ = d.logEvent(ctx, "epic_rebase_child_ensure_failed", "dispatcher", epicID, workerID, ensureErr.Error())
	}
	return wrapped
}

// tryDeterministicEpicRebase attempts to preserve target ancestry on the epic
// branch without an LLM worker. It returns true when the epic branch now
// contains target (either it already did, or a preserve merge was created,
// verified by the quality gate, and committed via compare-and-swap), meaning
// the caller may retry the ff. A content conflict, an operational error, a
// failing quality gate, or a worktree manager that does not implement
// epicMergePreserver returns false so the caller falls back to
// ensureEpicRebaseChild.
func (d *Dispatcher) tryDeterministicEpicRebase(ctx context.Context, epicID, workerID, epicBranch, targetBranch string) bool {
	preserver, ok := d.worktrees.(epicMergePreserver)
	if !ok {
		return false
	}
	oldEpicOID, headErr := d.worktrees.BranchHead(ctx, epicBranch)
	if headErr != nil {
		_ = d.logEvent(ctx, "epic_deterministic_rebase_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, headErr.Error()))
		return false
	}
	outcome, sha, err := preserver.preserveEpicAncestry(ctx, epicBranch, targetBranch)
	if err != nil {
		_ = d.logEvent(ctx, "epic_deterministic_rebase_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, err.Error()))
		return false
	}
	switch outcome {
	case epicPreserveNoop:
		_ = d.logEvent(ctx, "epic_deterministic_rebase_preserved", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q,"outcome":%d,"sha":%q}`, epicBranch, targetBranch, outcome, sha))
		return true
	case epicPreserveMerged:
		if !d.verifyEpicPreserveMerge(ctx, epicID, workerID, epicBranch, targetBranch, oldEpicOID, sha, preserver) {
			return false
		}
		_ = d.logEvent(ctx, "epic_deterministic_rebase_preserved", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q,"outcome":%d,"sha":%q}`, epicBranch, targetBranch, outcome, sha))
		return true
	default: // epicPreserveConflict
		_ = d.logEvent(ctx, "epic_deterministic_rebase_conflict", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q}`, epicBranch, targetBranch))
		return false
	}
}

// verifyEpicPreserveMerge runs the quality gate against the synthesized
// preserve-merge commit (sha) that preserveEpicAncestry already advanced
// epicBranch to via compare-and-swap. Main must never advance onto an
// unverified merge, so on gate failure or infra error this rolls epicBranch
// back to oldEpicOID before returning false. Returns true only when the gate
// passes.
func (d *Dispatcher) verifyEpicPreserveMerge(ctx context.Context, epicID, workerID, epicBranch, targetBranch, oldEpicOID, sha string, preserver epicMergePreserver) bool {
	wtID := d.epicQGWorktreeID(epicID)
	worktree, _, err := d.worktrees.Create(ctx, wtID, epicBranch)
	if err != nil {
		_ = d.logEvent(ctx, "epic_preserve_verify_worktree_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, err.Error()))
		d.rollbackEpicPreserveMerge(ctx, epicID, workerID, epicBranch, oldEpicOID, sha, preserver)
		return false
	}
	defer func() { _ = d.worktrees.Remove(context.Background(), worktree) }()

	passed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, d.qgMutationBase(targetBranch))
	if qgErr != nil {
		_ = d.logEvent(ctx, "epic_preserve_verify_error", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, qgErr.Error()))
		d.rollbackEpicPreserveMerge(ctx, epicID, workerID, epicBranch, oldEpicOID, sha, preserver)
		return false
	}
	if !passed {
		_ = d.logEvent(ctx, "epic_preserve_verify_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"output":%q}`, epicBranch, qgOutput))
		d.rollbackEpicPreserveMerge(ctx, epicID, workerID, epicBranch, oldEpicOID, sha, preserver)
		return false
	}
	return true
}

// rollbackEpicPreserveMerge reverts a preserve merge that failed post-merge
// verification, logging the outcome either way.
func (d *Dispatcher) rollbackEpicPreserveMerge(ctx context.Context, epicID, workerID, epicBranch, oldEpicOID, sha string, preserver epicMergePreserver) {
	if err := preserver.rollbackEpicPreserve(ctx, epicBranch, oldEpicOID, sha); err != nil {
		_ = d.logEvent(ctx, "epic_preserve_rollback_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, err.Error()))
		return
	}
	_ = d.logEvent(ctx, "epic_preserve_rolled_back", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"branch":%q,"old":%q,"rejected":%q}`, epicBranch, oldEpicOID, sha))
}

// ensureEpicRebaseChild returns the one active recovery child for an epic
// branch/target pair, creating it when no active canonical child exists.
//
//nolint:unparam // the recovery contract exposes the created-or-reused child for direct callers and tests.
func (d *Dispatcher) ensureEpicRebaseChild(ctx context.Context, epicID, epicBranch, targetBranch, cause string) (*protocol.Bead, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	title := fmt.Sprintf("Rebase %s onto %s", epicBranch, targetBranch)
	acceptance := rebaseChildAcceptance(epicID, epicBranch, targetBranch)
	children, err := d.beads.FindByParentAndTag(ctx, epicID, "rebase")
	if err != nil {
		return nil, fmt.Errorf("find epic rebase children: %w", err)
	}
	for i := range children {
		child := &children[i]
		if isCanonicalEpicRebaseChild(child, epicID, title, acceptance) {
			if err := d.addEpicRebaseDependency(ctx, epicID, child.ID); err != nil {
				return nil, err
			}
			return child, nil
		}
		if !isLegacyEpicRebaseChild(child, epicID, title) {
			continue
		}
		if err := d.beads.Update(ctx, child.ID, beadstore.UpdateParams{AcceptanceCriteria: &acceptance}); err != nil {
			return nil, fmt.Errorf("upgrade legacy epic rebase child: %w", err)
		}
		child.AcceptanceCriteria = acceptance
		if err := d.addEpicRebaseDependency(ctx, epicID, child.ID); err != nil {
			return nil, err
		}
		return child, nil
	}

	child, err := d.beads.Create(ctx, beadstore.CreateParams{
		Title:              title,
		Type:               "task",
		Priority:           0,
		Description:        fmt.Sprintf("Epic branch %s diverged from %s: %s", epicBranch, targetBranch, cause),
		ParentID:           epicID,
		AcceptanceCriteria: acceptance,
		Tags:               []string{"rebase"},
		Metadata: map[string]string{
			"epic_rebase_child":       "true",
			"epic_rebase_target":      targetBranch,
			"epic_rebase_epic_branch": epicBranch,
		},
		Tier: parentTierForCreate(ctx, d.beads, epicID),
	})
	if err != nil {
		return nil, fmt.Errorf("create epic rebase child: %w", err)
	}
	if child == nil {
		return nil, fmt.Errorf("create epic rebase child: store returned nil bead")
	}
	if err := d.addEpicRebaseDependency(ctx, epicID, child.ID); err != nil {
		return nil, err
	}
	return child, nil
}

func (d *Dispatcher) addEpicRebaseDependency(ctx context.Context, epicID, childID string) error {
	store, ok := d.beads.(dependencyStore)
	if !ok {
		return fmt.Errorf("bead store does not support dependencies")
	}
	if err := store.AddDependency(ctx, epicID, childID, "blocks"); err != nil {
		return fmt.Errorf("add epic rebase child dependency: %w", err)
	}
	return nil
}

func isCanonicalEpicRebaseChild(child *protocol.Bead, epicID, title, acceptance string) bool {
	if child == nil || (child.Status != "open" && child.Status != "in_progress") {
		return false
	}
	return child.Epic == epicID && child.Title == title && child.AcceptanceCriteria == acceptance
}

func isLegacyEpicRebaseChild(child *protocol.Bead, epicID, title string) bool {
	if child == nil || (child.Status != "open" && child.Status != "in_progress") {
		return false
	}
	return child.Epic == epicID && child.Title == title &&
		strings.Contains(child.AcceptanceCriteria, "Cmd: git fetch --all --prune && git rebase ")
}

func rebaseChildAcceptance(epicID, epicBranch, targetBranch string) string {
	return strings.Join([]string{
		fmt.Sprintf("Test: epic %s recovery preserves %s and %s ancestry", epicID, targetBranch, epicBranch),
		fmt.Sprintf("Cmd: git merge-base --is-ancestor %s HEAD && git merge-base --is-ancestor %s HEAD && go test ./pkg/dispatcher -run '^(TestEpicRebaseChildAcceptanceAllowsPreservedAncestry|TestEpicFFMergeFailureCreatesActionableRebaseChild)$'", targetBranch, epicBranch),
		fmt.Sprintf("Assert: %s and %s are ancestors of HEAD, dispatcher tests pass, and the epic can retry close without replaying an already-preserved merge.", targetBranch, epicBranch),
		"Read: pkg/dispatcher/dispatcher.go:ffMergeEpicBranch, pkg/dispatcher/dispatcher_test.go:TestEpicFFMergeFailureCreatesActionableRebaseChild",
		fmt.Sprintf("Constraint: once the -s ours preserve merge lands on %s, do not replay it via a terminal rebase onto the %s tip (e.g. `rebase --onto <epic-tip>` or a plain rebase onto <epic-tip>) — that flattens the preserve merge and drops %s ancestry, failing the Cmd above; if %s advances again, redo the -s ours merge instead.", epicBranch, epicBranch, targetBranch, epicBranch),
	}, " | ")
}

// completeEpicClose FF-merges the epic branch to targetBranch, then closes the
// epic, cancels stale ops agents, logs the event, and escalates to the manager
// if the epic is currently focused. If the FF merge fails a rebase child bead
// is created and the close is skipped.
func (d *Dispatcher) completeEpicClose(ctx context.Context, epicID, workerID, reason, targetBranch string) {
	if err := d.ffMergeEpicBranch(ctx, epicID, workerID, targetBranch); err != nil {
		d.mu.Lock()
		d.epicMergeFailed[epicID] = true
		d.mu.Unlock()
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, epicID,
			"epic ff merge failed", err.Error()), epicID, workerID)
		return
	}

	_ = d.CloseBead(ctx, epicID, reason)

	// Cancel any in-flight ops agents for this epic to prevent stale escalations.
	if n, err := d.ops.CancelForBead(epicID); n > 0 {
		_ = d.logEvent(ctx, "ops_agents_cancelled", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"count":%d,"reason":"epic_completed"}`, n))
		if err != nil {
			_ = d.logEvent(ctx, "ops_cancel_error", "dispatcher", epicID, workerID, err.Error())
		}
	}

	_ = d.logEvent(ctx, "epic_auto_closed", "dispatcher", epicID, workerID, "")

	// Alert the manager if the completed epic is the focused epic.
	d.mu.Lock()
	focused := d.focusedEpic
	d.mu.Unlock()
	if focused == epicID {
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscEpicComplete, epicID,
			"all children completed",
			`Run: oro directive focus "" to clear`), epicID, workerID)
	}

	// Spawn a dream agent to consolidate memories after epic completion.
	d.triggerDream(ctx)
}

// parseAcceptanceCmd extracts the Cmd: value from an acceptance criteria string.
// It supports both pipe-separated inline format ("... | Cmd: go test | ...")
// and line-per-field format. Returns "" if no Cmd: is present.
func parseAcceptanceCmd(ac string) (string, error) {
	if strings.Contains(ac, "\n") {
		for _, line := range strings.Split(ac, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Cmd:") {
				cmd := strings.TrimSpace(strings.TrimPrefix(trimmed, "Cmd:"))
				return cmd, validateAcceptanceCmdQuotes(cmd)
			}
		}
		return "", nil
	}
	parts, err := splitInlineAcceptanceFields(ac)
	if err != nil {
		return "", err
	}
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if strings.HasPrefix(trimmed, "Cmd:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "Cmd:")), nil
		}
	}
	return "", nil
}

func splitInlineAcceptanceFields(ac string) ([]string, error) {
	parts := make([]string, 0, 3)
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(ac); i++ {
		char := ac[i]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == '|' && startsAcceptanceField(ac[i+1:]) {
			parts = append(parts, ac[start:i])
			start = i + 1
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in acceptance command", quote)
	}
	return append(parts, ac[start:]), nil
}

func startsAcceptanceField(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "Test:") ||
		strings.HasPrefix(trimmed, "Cmd:") ||
		strings.HasPrefix(trimmed, "Assert:") ||
		strings.HasPrefix(trimmed, "Read:")
}

func validateAcceptanceCmdQuotes(cmd string) error {
	_, err := splitInlineAcceptanceFields(cmd)
	return err
}

func (d *Dispatcher) parseEpicAcceptanceCmd(
	ctx context.Context,
	eventType, epicID, workerID, acceptanceCriteria string,
) (string, bool) {
	cmd, err := parseAcceptanceCmd(acceptanceCriteria)
	if err != nil {
		_ = d.logEvent(ctx, eventType, "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return "", false
	}
	return cmd, true
}

// handleMergeConflictResult waits for the ops merge-conflict result and acts on it.
func (d *Dispatcher) handleMergeConflictResult(ctx context.Context, beadID, workerID, worktree, epicID, targetBranch string, assignmentID int64, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictResolved:
			_ = d.logEvent(ctx, "merge_conflict_resolved", "ops", beadID, workerID, result.Feedback)
			// Resolution succeeded — retry the merge.
			d.mergeAndComplete(ctx, beadID, workerID, worktree, protocol.BranchPrefix+beadID, epicID, targetBranch, assignmentID)
		default:
			// Resolution failed or unknown verdict — preserve/quarantine and escalate.
			_ = d.logEvent(ctx, "merge_conflict_failed", "ops", beadID, workerID, result.Feedback)
			_ = d.updateBeadStatus(ctx, beadID, "open")
			d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
				BeadID:       beadID,
				AssignmentID: assignmentID,
				WorkerID:     workerID,
				Worktree:     worktree,
				Branch:       protocol.BranchPrefix + beadID,
				Reason:       "merge_conflict_resolution_failed",
				Details:      result.Feedback,
			})
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID,
				"merge conflict resolution failed", result.Feedback), beadID, workerID)
			d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		}
	}
}

// maxQGRetries is the number of quality-gate retry attempts before escalating
