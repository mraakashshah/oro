// Package ops implements the ops agent spawner — a Dispatcher component that
// spawns short-lived claude -p processes for operational tasks such as code
// review, merge conflict resolution, and crash/stuck diagnosis.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oro/pkg/agentmodel"
	"oro/pkg/beadstore"
	"oro/pkg/janitor"
	"oro/pkg/processenv"
	"oro/pkg/protocol"
	"oro/pkg/worker"

	"github.com/google/uuid"
)

// --- Process abstraction ---

// Process represents a running subprocess. Same interface as the worker
// package for testability.
type Process interface {
	Wait() error
	Kill() error
	Output() (string, error) // read stdout after completion
	LastOutputAt() time.Time
}

// BatchSpawner creates new claude -p processes.
type BatchSpawner interface {
	Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, error)
}

// ReasoningBatchSpawner accepts a reasoning/effort level. Codex maps it to
// model_reasoning_effort; Claude maps it to its --effort flag.
type ReasoningBatchSpawner interface {
	SpawnWithReasoning(ctx context.Context, model string, reasoning string, prompt string, workdir string) (Process, error)
}

// RuntimeBatchSpawner routes ops subprocesses by runtime.
type RuntimeBatchSpawner interface {
	SpawnRuntime(ctx context.Context, runtime string, model string, reasoning string, prompt string, workdir string) (Process, error)
}

type spawnRouting struct {
	role            string
	runtimeOverride string
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
	OpsJanitor    Type = "janitor"   // spawned for low-cost codebase cleanliness triage
	OpsAudit      Type = "audit"     // spawned for periodic deep whole-repo audits
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
	case OpsJanitor:
		return protocol.TierFast // continuous low-cost codebase cleanliness triage
	case OpsAudit:
		return protocol.TierDeep // periodic whole-repo audits require deep judgment
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
	case OpsJanitor:
		return "ops_janitor"
	case OpsAudit:
		return "ops_audit"
	default:
		return "worker"
	}
}

