package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"oro/pkg/agentmodel"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	workerstream "oro/pkg/worker"
)

func (d *Dispatcher) markWorkerReviewing(workerID string, assignmentID int64) (worktree, targetBranch string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	w, ok := d.workers[workerID]
	if !ok {
		return "", "", false
	}
	if assignmentID > 0 && (w.assignmentID != assignmentID || w.state != protocol.WorkerBusy) {
		return "", "", false
	}
	w.state = protocol.WorkerReviewing
	return w.worktree, w.targetBranch, true
}

func (d *Dispatcher) handleReadyForReview(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ReadyForReview == nil {
		return
	}
	if err := d.observeStorageController(ctx); err != nil || !d.storageAdmissionAllowed() {
		return
	}
	ready := msg.ReadyForReview
	if !d.acceptReadyEvidence(ctx, workerID, ready) {
		return
	}
	beadID := ready.BeadID
	assignmentID := ready.AssignmentID
	legacyReady := legacyReadyEvidenceIdentity(ready)
	if legacyReady {
		d.touchProgress(workerID)
		d.recordWorkerProgress(ctx, workerID, beadID, "ready_for_review")
		_ = d.logEvent(ctx, "ready_for_review", workerID, beadID, workerID, "")
	}
	worktree, targetBranch, marked := d.markWorkerReviewing(workerID, assignmentID)
	if !marked {
		return
	}
	if !legacyReady {
		d.touchProgress(workerID)
		d.recordWorkerProgress(ctx, workerID, beadID, "ready_for_review")
		_ = d.logEvent(ctx, "ready_for_review", workerID, beadID, workerID, "")
	}

	blocked, err := d.blockReviewForDependency(ctx, workerID, beadID, "ready_for_review")
	if blocked {
		return
	}
	if err != nil {
		return
	}

	if worktree == "" {
		return
	}

	hygiene, err := d.checkPreReviewGitHygiene(ctx, beadID, worktree)
	if err != nil {
		d.handleReviewFailed(ctx, workerID, beadID, ops.Result{Err: err})
		return
	}
	if hygiene.Dirty {
		feedback := hygiene.Feedback()
		payload, marshalErr := json.Marshal(map[string][]string{"files": hygiene.Files})
		if marshalErr != nil {
			payload = []byte(`{"files":[]}`)
		}
		_ = d.logEvent(ctx, "pre_review_git_dirty", "dispatcher", beadID, workerID, string(payload))
		d.sendPreReviewGitDirtyFeedback(ctx, workerID, feedback)
		return
	}
	d.logManagedPreReviewHygieneRecheck(ctx, beadID, workerID, hygiene.IgnoredManagedFiles)

	// Look up bead details for the reviewer
	title, acceptance, _ := d.lookupBeadDetail(ctx, beadID, workerID)

	// Spawn review ops agent
	resultCh := d.ops.Review(ctx, ops.ReviewOpts{
		BeadID:             beadID,
		BeadTitle:          title,
		Worktree:           worktree,
		AcceptanceCriteria: acceptance,
		BaseBranch:         targetBranch,
		ProjectRoot:        worktree, // worktree is a full checkout with CLAUDE.md
	})

	// Handle review result asynchronously
	d.safeGo(func() {
		d.handleReviewResultForAssignment(ctx, workerID, beadID, assignmentID, resultCh)
	})
}

// PreReviewGitHygieneResult describes whether a worker worktree is clean
// enough to enter ops review.
type PreReviewGitHygieneResult struct {
	Dirty               bool
	Files               []string
	IgnoredManagedFiles []string
}

// Feedback returns actionable worker feedback for a dirty pre-review worktree.
func (r PreReviewGitHygieneResult) Feedback() string {
	if len(r.Files) == 0 {
		return "Pre-review git hygiene failed. Remove unrelated edits or stage/commit task files before requesting review again."
	}
	return "Pre-review git hygiene failed. stage/commit task files or remove unrelated edits before requesting review again. Dirty files: " +
		strings.Join(r.Files, ", ")
}

