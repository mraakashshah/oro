package dispatcher

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// maxHandoffsBeforeDiagnosis is the number of ralph handoffs for the same bead
// before the dispatcher spawns a diagnosis agent instead of respawning.
const maxHandoffsBeforeDiagnosis = 2

func (d *Dispatcher) handleHandoff(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Handoff == nil {
		return
	}
	beadID := msg.Handoff.BeadID
	if strings.TrimSpace(beadID) == "" {
		_ = d.logEvent(ctx, "handoff_rejected", workerID, beadID, workerID,
			`{"reason":"empty_bead_id"}`)
		return
	}

	if d.suppressScaleDownHandoff(ctx, workerID, beadID) {
		d.persistHandoffContext(ctx, msg.Handoff)
		return
	}

	_ = d.logEvent(ctx, "handoff", workerID, beadID, workerID, "")

	// Persist learnings and decisions from the handoff payload as memories.
	d.persistHandoffContext(ctx, msg.Handoff)

	// A HANDOFF is the worker's acknowledgement of PREEMPT. Release the old
	// durable assignment before ordinary handoff logic can offer it to another
	// worker. The assigningBeads reservation held by detachPreemptedHandoff
	// keeps normal scheduling out until reconciliation reaches a terminal state.
	if assignmentID, worktree, ok := d.detachPreemptedHandoff(workerID, beadID); ok {
		d.reconcilePreemptedDisconnect(workerID, beadID, assignmentID, worktree)
		return
	}

	// Track handoff count per bead.
	handoffCount, assignmentID := d.incrementHandoffCount(workerID, beadID)
	d.persistBeadCount(ctx, assignmentID, beadID, "handoff_count", handoffCount)

	// Send SHUTDOWN to the old worker and capture worktree+runtime+model+epic context for respawn.
	snap := d.shutdownWorkerForHandoff(workerID)

	if snap.worktree == "" {
		return
	}

	// On 2nd+ handoff for the same bead, spawn diagnosis agent instead of respawning.
	if handoffCount >= maxHandoffsBeforeDiagnosis {
		d.handleHandoffExhaustion(ctx, beadID, workerID, handoffCount, snap.worktree, msg)
		return
	}

	// Fetch bead details to get title and labels for memory search on respawn.
	var title string
	var labels []string
	if detail, err := d.beads.Show(ctx, beadID); err == nil && detail != nil {
		title = detail.Title
		labels = detail.Labels
	}

	d.respawnWorker(ctx, beadID, snap, title, labels)
}

func (d *Dispatcher) incrementHandoffCount(workerID, beadID string) (handoffCount int, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handoffCounts[beadID]++
	return d.handoffCounts[beadID], d.assignmentIDLocked(workerID, beadID)
}

func (d *Dispatcher) detachPreemptedHandoff(workerID, beadID string) (assignmentID int64, worktree string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, exists := d.workers[workerID]
	if !exists || w == nil || w.state != protocol.WorkerPreempting || w.beadID != beadID {
		return 0, "", false
	}

	assignmentID = w.assignmentID
	if assignmentID <= 0 {
		assignmentID = w.execution.AssignmentID
	}
	worktree = w.worktree
	d.assigningBeads[beadID] = true
	_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
	w.state = protocol.WorkerShuttingDown
	w.assignmentID = 0
	w.execution = WorkerExecutionContext{}
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	return assignmentID, worktree, true
}

func (d *Dispatcher) shutdownWorkerForHandoff(workerID string) workerAssignmentSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok {
		return workerAssignmentSnapshot{}
	}
	snap := workerAssignmentSnapshot{
		execution:     w.execution,
		worktree:      w.worktree,
		runtime:       w.runtime,
		model:         w.model,
		epicID:        w.epicID,
		baseBranch:    w.baseBranch,
		targetBranch:  w.targetBranch,
		qgEvidenceDir: w.qgEvidenceDir,
		targetSHA:     w.targetSHA,
	}
	_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
	w.state = protocol.WorkerShuttingDown
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	return snap
}

func (d *Dispatcher) suppressScaleDownHandoff(ctx context.Context, workerID, beadID string) bool {
	d.mu.Lock()
	w, ok := d.workers[workerID]
	suppress := ok && w != nil && w.shutdownReason == shutdownReasonScaleDown
	d.mu.Unlock()
	if !suppress {
		return false
	}
	_ = d.logEvent(ctx, "handoff_suppressed_scale_down", workerID, beadID, workerID,
		`{"reason":"scale_down"}`)
	return true
}

