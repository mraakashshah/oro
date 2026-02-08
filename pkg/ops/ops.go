// Package ops implements the ops agent spawner — a Dispatcher component that
// spawns short-lived claude -p processes for operational tasks such as code
// review, merge conflict resolution, and crash/stuck diagnosis.
package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"

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

// SubprocessSpawner creates new claude -p processes.
type SubprocessSpawner interface {
	Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, error)
}

// --- Ops types and verdicts ---

// Type identifies the kind of operational task.
type Type string

// Known ops task types.
const (
	OpsReview    Type = "review"
	OpsMerge     Type = "merge_conflict"
	OpsDiagnosis Type = "diagnosis"
)

// Model returns the preferred Claude model for this ops type.
func (t Type) Model() string {
	switch t {
	case OpsMerge, OpsDiagnosis:
		return "claude-opus-4-6" // judgment-heavy
	case OpsReview:
		return "claude-sonnet-4-5-20250929" // mechanical
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
	Worktree           string
	AcceptanceCriteria string
}

// MergeOpts configures a merge conflict agent.
type MergeOpts struct {
	BeadID           string
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
	spawner SubprocessSpawner
}

// NewSpawner creates a Spawner backed by the given SubprocessSpawner.
func NewSpawner(sp SubprocessSpawner) *Spawner {
	return &Spawner{
		active:  make(map[string]*Agent),
		spawner: sp,
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

		// Wait for process to finish, or context cancellation.
		done := make(chan error, 1)
		go func() {
			done <- proc.Wait()
		}()

		var waitErr error
		select {
		case waitErr = <-done:
			// Process exited.
		case <-ctx.Done():
			_ = proc.Kill()
			ch <- Result{
				Type:    opsType,
				BeadID:  beadID,
				Verdict: VerdictFailed,
				Err:     ctx.Err(),
			}
			return
		}

		stdout, _ := proc.Output()
		result := parseResult(opsType, beadID, stdout, waitErr)
		ch <- result
	}()

	return ch
}

// --- Result parsing ---

// parseResult interprets subprocess output to produce a Result.
func parseResult(opsType Type, beadID, stdout string, waitErr error) Result {
	r := Result{
		Type:   opsType,
		BeadID: beadID,
	}

	if waitErr != nil {
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
	}

	return r
}

// parseReviewOutput looks for APPROVED or REJECTED in the output.
func parseReviewOutput(stdout string) (Verdict, string) {
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
func parseMergeOutput(stdout string) (Verdict, string) {
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

func buildReviewPrompt(opts ReviewOpts) string {
	var b strings.Builder
	b.WriteString("Review the changes in this worktree against the acceptance criteria.\n")
	b.WriteString("Run: git diff main\n")
	if opts.AcceptanceCriteria != "" {
		b.WriteString("Check: ")
		b.WriteString(opts.AcceptanceCriteria)
		b.WriteString("\n")
	}
	b.WriteString("Respond with APPROVED or REJECTED with feedback.\n")
	return b.String()
}

func buildMergePrompt(opts MergeOpts) string {
	var b strings.Builder
	b.WriteString("Resolve merge conflicts in: ")
	b.WriteString(strings.Join(opts.ConflictFiles, ", "))
	b.WriteString("\n")
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
	b.WriteString("Resolve conflicts, run tests, commit.\n")
	return b.String()
}

func buildDiagnosisPrompt(opts DiagOpts) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Diagnose why bead %s is stuck.\n", opts.BeadID))
	b.WriteString(fmt.Sprintf("Symptom: %s\n", opts.Symptom))
	b.WriteString("Check: test output, recent commits, worktree state.\n")
	return b.String()
}
