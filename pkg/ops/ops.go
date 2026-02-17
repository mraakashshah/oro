// Package ops implements the ops agent spawner — a Dispatcher component that
// spawns short-lived claude -p processes for operational tasks such as code
// review, merge conflict resolution, and crash/stuck diagnosis.
package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// --- Process abstraction ---

// Process represents a running subprocess. Same interface as the worker
// package for testability.
type Process interface {
	Wait() error
	Kill() error
	Output() (string, error) // read stdout after completion
}

// BatchSpawner creates new claude -p processes.
type BatchSpawner interface {
	Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, error)
}

// --- Ops types and verdicts ---

// Type identifies the kind of operational task.
type Type string

// Known ops task types.
const (
	OpsReview     Type = "review"
	OpsMerge      Type = "merge_conflict"
	OpsDiagnosis  Type = "diagnosis"
	OpsEscalation Type = "escalation"
)

// Model returns the preferred Claude model for this ops type.
func (t Type) Model() string {
	switch t {
	case OpsMerge, OpsDiagnosis:
		return "claude-opus-4-6" // judgment-heavy
	case OpsReview:
		return "claude-opus-4-6" // full code review requires judgment
	case OpsEscalation:
		return "claude-sonnet-4-5-20250929" // one-shot triage is fast, not judgment-heavy
	default:
		return "claude-sonnet-4-5-20250929"
	}
}

// Verdict is the outcome of an ops agent run.
type Verdict string

// Known verdict values from ops agents.
const (
	VerdictApproved Verdict = "approved"
	VerdictRejected Verdict = "rejected"
	VerdictResolved Verdict = "resolved"
	VerdictFailed   Verdict = "failed"
)

// Result is the output of an ops agent.
type Result struct {
	Type     Type
	BeadID   string
	Verdict  Verdict
	Feedback string // reviewer feedback, resolution description, or diagnosis
	Err      error
}

// --- Agent ---

// Agent tracks a single running ops subprocess.
type Agent struct {
	ID       string
	Type     Type
	BeadID   string
	Worktree string
	proc     Process
	result   chan Result
}

// --- Option structs ---

// ReviewOpts configures a review agent.
type ReviewOpts struct {
	BeadID             string
	BeadTitle          string
	Worktree           string
	AcceptanceCriteria string
	BaseBranch         string // defaults to "main" if empty
	ProjectRoot        string // for reading CLAUDE.md, .claude/rules/, .claude/review-patterns.md
}

// MergeOpts configures a merge conflict agent.
type MergeOpts struct {
	BeadID           string
	Branch           string
	Worktree         string
	ConflictFiles    []string
	OurBeadContext   string
	TheirBeadContext string
}

// DiagOpts configures a diagnosis agent.
type DiagOpts struct {
	BeadID   string
	Worktree string
	Symptom  string
}

// --- Spawner ---

// Spawner manages short-lived claude -p processes for ops tasks.
type Spawner struct {
	mu      sync.Mutex
	active  map[string]*Agent
	spawner BatchSpawner
	timeout time.Duration // one-shot process timeout (defaults to 5 minutes)
}

// NewSpawner creates a Spawner backed by the given BatchSpawner.
func NewSpawner(sp BatchSpawner) *Spawner {
	return &Spawner{
		active:  make(map[string]*Agent),
		spawner: sp,
		timeout: 5 * time.Minute,
	}
}

// Review spawns a two-stage review agent. The result is delivered on the
// returned channel (non-blocking for the caller).
func (s *Spawner) Review(ctx context.Context, opts ReviewOpts) <-chan Result {
	prompt := buildReviewPrompt(opts)
	return s.run(ctx, OpsReview, opts.BeadID, opts.Worktree, prompt)
}

// ResolveMergeConflict spawns a merge conflict resolution agent.
func (s *Spawner) ResolveMergeConflict(ctx context.Context, opts MergeOpts) <-chan Result {
	prompt := buildMergePrompt(opts)
	return s.run(ctx, OpsMerge, opts.BeadID, opts.Worktree, prompt)
}

// Diagnose spawns a diagnosis agent for a stuck or crashed worker.
func (s *Spawner) Diagnose(ctx context.Context, opts DiagOpts) <-chan Result {
	prompt := buildDiagnosisPrompt(opts)
	return s.run(ctx, OpsDiagnosis, opts.BeadID, opts.Worktree, prompt)
}

