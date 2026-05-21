// Package ops implements the ops agent spawner — a Dispatcher component that
// spawns short-lived claude -p processes for operational tasks such as code
// review, merge conflict resolution, and crash/stuck diagnosis.
package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oro/pkg/agentmodel"
	"oro/pkg/processenv"
	"oro/pkg/protocol"

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

// ReasoningBatchSpawner accepts a Codex-style reasoning effort. Claude
// spawners ignore reasoning by not implementing it.
type ReasoningBatchSpawner interface {
	SpawnWithReasoning(ctx context.Context, model string, reasoning string, prompt string, workdir string) (Process, error)
}

// RuntimeBatchSpawner routes ops subprocesses by runtime.
type RuntimeBatchSpawner interface {
	SpawnRuntime(ctx context.Context, runtime string, model string, reasoning string, prompt string, workdir string) (Process, error)
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
	OpsEpicFix    Type = "epic_fix" // spawned when epic acceptance test fails
	OpsWriteAC    Type = "write_ac"
	OpsDecompose  Type = "decompose" // spawned when a bead exhausts all worker retry attempts
	OpsDream      Type = "dream"     // spawned for background memory consolidation
)

// Tier returns the provider-neutral routing tier for this ops type.
func (t Type) Tier() protocol.Tier {
	switch t {
	case OpsMerge, OpsDiagnosis, OpsEpicFix:
		return protocol.TierDeep // judgment-heavy
	case OpsReview:
		return protocol.TierDeep // full code review requires judgment
	case OpsWriteAC:
		return protocol.TierDeep // acceptance-criteria writing requires careful reasoning
	case OpsEscalation:
		return protocol.TierBalanced // one-shot triage is fast, not judgment-heavy
	case OpsDecompose:
		return protocol.TierDeep // bead decomposition requires careful judgment
	case OpsDream:
		return protocol.TierBackground // lightweight background memory consolidation
	default:
		return protocol.DefaultTier
	}
}

// Model returns the preferred legacy-model equivalent for this ops type.
func (t Type) Model() string {
	switch t.Tier() {
	case protocol.TierFast, protocol.TierBackground:
		return protocol.ModelHaiku
	case protocol.TierDeep:
		return protocol.ModelOpus
	default:
		return protocol.ModelSonnet
	}
}

// Role returns the agent config role used for this ops type.
func (t Type) Role() string {
	switch t {
	case OpsReview:
		return "ops_review"
	case OpsMerge:
		return "ops_merge"
	case OpsDiagnosis:
		return "ops_diagnosis"
	case OpsEscalation:
		return "ops_escalation"
	case OpsEpicFix:
		return "ops_epic_fix"
	case OpsWriteAC:
		return "ops_write_ac"
	case OpsDecompose:
		return "ops_decompose"
	case OpsDream:
		return "ops_dream"
	default:
		return "worker"
	}
}