func (d *Dispatcher) checkPreReviewGitHygiene(ctx context.Context, beadID, worktree string) (PreReviewGitHygieneResult, error) {
	if _, statErr := os.Stat(filepath.Join(worktree, ".git")); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return PreReviewGitHygieneResult{}, nil
		}
		return PreReviewGitHygieneResult{}, fmt.Errorf("pre-review git metadata: %w", statErr)
	}

	out, err := (&ExecCommandRunner{Dir: worktree}).Run(ctx, "git", "status", "--porcelain", "--untracked-files=all", "-z")
	if err != nil {
		return PreReviewGitHygieneResult{}, fmt.Errorf("pre-review git status: %w", err)
	}

	entries := parseGitStatusPorcelainZ(out)
	files := make([]string, 0, len(entries))
	ignoredManagedFiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if d.isIgnorableManagedQualityGateStatus(beadID, worktree, entry) {
			ignoredManagedFiles = append(ignoredManagedFiles, entry.Path)
			continue
		}
		files = append(files, entry.Path)
	}
	if len(files) == 0 {
		sort.Strings(ignoredManagedFiles)
		return PreReviewGitHygieneResult{IgnoredManagedFiles: ignoredManagedFiles}, nil
	}
	sort.Strings(files)
	return PreReviewGitHygieneResult{Dirty: true, Files: files}, nil
}

func (d *Dispatcher) logManagedPreReviewHygieneRecheck(
	ctx context.Context,
	beadID, workerID string,
	files []string,
) {
	if len(files) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"source": managedPreReviewHygieneSource(files),
		"files":  files,
	})
	if err != nil {
		payload = []byte(`{"source":"managed_runtime_artifact","files":[]}`)
	}
	_ = d.logEvent(ctx, "pre_review_hygiene_recheck", "dispatcher", beadID, workerID, string(payload))
}

func managedPreReviewHygieneSource(files []string) string {
	capabilityPath := filepath.ToSlash(filepath.Join(protocol.OroDir, "assignment-capability.json"))
	for _, file := range files {
		if file == capabilityPath {
			return "managed_assignment_capability"
		}
	}
	return "managed_runtime_artifact"
}

type gitStatusPorcelainEntry struct {
	Code string
	Path string
}

func parseGitStatusPorcelainZ(out []byte) []gitStatusPorcelainEntry {
	entries := strings.Split(string(out), "\x00")
	parsed := make([]gitStatusPorcelainEntry, 0, len(entries))
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		code := entry[:2]
		path := strings.TrimSpace(entry[3:])
		if path == "" {
			continue
		}
		parsed = append(parsed, gitStatusPorcelainEntry{Code: code, Path: path})
		if entry[0] == 'R' || entry[1] == 'R' {
			i++
		}
	}
	return parsed
}

type managedQualityGateProvider interface {
	ManagedQualityGatePath() string
}

func (d *Dispatcher) isIgnorableManagedQualityGateStatus(beadID, worktree string, entry gitStatusPorcelainEntry) bool {
	if entry.Code == "??" && entry.Path == filepath.ToSlash(filepath.Join(protocol.OroDir, "assignment-capability.json")) {
		return true
	}
	if entry.Code == "??" && (isManagedQualityGateCachePath(beadID, entry.Path)) {
		return true
	}
	if entry.Code != "??" || entry.Path != "quality_gate.sh" {
		return false
	}
	provider, ok := d.worktrees.(managedQualityGateProvider)
	if !ok {
		return false
	}
	managedPath := provider.ManagedQualityGatePath()
	if managedPath == "" {
		return false
	}
	linkPath := filepath.Join(worktree, entry.Path)
	return managedQualityGateSnapshotMatches(linkPath, managedPath)
}

func isManagedQualityGateCachePath(beadID, path string) bool {
	prefixes := []string{
		".tmp-gocache-" + beadID + "/",
		".gocache-" + beadID + "/",
		".golangci-cache-" + beadID + "/",
		".tmp-gocache/",
		".gocache-task/",
		".task-gocache/",
		".golangci-cache/",
		".golangci-lint-cache/",
	}
	if sanitizedBeadID := sanitizedQualityGateCacheBeadID(beadID); sanitizedBeadID != "" {
		prefixes = append(prefixes, ".gocache-"+sanitizedBeadID+"/")
	}
	return hasPathPrefix(path, prefixes)
}