// Escalate spawns a one-shot manager agent to handle a dispatcher escalation.
// The agent receives the escalation type, bead context, and recent history,
// then takes corrective action (e.g. restart worker, add AC, resolve conflict).
func (s *Spawner) Escalate(ctx context.Context, opts EscalationOpts) <-chan Result {
	prompt := buildEscalationPrompt(opts)
	return s.run(ctx, OpsEscalation, opts.BeadID, opts.Workdir, prompt)
}

// Cancel kills a running ops agent by task ID.
func (s *Spawner) Cancel(taskID string) error {
	s.mu.Lock()
	agent, ok := s.active[taskID]
	s.mu.Unlock()

	if !ok {
		return fmt.Errorf("ops: no active agent with task ID %q", taskID)
	}
	if err := agent.proc.Kill(); err != nil {
		return fmt.Errorf("ops: kill agent %q: %w", taskID, err)
	}
	return nil
}

// CancelForBead kills all running ops agents for the given bead ID.
// Returns the number of agents cancelled and any error from the first kill failure.
func (s *Spawner) CancelForBead(beadID string) (int, error) {
	s.mu.Lock()
	var toCancel []*Agent
	for _, agent := range s.active {
		if agent.BeadID == beadID {
			toCancel = append(toCancel, agent)
		}
	}
	s.mu.Unlock()

	var firstErr error
	for _, agent := range toCancel {
		if err := agent.proc.Kill(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ops: kill agent %q for bead %q: %w", agent.ID, beadID, err)
		}
	}
	return len(toCancel), firstErr
}

// Active returns the task IDs of all currently running ops agents.
func (s *Spawner) Active() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0, len(s.active))
	for id := range s.active {
		ids = append(ids, id)
	}
	return ids
}

// run is the internal engine that spawns a subprocess and manages its lifecycle.
func (s *Spawner) run(ctx context.Context, opsType Type, beadID, worktree, prompt string) <-chan Result {
	ch := make(chan Result, 1)

	taskID := uuid.New().String()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.active, taskID)
			s.mu.Unlock()
		}()

		proc, err := s.spawner.Spawn(ctx, opsType.Model(), prompt, worktree)
		if err != nil {
			ch <- Result{
				Type:    opsType,
				BeadID:  beadID,
				Verdict: VerdictFailed,
				Err:     fmt.Errorf("ops: spawn failed: %w", err),
			}
			return
		}

		agent := &Agent{
			ID:       taskID,
			Type:     opsType,
			BeadID:   beadID,
			Worktree: worktree,
			proc:     proc,
			result:   ch,
		}

		s.mu.Lock()
		s.active[taskID] = agent
		s.mu.Unlock()

		// Wait for process to finish, with timeout and context cancellation.
		completed, waitErr := s.waitForProcess(ctx, proc, opsType, beadID, ch)
		if !completed {
			return // Timeout or context cancelled, result already sent.
		}

		stdout, _ := proc.Output()
		result := parseResult(opsType, beadID, stdout, waitErr)
		ch <- result
	}()

	return ch
}

// waitForProcess waits for a process to complete with timeout and context cancellation.
// Returns (true, waitErr) if the process exited normally (waitErr may be nil for success).
// Returns (false, nil) if timeout/cancelled (result already sent on ch).
func (s *Spawner) waitForProcess(ctx context.Context, proc Process, opsType Type, beadID string, ch chan<- Result) (bool, error) {
	done := make(chan error, 1)
	go func() {
		done <- proc.Wait()
	}()

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()

	select {
	case waitErr := <-done:
		return true, waitErr
	case <-timer.C:
		_ = proc.Kill()
		ch <- Result{
			Type:    opsType,
			BeadID:  beadID,
			Verdict: VerdictFailed,
			Err:     fmt.Errorf("ops: process exceeded %v timeout", s.timeout),
		}
		return false, nil
	case <-ctx.Done():
		_ = proc.Kill()
		ch <- Result{
			Type:    opsType,
			BeadID:  beadID,
			Verdict: VerdictFailed,
			Err:     ctx.Err(),
		}
		return false, nil
	}
}

// --- Result parsing ---