// handleHandoffExhaustion spawns a diagnosis agent and creates a continuation bead
// when a bead exhausts its handoff limit.
func (d *Dispatcher) handleHandoffExhaustion(ctx context.Context, beadID, workerID string, handoffCount int, worktree string, msg protocol.Message) {
	_ = d.logEvent(ctx, "diagnosis_spawned", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"handoff_count":%d}`, handoffCount))
	resultCh := d.ops.Diagnose(ctx, ops.DiagOpts{
		BeadID:   beadID,
		Worktree: worktree,
		Symptom:  fmt.Sprintf("worker stuck after %d ralph handoffs", handoffCount),
	})
	d.safeGo(func() { d.handleDiagnosisResult(ctx, beadID, workerID, resultCh) })

	// Fetch parent bead details to inherit AC and title.
	var parentTitle, parentAC, parentTier string
	if detail, showErr := d.beads.Show(ctx, beadID); showErr == nil && detail != nil {
		parentTitle = detail.Title
		parentAC = detail.AcceptanceCriteria
		parentTier = string(detail.Tier)
	}

	// Create a continuation bead to capture remaining work from the exhausted handoff.
	contTitle := fmt.Sprintf("Continue: %s (handoff exhausted)", beadID)
	contDesc := fmt.Sprintf("Handoff exhausted after %d handoffs for %s (%s).\n\nContext from last handoff:\n%s",
		handoffCount, beadID, parentTitle, msg.Handoff.ContextSummary)
	created, createErr := d.beads.Create(ctx, beadstore.CreateParams{
		Title:              contTitle,
		Type:               "task",
		Priority:           1,
		Description:        contDesc,
		ParentID:           beadID,
		Tier:               parentTier,
		AcceptanceCriteria: parentAC,
	})
	switch {
	case createErr != nil:
		_ = d.logEvent(ctx, "continuation_bead_create_failed", "dispatcher", beadID, workerID, createErr.Error())
	case created == nil:
		_ = d.logEvent(ctx, "continuation_bead_create_failed", "dispatcher", beadID, workerID, "bead store returned nil continuation bead")
	default:
		_ = d.logEvent(ctx, "continuation_bead_created", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"new_bead_id":%q}`, created.ID))
	}
}

// respawnWorker stores a pending handoff and spawns a fresh worker process.
func (d *Dispatcher) respawnWorker(ctx context.Context, beadID string, snap workerAssignmentSnapshot, title string, labels []string) {
	assignmentID := snap.execution.AssignmentID
	if assignmentID <= 0 {
		assignmentID = d.activeAssignmentIDForBead(ctx, beadID)
		snap.execution = workerExecutionContext(assignmentID, false, filepath.Base(d.cfg.RepoRoot))
	}
	newID := ""
	if d.procMgr != nil {
		newID = fmt.Sprintf("worker-handoff-%d", d.nowFunc().UnixNano())
	}
	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		assignmentID:  assignmentID,
		execution:     snap.execution,
		beadID:        beadID,
		epicID:        snap.epicID,
		worktree:      snap.worktree,
		baseBranch:    snap.baseBranch,
		targetBranch:  snap.targetBranch,
		qgEvidenceDir: snap.qgEvidenceDir,
		targetSHA:     snap.targetSHA,
		runtime:       snap.runtime,
		model:         snap.model,
		title:         title,
		labels:        labels,
	}
	if newID != "" && d.cfg.MaxWorkers > 0 && d.liveWorkerCountLocked() >= d.cfg.MaxWorkers {
		newID = ""
	}
	if newID != "" {
		d.pendingManagedIDs[newID] = true
		d.pendingManagedSince[newID] = d.nowFunc()
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "handoff_pending", "dispatcher", beadID, "", snap.worktree)
	d.assignPendingHandoffsToIdleWorkers()
	if newID != "" {
		d.mu.Lock()
		_, stillPending := d.pendingHandoffs[beadID]
		if !stillPending {
			delete(d.pendingManagedIDs, newID)
			delete(d.pendingManagedSince, newID)
			newID = ""
		}
		d.mu.Unlock()
	}

	if d.procMgr != nil && newID != "" {
		if _, err := d.procMgr.Spawn(newID); err != nil {
			d.mu.Lock()
			delete(d.pendingManagedIDs, newID)
			delete(d.pendingManagedSince, newID)
			d.mu.Unlock()
			_ = d.logEvent(ctx, "handoff_spawn_failed", "dispatcher", beadID, newID, err.Error())
		} else {
			_ = d.logEvent(ctx, "handoff_spawned", "dispatcher", beadID, newID, snap.worktree)
		}
	}
}