func sanitizedQualityGateCacheBeadID(beadID string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, beadID)
}

func hasPathPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func managedQualityGateSnapshotMatches(linkPath, managedPath string) bool {
	got, err := os.ReadFile(linkPath) //nolint:gosec // path is from git status for the worker worktree.
	if err != nil {
		return false
	}
	want, err := os.ReadFile(managedPath) //nolint:gosec // path is dispatcher-managed project configuration.
	if err != nil {
		return false
	}
	return bytes.Equal(got, want)
}

func (d *Dispatcher) sendPreReviewGitDirtyFeedback(ctx context.Context, workerID, feedback string) {
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerReserved
	}
	snap := d.opusEscalationSnapshotLocked(workerID)
	d.mu.Unlock()

	payload := d.buildAssignPayload(ctx, &snap, 0, feedback, "", snap.execution)

	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.state != protocol.WorkerReserved {
		return
	}
	w.state = protocol.WorkerBusy
	w.lastProgress = d.nowFunc()
	_ = d.sendToWorker(w, protocol.Message{
		Type:   protocol.MsgAssign,
		Assign: payload,
	})
}

// maxReviewRejections is the number of rejection cycles before escalating to
// the Manager instead of re-assigning the bead to the worker.
const maxReviewRejections = 2

// ReviewFailureClass distinguishes genuine review findings from failures in
// the review execution environment.
type ReviewFailureClass string

const (
	// ReviewFailureOrdinary is a normal reviewer rejection that should be sent
	// back to the worker as implementation feedback.
	ReviewFailureOrdinary ReviewFailureClass = "ordinary"
	// ReviewFailureEnvBlocked means the acceptance command passed, but an
	// environment sandbox blocked broader verification.
	ReviewFailureEnvBlocked ReviewFailureClass = "env_blocked"
	// ReviewFailureInfraBlocked means the acceptance command passed, but the
	// review agent or tooling failed before producing useful implementation feedback.
	ReviewFailureInfraBlocked ReviewFailureClass = "infra_blocked"
	// ReviewFailureRateLimited means the reviewer exhausted its five-hour usage
	// window and the bead must wait before another review attempt.
	ReviewFailureRateLimited ReviewFailureClass = "rate_limited"
)

// handleReviewResult waits for the ops review result and acts on it.
func (d *Dispatcher) handleReviewResult(ctx context.Context, workerID, beadID string, resultCh <-chan ops.Result) {
	d.mu.Lock()
	assignmentID := int64(0)
	if w, ok := d.workers[workerID]; ok {
		assignmentID = w.assignmentID
	}
	d.mu.Unlock()
	d.handleReviewResultForAssignment(ctx, workerID, beadID, assignmentID, resultCh)
}

func (d *Dispatcher) handleReviewResultForAssignment(
	ctx context.Context,
	workerID, beadID string,
	assignmentID int64,
	resultCh <-chan ops.Result,
) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictApproved:
			d.handleReviewApproved(ctx, workerID, beadID, result)
		case ops.VerdictRejected:
			switch classifyReviewFailure(result) {
			case ReviewFailureEnvBlocked, ReviewFailureInfraBlocked, ReviewFailureRateLimited:
				d.handleReviewBlockedForAssignment(ctx, workerID, beadID, assignmentID, result)
				return
			}
			d.handleReviewRejection(ctx, workerID, beadID, result.Feedback)
		default:
			switch classifyReviewFailure(result) {
			case ReviewFailureInfraBlocked, ReviewFailureRateLimited:
				d.handleReviewBlockedForAssignment(ctx, workerID, beadID, assignmentID, result)
				return
			}
			d.handleReviewFailed(ctx, workerID, beadID, result)
		}
	}
}