// parseResult interprets subprocess output to produce a Result.
func parseResult(opsType Type, beadID, stdout string, waitErr error) Result {
	r := Result{
		Type:   opsType,
		BeadID: beadID,
	}

	if waitErr != nil {
		// For OpsReview, the text-based verdict (APPROVED/REJECTED) is the
		// reviewer's actual decision. claude -p sometimes exits non-zero even
		// on successful reviews, so we trust the parsed stdout over the exit code.
		if opsType == OpsReview {
			verdict, feedback := parseReviewOutput(stdout)
			if verdict == VerdictApproved || verdict == VerdictRejected {
				r.Verdict = verdict
				r.Feedback = feedback
				r.Err = fmt.Errorf("ops: process exited with error: %w", waitErr)
				return r
			}
		}
		r.Verdict = VerdictFailed
		r.Err = fmt.Errorf("ops: process exited with error: %w", waitErr)
		r.Feedback = stdout
		return r
	}

	switch opsType {
	case OpsReview:
		r.Verdict, r.Feedback = parseReviewOutput(stdout)
	case OpsMerge:
		r.Verdict, r.Feedback = parseMergeOutput(stdout)
	case OpsDiagnosis:
		// Diagnosis has no verdict parsing — the whole output is the feedback.
		r.Feedback = stdout
	case OpsEscalation:
		r.Verdict, r.Feedback = parseEscalationOutput(stdout)
	}

	return r
}

// parseReviewOutput looks for APPROVED or REJECTED in the output.
func parseReviewOutput(stdout string) (verdict Verdict, feedback string) {
	upper := strings.ToUpper(stdout)
	if strings.Contains(upper, "APPROVED") {
		return VerdictApproved, extractFeedback(stdout, "APPROVED")
	}
	if strings.Contains(upper, "REJECTED") {
		return VerdictRejected, extractFeedback(stdout, "REJECTED")
	}
	return VerdictFailed, stdout
}

// parseMergeOutput looks for RESOLVED or FAILED in the output.
func parseMergeOutput(stdout string) (verdict Verdict, feedback string) {
	upper := strings.ToUpper(stdout)
	if strings.Contains(upper, "RESOLVED") {
		return VerdictResolved, extractFeedback(stdout, "RESOLVED")
	}
	if strings.Contains(upper, "FAILED") {
		return VerdictFailed, extractFeedback(stdout, "FAILED")
	}
	return VerdictFailed, stdout
}

// extractFeedback returns text after the verdict keyword (on the same line
// or the remaining output), trimmed of whitespace.
func extractFeedback(stdout, keyword string) string {
	upper := strings.ToUpper(stdout)
	idx := strings.Index(upper, keyword)
	if idx < 0 {
		return stdout
	}
	after := stdout[idx+len(keyword):]
	// Skip colon/dash separators right after keyword.
	after = strings.TrimLeft(after, ":- ")
	return strings.TrimSpace(after)
}

// --- Prompt builders ---
// buildReviewPrompt is in review_prompt.go

func buildMergePrompt(opts MergeOpts) string {
	var b strings.Builder
	b.WriteString("CRITICAL: Do NOT use TaskOutput or run tasks in the background.\n")
	b.WriteString("Use the Read tool to check output files. Run all commands in foreground.\n\n")

	branch := opts.Branch
	if branch == "" {
		branch = "your branch"
	}

	b.WriteString("You are resolving a rebase conflict on branch ")
	b.WriteString(branch)
	b.WriteString(".\n\n")
	b.WriteString("To resolve:\n")
	b.WriteString("1. Check conflict markers in files: ")
	b.WriteString(strings.Join(opts.ConflictFiles, ", "))
	b.WriteString("\n")
	b.WriteString("2. Edit files to resolve conflicts\n")
	b.WriteString("3. Stage resolved files: git add <files>\n")
	b.WriteString("4. Continue rebase: git rebase --continue\n")
	b.WriteString("5. If rebase completes, run: git rebase main\n\n")

	if opts.OurBeadContext != "" {
		b.WriteString("Our side: ")
		b.WriteString(opts.OurBeadContext)
		b.WriteString("\n")
	}
	if opts.TheirBeadContext != "" {
		b.WriteString("Their side: ")
		b.WriteString(opts.TheirBeadContext)
		b.WriteString("\n")
	}
	b.WriteString("\nResolve conflicts, run tests, commit.\n")
	return b.String()
}

func buildDiagnosisPrompt(opts DiagOpts) string {
	var b strings.Builder
	b.WriteString("CRITICAL: Do NOT use TaskOutput or run tasks in the background.\n")
	b.WriteString("Use the Read tool to check output files. Run all commands in foreground.\n\n")
	fmt.Fprintf(&b, "Diagnose why bead %s is stuck.\n", opts.BeadID)
	fmt.Fprintf(&b, "Symptom: %s\n", opts.Symptom)
	b.WriteString("Check: test output, recent commits, worktree state.\n")
	return b.String()
}