// Timeout returns the per-type process timeout. Returns 0 for all types except
// OpsWriteAC, which needs 10 minutes for careful acceptance-criteria generation.
// When 0, the Spawner falls back to its default timeout.
func (t Type) Timeout() time.Duration {
	switch t {
	case OpsReview:
		return 35 * time.Minute
	case OpsWriteAC:
		return 10 * time.Minute
	case OpsDream:
		return 60 * time.Second
	default:
		return 0
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
	ProjectRoot        string // for reading shared instructions, Claude compatibility files, .claude/rules/, assets/review-patterns.md
	AgentInstructions  string // explicit shared instructions path; falls back to ProjectRoot/ORO_AGENT.md when empty
	ClaudeMD           string // explicit path to CLAUDE.md; falls back to ProjectRoot/CLAUDE.md when empty
	ReviewPatterns     string // explicit path to review-patterns.md; falls back to ProjectRoot/assets/review-patterns.md when empty
}

// MergeOpts configures a merge conflict agent.
type MergeOpts struct {
	BeadID           string
	Branch           string
	Worktree         string
	ConflictFiles    []string
	OurBeadContext   string
	TheirBeadContext string
	TargetBranch     string // defaults to "main" if empty
}

// DiagOpts configures a diagnosis agent.
type DiagOpts struct {
	BeadID   string
	Worktree string
	Symptom  string
}

// WriteACOpts configures an acceptance-criteria writing agent.
type WriteACOpts struct {
	BeadID          string
	BeadTitle       string
	BeadDescription string
	Workdir         string
	OroDocsDir      string // explicit path to docs dir; falls back to "docs/plans/" when empty
}

// DecomposeOpts configures a bead decomposition agent.
type DecomposeOpts struct {
	BeadID   string
	Workdir  string
	Reason   string // dispatcher reason that triggered decomposition (for example OVERSIZED_BEAD)
	QGOutput string // quality gate output that triggered decomposition
	Tier     string // parent bead's routing tier; included in oro task create when non-empty
}

// DreamOpts configures a memory-consolidation dream agent.
type DreamOpts struct {
	Memories string // serialized memories to process; may be empty
}

// EpicFixOpts configures an epic acceptance-failure diagnostic agent.
type EpicFixOpts struct {
	EpicID string // the parent epic whose acceptance test failed
	AC     string // full acceptance criteria text
	Cmd    string // the Cmd: command that was run
	Output string // combined stdout+stderr from the failed run
	Tier   string // parent epic's routing tier; included in oro task create when non-empty
}

// --- Spawner ---

// Spawner manages short-lived claude -p processes for ops tasks.
type Spawner struct {
	mu            sync.Mutex
	active        map[string]*Agent
	spawner       BatchSpawner
	timeout       time.Duration // one-shot process timeout (defaults to 5 minutes)
	reviewTimeout time.Duration // optional OpsReview override; zero preserves Type.Timeout().
}

// NewSpawner creates a Spawner backed by the given BatchSpawner.
func NewSpawner(sp BatchSpawner) *Spawner {
	return &Spawner{
		active:  make(map[string]*Agent),
		spawner: sp,
		timeout: 5 * time.Minute,
	}
}

// NewSpawnerWithReviewTimeout creates a Spawner with an optional OpsReview
// timeout override. A zero or negative reviewTimeout preserves the per-type
// default returned by OpsReview.Timeout().
func NewSpawnerWithReviewTimeout(sp BatchSpawner, reviewTimeout time.Duration) *Spawner {
	s := NewSpawner(sp)
	s.reviewTimeout = reviewTimeout
	return s
}

// Review spawns a two-stage review agent. The result is delivered on the
// returned channel (non-blocking for the caller).
func (s *Spawner) Review(ctx context.Context, opts ReviewOpts) <-chan Result {
	if docsOnly, err := isDocsOnlyDiff(ctx, opts.Worktree, opts.BaseBranch); err == nil && docsOnly {
		ch := make(chan Result, 1)
		ch <- Result{
			Type:     OpsReview,
			BeadID:   opts.BeadID,
			Verdict:  VerdictApproved,
			Feedback: "Approved automatically: diff only touches markdown/docs files.",
		}
		return ch
	}

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

// DiagnoseEpicFailure spawns an agent that reads the failed acceptance test
// output and creates fix tasks under the epic. The result channel delivers the
// agent's feedback when it exits (fire-and-forget is fine; callers may ignore
// the channel).
func (s *Spawner) DiagnoseEpicFailure(ctx context.Context, opts EpicFixOpts) <-chan Result {
	prompt := buildEpicFixPrompt(opts)
	return s.run(ctx, OpsEpicFix, opts.EpicID, "", prompt)
}

// Escalate spawns a one-shot manager agent to handle a dispatcher escalation.
// The agent receives the escalation type, task context, and recent history,
// then takes corrective action (e.g. restart worker, add AC, resolve conflict).
func (s *Spawner) Escalate(ctx context.Context, opts EscalationOpts) <-chan Result {
	prompt := buildEscalationPrompt(opts)
	return s.run(ctx, OpsEscalation, opts.BeadID, opts.Workdir, prompt)
}

// WriteAC spawns an agent that writes acceptance criteria for a task.
//
//oro:testonly — wired into production by dispatcher (OpsWriteAC escalation path)
func (s *Spawner) WriteAC(ctx context.Context, opts WriteACOpts) <-chan Result {
	prompt := buildWriteACPrompt(opts)
	return s.run(ctx, OpsWriteAC, opts.BeadID, opts.Workdir, prompt)
}

// Decompose spawns a one-shot agent that decomposes a task into smaller child
// tasks when a task has exhausted all worker retry attempts.
func (s *Spawner) Decompose(ctx context.Context, opts DecomposeOpts) <-chan Result {
	prompt := buildDecomposePrompt(opts)
	return s.run(ctx, OpsDecompose, opts.BeadID, opts.Workdir, prompt)
}

// Dream spawns a lightweight memory-consolidation agent. The agent reviews the
// provided memories and emits any distilled insights as feedback. The result
// channel delivers the agent's output when it exits (callers may ignore it).
func (s *Spawner) Dream(ctx context.Context, opts DreamOpts) <-chan Result {
	prompt := buildDreamPrompt(opts)
	return s.run(ctx, OpsDream, "", "", prompt)
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

// HasActiveForBead reports whether any ops agent is currently running for the
// given bead ID. Used for dedup checks (e.g. MISSING_AC one-shot guard).
func (s *Spawner) HasActiveForBead(beadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, agent := range s.active {
		if agent.BeadID == beadID {
			return true
		}
	}
	return false
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

		runtime, model, reasoning := agentmodel.ResolveForRole(opsType.Role())
		proc, err := spawnOps(ctx, s.spawner, runtime, model, reasoning, prompt, worktree)
		if err != nil {
			ch <- Result{
				Type:    opsType,
				BeadID:  beadID,
				Verdict: VerdictFailed,
				Err:     fmt.Errorf("ops: spawn failed for runtime %q model %q reasoning %q: %w", runtime, model, reasoning, err),
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

func spawnOps(ctx context.Context, spawner BatchSpawner, runtime, model, reasoning, prompt, worktree string) (Process, error) {
	if runtimeSpawner, ok := spawner.(RuntimeBatchSpawner); ok {
		proc, err := runtimeSpawner.SpawnRuntime(ctx, runtime, model, reasoning, prompt, worktree)
		if err != nil {
			return nil, fmt.Errorf("spawn %s ops runtime: %w", runtime, err)
		}
		return proc, nil
	}
	if reasoningSpawner, ok := spawner.(ReasoningBatchSpawner); ok {
		proc, err := reasoningSpawner.SpawnWithReasoning(ctx, model, reasoning, prompt, worktree)
		if err != nil {
			return nil, fmt.Errorf("spawn ops model %q: %w", model, err)
		}
		return proc, nil
	}
	proc, err := spawner.Spawn(ctx, model, prompt, worktree)
	if err != nil {
		return nil, fmt.Errorf("spawn ops model %q: %w", model, err)
	}
	return proc, nil
}

// waitForProcess waits for a process to complete with timeout and context cancellation.
// Returns (true, waitErr) if the process exited normally (waitErr may be nil for success).
// Returns (false, nil) if timeout/cancelled (result already sent on ch).
func (s *Spawner) waitForProcess(ctx context.Context, proc Process, opsType Type, beadID string, ch chan<- Result) (bool, error) {
	done := make(chan error, 1)
	go func() {
		done <- proc.Wait()
	}()

	timeout := s.effectiveTimeout(opsType)

	timer := time.NewTimer(timeout)
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
			Err:     fmt.Errorf("ops: process exceeded %v timeout", timeout),
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

func (s *Spawner) effectiveTimeout(opsType Type) time.Duration {
	timeout := s.timeout
	if t := opsType.Timeout(); t > 0 {
		timeout = t
	}
	if opsType == OpsReview && s.reviewTimeout > 0 {
		timeout = s.reviewTimeout
	}
	return timeout
}

func isDocsOnlyDiff(ctx context.Context, worktree, baseBranch string) (bool, error) {
	if worktree == "" {
		return false, nil
	}
	base := baseBranch
	if base == "" {
		base = "main"
	}

	diffCmd := exec.CommandContext(ctx, "git", "diff", "--name-only", base, "--") //nolint:gosec // fixed git invocation
	diffCmd.Dir = worktree
	diffCmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	diffOut, err := diffCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git diff docs-only check: %w", err)
	}

	untrackedCmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard") //nolint:gosec // fixed git invocation
	untrackedCmd.Dir = worktree
	untrackedCmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	untrackedOut, err := untrackedCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git ls-files docs-only check: %w", err)
	}

	paths := append(strings.Fields(string(diffOut)), strings.Fields(string(untrackedOut))...)
	if len(paths) == 0 {
		return false, nil
	}
	for _, path := range paths {
		if !isDocsOnlyPath(path) {
			return false, nil
		}
	}
	return true, nil
}

func isDocsOnlyPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".md" && ext != ".markdown" && ext != ".mdx" {
		return false
	}
	clean := filepath.ToSlash(path)
	return clean == "README.md" ||
		strings.HasPrefix(clean, "docs/") ||
		strings.HasPrefix(clean, "assets/") ||
		strings.HasPrefix(clean, ".claude/")
}

// --- Result parsing ---

// parseResult interprets subprocess output to produce a Result.
func parseResult(opsType Type, beadID, stdout string, waitErr error) Result {
	r := Result{
		Type:   opsType,
		BeadID: beadID,
	}

	if waitErr != nil {
		return parseFailedResult(r, opsType, stdout, waitErr)
	}

	switch opsType {
	case OpsReview:
		r.Verdict, r.Feedback = parseReviewOutput(stdout)
	case OpsMerge:
		r.Verdict, r.Feedback = parseMergeOutput(stdout)
	case OpsDiagnosis, OpsEpicFix, OpsDream:
		// Diagnosis / epic-fix / dream have no verdict parsing — the whole output is the feedback.
		r.Feedback = stdout
	case OpsEscalation:
		r.Verdict, r.Feedback = parseEscalationOutput(stdout)
	case OpsDecompose:
		r.Verdict, r.Feedback = parseDecomposeOutput(stdout)
	}

	return r
}

func parseFailedResult(r Result, opsType Type, stdout string, waitErr error) Result {
	if opsType == OpsMerge {
		verdict, feedback := parseMergeOutput(stdout)
		if verdict == VerdictResolved {
			r.Verdict = verdict
			r.Feedback = feedback
			return r
		}
	}
	if sandbox := sandboxDenialResult(stdout); sandbox.detected {
		r.Verdict = VerdictFailed
		r.Err = sandbox.err
		r.Feedback = sandbox.feedback
		return r
	}
	r.Verdict = VerdictFailed
	r.Err = fmt.Errorf("ops: process exited with error: %w", waitErr)
	r.Feedback = stdout
	return r
}

type sandboxDenial struct {
	detected bool
	feedback string
	err      error
}

func sandboxDenialResult(stdout string) sandboxDenial {
	lower := strings.ToLower(stdout)
	if !strings.Contains(lower, "sandbox") && !strings.Contains(lower, "readonly database") {
		return sandboxDenial{}
	}
	if !strings.Contains(lower, "attempt to write a readonly database") &&
		!strings.Contains(lower, "sandbox blocked") &&
		!strings.Contains(lower, "operation not permitted") &&
		!strings.Contains(lower, "permission denied") {
		return sandboxDenial{}
	}
	msg := "ops: sandbox blocked Oro state DB write; run decompose ops with full filesystem access and preserve ORO_HOME/ORO_DB_PATH"
	return sandboxDenial{detected: true, feedback: msg, err: errors.New(msg)}
}

// parseReviewOutput requires the final non-empty stdout line to be an exact
// machine-readable verdict line.
func parseReviewOutput(stdout string) (verdict Verdict, feedback string) {
	finalLine := ""
	for line := range strings.SplitSeq(stdout, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			finalLine = strings.ToUpper(trimmed)
		}
	}

	switch finalLine {
	case "VERDICT: APPROVED":
		return VerdictApproved, strings.TrimSpace(stdout)
	case "VERDICT: REJECTED":
		return VerdictRejected, strings.TrimSpace(stdout)
	default:
		return VerdictFailed, stdout
	}
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
	if mergeOutputIndicatesCleanRebase(upper) {
		return VerdictResolved, strings.TrimSpace(stdout)
	}
	return VerdictFailed, stdout
}

func mergeOutputIndicatesCleanRebase(upper string) bool {
	if strings.Contains(upper, "REBASE COMPLETED CLEANLY") {
		return true
	}
	if strings.Contains(upper, "WORKING TREE CLEAN") &&
		(strings.Contains(upper, "REBASE") || strings.Contains(upper, "RESOLUTION")) {
		return true
	}
	if strings.Contains(upper, "NOTHING TO COMMIT") && strings.Contains(upper, "REBASE") {
		return true
	}
	return false
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

	targetBranch := opts.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
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
	b.WriteString("5. If rebase completes, run: git rebase ")
	b.WriteString(targetBranch)
	b.WriteString("\n\n")

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
	fmt.Fprintf(&b, "Diagnose why task %s is stuck.\n", opts.BeadID)
	fmt.Fprintf(&b, "Symptom: %s\n", opts.Symptom)
	b.WriteString("Check: test output, recent commits, worktree state.\n")
	return b.String()
}