func (d *Dispatcher) handleReviewApproved(ctx context.Context, workerID, beadID string, result ops.Result) {
	// Fail closed: a nonzero subprocess exit (Err != nil) with "APPROVED"
	// in stdout signals a model/runtime error, not a genuine review decision.
	// Trust the exit status over the text to prevent false approvals.
	if result.Err != nil {
		detail := result.Err.Error()
		_ = d.logEvent(ctx, "review_error", "ops", beadID, workerID, detail)
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "review error", detail), beadID, workerID)
		d.clearBeadTracking(beadID)
		return
	}
	blocked, err := d.blockReviewForDependency(ctx, workerID, beadID, "review_approved")
	if blocked {
		return
	}
	if err != nil {
		return
	}
	_ = d.logEvent(ctx, "review_approved", "ops", beadID, workerID, result.Feedback)
	d.clearRejectionCount(beadID)
	d.appendExtractedReviewPatterns(ctx, beadID, workerID, result.Feedback)
	d.sendReviewApproved(workerID, result.Feedback)
}

// blockReviewForDependency prevents a review lifecycle transition when the
// parent bead has gained an unresolved blocking dependency. It reserves the
// reviewing assignment while inspecting storage so a terminal verdict cannot
// deliver approval between the dependency check and terminal cleanup.
func (d *Dispatcher) blockReviewForDependency(ctx context.Context, workerID, beadID, phase string) (bool, error) {
	assignmentID, claimed := d.claimReviewDependencyCheck(workerID, beadID)
	if !claimed {
		return true, nil
	}

	blockerID, err := d.unresolvedBlockingDependency(ctx, beadID)
	if err != nil {
		d.finishDependencyBlockedReview(ctx, workerID, beadID, assignmentID, phase, "", err)
		return true, err
	}
	if blockerID == "" {
		d.restoreReviewDependencyCheck(workerID, beadID, assignmentID)
		return false, nil
	}

	d.finishDependencyBlockedReview(ctx, workerID, beadID, assignmentID, phase, blockerID, nil)
	return true, nil
}

func (d *Dispatcher) claimReviewDependencyCheck(workerID, beadID string) (int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID || w.state != protocol.WorkerReviewing {
		return 0, false
	}
	w.state = protocol.WorkerReserved
	return w.assignmentID, true
}

func (d *Dispatcher) restoreReviewDependencyCheck(workerID, beadID string, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID || w.assignmentID != assignmentID || w.state != protocol.WorkerReserved {
		return
	}
	w.state = protocol.WorkerReviewing
}

func (d *Dispatcher) unresolvedBlockingDependency(ctx context.Context, beadID string) (string, error) {
	bead, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return "", fmt.Errorf("show review bead %s: %w", beadID, err)
	}
	if bead == nil {
		return "", fmt.Errorf("show review bead %s: missing", beadID)
	}
	for _, dep := range bead.Dependencies {
		if dep.Type != "blocks" && dep.Type != "conditional-blocks" {
			continue
		}
		dependency, showErr := d.beads.Show(ctx, dep.DependsOnID)
		if showErr != nil {
			return "", fmt.Errorf("show blocking dependency %s: %w", dep.DependsOnID, showErr)
		}
		if dependency != nil && dependency.Status != "closed" {
			return dep.DependsOnID, nil
		}
	}
	return "", nil
}

func (d *Dispatcher) finishDependencyBlockedReview(
	ctx context.Context,
	workerID, beadID string,
	assignmentID int64,
	phase, blockerID string,
	lookupErr error,
) {
	detail := fmt.Sprintf(`{"phase":%q,"blocker_id":%q}`, phase, blockerID)
	eventType := "review_blocked_by_dependency"
	if lookupErr != nil {
		eventType = "review_dependency_lookup_failed"
		detail = fmt.Sprintf(`{"phase":%q,"error":%q}`, phase, lookupErr.Error())
	}
	_ = d.logEvent(ctx, eventType, "dispatcher", beadID, workerID, detail)
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, eventType+"_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	}
	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, eventType+"_reopen_failed", "dispatcher", beadID, workerID, err.Error())
		}
	}
	d.clearBeadTracking(beadID)
	d.releaseBlockedReviewAssignment(workerID, beadID, assignmentID)
}