// handleDiagnosisResult waits for the ops diagnosis result. If diagnosis
// succeeds (non-empty feedback, no error), it logs the result. If diagnosis
// fails or is inconclusive, it escalates to the Manager.
func (d *Dispatcher) handleDiagnosisResult(ctx context.Context, beadID, workerID string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		if result.Err != nil {
			// Diagnosis failed — escalate to manager.
			_ = d.logEvent(ctx, "diagnosis_escalated", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, result.Err.Error()))
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
				"diagnosis failed", result.Err.Error()), beadID, workerID)
			d.clearBeadTracking(beadID)
			return
		}

		// Diagnosis succeeded — log feedback and escalate with diagnosis context.
		_ = d.logEvent(ctx, "diagnosis_complete", "dispatcher", beadID, workerID, result.Feedback)
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"diagnosis complete", result.Feedback), beadID, workerID)
		d.clearBeadTracking(beadID)
	}
}

// persistHandoffContext stores handoff context for cross-session retrieval.
func (d *Dispatcher) persistHandoffContext(ctx context.Context, h *protocol.HandoffPayload) {
	if d.cardStore != nil && d.memoryServices.HandoffInserter != nil {
		sink := d.memoryServices.HandoffInserter(d.cardStore)
		d.persistHandoffContextToCards(ctx, h, sink)
		return
	}
	if d.memories == nil {
		return
	}

	for _, learning := range h.Learnings {
		_, _ = d.memories.Insert(ctx, protocol.MemoryInsertParams{
			Content:       learning,
			Type:          "lesson",
			Source:        "self_report",
			BeadID:        h.BeadID,
			WorkerID:      h.WorkerID,
			Confidence:    0.8,
			FilesModified: h.FilesModified,
		})
	}

	for _, decision := range h.Decisions {
		_, _ = d.memories.Insert(ctx, protocol.MemoryInsertParams{
			Content:       decision,
			Type:          "decision",
			Source:        "self_report",
			BeadID:        h.BeadID,
			WorkerID:      h.WorkerID,
			Confidence:    0.8,
			FilesModified: h.FilesModified,
		})
	}

	// Persist structured session summary as type=summary for bead continuity.
	if h.Summary != nil {
		_, _ = d.memories.Insert(ctx, protocol.MemoryInsertParams{
			Content:    h.Summary.FormatContent(),
			Type:       "summary",
			Source:     "self_report",
			BeadID:     h.BeadID,
			WorkerID:   h.WorkerID,
			Confidence: 0.9,
		})
	}
}

func (d *Dispatcher) persistHandoffContextToCards(ctx context.Context, h *protocol.HandoffPayload, sink LearningSink) {
	if sink == nil {
		return
	}
	for _, learning := range h.Learnings {
		_, _ = sink.AppendLearningPending(ctx, h.BeadID, handoffCardCandidate(protocol.MemoryInsertParams{
			Content:       learning,
			Type:          "lesson",
			Source:        "self_report",
			BeadID:        h.BeadID,
			WorkerID:      h.WorkerID,
			Confidence:    0.8,
			FilesModified: h.FilesModified,
		}))
	}
	for _, decision := range h.Decisions {
		_, _ = sink.AppendLearningPending(ctx, h.BeadID, handoffCardCandidate(protocol.MemoryInsertParams{
			Content:       decision,
			Type:          "decision",
			Source:        "self_report",
			BeadID:        h.BeadID,
			WorkerID:      h.WorkerID,
			Confidence:    0.8,
			FilesModified: h.FilesModified,
		}))
	}
}

func handoffCardCandidate(params protocol.MemoryInsertParams) cards.CardCandidate {
	cardType := string(cards.CardTypePattern)
	if params.Type == string(cards.CardTypeDecision) {
		cardType = string(cards.CardTypeDecision)
	}
	title := truncateHandoffCandidate(params.Content, 200)
	tags := append([]string{"source:" + params.Source}, params.Tags...)
	if params.WorkerID != "" {
		tags = append(tags, "worker:"+params.WorkerID)
	}
	return cards.CardCandidate{
		Type:        cardType,
		Title:       title,
		BodySummary: title,
		BodyFull:    params.Content,
		Confidence:  params.Confidence,
		Evidence:    params.FilesModified,
		Tags:        tags,
	}
}

func truncateHandoffCandidate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return strings.TrimSpace(s[:limit])
}

// markWorkerReviewing flips the worker to Reviewing and returns the assignment
// details the unlocked remainder needs. A missing worker yields zero values, which
// the caller treats as "no worktree" and returns. Extracted from
// handleReadyForReview for funlen; behaviour is unchanged.