// Timeout returns the per-type process timeout. Recovery operations get a
// 15-minute budget so healthy conflict resolution and verification can finish.
// When 0, the Spawner falls back to its default timeout.
func (t Type) Timeout() time.Duration {
	switch t {
	case OpsReview:
		return 35 * time.Minute
	case OpsMerge, OpsDiagnosis, OpsEscalation, OpsEpicFix:
		return 15 * time.Minute
	case OpsWriteAC:
		return 10 * time.Minute
	case OpsDream:
		return 60 * time.Second
	case OpsJanitor:
		return 10 * time.Minute
	case OpsAudit:
		return 20 * time.Minute
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
	MultiPersona       bool
	MaxReviewers       int
	CheapThenDeep      bool
	CheapGateThreshold int
	ScopedFindings     []Finding
	ProjectRoot        string // for reading shared instructions, Claude compatibility files, .claude/rules/, assets/review-patterns.md
	AgentInstructions  string // explicit shared instructions path; falls back to ProjectRoot/ORO_AGENT.md when empty
	ClaudeMD           string // explicit path to CLAUDE.md; falls back to ProjectRoot/CLAUDE.md when empty
	ReviewPatterns     string // explicit path to review-patterns.md; falls back to ProjectRoot/assets/review-patterns.md when empty
	PersistFindings    bool   // when true, merged structured findings are appended to the bead journey
	BeadStore          beadstore.Store
	ReviewPolicy       *ReviewPolicy // nil uses the default review policy
}

// AuditOpts configures a whole-repository audit fan-out.
type AuditOpts struct {
	BeadID       string
	Worktree     string
	MaxReviewers int
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
	Memories       string   // serialized memories to process; may be empty
	ActiveBiasTags []string // calibration tags to counter in the next proposal prompt
}

// JanitorOpts configures a deterministic-cleanliness triage agent.
type JanitorOpts struct {
	Candidates []janitor.Candidate
	Suppressed []Finding
	OpenTitles []string
	Worktree   string
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
	reviewSpawner BatchSpawner
	timeout       time.Duration // one-shot process timeout (defaults to 5 minutes)
	reviewTimeout time.Duration // optional OpsReview override; zero preserves Type.Timeout().
	reviewIdle    time.Duration // optional OpsReview idle watchdog; zero disables it.
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

// SetReviewSpawner configures the BatchSpawner used only for OpsReview runs.
// A nil review spawner preserves the default spawner path.
func (s *Spawner) SetReviewSpawner(sp BatchSpawner) {
	s.reviewSpawner = sp
}

// Review spawns a two-stage review agent. The result is delivered on the
// returned channel (non-blocking for the caller).
func (s *Spawner) Review(ctx context.Context, opts ReviewOpts) <-chan Result {
	if docsOnly, err := isDocsOnlyDiff(ctx, opts.Worktree, opts.BaseBranch); err == nil && docsOnly {
		outcome, outcomeErr := buildDocsOnlyReviewOutcome(reviewPolicy(opts))
		if outcomeErr == nil {
			feedback, marshalErr := json.Marshal(outcome)
			if marshalErr == nil {
				ch := make(chan Result, 1)
				ch <- Result{
					Type:     OpsReview,
					BeadID:   opts.BeadID,
					Verdict:  VerdictApproved,
					Feedback: string(feedback),
				}
				return ch
			}
		}
		return s.runTypedReview(ctx, opts)
	}

	if opts.MultiPersona {
		return s.reviewMultiPersona(ctx, opts)
	}

	prompt := buildReviewPrompt(opts)
	return s.run(ctx, OpsReview, opts.BeadID, opts.Worktree, prompt)
}

func (s *Spawner) runTypedReview(ctx context.Context, opts ReviewOpts) <-chan Result {
	prompt := buildStructuredReviewPrompt(opts)
	rawResults := s.run(ctx, OpsReview, opts.BeadID, opts.Worktree, prompt)
	out := make(chan Result, 1)
	go func() {
		result := <-rawResults
		if result.Err != nil {
			out <- result
			return
		}
		outcome, err := parseStructuredReviewReport(result.Feedback)
		if err != nil {
			result.Verdict = VerdictFailed
			result.Err = fmt.Errorf("parse typed review outcome: %w", err)
			out <- result
			return
		}
		result.Verdict = reviewReportFromOutcome(outcome).Verdict
		out <- result
	}()
	return out
}

func (s *Spawner) runCheapTriage(ctx context.Context, opts ReviewOpts) []Finding {
	prompt := buildCheapTriagePrompt(opts)
	result := <-s.runWith(ctx, OpsReview, spawnRouting{role: "ops_review_triage"}, opts.BeadID, opts.Worktree, prompt)
	report, _ := parseReviewReport(result.Feedback)
	return cheapGate(report.Findings)
}

func (s *Spawner) reviewMultiPersona(ctx context.Context, opts ReviewOpts) <-chan Result {
	personas := selectPersonas(opts)
	if len(personas) == 0 {
		return s.run(ctx, OpsReview, opts.BeadID, opts.Worktree, buildReviewPrompt(opts))
	}

	if opts.CheapThenDeep && diffSizeExceeds(opts, opts.CheapGateThreshold) {
		opts = scopeToSurvivors(opts, s.runCheapTriage(ctx, opts))
	}

	prompt := buildStructuredReviewPrompt(opts)
	out := make(chan Result, 1)
	go func() {
		policy := reviewPolicy(opts)
		reports, executions := s.collectPersonaReviewExecutions(ctx, OpsReview, opts, personas, prompt, policy)
		out <- reviewOutcomeResult(opts, mergeReports(policy, reports, executions))
	}()
	return out
}

func (s *Spawner) collectPersonaReviews(ctx context.Context, opsType Type, opts ReviewOpts, personas []Persona, prompt string) []ReviewReport {
	reports, _ := s.collectPersonaReviewExecutions(ctx, opsType, opts, personas, prompt, ReviewPolicy{})
	return reports
}

func (s *Spawner) collectPersonaReviewExecutions(
	ctx context.Context,
	opsType Type,
	opts ReviewOpts,
	personas []Persona,
	prompt string,
	policy ReviewPolicy,
) ([]ReviewReport, []ReviewPersonaExecution) {
	maxReviewers := opts.MaxReviewers
	if maxReviewers <= 0 {
		maxReviewers = 4
	}
	if maxReviewers > len(personas) {
		maxReviewers = len(personas)
	}

	reports := make([]ReviewReport, 0, len(personas))
	executions := make([]ReviewPersonaExecution, 0, len(personas))
	for start := 0; start < len(personas); start += maxReviewers {
		end := start + maxReviewers
		if end > len(personas) {
			end = len(personas)
		}
		chans := make([]<-chan Result, 0, end-start)
		for _, persona := range personas[start:end] {
			chans = append(chans, s.runWith(
				ctx,
				opsType,
				spawnRouting{role: persona.Role},
				opts.BeadID,
				opts.Worktree,
				prompt+persona.Fragment,
			))
		}
		for i, ch := range chans {
			report, execution := personaReviewResult(<-ch, personas[start+i], policy)
			reports = append(reports, report)
			executions = append(executions, execution)
		}
	}
	return reports, executions
}

func personaReviewResult(result Result, persona Persona, policy ReviewPolicy) (ReviewReport, ReviewPersonaExecution) {
	report, _ := parseReviewReport(result.Feedback)
	_, parseErr := parseStructuredReviewReport(result.Feedback)
	if parseErr != nil {
		_, _, parseErr = parseLegacyStructuredReviewReport(result.Feedback)
	}
	report.Reviewer = persona.ID
	execution := ReviewPersonaExecution{
		Persona:  persona.ID,
		Required: personaRequired(policy, persona.ID),
		Kind:     ReviewExecSucceeded,
	}
	for findingIndex := range report.Findings {
		finding := &report.Findings[findingIndex]
		finding.Sources = []string{persona.ID}
		finding.Status = ""
		finding.History = nil
	}
	if parseErr != nil || result.Err != nil || result.Verdict == VerdictFailed {
		report.Verdict = VerdictFailed
		execution.Kind = ReviewExecExitError
	}
	return report, execution
}

func personaRequired(policy ReviewPolicy, persona string) bool {
	for _, required := range requiredPersonas(policy) {
		if required == persona {
			return true
		}
	}
	return false
}

func reviewOutcomeResult(opts ReviewOpts, outcome ReviewOutcome) Result {
	feedback, err := json.Marshal(outcome)
	if err != nil {
		return Result{
			Type:     OpsReview,
			BeadID:   opts.BeadID,
			Verdict:  VerdictFailed,
			Feedback: err.Error(),
			Err:      fmt.Errorf("marshal merged review outcome: %w", err),
		}
	}
	verdict := VerdictFailed
	switch outcome.Decision {
	case ReviewApproved:
		verdict = VerdictApproved
	case ReviewRejected:
		verdict = VerdictRejected
	}
	return Result{
		Type:     OpsReview,
		BeadID:   opts.BeadID,
		Verdict:  verdict,
		Feedback: string(feedback),
	}
}

// Audit spawns six focused whole-repository auditors in bounded waves. Each
// section receives the shared audit base prompt plus only its own fragment.
func (s *Spawner) Audit(ctx context.Context, opts AuditOpts) <-chan Result {
	prompt := buildAuditPrompt(opts)
	personas := auditSections()
	reviewOpts := ReviewOpts{
		BeadID:       opts.BeadID,
		Worktree:     opts.Worktree,
		MaxReviewers: opts.MaxReviewers,
	}
	out := make(chan Result, 1)
	go func() {
		manifest, err := buildRepoManifest(ctx, opts.Worktree)
		if err != nil {
			out <- Result{
				Type:     OpsAudit,
				BeadID:   opts.BeadID,
				Verdict:  VerdictFailed,
				Feedback: err.Error(),
				Err:      err,
			}
			return
		}
		reports := s.collectPersonaReviews(ctx, OpsAudit, reviewOpts, personas, prompt)
		if allReviewReportsFailed(reports) {
			out <- Result{
				Type:     OpsAudit,
				BeadID:   opts.BeadID,
				Verdict:  VerdictFailed,
				Feedback: "all audit section reports failed to parse",
				Err:      errors.New("all audit section reports failed"),
			}
			return
		}
		out <- mergeAuditReports(reports, manifest, reviewOpts)
	}()
	return out
}

func allReviewReportsFailed(reports []ReviewReport) bool {
	if len(reports) == 0 {
		return true
	}
	for _, report := range reports {
		if report.Verdict != VerdictFailed {
			return false
		}
	}
	return true
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

// Janitor spawns a cheap deterministic-cleanliness triage agent.
func (s *Spawner) Janitor(ctx context.Context, opts JanitorOpts) <-chan Result {
	prompt := buildJanitorPrompt(opts)
	return s.run(ctx, OpsJanitor, "", opts.Worktree, prompt)
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
	return s.cancelForBead(beadID, func(*Agent) bool { return true })
}

// CancelReviewsForBead kills running review agents for the given bead ID.
// Other operations for the bead remain active.
func (s *Spawner) CancelReviewsForBead(beadID string) (int, error) {
	return s.cancelForBead(beadID, func(agent *Agent) bool { return agent.Type == OpsReview })
}

func (s *Spawner) cancelForBead(beadID string, matches func(*Agent) bool) (int, error) {
	s.mu.Lock()
	var toCancel []*Agent
	for _, agent := range s.active {
		if agent.BeadID == beadID && matches(agent) {
			toCancel = append(toCancel, agent)
		}
	}
	s.mu.Unlock()

	var firstErr error
	for _, agent := range toCancel {
		if err := agent.proc.Kill(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ops: kill agent %q for bead %q: %w", agent.ID, beadID, err)
		}
		s.mu.Lock()
		delete(s.active, agent.ID)
		s.mu.Unlock()
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
	return s.runWith(ctx, opsType, spawnRouting{role: opsType.Role()}, beadID, worktree, prompt)
}

func (s *Spawner) runWith(ctx context.Context, opsType Type, routing spawnRouting, beadID, worktree, prompt string) <-chan Result {
	ch := make(chan Result, 1)

	taskID := uuid.New().String()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.active, taskID)
			s.mu.Unlock()
		}()

		role := routing.role
		if role == "" {
			role = opsType.Role()
		}
		runtime, model, reasoning := agentmodel.ResolveForRole(role)
		if routing.runtimeOverride != "" {
			runtime = routing.runtimeOverride
		}
		sp := s.spawner
		if opsType == OpsReview && s.reviewSpawner != nil {
			sp = s.reviewSpawner
		}
		proc, err := spawnOps(ctx, sp, runtime, model, reasoning, prompt, worktree)
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

	startedAt := time.Now()
	timeout := s.effectiveTimeout(opsType)
	idle := s.effectiveIdle(opsType)

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	idleC, stopIdle := idleWatchdog(idle)
	defer stopIdle()

	for {
		select {
		case waitErr := <-done:
			return true, waitErr
		case <-idleC:
			if !reviewIdleExceeded(proc, startedAt, idle) {
				continue
			}
			killAndReap(proc, done)
			ch <- Result{
				Type:    opsType,
				BeadID:  beadID,
				Verdict: VerdictFailed,
				Err:     fmt.Errorf("ops: review wedged (no output for %v)", idle),
			}
			return false, nil
		case <-timer.C:
			killAndReap(proc, done)
			ch <- Result{
				Type:    opsType,
				BeadID:  beadID,
				Verdict: VerdictFailed,
				Err:     fmt.Errorf("ops: process exceeded %v timeout", timeout),
			}
			return false, nil
		case <-ctx.Done():
			killAndReap(proc, done)
			ch <- Result{
				Type:    opsType,
				BeadID:  beadID,
				Verdict: VerdictFailed,
				Err:     ctx.Err(),
			}
			return false, nil
		}
	}
}

// killAndReap waits for the existing Wait call after stopping a subprocess.
// This prevents a timeout verdict from racing an unreaped launcher or any
// process-group descendant that still owns the subprocess output pipes.
func killAndReap(proc Process, done <-chan error) {
	_ = proc.Kill()
	<-done
}

func idleWatchdog(idle time.Duration) (ticks <-chan time.Time, stop func()) {
	if idle <= 0 {
		return nil, func() {}
	}
	interval := idle / 2
	if interval <= 0 {
		interval = idle
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

func reviewIdleExceeded(proc Process, startedAt time.Time, idle time.Duration) bool {
	lastOutputAt := proc.LastOutputAt()
	if lastOutputAt.IsZero() {
		lastOutputAt = startedAt
	}
	return time.Since(lastOutputAt) > idle
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

func (s *Spawner) effectiveIdle(opsType Type) time.Duration {
	if opsType != OpsReview || s.reviewIdle <= 0 {
		return 0
	}
	return s.reviewIdle
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
	case OpsDiagnosis, OpsEpicFix, OpsDream, OpsJanitor, OpsAudit:
		// These runs have no verdict parsing — the whole output is the feedback.
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
	stdout = reviewOutputText(stdout)
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

func reviewOutputText(stdout string) string {
	var text strings.Builder
	recognized := false

	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		activity := worker.ParseStreamEvent([]byte(line))
		switch activity.Kind {
		case worker.ActivityResult:
			return activity.Text
		case worker.ActivityTextDelta:
			recognized = true
			text.WriteString(activity.Text)
		case worker.ActivityToolUse:
			recognized = true
		default:
			if isStreamJSONEnvelope(line) {
				recognized = true
				continue
			}
			return stdout
		}
	}

	if recognized && text.Len() > 0 {
		return text.String()
	}
	return stdout
}

func isStreamJSONEnvelope(line string) bool {
	var top struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(line), &top); err != nil {
		return false
	}
	return top.Type != ""
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