func (d *Dispatcher) appendExtractedReviewPatterns(ctx context.Context, beadID, workerID, feedback string) {
	patterns := ops.ExtractPatterns(feedback)
	if len(patterns) == 0 {
		return
	}
	if err := d.appendReviewPatternCandidates(ctx, beadID, workerID, patterns); err != nil {
		_ = d.logEvent(ctx, "append_review_pattern_candidates_failed", "ops", beadID, workerID, err.Error())
	}
}

func (d *Dispatcher) sendReviewApproved(workerID, feedback string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok {
		return
	}
	_ = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgReviewResult,
		ReviewResult: &protocol.ReviewResultPayload{
			Verdict:  "approved",
			Feedback: feedback,
		},
	})
}

func (d *Dispatcher) handleReviewFailed(ctx context.Context, workerID, beadID string, result ops.Result) {
	detail := reviewFailureDetail(result)
	_ = d.logEvent(ctx, "review_failed", "ops", beadID, workerID, detail)
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "review failed", detail), beadID, workerID)
	if result.Err == nil && d.reviewingWorkerMatches(workerID, beadID) {
		d.handleReviewRejection(ctx, workerID, beadID, "Review failed: "+detail)
		return
	}
	d.clearBeadTracking(beadID)
}

func reviewFailureDetail(result ops.Result) string {
	if result.Feedback != "" {
		return boundedReviewFailureDetail(result.Feedback)
	}
	if result.Err != nil {
		return result.Err.Error()
	}
	return "review completed without a machine-readable verdict"
}

const maxReviewFailureDetailBytes = 2 * 1024

func boundedReviewFailureDetail(detail string) string {
	if len(detail) <= maxReviewFailureDetailBytes {
		return detail
	}
	return detail[len(detail)-maxReviewFailureDetailBytes:]
}

func classifyReviewFailure(result ops.Result) ReviewFailureClass {
	raw := reviewFailureDetail(result)
	detail := strings.ToLower(raw)
	if result.Err != nil && reviewStartupHookFailed(raw) {
		return ReviewFailureInfraBlocked
	}
	if reviewRateLimited(raw) {
		return ReviewFailureRateLimited
	}
	if !strings.Contains(detail, "acceptance command passed") {
		return ReviewFailureOrdinary
	}
	if reviewEnvBlocked(detail) {
		return ReviewFailureEnvBlocked
	}
	if reviewInfraBlocked(detail) {
		return ReviewFailureInfraBlocked
	}
	return ReviewFailureOrdinary
}

func reviewRateLimited(detail string) bool {
	for _, line := range strings.Split(detail, "\n") {
		var event struct {
			RateLimitType string `json:"rateLimitType"`
			OverageStatus string `json:"overageStatus"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &event); err != nil {
			continue
		}
		if strings.EqualFold(event.RateLimitType, "five_hour") && strings.EqualFold(event.OverageStatus, "rejected") {
			return true
		}
	}
	return false
}

func reviewEnvBlocked(detail string) bool {
	denied := containsAny(detail, "permission denied", "operation not permitted", "read-only file system")
	if strings.Contains(detail, "listen unix") && strings.Contains(detail, "bind: operation not permitted") {
		return true
	}
	if denied && containsAny(detail, "uv cache", ".cache/uv") {
		return true
	}
	if denied && containsAny(detail, "home directory", "$home", "user home") {
		return true
	}
	return false
}

func reviewInfraBlocked(detail string) bool {
	return containsAny(detail,
		"taskoutput",
		"tail -f",
		"context deadline exceeded",
		"timed out waiting for review",
	)
}

func reviewStartupHookFailed(raw string) bool {
	detail := strings.ToLower(raw)
	return strings.Contains(detail, `"subtype":"hook_started"`) &&
		strings.Contains(detail, "sessionstart:startup") &&
		!reviewStreamHadAgentActivity(raw)
}

func reviewStreamHadAgentActivity(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		switch workerstream.ParseStreamEvent([]byte(line)).Kind {
		case workerstream.ActivityToolUse, workerstream.ActivityTextDelta, workerstream.ActivityResult:
			return true
		}
	}
	return false
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) reviewingWorkerMatches(workerID, beadID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	return ok && w != nil && w.beadID == beadID && w.state == protocol.WorkerReviewing
}

func (d *Dispatcher) handleReviewBlocked(ctx context.Context, workerID, beadID string, result ops.Result) {
	d.mu.Lock()
	assignmentID := int64(0)
	if w, ok := d.workers[workerID]; ok {
		assignmentID = w.assignmentID
	}
	d.mu.Unlock()
	d.handleReviewBlockedForAssignment(ctx, workerID, beadID, assignmentID, result)
}

func (d *Dispatcher) handleReviewBlockedForAssignment(
	ctx context.Context,
	workerID, beadID string,
	expectedAssignmentID int64,
	result ops.Result,
) {
	assignmentID, matchesReviewingWorker := d.claimBlockedReviewAssignment(
		workerID, beadID, expectedAssignmentID,
	)

	class := classifyReviewFailure(result)
	eventType := "review_env_blocked"
	reason := "review environment blocked"
	switch class {
	case ReviewFailureInfraBlocked:
		eventType = "review_infra_blocked"
		reason = "review infrastructure blocked"
	case ReviewFailureRateLimited:
		eventType = "review_rate_limited"
		reason = "reviewer rate limited"
	}
	detail := reviewFailureDetail(result)
	if !matchesReviewingWorker {
		_ = d.logEvent(ctx, eventType+"_stale", "ops", beadID, workerID, detail)
		return
	}
	_ = d.logEvent(ctx, eventType, "ops", beadID, workerID, detail)

	preserveBlockedCount, reviewEscalated, blockedCount := d.processBlockedReviewRetry(
		ctx, workerID, beadID, eventType, class,
	)
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, eventType+"_assignment_cleanup_failed", "ops", beadID, workerID, err.Error())
	}
	if preserveBlockedCount {
		d.clearBeadTrackingPreservingBlockedReviewCount(beadID)
	} else {
		d.clearBeadTracking(beadID)
	}

	d.releaseBlockedReviewAssignment(workerID, beadID, assignmentID)

	if reviewEscalated {
		_ = d.logEvent(ctx, "review_escalated", "ops", beadID, workerID,
			fmt.Sprintf(`{"rejections":%d,"feedback":%q}`, blockedCount, detail))
	}
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, reason, detail), beadID, workerID)
	if reviewEscalated {
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("review blocked %d times", blockedCount), detail), beadID, workerID)
	}
}

func (d *Dispatcher) claimBlockedReviewAssignment(workerID, beadID string, expectedAssignmentID int64) (int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID || w.state != protocol.WorkerReviewing || w.assignmentID != expectedAssignmentID {
		return 0, false
	}
	w.state = protocol.WorkerReserved
	return w.assignmentID, true
}

func (d *Dispatcher) releaseBlockedReviewAssignment(workerID, beadID string, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID || w.state != protocol.WorkerReserved || w.assignmentID != assignmentID {
		return
	}
	w.state = protocol.WorkerIdle
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	w.worktree = ""
	w.model = ""
}

func (d *Dispatcher) processBlockedReviewRetry(
	ctx context.Context,
	workerID, beadID, eventType string,
	class ReviewFailureClass,
) (preserveCount, escalated bool, count int) {
	if class != ReviewFailureEnvBlocked && class != ReviewFailureInfraBlocked {
		d.reopenBlockedReview(ctx, beadID, workerID, eventType, class)
		return false, false, 0
	}

	d.mu.Lock()
	d.reviewBlockedCounts[beadID]++
	count = d.reviewBlockedCounts[beadID]
	d.mu.Unlock()
	if count <= maxReviewRejections {
		d.reopenBlockedReview(ctx, beadID, workerID, eventType, class)
		return true, false, count
	}
	return false, true, count
}

// reserveReviewRetryAttempt reserves an active worker when present, then checks
// whether the bead became blocked before retrying a rejected review. Dependency
// lookup failures fail closed so uncertain readiness cannot consume capacity.
func (d *Dispatcher) reserveReviewRetryAttempt(ctx context.Context, workerID, beadID, feedback string) (bool, error) {
	d.mu.Lock()
	w, ok := d.workers[workerID]
	assignmentID := int64(0)
	if ok {
		assignmentID = w.assignmentID
		w.state = protocol.WorkerReserved
	}
	d.mu.Unlock()

	blockerID, lookupErr := d.qgRetryBlockingDependency(ctx, beadID)
	if blockerID == "" && lookupErr == nil {
		return true, nil
	}
	d.storeRejectionFeedback(ctx, beadID, feedback)
	if lookupErr != nil {
		_ = d.logEvent(ctx, "review_retry_dependency_lookup_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, lookupErr.Error()))
	}
	if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
		_ = d.logEvent(ctx, "review_retry_blocked_status_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "review_retry_blocked_assignment_cleanup_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	d.clearBeadTracking(beadID)
	_ = d.logEvent(ctx, "review_retry_blocked_by_dependency", workerID, beadID, workerID,
		fmt.Sprintf(`{"blocker_id":%q,"lookup_failed":%t}`, blockerID, lookupErr != nil))
	return false, lookupErr
}

// handleReviewRejection processes a rejected review verdict: increments the
// rejection counter, escalates if the cap is reached, or re-assigns the bead
// to the worker with reviewer feedback using the two-phase reservation pattern.
func (d *Dispatcher) handleReviewRejection(ctx context.Context, workerID, beadID, feedback string) {
	_ = d.logEvent(ctx, "review_rejected", "ops", beadID, workerID, feedback)

	reserved, err := d.reserveReviewRetryAttempt(ctx, workerID, beadID, feedback)
	if err != nil || !reserved {
		return
	}

	// Increment rejection counter after the dependency-safe reservation.
	d.mu.Lock()
	d.rejectionCounts[beadID]++
	count := d.rejectionCounts[beadID]

	if count > maxReviewRejections {
		// Reset worker to Idle so it can receive new work instead of
		// remaining stuck in WorkerReviewing with a stale beadID.
		if w, wOK := d.workers[workerID]; wOK {
			w.state = protocol.WorkerIdle
			w.beadID = ""
			w.epicID = ""
			w.isEpicDecomp = false
			w.worktree = ""
			w.model = ""
		}
		d.mu.Unlock()

		// Clear tracking BEFORE logging the escalation event so that any
		// observer waiting on the event (e.g. tests) sees clean tracking
		// maps once the event is visible.  The previous ordering left a
		// race window: the event was logged, then escalate() ran (which
		// can block on tmux), and only then was tracking cleared.
		d.clearBeadTracking(beadID)

		_ = d.logEvent(ctx, "review_escalated", "ops", beadID, workerID,
			fmt.Sprintf(`{"rejections":%d,"feedback":%q}`, count, feedback))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("review rejected %d times", count), feedback), beadID, workerID)

		// Kill the worker subprocess so a fresh process can take over.
		if d.procMgr != nil {
			_ = d.procMgr.Kill(workerID)
		}
		return
	}

	d.mu.Unlock()

	// Capture snapshot for buildAssignPayload (I/O runs outside lock).
	// Set model=Opus on the snapshot only — w.model is NOT escalated on the live
	// worker for review rejection (preserving prior behaviour).
	d.mu.Lock()
	snap := d.opusEscalationSnapshotLocked(workerID)
	d.mu.Unlock()

	var payload *protocol.AssignPayload
	d.withReservation(workerID,
		// I/O function: store rejection feedback and build full payload outside lock.
		// Persisting the feedback keeps it retrievable in subsequent retry cycles
		// without consulting general memory context.
		func() string {
			memCtx := d.buildRejectionMemoryContext(ctx, beadID, feedback)
			payload = d.buildAssignPayload(ctx, &snap, count, feedback, memCtx, snap.execution)
			return memCtx
		},
		// Assign function: update state and send message under lock.
		func(w *trackedWorker, _ string) bool {
			w.state = protocol.WorkerBusy
			w.lastProgress = d.nowFunc()
			// payload.Model is already "opus" from the snapshot; w.model is not
			// escalated here (preserving prior behaviour).
			_ = d.sendToWorker(w, protocol.Message{
				Type:   protocol.MsgAssign,
				Assign: payload,
			})
			return true
		},
	)
}

func (d *Dispatcher) opusEscalationSnapshotLocked(workerID string) trackedWorker {
	var snap trackedWorker
	if w, ok := d.workers[workerID]; ok {
		snap = *w
	}
	snap.runtime, snap.model, snap.reasoning = agentmodel.ResolveForRole("worker_escalation")
	return snap
}

// appendReviewPatterns appends captured anti-patterns to assets/review-patterns.md
// in the main repository root. Returns error if directory is unwritable or file cannot be opened.
func (d *Dispatcher) appendReviewPatterns(ctx context.Context, beadID, workerID string, patterns []string) error {
	root := d.repoRoot
	patternsFile := filepath.Join(root, "assets", "review-patterns.md")

	// Ensure assets/ directory exists
	assetsDir := filepath.Dir(patternsFile)
	//nolint:gosec // directory permissions for project config
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, fmt.Sprintf("mkdir failed: %v", err))
		return fmt.Errorf("create assets directory: %w", err)
	}

	//nolint:gosec // patternsFile is derived from trusted beadsDir
	f, err := os.OpenFile(patternsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, fmt.Sprintf("open file failed: %v", err))
		return fmt.Errorf("open review-patterns.md: %w", err)
	}
	defer f.Close()

	for _, p := range patterns {
		if _, err := f.WriteString(p + "\n"); err != nil {
			_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, fmt.Sprintf("write failed: %v", err))
			return fmt.Errorf("write pattern: %w", err)
		}
	}
	return nil
}

// appendReviewPatternCandidates writes one structured record per candidate to
// the ReviewPatternCandidates inbox path. Each record is written with a single
// WriteString call so concurrent appends from parallel workers produce complete,
// non-interleaved records. Parent directory is created if absent.
func (d *Dispatcher) appendReviewPatternCandidates(ctx context.Context, beadID, workerID string, candidates []string) error {
	path := d.cfg.ReviewPatternCandidates
	if path == "" {
		return nil
	}

	//nolint:gosec // path is derived from trusted project config
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = d.logEvent(ctx, "append_review_pattern_candidates_failed", "ops", beadID, workerID, fmt.Sprintf("mkdir failed: %v", err))
		return fmt.Errorf("create candidates directory: %w", err)
	}

	//nolint:gosec // path is derived from trusted project config
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		_ = d.logEvent(ctx, "append_review_pattern_candidates_failed", "ops", beadID, workerID, fmt.Sprintf("open file failed: %v", err))
		return fmt.Errorf("open candidates file: %w", err)
	}
	defer f.Close()

	now := d.nowFunc().UTC().Format(time.RFC3339)
	for _, c := range candidates {
		record := fmt.Sprintf("---\nbead: %s\nworker: %s\ncaptured_at: %s\n\n%s\n\n", beadID, workerID, now, c)
		if _, err := f.WriteString(record); err != nil {
			_ = d.logEvent(ctx, "append_review_pattern_candidates_failed", "ops", beadID, workerID, fmt.Sprintf("write failed: %v", err))
			return fmt.Errorf("write candidate record: %w", err)
		}
	}
	return nil
}

// clearRejectionCount, clearHandoffCount, clearBeadTracking, pruneStaleTracking → bead_tracker.go

// validateReconnectBead checks if a bead is valid for reconnection.
// Returns true if bead is open and can be reconnected, false otherwise.
// oro-3xdf: Rejects closed or missing beads to prevent stuck workers.

func (d *Dispatcher) reopenBlockedReview(
	ctx context.Context,
	beadID, workerID, eventType string,
	class ReviewFailureClass,
) {
	if !d.shouldReopenBead(ctx, beadID) {
		return
	}
	if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
		_ = d.logEvent(ctx, eventType+"_reopen_failed", "ops", beadID, workerID, err.Error())
		return
	}
	if class != ReviewFailureRateLimited {
		return
	}

	until := d.nowFunc().UTC().Add(reviewRateLimitDeferDuration).Format(time.RFC3339)
	if err := d.beads.Defer(ctx, beadID, until); err != nil {
		_ = d.logEvent(ctx, eventType+"_defer_failed", "ops", beadID, workerID, err.Error())
		return
	}
	_ = d.logEvent(ctx, eventType+"_deferred", "ops", beadID, workerID, until)
}
