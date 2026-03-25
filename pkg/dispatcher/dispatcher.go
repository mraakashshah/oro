// Package dispatcher implements the Oro orchestrator — the core coordination
// engine that composes protocol, merge, worker, and ops packages into a
// unified runtime. The Dispatcher manages a UDS server for worker connections,
// SQLite WAL for runtime state, a priority queue from bd ready, worker
// lifecycle supervision, merge execution, ops agent spawning, command
// processing, and escalation to the Manager.
//
// The Dispatcher is INERT until it receives a "start" directive. After that
// it runs autonomously, polling for work and assigning beads to idle workers.
package dispatcher

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	"github.com/fsnotify/fsnotify"
)

// --- Dispatcher states ---

// State represents the dispatcher's operational state.
type State string

// Dispatcher state constants.
const (
	StateInert    State = "inert"    // Waiting for start directive.
	StateRunning  State = "running"  // Actively assigning work.
	StatePaused   State = "paused"   // Workers continue, no new assignments.
	StateStopping State = "stopping" // Finishing current work, no new assignments.
)

// MetaBranch is the metadata key on an epic bead that names the target branch
// for the epic's FF merge when all children complete. Falls back to
// Config.DefaultBranch (typically "main") when absent.
const MetaBranch = "branch"

// statusThrottleWindow is the dedup window for status directives. Repeated
// status requests within this window return a cached response to avoid
// redundant rebuilds when the manager sends bursts of 2-5 status calls.
const statusThrottleWindow = 5 * time.Second

// --- Domain types ---

// Bead, BeadDetail, and model constants are now in pkg/protocol/types.go

// --- Interfaces for testability ---

// BeadSource provides ready work items. Production impl shells out to `bd ready`.
type BeadSource interface {
	Ready(ctx context.Context) ([]protocol.Bead, error)
	InProgress(ctx context.Context) ([]protocol.Bead, error)
	Show(ctx context.Context, id string) (*protocol.BeadDetail, error)
	Close(ctx context.Context, id string, reason string) error
	Create(ctx context.Context, title, beadType string, priority int, description, parent, acceptanceCriteria string) (string, error)
	Update(ctx context.Context, id, status string) error
	Sync(ctx context.Context) error
	AllChildrenClosed(ctx context.Context, epicID string) (bool, error)
	HasChildren(ctx context.Context, epicID string) (bool, error)
	FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)
	Export(ctx context.Context) ([]byte, error)
}

// WorktreeManager creates and removes git worktrees.
type WorktreeManager interface {
	Create(ctx context.Context, beadID, baseBranch string) (path string, branch string, err error)
	Remove(ctx context.Context, path string) error
	Prune(ctx context.Context) error
	DeleteBranch(ctx context.Context, branch string) error
	BranchExists(ctx context.Context, branch string) (bool, error)
	MergeFFOnly(ctx context.Context, branch string, target string) (commitSHA string, err error)
	// UpdateBranchRef advances targetBranch to point at the tip of sourceBranch
	// without requiring sourceBranch to be checked out. Used when the target is
	// not the HEAD branch (i.e., not the branch checked out in the main worktree).
	UpdateBranchRef(ctx context.Context, targetBranch, sourceBranch string) error
	GCClosedWorktrees(ctx context.Context, isBeadClosed func(string) bool) error
	// Exists reports whether the worktree at path is still present on disk.
	// Returns false if the path does not exist or cannot be accessed.
	Exists(ctx context.Context, path string) bool
}

// Escalator sends messages to the Manager. Production impl uses tmux send-keys.
type Escalator interface {
	Escalate(ctx context.Context, msg string) error
}

// ProcessManager spawns and kills oro worker OS processes.
// Production implementations use exec.Command to run `oro worker`.
type ProcessManager interface {
	Spawn(id string) (*os.Process, error)
	Kill(id string) error
}

// CodeIndex provides code search for injecting relevant code into prompts.
type CodeIndex interface {
	FTS5Search(ctx context.Context, query string, limit int) ([]CodeChunk, error)
	Search(ctx context.Context, query string, topK int) ([]SearchResult, error)
}

// CodeChunk represents a code search result.
type CodeChunk struct {
	FilePath  string
	Name      string
	Kind      string
	StartLine int
	EndLine   int
	Content   string
}

// SearchResult pairs a CodeChunk with its relevance score and optional rerank reason.
type SearchResult struct {
	CodeChunk
	Score  float64
	Reason string
}

// AcceptanceRunner executes an epic's acceptance test command and reports
// whether it passed.
type AcceptanceRunner interface {
	Run(ctx context.Context, cmd string) (output string, passed bool, err error)
}

// ShellAcceptanceRunner runs the acceptance command through sh -c and reports
// pass when the process exits with code 0.
type ShellAcceptanceRunner struct{}

// Run executes cmd via sh -c and returns the combined output. passed is true
// when the process exits with code 0.
func (r *ShellAcceptanceRunner) Run(ctx context.Context, cmd string) (output string, passed bool, err error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd) //nolint:gosec // cmd is a user-defined acceptance test string
	out, runErr := c.CombinedOutput()
	output = string(out)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return output, false, nil
		}
		return output, false, fmt.Errorf("running acceptance test: %w", runErr)
	}
	return output, true, nil
}

// QGRunner executes the quality gate script in a worktree and reports
// whether it passed. skipMutation true means mutation testing is skipped.
type QGRunner interface {
	Run(ctx context.Context, worktree string, skipMutation bool) (passed bool, output string, err error)
}

// ShellQGRunner runs quality_gate.sh inside the worktree via bash. It looks
// for scripts/quality_gate.sh first, then quality_gate.sh at the repo root.
// It returns (true, output, nil) on exit 0, (false, output, nil) on non-zero
// exit, and (false, "", err) if the script cannot be found or launched.
type ShellQGRunner struct{}

// Run implements QGRunner using the same logic as worker.RunQualityGate but
// self-contained in the dispatcher package to avoid an import cycle.
func (r *ShellQGRunner) Run(ctx context.Context, worktree string, skipMutation bool) (passed bool, output string, err error) {
	candidates := []string{
		filepath.Join(worktree, "scripts", "quality_gate.sh"),
		filepath.Join(worktree, "quality_gate.sh"),
	}
	scriptPath := ""
	for _, p := range candidates {
		if _, statErr := os.Stat(p); statErr == nil {
			scriptPath = p
			break
		}
	}
	if scriptPath == "" {
		return false, "", fmt.Errorf("quality gate script not found in scripts/quality_gate.sh or quality_gate.sh")
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree
	if skipMutation {
		cmd.Env = append(os.Environ(), "ORO_SKIP_MUTATION=1")
	}
	out, runErr := cmd.CombinedOutput()
	output = string(out)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return false, output, nil
		}
		return false, output, fmt.Errorf("run quality gate: %w", runErr)
	}
	return true, output, nil
}

// --- Worker tracking ---

// WorkerState is now in pkg/protocol/types.go

// trackedWorker holds runtime state for a connected worker.
type trackedWorker struct {
	id               string
	conn             net.Conn
	state            protocol.WorkerState
	beadID           string
	epicID           string // parent epic ID if the assigned bead is a child of an epic
	isEpicDecomp     bool   // true when worker is assigned an epic for decomposition (no merge on done)
	worktree         string
	baseBranch       string // branch the worktree was created from (main or epic/<epicID>)
	targetBranch     string // branch the worker's changes should merge into (same as baseBranch)
	model            string // resolved model for the current bead assignment
	lastSeen         time.Time
	lastProgress     time.Time // last time meaningful progress was observed (DONE/READY_FOR_REVIEW/QG/first STATUS)
	contextPct       int       // context usage percentage from last heartbeat (0-100)
	encoder          *json.Encoder
	pendingMsgs      []protocol.Message // buffered messages for disconnected worker
	shutdownCancel   context.CancelFunc // cancels previous shutdown goroutine (1nf.5)
	shutdownApproved bool               // set by handleShutdownApproved; checked by checkShutdownApproved
	managed          bool               // true if spawned by the dispatcher (vs externally connected)
	prevSession      bool               // true if worker ID predates this dispatcher's startTime (previous session)
}

// pendingHandoff holds context for a bead whose worker has been shut down
// during a ralph handoff. The next worker to connect will be assigned this
// bead+worktree instead of going through normal assignment.
type pendingHandoff struct {
	beadID       string
	epicID       string // parent epic ID if the bead is a child of an epic
	worktree     string
	baseBranch   string // branch the worktree was created from (main or epic/<epicID>)
	targetBranch string // branch the worker's changes should merge into (same as baseBranch)
	model        string
	title        string   // bead title for memory search on respawn
	labels       []string // bead labels for memory search on respawn
}

// --- Config ---

// Config holds Dispatcher configuration.
type Config struct {
	SocketPath            string        // UDS socket path.
	DBPath                string        // SQLite database path.
	RepoRoot              string        // Absolute path to the repository root. Used so bd commands run from the right directory even when the process is started from a worktree. Falls back to os.Getwd() if empty.
	BeadsDir              string        // Path to the beads directory (defaults to protocol.BeadsDir when empty). Set from ProjectPaths.BeadsDir for stealth-mode support.
	MaxWorkers            int           // Worker pool ceiling for auto-scale (default 10).
	InitialWorkers        int           // Initial targetWorkers on startup (default: MaxWorkers).
	HeartbeatTimeout      time.Duration // Worker heartbeat timeout (default 45s).
	ProgressTimeout       time.Duration // Max time without meaningful progress before STUCK_WORKER escalation (default 15m).
	PollInterval          time.Duration // bd ready poll interval (default 10s).
	FallbackPollInterval  time.Duration // Fallback poll interval for fsnotify safety net (default 60s).
	ShutdownTimeout       time.Duration // Graceful shutdown timeout (default 10s).
	ConsolidateAfterN     int           // Trigger context consolidation after N completed beads (default 5).
	PaneContextThreshold  int           // Context percentage threshold for pane handoff (default 60).
	PaneMonitorInterval   time.Duration // Pane context_pct poll interval (default 5s).
	PaneRestartCooldown   time.Duration // Min time between manager pane restarts (default 2m).
	PaneInactivityTimeout time.Duration // Manager inactivity duration before restart (default 10m).
	ReviewTimeout         time.Duration // Max time a reviewing worker can stall before STUCK_WORKER escalation (default 15m).
	BackupInterval        time.Duration // Interval between full-state JSONL backups to .beads/backup/full-state.jsonl (default 5m).
	Estimator             BeadEstimator // LLM-based bead complexity estimator (default NewBeadEstimator()).
	WorkerProgram         string        // Absolute path to worker-program.md. Defaults to <RepoRoot>/worker-program.md.
	DefaultBranch         string        // Base branch for worktree creation and epic FF merges (default "main"). Set via --base-branch flag.
}

// defaultWorkerCounts returns the resolved (initialWorkers, maxWorkers) pair,
// applying defaults: maxWorkers defaults to 10; initialWorkers defaults to maxWorkers.
func defaultWorkerCounts(initial, ceiling int) (initialOut, ceilingOut int) {
	if ceiling == 0 {
		ceiling = 10
	}
	if initial == 0 {
		initial = ceiling
	}
	if initial > ceiling {
		initial = ceiling
	}
	return initial, ceiling
}

func (c *Config) withDefaults() Config {
	out := *c
	out.InitialWorkers, out.MaxWorkers = defaultWorkerCounts(out.InitialWorkers, out.MaxWorkers)
	if out.HeartbeatTimeout == 0 {
		out.HeartbeatTimeout = 45 * time.Second
	}
	if out.ProgressTimeout == 0 {
		out.ProgressTimeout = 10 * time.Minute
	}
	if out.PollInterval == 0 {
		out.PollInterval = 10 * time.Second
	}
	if out.FallbackPollInterval == 0 {
		out.FallbackPollInterval = 60 * time.Second
	}
	if out.ShutdownTimeout == 0 {
		out.ShutdownTimeout = 10 * time.Second
	}
	if out.ConsolidateAfterN == 0 {
		out.ConsolidateAfterN = 5
	}
	if out.PaneContextThreshold == 0 {
		out.PaneContextThreshold = 40
	}
	if out.PaneMonitorInterval == 0 {
		out.PaneMonitorInterval = 5 * time.Second
	}
	if out.PaneRestartCooldown == 0 {
		out.PaneRestartCooldown = 2 * time.Minute
	}
	if out.PaneInactivityTimeout == 0 {
		out.PaneInactivityTimeout = 10 * time.Minute
	}
	if out.ReviewTimeout == 0 {
		out.ReviewTimeout = 15 * time.Minute
	}
	if out.BackupInterval == 0 {
		out.BackupInterval = 5 * time.Minute
	}
	if out.Estimator == nil {
		out.Estimator = NewBeadEstimator()
	}
	if out.DefaultBranch == "" {
		out.DefaultBranch = "main"
	}
	return out
}

// validate checks that all Config values are valid. Returns an error if any
// duration is <= 0 or if MaxWorkers is negative. Call this AFTER withDefaults().
func (c Config) validate() error {
	if c.MaxWorkers < 0 {
		return fmt.Errorf("MaxWorkers must be non-negative, got %d", c.MaxWorkers)
	}
	if c.HeartbeatTimeout <= 0 {
		return fmt.Errorf("HeartbeatTimeout must be positive, got %v", c.HeartbeatTimeout)
	}
	if c.ProgressTimeout <= 0 {
		return fmt.Errorf("ProgressTimeout must be positive, got %v", c.ProgressTimeout)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("PollInterval must be positive, got %v", c.PollInterval)
	}
	if c.FallbackPollInterval <= 0 {
		return fmt.Errorf("FallbackPollInterval must be positive, got %v", c.FallbackPollInterval)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("ShutdownTimeout must be positive, got %v", c.ShutdownTimeout)
	}
	return nil
}

// --- Dispatcher ---

// Dispatcher is the main orchestrator. Worker management and bead tracking
// are factored into embedded WorkerPool and BeadTracker structs whose fields
// are promoted so that existing callers (including tests) can access them
// directly (e.g. d.workers, d.attemptCounts). Both embedded structs share
// the Dispatcher-level mu for synchronisation.
type Dispatcher struct {
	cfg           Config
	db            *sql.DB
	merger        *merge.Coordinator
	ops           *ops.Spawner
	beads         BeadSource
	worktrees     WorktreeManager
	escalator     Escalator
	memories      *memory.Store
	codeIndex     CodeIndex // interface for FTS5 code search (nil means no search)
	procMgr       ProcessManager
	acceptance    AcceptanceRunner // runs epic acceptance test commands
	qgRunner      QGRunner         // runs quality gate before merge (defaults to &ShellQGRunner{})
	paneRestarter PaneRestarter    // restarts named tmux panes (nil means no restart)
	estimator     BeadEstimator    // estimates bead completion time (nil means no estimation)
	// WorkerPool holds the connected-worker registry (embedded for field promotion).
	WorkerPool
	// BeadTracker holds per-bead counters and mappings (embedded for field promotion).
	BeadTracker

	mu                          sync.Mutex
	reconcilingScale            atomic.Bool // prevents concurrent reconcileScale() calls (oro-ovpc.1)
	state                       State
	listener                    net.Listener
	focusedEpic                 string
	targetWorkers               int
	completionsSinceConsolidate int // counts completed beads since last context consolidation

	// repoRoot is the effective repository root (cfg.RepoRoot with cwd fallback).
	// Used as the target directory for git operations on the primary repo (e.g. epic FF merge).
	repoRoot string

	// shutdownRunner is the CommandRunner used by shutdownResetActiveBeads to run
	// `bd update` from the repo root. Initialised by New() to
	// &ExecCommandRunner{Dir: cfg.RepoRoot}; overridable in tests.
	shutdownRunner CommandRunner

	// beadsDir is the directory to watch for bead changes (defaults to protocol.BeadsDir)
	beadsDir string

	// panesDir is the directory to watch for pane context_pct files (defaults to ~/.oro/panes)
	panesDir string

	// signaledPanes tracks which panes have been signaled to avoid re-signaling
	signaledPanes map[string]bool

	// paneStates tracks per-pane restart state (lastRestartAt, restartCount, restarting flag)
	paneStates map[string]*paneState

	// startTime records when Run() was called (for uptime).
	startTime time.Time

	// cachedQueueDepth stores the last-known count from beads.Ready() in the assign loop.
	cachedQueueDepth int

	// nowFunc allows tests to control time.
	nowFunc func() time.Time

	// testUnlockHook, if non-nil, is called after releasing the lock in
	// registerWorker/handleQGFailure (before memory.ForPrompt). Tests use
	// this to inject a synchronization point that guarantees a concurrent
	// deletion occurs during the unlock window.
	testUnlockHook func()

	// testPanePollDone, if non-nil, is called after each pane monitor poll
	// iteration completes. Tests use this to synchronize without time.Sleep.
	testPanePollDone func()

	// escalationRetryInterval controls how often escalationRetryLoop fires.
	// Defaults to 2 minutes; tests may set this to a shorter value.
	escalationRetryInterval time.Duration

	// loopPanicBackoffFn, if non-nil, overrides the exponential backoff duration
	// used by the panic-restart wrappers in assignLoop/heartbeatLoop/escalationRetryLoop.
	// Intended for tests that need deterministic, fast backoff values.
	loopPanicBackoffFn func(restartCount int) time.Duration

	// tryAssignFn, if non-nil, is called instead of d.tryAssign inside
	// assignLoop and assignLoopPoll. Allows tests to inject panics or track calls.
	tryAssignFn func(ctx context.Context)

	// checkHeartbeatsFn, if non-nil, is called instead of d.checkHeartbeats inside
	// heartbeatLoop. Allows tests to inject panics or track calls.
	checkHeartbeatsFn func(ctx context.Context)

	// retryEscalationsFn, if non-nil, is called instead of d.retryPendingEscalations
	// inside escalationRetryLoop. Allows tests to inject panics or track calls.
	retryEscalationsFn func(ctx context.Context)

	// priorityBeads holds bead IDs that should be assigned before normal queue ordering.
	// Used by spawn-for directive to guarantee a specific bead gets the next idle worker.
	priorityBeads map[string]bool

	// pendingManagedIDs tracks worker IDs spawned by the dispatcher that have not yet
	// connected. When the worker connects and calls registerWorker, the ID is consumed
	// from this set and the trackedWorker.managed flag is set to true.
	pendingManagedIDs map[string]bool

	// unexpectedManagedExits counts managed workers removed by checkHeartbeats
	// (heartbeat or progress timeout). Used by reconcileScale to cap spawning:
	// managedCount + unexpectedManagedExits >= 2*target blocks scaleUp, preventing
	// runaway crash-respawn loops while keeping unmanaged workers invisible to scaling.
	// Reset to 0 by applyScaleDirective when the target changes.
	unexpectedManagedExits int

	// workerReadyCh is signaled (non-blocking) when a worker becomes idle without
	// a pending handoff. assignLoop and assignLoopPoll listen on it to call
	// tryAssign immediately instead of waiting for the next poll tick.
	// Buffered with capacity 1: multiple concurrent registrations coalesce into
	// a single tryAssign call.
	workerReadyCh chan struct{}

	// shutdownCh is closed when a shutdown directive is received, causing Run() to exit.
	shutdownCh chan struct{}
	// shutdownAuthorized gates whether SIGTERM is honored by the signal handler.
	shutdownAuthorized atomic.Bool

	// wg tracks all goroutines spawned by Run() to ensure graceful shutdown
	wg sync.WaitGroup

	// acceptSem limits concurrent connection handlers in acceptLoop
	acceptSem chan struct{}

	// lastStatusTime and lastStatusJSON implement throttling for status
	// directives. If a status request arrives within statusThrottleWindow
	// of the previous one, the cached JSON is returned without rebuilding.
	lastStatusTime time.Time
	lastStatusJSON string
}

// New creates a Dispatcher. It does NOT start listening or polling — call Run().
// Returns nil and an error if the Config is invalid after applying defaults.
// codeIdx may be nil to disable code search context injection.
func New(cfg Config, db *sql.DB, merger *merge.Coordinator, opsSpawner *ops.Spawner, beads BeadSource, wt WorktreeManager, esc Escalator, codeIdx CodeIndex) (*Dispatcher, error) {
	resolved := cfg.withDefaults()
	if err := resolved.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	// Determine the effective repo root for bd commands.
	// Falls back to the process working directory when RepoRoot is not set.
	rootDir, beadsDir := resolved.RepoRoot, resolved.BeadsDir
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}
	if beadsDir == "" {
		beadsDir = protocol.BeadsDir
	}
	memStore := memory.NewStore(db)
	memStore.SetEmbedder(memory.NewEmbedder())
	var estimator BeadEstimator
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		estimator = NewLLMEstimator(key)
	}
	return &Dispatcher{
		cfg:            resolved,
		db:             db,
		merger:         merger,
		ops:            opsSpawner,
		beads:          beads,
		worktrees:      wt,
		escalator:      esc,
		memories:       memStore,
		codeIndex:      codeIdx,
		repoRoot:       rootDir,
		shutdownRunner: &ExecCommandRunner{Dir: rootDir},
		acceptance:     &ShellAcceptanceRunner{},
		estimator:      estimator,
		qgRunner:       &ShellQGRunner{},
		state:          StateInert,
		targetWorkers:  resolved.InitialWorkers,
		WorkerPool: WorkerPool{
			workers: make(map[string]*trackedWorker),
		},
		BeadTracker: BeadTracker{
			rejectionCounts:  make(map[string]int),
			handoffCounts:    make(map[string]int),
			attemptCounts:    make(map[string]int),
			pendingHandoffs:  make(map[string]*pendingHandoff),
			qgStuckTracker:   make(map[string]*qgHistory),
			escalatedBeads:   make(map[string]bool),
			worktreeFailures: make(map[string]time.Time),
			exhaustedBeads:   make(map[string]bool),
			assigningBeads:   make(map[string]bool),
			mergingBeads:     make(map[string]bool),
			worktreeByBead:   make(map[string]string),
		},
		priorityBeads:     make(map[string]bool),
		pendingManagedIDs: make(map[string]bool),
		workerReadyCh:     make(chan struct{}, 1),
		shutdownCh:        make(chan struct{}),
		beadsDir:          beadsDir,
		panesDir:          filepath.Join(os.Getenv("HOME"), ".oro", "panes"),
		signaledPanes:     make(map[string]bool),
		paneStates:        make(map[string]*paneState),
		nowFunc:           time.Now,
		acceptSem:         make(chan struct{}, 100), // limit to 100 concurrent connection handlers
	}, nil
}

// loopPanicBackoff returns the exponential backoff duration for the n-th
// consecutive panic in a critical loop. The sequence is 1s, 2s, 4s, …
// capped at 30s. After 5 minutes without a panic the restart counter is
// reset, so the first panic after a quiet period always returns 1s.
// loopPanicBackoffFn may be set by tests to override this behaviour.
func (d *Dispatcher) loopPanicBackoff(n int) time.Duration {
	if d.loopPanicBackoffFn != nil {
		return d.loopPanicBackoffFn(n)
	}
	backoff := time.Duration(1<<uint(n-1)) * time.Second
	if backoff > 30*time.Second {
		return 30 * time.Second
	}
	return backoff
}

// handleLoopPanic is called inside a deferred recover in a critical loop
// iteration. It logs a goroutine_panic event, sleeps for the computed backoff,
// and returns true if the outer loop should exit (ctx cancelled / shutdown).
func (d *Dispatcher) handleLoopPanic(ctx context.Context, r interface{}, restartCount *int, lastPanicTime *time.Time) (exit bool) {
	now := d.nowFunc()
	if !lastPanicTime.IsZero() && now.Sub(*lastPanicTime) > 5*time.Minute {
		*restartCount = 0
	}
	*restartCount++
	*lastPanicTime = now
	backoff := d.loopPanicBackoff(*restartCount)
	_ = d.logEvent(ctx, "goroutine_panic", "dispatcher", "", "",
		fmt.Sprintf(`{"panic":%q,"stack":%q,"restart_count":%d}`,
			fmt.Sprint(r), string(debug.Stack()), *restartCount))
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-d.shutdownCh:
		return true
	case <-timer.C:
		return false
	}
}

// callTryAssign calls tryAssignFn if set (test injection), otherwise tryAssign.
func (d *Dispatcher) callTryAssign(ctx context.Context) {
	if d.tryAssignFn != nil {
		d.tryAssignFn(ctx)
		return
	}
	d.tryAssign(ctx)
}

// callCheckHeartbeats calls checkHeartbeatsFn if set (test injection), otherwise checkHeartbeats.
func (d *Dispatcher) callCheckHeartbeats(ctx context.Context) {
	if d.checkHeartbeatsFn != nil {
		d.checkHeartbeatsFn(ctx)
		return
	}
	d.checkHeartbeats(ctx)
}

// callRetryPendingEscalations calls retryEscalationsFn if set (test injection),
// otherwise retryPendingEscalations.
func (d *Dispatcher) callRetryPendingEscalations(ctx context.Context) {
	if d.retryEscalationsFn != nil {
		d.retryEscalationsFn(ctx)
		return
	}
	d.retryPendingEscalations(ctx)
}

// safeGo runs fn in a tracked goroutine with panic recovery. If fn panics,
// the panic and stack trace are logged to the events table and the goroutine
// exits cleanly instead of crashing the process. The goroutine is tracked
// by d.wg for graceful shutdown.
func (d *Dispatcher) safeGo(fn func()) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				_ = d.logEvent(context.Background(), "goroutine_panic", "dispatcher", "", "",
					fmt.Sprintf(`{"panic":%q,"stack":%q}`, fmt.Sprint(r), string(debug.Stack())))
			}
		}()
		fn()
	}()
}

// GetState returns the current dispatcher state.
func (d *Dispatcher) GetState() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// setState transitions the dispatcher to a new state.
func (d *Dispatcher) setState(s State) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = s
}

// ShutdownAuthorized returns the atomic flag that gates SIGTERM handling.
// The signal handler checks this flag to decide whether to honor SIGTERM.
func (d *Dispatcher) ShutdownAuthorized() *atomic.Bool {
	return &d.shutdownAuthorized
}

// SetQGRunner replaces the quality gate runner. Intended for use in tests
// where the real quality_gate.sh script is not available.
//
//oro:testonly
func (d *Dispatcher) SetQGRunner(r QGRunner) {
	d.qgRunner = r
}

// Run starts the Dispatcher event loop. It:
//  1. Initializes the SQLite schema
//  2. Starts the UDS listener
//  3. Polls for commands (directives) and ready beads
//  4. Monitors worker heartbeats
//
// Run blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.mu.Lock()
	d.startTime = d.nowFunc()
	d.mu.Unlock()

	// Init schema
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	// Prune orphaned worktrees from a previous crash. Errors are logged
	// but non-fatal — they must not prevent dispatcher startup.
	if pruneErr := d.worktrees.Prune(ctx); pruneErr != nil {
		_ = d.logEvent(ctx, "worktree_prune_failed", "dispatcher", "", "", pruneErr.Error())
	}

	// Restore in-memory tracking maps from active assignments persisted in SQLite.
	if err := d.restoreState(ctx); err != nil {
		return fmt.Errorf("restore state: %w", err)
	}

	// Reset any in_progress beads left over from a previous crash so they
	// become re-assignable. Non-fatal: errors are logged and startup continues.
	d.resetOrphanedBeads(ctx)

	// Clean up stale socket from a previous crash (if any). If another
	// dispatcher is actively listening, this returns an error so we don't
	// clobber it.
	if err := cleanStaleSocket(d.cfg.SocketPath); err != nil {
		return fmt.Errorf("stale socket check %s: %w", d.cfg.SocketPath, err)
	}

	// Start UDS listener
	ln, err := net.Listen("unix", d.cfg.SocketPath) //nolint:noctx // UDS bind is instant
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", d.cfg.SocketPath, err)
	}
	// Restrict socket permissions to 0600 (owner-only access) to prevent
	// unauthorized local users from connecting and impersonating workers.
	if err := os.Chmod(d.cfg.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket %s: %w", d.cfg.SocketPath, err)
	}
	d.mu.Lock()
	d.listener = ln
	d.mu.Unlock()

	// Accept connections
	d.safeGo(func() { d.acceptLoop(ctx, ln) })

	// Bead assignment loop
	d.safeGo(func() { d.assignLoop(ctx) })

	// Heartbeat monitor
	d.safeGo(func() { d.heartbeatLoop(ctx) })

	// Pane context monitor
	d.safeGo(func() { d.paneMonitorLoop(ctx) })

	// Escalation retry loop — re-deliver unacked escalations every 2 minutes.
	d.safeGo(func() { d.escalationRetryLoop(ctx) })

	select {
	case <-ctx.Done():
	case <-d.shutdownCh:
	}

	// Close listener first so acceptLoop will exit
	_ = ln.Close()

	// --- Graceful shutdown ---
	d.shutdownWithTimeout()

	return nil
}

// shutdownWithTimeout orchestrates graceful shutdown with a hard timeout.
// It wraps shutdownSequence in a context with 2*ShutdownTimeout to prevent
// indefinite hangs if workers never respond to PREPARE_SHUTDOWN.
func (d *Dispatcher) shutdownWithTimeout() {
	// Wrap shutdownSequence in a hard timeout of 2*ShutdownTimeout to prevent
	// indefinite hangs if workers never respond to PREPARE_SHUTDOWN.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*d.cfg.ShutdownTimeout)
	defer shutdownCancel()

	shutdownDone := make(chan struct{})
	go func() {
		// Phase 1: cancel ops/merges, Phase 2: stop workers, Phase 3: remove worktrees.
		d.shutdownSequence()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
		// Shutdown sequence completed successfully
	case <-shutdownCtx.Done():
		// Hard timeout exceeded — force-close all connections and clear worker map
		d.mu.Lock()
		for id, w := range d.workers {
			_ = w.conn.Close()
			delete(d.workers, id)
		}
		d.mu.Unlock()
	}

	// Wait for all goroutines to finish with a 5s timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines finished
	case <-time.After(5 * time.Second):
		// Timeout - goroutines did not finish in time
	}
}

// --- UDS server ---

// acceptLoop accepts new worker connections.
func (d *Dispatcher) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		// Acquire semaphore slot before spawning handler
		select {
		case d.acceptSem <- struct{}{}:
			d.safeGo(func() {
				defer func() { <-d.acceptSem }() // Release semaphore slot
				d.handleConn(ctx, conn)
			})
		case <-ctx.Done():
			_ = conn.Close()
			return
		}
	}
}

// handleConn reads line-delimited JSON messages from a worker connection.
func (d *Dispatcher) handleConn(ctx context.Context, conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	// Configure scanner to accept messages up to MaxMessageSize (1MB).
	// Default scanner max is 64KB which is too small for large payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)
	var workerID string

	defer func() {
		_ = conn.Close()
		if workerID != "" {
			d.mu.Lock()
			// Capture beadID before deleting worker
			var beadID string
			if w, exists := d.workers[workerID]; exists {
				beadID = w.beadID
			}
			delete(d.workers, workerID)
			d.mu.Unlock()

			// Clear tracking maps and reset bead to open so it can be reassigned.
			if beadID != "" {
				d.clearBeadTracking(beadID)
				_ = d.beads.Update(context.Background(), beadID, "open")
			}
		}
	}()

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		// Handle DIRECTIVE messages from manager (short-lived connection).
		if msg.Type == protocol.MsgDirective {
			d.handleDirectiveWithACK(ctx, conn, msg)
			return // Manager disconnects after receiving ACK
		}

		// Extract workerID from the first message that carries one.
		if workerID == "" {
			workerID = extractWorkerID(msg)
			if workerID != "" {
				d.registerWorker(workerID, conn)
			}
		}

		d.handleMessage(ctx, workerID, msg)
	}
}

// extractWorkerID pulls the worker ID from any message payload.
func extractWorkerID(msg protocol.Message) string {
	switch {
	case msg.Heartbeat != nil:
		return msg.Heartbeat.WorkerID
	case msg.Status != nil:
		return msg.Status.WorkerID
	case msg.Done != nil:
		return msg.Done.WorkerID
	case msg.Handoff != nil:
		return msg.Handoff.WorkerID
	case msg.ReadyForReview != nil:
		return msg.ReadyForReview.WorkerID
	case msg.Reconnect != nil:
		return msg.Reconnect.WorkerID
	case msg.ShutdownApproved != nil:
		return msg.ShutdownApproved.WorkerID
	default:
		return ""
	}
}

// registerWorker, consumePendingHandoff → worker_pool.go

// --- Message handling ---

// extractBeadID extracts the bead ID from a message payload if present.
func extractBeadID(msg protocol.Message) string {
	switch msg.Type {
	case protocol.MsgHeartbeat:
		if msg.Heartbeat != nil {
			return msg.Heartbeat.BeadID
		}
	case protocol.MsgStatus:
		if msg.Status != nil {
			return msg.Status.BeadID
		}
	case protocol.MsgDone:
		if msg.Done != nil {
			return msg.Done.BeadID
		}
	case protocol.MsgHandoff:
		if msg.Handoff != nil {
			return msg.Handoff.BeadID
		}
	case protocol.MsgReadyForReview:
		if msg.ReadyForReview != nil {
			return msg.ReadyForReview.BeadID
		}
	case protocol.MsgReconnect:
		if msg.Reconnect != nil {
			return msg.Reconnect.BeadID
		}
	}
	return ""
}

// handleMessage dispatches an incoming worker message.
func (d *Dispatcher) handleMessage(ctx context.Context, workerID string, msg protocol.Message) {
	// Extract and validate bead ID from message payloads that carry one.
	beadID := extractBeadID(msg)

	// Validate bead ID if present (empty is allowed for some message types like SHUTDOWN_APPROVED).
	if beadID != "" {
		if err := protocol.ValidateBeadID(beadID); err != nil {
			_ = d.logEvent(ctx, "invalid_bead_id", workerID, beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
			return
		}
	}

	switch msg.Type {
	case protocol.MsgHeartbeat:
		d.handleHeartbeat(ctx, workerID, msg)
	case protocol.MsgStatus:
		d.handleStatus(ctx, workerID, msg)
	case protocol.MsgDone:
		d.handleDone(ctx, workerID, msg)
	case protocol.MsgHandoff:
		d.handleHandoff(ctx, workerID, msg)
	case protocol.MsgReadyForReview:
		d.handleReadyForReview(ctx, workerID, msg)
	case protocol.MsgReconnect:
		d.handleReconnect(ctx, workerID, msg)
	case protocol.MsgShutdownApproved:
		d.handleShutdownApproved(ctx, workerID, msg)
	}
}

func (d *Dispatcher) handleHeartbeat(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Heartbeat == nil {
		return
	}
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.lastSeen = d.nowFunc()
		w.contextPct = msg.Heartbeat.ContextPct
		if w.state == protocol.WorkerBusy {
			w.lastProgress = d.nowFunc()
		}
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "heartbeat", workerID, msg.Heartbeat.BeadID, workerID, "")
}

func (d *Dispatcher) handleStatus(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Status == nil {
		return
	}
	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "status", workerID, msg.Status.BeadID, workerID,
		fmt.Sprintf(`{"state":%q,"result":%q}`, msg.Status.State, msg.Status.Result))
}

func (d *Dispatcher) handleDone(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Done == nil {
		return
	}
	beadID := msg.Done.BeadID

	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "done", workerID, beadID, workerID, "")

	// Reject merge if quality gate did not pass — retry or escalate.
	if !msg.Done.QualityGatePassed {
		d.handleQGFailure(ctx, workerID, beadID, msg.Done.QGOutput)
		return
	}

	// Get worktree from tracked worker
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, branch, epicID, targetBranch string
	var isEpicDecomp bool
	if ok {
		worktree = w.worktree
		branch = protocol.BranchPrefix + beadID
		epicID = w.epicID             // Capture epicID before clearing
		targetBranch = w.targetBranch // Capture targetBranch before clearing
		isEpicDecomp = w.isEpicDecomp
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	d.mu.Unlock()

	if !ok || worktree == "" {
		return
	}

	// Clear tracking state for completed bead.
	d.clearBeadTracking(beadID)

	if isEpicDecomp {
		// Epic decomposition complete — skip merge/close; just clean up the worktree.
		_ = d.logEvent(ctx, "epic_decomp_done", workerID, beadID, workerID, "")
		d.safeGo(func() {
			if err := d.worktrees.Remove(ctx, worktree); err != nil {
				_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
			}
		})
		return
	}

	// Merge in background
	d.safeGo(func() { d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, epicID, targetBranch) })
}

// handleQGFailure processes a quality-gate failure: checks for stuck detection
// (repeated identical outputs), increments the attempt counter, escalates if
// either cap is reached, or re-assigns with feedback.
func (d *Dispatcher) handleQGFailure(ctx context.Context, workerID, beadID, qgOutput string) {
	d.touchProgress(workerID)

	// Create typed QualityGateError for logging and potential error discrimination
	qgErr := &protocol.QualityGateError{
		BeadID:   beadID,
		WorkerID: workerID,
		Output:   qgOutput,
		Attempt:  0, // Will be updated after lock
	}

	_ = d.logEvent(ctx, "quality_gate_rejected", workerID, beadID, workerID,
		fmt.Sprintf(`{"reason":"QualityGatePassed=false","error":%q}`, qgErr.Error()))

	// Check stuck detection: hash QGOutput and track consecutive identical hashes.
	if d.isQGStuck(beadID, qgOutput) {
		_ = d.logEvent(ctx, "qg_stuck_detected", workerID, beadID, workerID,
			fmt.Sprintf(`{"repeated_count":%d}`, maxStuckCount))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("QG output repeated %d times — worker stuck", maxStuckCount), qgOutput), beadID, workerID)
		d.clearBeadTracking(beadID)
		return
	}

	d.mu.Lock()
	d.attemptCounts[beadID]++
	attempt := d.attemptCounts[beadID]
	qgErr.Attempt = attempt

	if attempt >= maxQGRetries {
		d.mu.Unlock()
		d.handleQGExhausted(ctx, workerID, beadID, qgOutput, attempt, qgErr)
		return
	}

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerReserved
	}
	d.mu.Unlock()

	d.persistBeadCount(ctx, beadID, "attempt_count", attempt)
	d.qgRetryWithReservation(ctx, workerID, beadID, qgOutput, attempt)
}

// withReservation executes a two-phase reservation pattern for worker re-assignment:
// Phase 1 (caller): Reserve the worker (set state to WorkerReserved) under lock.
// Phase 2 (this helper): Run ioFn outside lock, then verify reservation still valid
// and call assignFn under lock. The worker must already be in WorkerReserved state
// before calling this helper.
//
// ioFn performs I/O operations (e.g., memory retrieval) and returns context string.
// assignFn receives the worker and I/O result, updates state, and sends ASSIGN message.
// assignFn returns true if the assignment succeeded, false if it failed.
//
// Returns true if assignment succeeded, false if worker was disconnected or assignment failed.
func (d *Dispatcher) withReservation(workerID string, ioFn func() string, assignFn func(w *trackedWorker, memCtx string) bool) bool {
	// I/O phase: run outside lock to avoid blocking other operations.
	if d.testUnlockHook != nil {
		d.testUnlockHook()
	}
	memCtx := ioFn()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Phase 2: Verify reservation still valid, then call assignFn.
	w, ok := d.workers[workerID]
	if !ok || w.state != protocol.WorkerReserved {
		return false
	}

	return assignFn(w, memCtx)
}

// qgRetryWithReservation performs the I/O phase (memory retrieval) and
// completes the two-phase reservation for a QG retry. The worker must already
// be in protocol.WorkerReserved state before this is called.
func (d *Dispatcher) qgRetryWithReservation(ctx context.Context, workerID, beadID, qgOutput string, attempt int) {
	// Capture a snapshot for buildAssignPayload (I/O runs outside lock).
	// Always set model=Opus on the snapshot — QG retry always escalates.
	d.mu.Lock()
	var snap trackedWorker
	if w, ok := d.workers[workerID]; ok {
		snap = *w
	}
	snap.model = protocol.ModelOpus
	d.mu.Unlock()

	var payload *protocol.AssignPayload
	success := d.withReservation(workerID,
		// I/O function: fetch memories and build full payload outside lock.
		func() string {
			memCtx := d.fetchBeadMemories(ctx, beadID)
			payload = d.buildAssignPayload(ctx, &snap, attempt, qgOutput, memCtx)
			return memCtx
		},
		// Assign function: update state and send message under lock.
		func(w *trackedWorker, memCtx string) bool {
			// Escalate to opus if not already opus.
			if w.model != protocol.ModelOpus {
				w.model = protocol.ModelOpus
				d.attemptCounts[beadID] = 0 // Reset so opus gets fresh retries
			}
			payload.Model = w.model // sync with live escalated value

			if err := d.sendToWorker(w, protocol.Message{
				Type:   protocol.MsgAssign,
				Assign: payload,
			}); err != nil {
				// Worker is unreachable — release the bead back to the ready pool.
				w.state = protocol.WorkerIdle
				w.beadID = ""
				w.epicID = ""
				w.isEpicDecomp = false
				_ = d.logEvent(ctx, "qg_retry_send_failed", workerID, beadID, workerID,
					fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
				_ = d.completeAssignment(ctx, beadID)
				return false
			}
			w.state = protocol.WorkerBusy
			w.beadID = beadID
			w.lastProgress = d.nowFunc()
			return true
		},
	)

	// If assignment failed, clean up tracking state outside the lock.
	if !success {
		d.clearBeadTracking(beadID)
	}
}

// fetchBeadMemories retrieves relevant memories for a bead (best-effort).
// Returns empty string if memories are unavailable.
func (d *Dispatcher) fetchBeadMemories(ctx context.Context, beadID string) string {
	if d.memories == nil {
		return ""
	}
	searchTerm := beadID
	detail, showErr := d.beads.Show(ctx, beadID)
	if showErr != nil {
		// Log BeadNotFoundError for visibility (best-effort, non-fatal)
		bnfErr := &protocol.BeadNotFoundError{BeadID: beadID}
		_ = d.logEvent(ctx, "bead_lookup_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"error":%q}`, bnfErr.Error()))
	} else if detail != nil && detail.Title != "" {
		searchTerm = detail.Title
	}
	memCtx, _ := memory.ForPrompt(ctx, d.memories, nil, searchTerm, 0)
	return memCtx
}

// storeRejectionFeedback persists reviewer feedback in the rejection_history
// table (not memories), so rejections accumulate across retry cycles without
// polluting the memory search index. Best-effort: errors are silently ignored.
func (d *Dispatcher) storeRejectionFeedback(ctx context.Context, beadID, feedback string) {
	if d.memories == nil || feedback == "" {
		return
	}
	_ = d.memories.InsertRejection(ctx, beadID, "", feedback)
}

// buildRejectionMemoryContext stores the current reviewer feedback in
// rejection_history and returns a MemoryContext that combines:
//   - a "## Review Rejection Feedback" section with the current feedback
//   - prior rejections fetched from rejection_history via GetRejections
//   - general bead memories fetched from memories via ForPrompt
//
// This ensures the worker always sees why it was rejected even when the
// memory store has no prior entries.
func (d *Dispatcher) buildRejectionMemoryContext(ctx context.Context, beadID, feedback string) string {
	// Fetch general memories via ForPrompt.
	generalMemCtx := d.fetchBeadMemories(ctx, beadID)

	// Fetch prior rejections BEFORE storing the current one so "prior"
	// truly means prior and the current feedback doesn't appear twice.
	var priorCtx string
	if d.memories != nil {
		if rejections, err := d.memories.GetRejections(ctx, beadID); err == nil && len(rejections) > 0 {
			lines := make([]string, 0, len(rejections)+2)
			lines = append(lines, "## Prior Rejection History")
			for _, r := range rejections {
				lines = append(lines, fmt.Sprintf("- %s", r.Feedback))
			}
			priorCtx = strings.Join(lines, "\n")
		}
	}

	// Persist AFTER fetching so the current feedback is not duplicated.
	d.storeRejectionFeedback(ctx, beadID, feedback)

	// Always prepend the current rejection section.
	if feedback == "" {
		return generalMemCtx
	}

	rejectionSection := fmt.Sprintf("## Review Rejection Feedback\n%s", feedback)

	parts := []string{rejectionSection}
	if priorCtx != "" {
		parts = append(parts, priorCtx)
	}
	if generalMemCtx != "" {
		parts = append(parts, generalMemCtx)
	}
	return strings.Join(parts, "\n\n")
}

// mergeAndComplete runs merge.Coordinator.Merge and handles the result.
// guardMerge marks a bead as merging and returns a cleanup function.
// Used to prevent duplicate mergeAndComplete from external close (oro-x4x8).
func (d *Dispatcher) guardMerge(beadID string) func() {
	d.mu.Lock()
	d.mergingBeads[beadID] = true
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		delete(d.mergingBeads, beadID)
		d.mu.Unlock()
	}
}

// checkPreMergeQG runs the mutation quality gate before merging. It returns
// true when the gate passes and the merge should proceed. On failure or error
// it handles cleanup and returns false so the caller can return early.
func (d *Dispatcher) checkPreMergeQG(ctx context.Context, beadID, workerID, worktree string) bool {
	qgPassed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, false)
	if qgErr != nil {
		_ = d.logEvent(ctx, "pre_merge_qg_error", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, qgErr.Error()))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge QG error", qgErr.Error()), beadID, workerID)
		d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree)
		return false
	}
	if !qgPassed {
		_ = d.logEvent(ctx, "pre_merge_qg_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"output":%q}`, qgOutput))
		_ = d.beads.Update(ctx, beadID, "open")
		d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree)
		return false
	}
	return true
}

func (d *Dispatcher) mergeAndComplete(ctx context.Context, beadID, workerID, worktree, branch, epicID, targetBranch string) {
	defer d.guardMerge(beadID)()

	if !d.checkPreMergeQG(ctx, beadID, workerID, worktree) {
		return
	}

	result, err := d.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       beadID,
		TargetBranch: targetBranch,
	})
	if err != nil {
		var conflictErr *merge.ConflictError
		if errors.As(err, &conflictErr) {
			// Spawn ops agent to resolve conflict
			resultCh := d.ops.ResolveMergeConflict(ctx, ops.MergeOpts{
				BeadID:        beadID,
				Branch:        branch,
				Worktree:      worktree,
				ConflictFiles: conflictErr.Files,
				TargetBranch:  targetBranch,
			})
			d.safeGo(func() { d.handleMergeConflictResult(ctx, beadID, workerID, worktree, epicID, targetBranch, resultCh) })
			_ = d.logEvent(ctx, "merge_conflict", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"files":%q}`, conflictErr.Files))
			return
		}
		// Non-conflict merge failure — clean up worktree+branch+tracking first, then escalate (oro-4mu1.4).
		d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree)
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID, "merge failed", err.Error()), beadID, workerID)
		_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, err.Error())
		return
	}

	// Clean merge — close bead, complete assignment, remove worktree.
	_ = d.beads.Close(ctx, beadID, fmt.Sprintf("Merged: %s", result.CommitSHA))
	_ = d.completeAssignment(ctx, beadID)

	// Cancel any in-flight ops agents for this bead to prevent stale escalations.
	d.cancelOpsAgents(ctx, beadID, workerID, "bead_merged")

	_ = d.logEvent(ctx, "merged", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"sha":%q}`, result.CommitSHA))
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeComplete, beadID, "merged to main", result.CommitSHA), beadID, workerID)

	// Auto-close parent epic if all children are completed.
	d.autoCloseEpicIfComplete(ctx, workerID, epicID)
	d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree)

	// Trigger memory consolidation after every N bead completions.
	d.maybeConsolidateMemory(ctx)
}

// maybeConsolidateMemory increments the completion counter and triggers an
// async memory consolidation when the threshold is reached.
func (d *Dispatcher) maybeConsolidateMemory(ctx context.Context) {
	d.mu.Lock()
	d.completionsSinceConsolidate++
	shouldConsolidate := d.cfg.ConsolidateAfterN > 0 && d.completionsSinceConsolidate >= d.cfg.ConsolidateAfterN
	if shouldConsolidate {
		d.completionsSinceConsolidate = 0
	}
	d.mu.Unlock()

	if shouldConsolidate {
		d.safeGo(func() {
			merged, pruned, err := memory.Consolidate(ctx, d.memories, memory.ConsolidateOpts{})
			if err != nil {
				_ = d.logEvent(ctx, "memory_consolidation_failed", "dispatcher", "", "",
					fmt.Sprintf(`{"error":%q}`, err.Error()))
				return
			}
			_ = d.logEvent(ctx, "memory_consolidation", "dispatcher", "", "",
				fmt.Sprintf(`{"merged":%d,"pruned":%d}`, merged, pruned))
		})
	}
}

// removeWorktreeAndClearTracking removes a worktree, deletes the agent branch,
// and clears the tracking entry. Safe to call after successful merge completion.
// Logs but does not return errors.
func (d *Dispatcher) removeWorktreeAndClearTracking(ctx context.Context, beadID, workerID, worktree string) {
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
	if err := d.worktrees.DeleteBranch(ctx, branch); err != nil {
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
func (d *Dispatcher) tryCloseEpic(ctx context.Context, epicID, workerID string) {
	allClosed, err := d.beads.AllChildrenClosed(ctx, epicID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_auto_close_check_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	if !allClosed {
		return
	}

	// Fetch the epic's acceptance criteria to look for an executable Cmd:.
	detail, showErr := d.beads.Show(ctx, epicID)
	if showErr != nil {
		_ = d.logEvent(ctx, "epic_ac_fetch_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, showErr.Error()))
		// Fall back to count-based close so a transient Show error doesn't block.
		// Use DefaultBranch since we have no detail metadata to inspect.
		d.completeEpicClose(ctx, epicID, workerID, "All children completed (AC fetch failed)", d.cfg.DefaultBranch)
		return
	}

	// Determine the target branch for the epic FF merge. Prefer the value stored
	// in Metadata[MetaBranch]; fall back to DefaultBranch (typically "main").
	targetBranch := d.cfg.DefaultBranch
	if detail.Metadata != nil {
		if v, ok := detail.Metadata[MetaBranch]; ok {
			if s, ok := v.(string); ok && s != "" {
				targetBranch = s
			}
		}
	}

	cmd := parseAcceptanceCmd(detail.AcceptanceCriteria)
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

// ffMergeEpicBranch merges the epic branch into targetBranch and deletes it.
// When targetBranch equals cfg.DefaultBranch (the HEAD branch), it uses
// MergeFFOnly (git merge --ff-only) so the working tree is updated. For any
// other target it uses UpdateBranchRef (git update-ref), which advances the
// ref without requiring it to be checked out. Returns nil if the branch does
// not exist (no-op) or if the merge succeeds. Returns an error if the merge
// fails; in that case a rebase child bead is created so the epic will be
// retried when the rebase completes.
func (d *Dispatcher) ffMergeEpicBranch(ctx context.Context, epicID, workerID, targetBranch string) error {
	epicBranch := protocol.EpicBranchPrefix + epicID

	exists, err := d.worktrees.BranchExists(ctx, epicBranch)
	if err != nil {
		_ = d.logEvent(ctx, "epic_branch_check_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, err.Error()))
		// Treat check failure as branch absent: skip merge, allow close.
		return nil
	}
	if !exists {
		_ = d.logEvent(ctx, "epic_branch_absent", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q}`, epicBranch))
		return nil
	}

	var mergeErr error
	if targetBranch == d.cfg.DefaultBranch {
		// Target is the HEAD branch: use ff-only merge so the working tree advances.
		_, mergeErr = d.worktrees.MergeFFOnly(ctx, epicBranch, d.repoRoot)
	} else {
		// Target is not checked out: directly advance the ref.
		mergeErr = d.worktrees.UpdateBranchRef(ctx, targetBranch, epicBranch)
	}
	if mergeErr != nil {
		wrapped := fmt.Errorf("ff merge %s to %s: %w", epicBranch, targetBranch, mergeErr)
		_ = d.logEvent(ctx, "epic_ff_merge_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, wrapped.Error()))
		// Create a rebase child bead so the epic is retried after the rebase.
		_, _ = d.beads.Create(ctx,
			fmt.Sprintf("Rebase %s onto %s", epicBranch, targetBranch),
			"task", 1,
			fmt.Sprintf("FF merge of %s failed: %s. Rebase the epic branch onto %s and re-trigger close.", epicBranch, wrapped.Error(), targetBranch),
			epicID, "")
		return wrapped
	}

	_ = d.logEvent(ctx, "epic_ff_merged", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"branch":%q}`, epicBranch))

	if delErr := d.worktrees.DeleteBranch(ctx, epicBranch); delErr != nil {
		_ = d.logEvent(ctx, "epic_branch_delete_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, delErr.Error()))
	}
	return nil
}

// completeEpicClose FF-merges the epic branch to targetBranch, then closes the
// epic, cancels stale ops agents, logs the event, and escalates to the manager
// if the epic is currently focused. If the FF merge fails a rebase child bead
// is created and the close is skipped.
func (d *Dispatcher) completeEpicClose(ctx context.Context, epicID, workerID, reason, targetBranch string) {
	if err := d.ffMergeEpicBranch(ctx, epicID, workerID, targetBranch); err != nil {
		return
	}

	_ = d.beads.Close(ctx, epicID, reason)

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
}

// parseAcceptanceCmd extracts the Cmd: value from an acceptance criteria string.
// It supports both pipe-separated inline format ("... | Cmd: go test | ...")
// and line-per-field format. Returns "" if no Cmd: is present.
func parseAcceptanceCmd(ac string) string {
	for _, part := range strings.Split(ac, "|") {
		trimmed := strings.TrimSpace(part)
		if strings.HasPrefix(trimmed, "Cmd:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "Cmd:"))
		}
	}
	for _, line := range strings.Split(ac, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Cmd:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "Cmd:"))
		}
	}
	return ""
}

// handleMergeConflictResult waits for the ops merge-conflict result and acts on it.
func (d *Dispatcher) handleMergeConflictResult(ctx context.Context, beadID, workerID, worktree, epicID, targetBranch string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictResolved:
			_ = d.logEvent(ctx, "merge_conflict_resolved", "ops", beadID, workerID, result.Feedback)
			// Resolution succeeded — retry the merge.
			d.mergeAndComplete(ctx, beadID, workerID, worktree, protocol.BranchPrefix+beadID, epicID, targetBranch)
		default:
			// Resolution failed or unknown verdict — escalate.
			_ = d.logEvent(ctx, "merge_conflict_failed", "ops", beadID, workerID, result.Feedback)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID,
				"merge conflict resolution failed", result.Feedback), beadID, workerID)
		}
	}
}

// maxQGRetries is the number of quality-gate retry attempts before escalating
// to the Manager instead of re-assigning the bead to the worker.
const maxQGRetries = 3

// maxHandoffsBeforeDiagnosis is the number of ralph handoffs for the same bead
// before the dispatcher spawns a diagnosis agent instead of respawning.
const maxHandoffsBeforeDiagnosis = 2

func (d *Dispatcher) handleHandoff(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Handoff == nil {
		return
	}
	beadID := msg.Handoff.BeadID

	_ = d.logEvent(ctx, "handoff", workerID, beadID, workerID, "")

	// Persist learnings and decisions from the handoff payload as memories.
	d.persistHandoffContext(ctx, msg.Handoff)

	// Track handoff count per bead.
	d.mu.Lock()
	d.handoffCounts[beadID]++
	handoffCount := d.handoffCounts[beadID]
	d.mu.Unlock()

	d.persistBeadCount(ctx, beadID, "handoff_count", handoffCount)

	// Send SHUTDOWN to the old worker and capture worktree+model+epic context for respawn.
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, model, epicID, baseBranch, targetBranch string
	if ok {
		worktree = w.worktree
		model = w.model
		epicID = w.epicID             // Capture epicID before clearing
		baseBranch = w.baseBranch     // Capture baseBranch before clearing
		targetBranch = w.targetBranch // Capture targetBranch before clearing
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.state = protocol.WorkerShuttingDown // transient state — invisible to tryAssign
		w.beadID = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	d.mu.Unlock()

	if worktree == "" {
		return
	}

	// On 2nd+ handoff for the same bead, spawn diagnosis agent instead of respawning.
	if handoffCount >= maxHandoffsBeforeDiagnosis {
		d.handleHandoffExhaustion(ctx, beadID, workerID, handoffCount, worktree, msg)
		return
	}

	// Fetch bead details to get title and labels for memory search on respawn.
	var title string
	var labels []string
	if detail, err := d.beads.Show(ctx, beadID); err == nil {
		title = detail.Title
		labels = detail.Labels
	}

	d.respawnWorker(ctx, beadID, worktree, model, epicID, baseBranch, targetBranch, title, labels)
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
	var parentTitle, parentAC string
	if detail, showErr := d.beads.Show(ctx, beadID); showErr == nil {
		parentTitle = detail.Title
		parentAC = detail.AcceptanceCriteria
	}

	// Create a continuation bead to capture remaining work from the exhausted handoff.
	contTitle := fmt.Sprintf("Continue: %s (handoff exhausted)", beadID)
	contDesc := fmt.Sprintf("Handoff exhausted after %d handoffs for %s (%s).\n\nContext from last handoff:\n%s",
		handoffCount, beadID, parentTitle, msg.Handoff.ContextSummary)
	newID, createErr := d.beads.Create(ctx, contTitle, "task", 1, contDesc, beadID, parentAC)
	if createErr != nil {
		_ = d.logEvent(ctx, "continuation_bead_create_failed", "dispatcher", beadID, workerID, createErr.Error())
	} else {
		_ = d.logEvent(ctx, "continuation_bead_created", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"new_bead_id":%q}`, newID))
	}
}

// respawnWorker stores a pending handoff and spawns a fresh worker process.
func (d *Dispatcher) respawnWorker(ctx context.Context, beadID, worktree, model, epicID, baseBranch, targetBranch, title string, labels []string) {
	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		beadID:       beadID,
		epicID:       epicID,
		worktree:     worktree,
		baseBranch:   baseBranch,
		targetBranch: targetBranch,
		model:        model,
		title:        title,
		labels:       labels,
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "handoff_pending", "dispatcher", beadID, "", worktree)

	if d.procMgr != nil {
		newID := fmt.Sprintf("worker-handoff-%d", d.nowFunc().UnixNano())
		if _, err := d.procMgr.Spawn(newID); err != nil {
			_ = d.logEvent(ctx, "handoff_spawn_failed", "dispatcher", beadID, newID, err.Error())
		} else {
			_ = d.logEvent(ctx, "handoff_spawned", "dispatcher", beadID, newID, worktree)
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

// persistHandoffContext stores learnings and decisions from a HandoffPayload
// into the memory store for cross-session retrieval.
func (d *Dispatcher) persistHandoffContext(ctx context.Context, h *protocol.HandoffPayload) {
	if d.memories == nil {
		return
	}

	for _, learning := range h.Learnings {
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
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
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
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
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
			Content:    h.Summary.FormatContent(),
			Type:       "summary",
			Source:     "self_report",
			BeadID:     h.BeadID,
			WorkerID:   h.WorkerID,
			Confidence: 0.9,
		})
	}
}

func (d *Dispatcher) handleReadyForReview(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ReadyForReview == nil {
		return
	}
	beadID := msg.ReadyForReview.BeadID

	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "ready_for_review", workerID, beadID, workerID, "")

	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, targetBranch string
	if ok {
		w.state = protocol.WorkerReviewing
		worktree = w.worktree
		targetBranch = w.targetBranch
	}
	d.mu.Unlock()

	if worktree == "" {
		return
	}

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
	d.safeGo(func() { d.handleReviewResult(ctx, workerID, beadID, resultCh) })
}

// maxReviewRejections is the number of rejection cycles before escalating to
// the Manager instead of re-assigning the bead to the worker.
const maxReviewRejections = 2

// handleReviewResult waits for the ops review result and acts on it.
func (d *Dispatcher) handleReviewResult(ctx context.Context, workerID, beadID string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictApproved:
			_ = d.logEvent(ctx, "review_approved", "ops", beadID, workerID, result.Feedback)
			d.clearRejectionCount(beadID)

			// Capture anti-patterns from reviewer output
			patterns := ops.ExtractPatterns(result.Feedback)
			if len(patterns) > 0 {
				if err := d.appendReviewPatterns(ctx, beadID, workerID, patterns); err != nil {
					// Non-blocking: log the error but continue
					_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, err.Error())
				}
			}

			// Notify worker to proceed to DONE.
			d.mu.Lock()
			w, ok := d.workers[workerID]
			if ok {
				_ = d.sendToWorker(w, protocol.Message{
					Type: protocol.MsgReviewResult,
					ReviewResult: &protocol.ReviewResultPayload{
						Verdict:  "approved",
						Feedback: result.Feedback,
					},
				})
			}
			d.mu.Unlock()
		case ops.VerdictRejected:
			d.handleReviewRejection(ctx, workerID, beadID, result.Feedback)
		default:
			_ = d.logEvent(ctx, "review_failed", "ops", beadID, workerID, result.Feedback)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "review failed", result.Feedback), beadID, workerID)
			d.clearBeadTracking(beadID)
		}
	}
}

// handleReviewRejection processes a rejected review verdict: increments the
// rejection counter, escalates if the cap is reached, or re-assigns the bead
// to the worker with reviewer feedback using the two-phase reservation pattern.
func (d *Dispatcher) handleReviewRejection(ctx context.Context, workerID, beadID, feedback string) {
	_ = d.logEvent(ctx, "review_rejected", "ops", beadID, workerID, feedback)

	// Increment rejection counter and reserve worker in a single lock.
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

		_ = d.logEvent(ctx, "review_escalated", "ops", beadID, workerID,
			fmt.Sprintf(`{"rejections":%d,"feedback":%q}`, count, feedback))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("review rejected %d times", count), feedback), beadID, workerID)
		d.clearBeadTracking(beadID)

		// Kill the worker subprocess so a fresh process can take over.
		if d.procMgr != nil {
			_ = d.procMgr.Kill(workerID)
		}
		return
	}

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, wOK := d.workers[workerID]; wOK {
		w.state = protocol.WorkerReserved
	}
	d.mu.Unlock()

	// Capture snapshot for buildAssignPayload (I/O runs outside lock).
	// Set model=Opus on the snapshot only — w.model is NOT escalated on the live
	// worker for review rejection (preserving prior behaviour).
	d.mu.Lock()
	var snap trackedWorker
	if w, wOK := d.workers[workerID]; wOK {
		snap = *w
	}
	snap.model = protocol.ModelOpus
	d.mu.Unlock()

	var payload *protocol.AssignPayload
	d.withReservation(workerID,
		// I/O function: store rejection feedback and build full payload outside lock.
		// Persisting the feedback before ForPrompt ensures the current rejection reason
		// is retrievable in subsequent retry cycles via the memory store.
		func() string {
			memCtx := d.buildRejectionMemoryContext(ctx, beadID, feedback)
			payload = d.buildAssignPayload(ctx, &snap, count, feedback, memCtx)
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

// clearRejectionCount, clearHandoffCount, clearBeadTracking, pruneStaleTracking → bead_tracker.go

// validateReconnectBead checks if a bead is valid for reconnection.
// Returns true if bead is open and can be reconnected, false otherwise.
// oro-3xdf: Rejects closed or missing beads to prevent stuck workers.
func (d *Dispatcher) validateReconnectBead(ctx context.Context, beadID, workerID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		_ = d.logEvent(ctx, "reconnect_closed_bead_rejected", workerID, beadID, workerID,
			"rejecting reconnect: bead lookup failed (not found or error)")
		return false
	}
	if detail.Status == "closed" {
		_ = d.logEvent(ctx, "reconnect_closed_bead_rejected", workerID, beadID, workerID,
			"rejecting reconnect: bead is closed")
		return false
	}
	return true
}

// processReconnectUnderLock handles reconnection logic while holding d.mu.
// Caller must hold d.mu.
// oro-ovpc: Prevents bead stealing by checking for existing assignments.
func (d *Dispatcher) processReconnectUnderLock(ctx context.Context, w *trackedWorker, workerID, beadID, state string) {
	// Check if another worker is already assigned to this bead.
	var beadStolenFrom string
	for otherID, other := range d.workers {
		if otherID != workerID && other.beadID == beadID && other.state == protocol.WorkerBusy {
			beadStolenFrom = otherID
			break
		}
	}

	if beadStolenFrom != "" {
		_ = d.logEvent(ctx, "reconnect_bead_conflict", workerID, beadID, beadStolenFrom,
			fmt.Sprintf("worker %s already assigned to %s", beadStolenFrom, beadID))
	} else {
		w.beadID = beadID
	}

	w.lastSeen = d.nowFunc()
	if state == "running" && w.beadID == beadID {
		w.state = protocol.WorkerBusy
		w.lastProgress = d.nowFunc()
	} else {
		w.state = protocol.WorkerIdle
	}

	// Replay pending messages
	for _, pending := range w.pendingMsgs {
		_ = d.sendToWorker(w, pending)
	}
	w.pendingMsgs = nil
}

func (d *Dispatcher) handleReconnect(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Reconnect == nil {
		return
	}

	// Validate the reconnect payload to prevent unbounded buffered events
	if err := msg.Reconnect.Validate(); err != nil {
		_ = d.logEvent(ctx, "reconnect_rejected", workerID, msg.Reconnect.BeadID, workerID, err.Error())
		return
	}

	_ = d.logEvent(ctx, "reconnect", workerID, msg.Reconnect.BeadID, workerID, msg.Reconnect.State)

	beadID := msg.Reconnect.BeadID

	// oro-sydf: If BeadID is empty, the worker was idle before the network glitch.
	// Skip bead validation entirely — there is no bead to look up — and
	// transition the worker directly to idle so tryAssign can pick it up.
	if beadID == "" {
		d.mu.Lock()
		if w, ok := d.workers[workerID]; ok {
			w.state = protocol.WorkerIdle
			w.beadID = ""
			w.lastSeen = d.nowFunc()
		}
		d.mu.Unlock()
		return
	}

	// oro-3xdf: Check if the bead is valid (open, not closed/missing).
	// Do this outside the lock to avoid I/O while holding mutex.
	if !d.validateReconnectBead(ctx, beadID, workerID) {
		// oro-xj37: Transition worker to Idle so tryAssign can pick it up.
		// Without this, the worker stays in its previous state permanently.
		d.mu.Lock()
		if w, ok := d.workers[workerID]; ok {
			w.state = protocol.WorkerIdle
			w.beadID = ""
			w.lastSeen = d.nowFunc()
		}
		d.mu.Unlock()
		return
	}

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return
	}

	d.processReconnectUnderLock(ctx, w, workerID, beadID, msg.Reconnect.State)
	d.mu.Unlock()

	// Process any buffered events
	for _, buffered := range msg.Reconnect.BufferedEvents {
		d.handleMessage(ctx, workerID, buffered)
	}
}

func (d *Dispatcher) handleShutdownApproved(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ShutdownApproved == nil {
		return
	}

	_ = d.logEvent(ctx, "shutdown_approved", workerID, "", workerID, "")

	// Send hard SHUTDOWN to finalize
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var beadID string
	if ok {
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.shutdownApproved = true
		beadID = w.beadID // capture before clearing
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	d.mu.Unlock()

	// Requeue any in-flight bead so it can be reassigned.
	if beadID != "" {
		if err := d.beads.Update(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "scale_down_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		d.clearBeadTracking(beadID)
		_ = d.logEvent(ctx, "bead_requeued_scale_down", "dispatcher", beadID, workerID,
			`{"reason":"shutdown_approved"}`)
	}
}

// handleDirectiveWithACK handles a DIRECTIVE message from the manager and sends an ACK response.
// This is used for short-lived manager connections that send a directive and expect an ACK.
func (d *Dispatcher) handleDirectiveWithACK(ctx context.Context, conn net.Conn, msg protocol.Message) {
	if msg.Directive == nil {
		return
	}

	dir := protocol.Directive(msg.Directive.Op)
	args := msg.Directive.Args
	ack := protocol.ACKPayload{OK: true}

	if !dir.Valid() {
		ack.OK = false
		ack.Detail = "invalid directive"
	} else {
		detail, err := d.applyDirective(dir, args)
		if err != nil {
			ack.OK = false
			ack.Detail = err.Error()
		} else {
			_ = d.logEvent(ctx, "directive", "manager", "", "",
				fmt.Sprintf(`{"directive":%q,"args":%q}`, msg.Directive.Op, args))
			ack.Detail = detail
		}
	}

	// Send ACK response
	ackMsg := protocol.Message{
		Type: protocol.MsgACK,
		ACK:  &ack,
	}
	data, err := json.Marshal(ackMsg)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// GracefulShutdownWorker, shutdownWaitLoop, handleShutdownTimeout, checkShutdownApproved → worker_pool.go

// --- Priority queue / assignment loop ---

// assignLoop watches .beads/ directory and assigns work when files change.
// Falls back to 60s polling as a safety net.
func (d *Dispatcher) assignLoop(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Fallback to pure polling if fsnotify fails
		d.assignLoopPoll(ctx)
		return
	}
	defer func() { _ = watcher.Close() }()

	if err := watcher.Add(d.beadsDir); err != nil {
		// Fallback to pure polling if watch fails
		d.assignLoopPoll(ctx)
		return
	}

	// Fallback poll as safety net (default 60s)
	fallbackTicker := time.NewTicker(d.cfg.FallbackPollInterval)
	defer fallbackTicker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		if d.assignLoopIter(ctx, watcher, fallbackTicker, &restartCount, &lastPanicTime) {
			return
		}
	}
}

// assignLoopIter runs one select iteration of assignLoop with panic recovery.
// Returns true when the loop should exit cleanly (ctx cancelled or shutdown).
func (d *Dispatcher) assignLoopIter(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	fallbackTicker *time.Ticker,
	restartCount *int,
	lastPanicTime *time.Time,
) (exit bool) {
	defer func() {
		if r := recover(); r != nil {
			if d.handleLoopPanic(ctx, r, restartCount, lastPanicTime) {
				exit = true
			}
		}
	}()
	select {
	case <-ctx.Done():
		return true
	case <-d.shutdownCh:
		return true
	case <-watcher.Events:
		// File changed in .beads/ directory
		d.callTryAssign(ctx)
	case err := <-watcher.Errors:
		if err != nil {
			_ = d.logEvent(ctx, "watcher_error", "dispatcher", "", "", err.Error())
		}
	case <-d.workerReadyCh:
		// A new idle worker connected — assign immediately without waiting for poll.
		d.callTryAssign(ctx)
	case <-fallbackTicker.C:
		// Safety net poll
		d.callTryAssign(ctx)
	}
	return false
}

// assignLoopPoll is a fallback polling loop when fsnotify is unavailable.
// Each iteration is wrapped in a defer/recover so a panic inside tryAssign
// logs a goroutine_panic event and restarts the loop after exponential backoff.
func (d *Dispatcher) assignLoopPoll(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		exit := func() (shouldExit bool) {
			defer func() {
				if r := recover(); r != nil {
					if d.handleLoopPanic(ctx, r, &restartCount, &lastPanicTime) {
						shouldExit = true
					}
				}
			}()
			select {
			case <-ctx.Done():
				return true
			case <-d.shutdownCh:
				return true
			case <-d.workerReadyCh:
				// A new idle worker connected — assign immediately without waiting for poll.
				d.callTryAssign(ctx)
			case <-ticker.C:
				d.callTryAssign(ctx)
			}
			return false
		}()
		if exit {
			return
		}
	}
}

// sortBeadsByPriority sorts beads into four groups (all ties broken by priority):
//  1. spawn-for beads (explicit priorityBeads map)
//  2. focused epic children
//  3. non-epic standalone beads (Epic == "")
//  4. unfocused epic children, oldest epic first (lower ID = older)
//
// Returns a snapshot of priorityBeads for cleanup.
func (d *Dispatcher) sortBeadsByPriority(beads []protocol.Bead) map[string]bool {
	d.mu.Lock()
	epic := d.focusedEpic
	pbSnapshot := make(map[string]bool, len(d.priorityBeads))
	for id := range d.priorityBeads {
		pbSnapshot[id] = true
	}
	d.mu.Unlock()

	group := func(b protocol.Bead) int {
		if pbSnapshot[b.ID] {
			return 0 // spawn-for
		}
		if epic != "" && b.Epic == epic {
			return 1 // focused epic child
		}
		if b.Epic == "" {
			return 2 // non-epic standalone
		}
		return 3 // unfocused epic child
	}

	sort.SliceStable(beads, func(i, j int) bool {
		bi, bj := beads[i], beads[j]
		gi, gj := group(bi), group(bj)
		if gi != gj {
			return gi < gj
		}
		// Within group 4: finish oldest epics first (lower ID = older).
		if gi == 3 && bi.Epic != bj.Epic {
			return bi.Epic < bj.Epic
		}
		return bi.Priority < bj.Priority
	})
	return pbSnapshot
}

// tryAssign attempts to assign ready beads to idle workers.
func (d *Dispatcher) tryAssign(ctx context.Context) {
	// Only assign in running state.
	if d.GetState() != StateRunning {
		return
	}

	// Detect beads closed externally while a worker is assigned and clean up.
	d.checkClosedBeadAssignments(ctx)

	// Reconcile worker pool size (spawns/removes workers to match target).
	d.reconcileScale()

	// Find idle workers and count total workers.
	d.mu.Lock()
	var idle []*trackedWorker
	totalWorkers := 0
	for _, w := range d.workers {
		totalWorkers++
		if w.state == protocol.WorkerIdle {
			idle = append(idle, w)
		}
	}
	d.mu.Unlock()

	// Poll for ready beads.
	allBeads, err := d.beads.Ready(ctx)
	if err != nil {
		return
	}

	// Cache queue depth for status reporting.
	d.mu.Lock()
	d.cachedQueueDepth = len(allBeads)
	d.mu.Unlock()

	beads := d.filterAssignable(ctx, allBeads)

	pbSnapshot := d.sortBeadsByPriority(beads)

	// Auto-scale: if we have assignable beads but no idle workers, scale up to MaxWorkers.
	d.maybeAutoScale(ctx, len(beads), len(idle))

	// Priority contention is now handled by the preemption system (oro-wofg).
	// Escalating to the manager is noisy and unhelpful.
	// if len(idle) == 0 && totalWorkers > 0 {
	// 	d.checkPriorityContention(ctx, beads, totalWorkers)
	// 	return
	// }
	if len(idle) == 0 {
		return
	}

	// Assign beads to idle workers. Advance the idle cursor only when a worker is
	// actually claimed — epics skipped in assignBead leave the worker idle so the
	// next bead in the list can still be paired with it.
	idleIdx := 0
	for _, bead := range beads {
		if idleIdx >= len(idle) {
			break
		}
		_ = d.assignBead(ctx, idle[idleIdx], bead)
		// Advance idle cursor and clean up priority snapshot under a single lock.
		d.mu.Lock()
		if idle[idleIdx].state != protocol.WorkerIdle {
			idleIdx++
		}
		if pbSnapshot[bead.ID] {
			delete(d.priorityBeads, bead.ID)
		}
		d.mu.Unlock()
	}
}

// checkClosedBeadAssignments detects beads that have been closed externally
// while a worker is still assigned to them. For each such bead it clears the
// in-memory worker state, completes the DB assignment record, and sends a
// SHUTDOWN signal so the worker exits cleanly. Called on every assign-loop
// tick, ensuring cleanup occurs within one tick interval of external closure.
func (d *Dispatcher) checkClosedBeadAssignments(ctx context.Context) {
	// Collect (workerID, beadID) pairs for all busy/reserved workers under lock.
	type assignment struct {
		workerID string
		beadID   string
	}
	d.mu.Lock()
	var active []assignment
	for _, w := range d.workers {
		if w.beadID != "" && (w.state == protocol.WorkerBusy || w.state == protocol.WorkerReserved) {
			active = append(active, assignment{w.id, w.beadID})
		}
	}
	d.mu.Unlock()

	for _, a := range active {
		d.handleClosedAssignment(ctx, a.workerID, a.beadID)
	}
}

// handleClosedAssignment checks whether a single bead has been closed
// externally and, if so, shuts down the assigned worker and triggers cleanup.
func (d *Dispatcher) handleClosedAssignment(ctx context.Context, workerID, beadID string) {
	// Skip beads with in-flight merges to prevent duplicate mergeAndComplete (oro-x4x8).
	d.mu.Lock()
	merging := d.mergingBeads[beadID]
	d.mu.Unlock()
	if merging {
		return
	}

	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		// Transient lookup error — don't kill the worker, retry next cycle.
		return
	}
	switch {
	case detail == nil:
		// Bead not found in source — treat as externally removed.
		_ = d.logEvent(ctx, "bead_closed_externally", "dispatcher", beadID, workerID,
			"bead not found in source; sending shutdown")
	case detail.Status == "closed":
		_ = d.logEvent(ctx, "bead_closed_externally", "dispatcher", beadID, workerID,
			"bead closed while worker assigned; sending shutdown")
	default:
		// Bead exists and is not explicitly closed — keep worker assigned.
		return
	}

	// Send SHUTDOWN, capture worktree, and clear worker state under lock.
	var worktree, epicID, targetBranch string
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok && w.beadID == beadID {
		worktree = w.worktree
		epicID = w.epicID             // Capture epicID before clearing
		targetBranch = w.targetBranch // Capture targetBranch before clearing
		if err := d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown}); err != nil {
			// Socket is dead — remove worker entirely to prevent
			// tryAssign from cycling beads through a zombie (oro-e2jk).
			_ = w.conn.Close()
			delete(d.workers, workerID)
		} else {
			w.state = protocol.WorkerShuttingDown // transient state — invisible to tryAssign
			w.beadID = ""
			w.epicID = ""
			w.worktree = ""
		}
	}
	d.mu.Unlock()

	// If the worker had a worktree, attempt to merge any commits on the
	// agent branch before cleaning up. mergeAndComplete handles assignment
	// completion, tracking cleanup, and worktree removal internally.
	if worktree != "" {
		branch := protocol.BranchPrefix + beadID
		d.safeGo(func() { d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, epicID, targetBranch) })
	} else {
		// No worktree — just complete the DB record and clear tracking.
		_ = d.completeAssignment(ctx, beadID)
		d.clearBeadTracking(beadID)
	}
}

// filterAssignable returns beads eligible for assignment: excludes closed beads,
// beads with status in_progress or blocked, beads with recent worktree creation
// failures (within cooldown window), beads currently in-flight (assigningBeads),
// beads with unresolved blocking dependencies, and beads whose agent branch is
// already merged to main.
// Epics are allowed through; assignBead performs the HasChildren check.
func (d *Dispatcher) filterAssignable(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	now := d.nowFunc()
	d.mu.Lock()

	// Build the set of open bead IDs for dependency resolution.
	// A bead is "open" (can block others) if it is not closed.
	openBeadIDs := make(map[string]bool, len(allBeads))
	for _, b := range allBeads {
		if b.Status != "closed" {
			openBeadIDs[b.ID] = true
		}
	}

	// Collect bead IDs already assigned to busy/reserved workers.
	activeBeads := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" && w.state != protocol.WorkerIdle {
			activeBeads[w.beadID] = true
		}
	}

	// First pass: cheap in-memory filters (no I/O). Lock is held.
	candidates := make([]protocol.Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if d.isBeadAssignable(b, now, activeBeads) && !hasUnresolvedBlockingDep(b, openBeadIDs) {
			candidates = append(candidates, b)
		}
	}
	d.mu.Unlock()

	// Second pass: check whether the agent branch is already merged to main.
	// This requires a git subprocess, so it runs outside the lock.
	out := make([]protocol.Bead, 0, len(candidates))
	for _, b := range candidates {
		if d.isBranchMerged(ctx, b.ID) {
			_ = d.beads.Close(ctx, b.ID, "branch already merged to main")
			_ = d.logEvent(ctx, "bead_branch_already_merged", "dispatcher", b.ID, "", "")
			continue
		}
		out = append(out, b)
	}
	return out
}

// isBranchMerged reports whether agent/<beadID> is an ancestor of main,
// meaning the branch has already been merged. Uses git merge-base --is-ancestor.
// Returns false when the branch does not exist, the git command fails, or the
// bead has never been assigned (no branch exists yet).
func (d *Dispatcher) isBranchMerged(ctx context.Context, beadID string) bool {
	branch := protocol.BranchPrefix + beadID // "agent/<beadID>"
	_, err := d.shutdownRunner.Run(ctx, "git", "merge-base", "--is-ancestor", branch, "main")
	return err == nil
}

// isBeadAssignable reports whether a bead passes all assignment filters.
// Caller must hold d.mu. activeBeads maps bead IDs held by non-idle workers.
// Epics are allowed through here; HasChildren is checked in assignBead (requires I/O).
func (d *Dispatcher) isBeadAssignable(b protocol.Bead, now time.Time, activeBeads map[string]bool) bool {
	if b.Status == "closed" {
		return false
	}
	// oro-wee1: Filter out beads with status in_progress (human-owned) or blocked.
	// Only beads with status "open" or empty (defaulting to open) should be assignable.
	if b.Status == "in_progress" || b.Status == "blocked" {
		return false
	}
	if failedAt, ok := d.worktreeFailures[b.ID]; ok && now.Sub(failedAt) < worktreeFailureCooldown {
		return false
	}
	if activeBeads[b.ID] {
		return false
	}
	// oro-30o: Skip beads currently in-flight (assigningBeads set but worker not yet
	// transitioned to Busy). This prevents the scale-up duplicate assignment window
	// where a newly connected worker picks up a bead already being assigned to
	// another worker, causing a worktree_error (branch already exists).
	if d.assigningBeads[b.ID] {
		return false
	}
	// Skip beads currently being merged and closed. There's a race window
	// between mergeAndComplete setting mergingBeads and bd close propagating
	// the status change — without this check the bead appears "ready" to
	// bd ready --json and gets re-assigned, causing bead_closed_externally spam.
	if d.mergingBeads[b.ID] {
		return false
	}
	if d.exhaustedBeads[b.ID] {
		return false
	}
	return true
}

// hasUnresolvedBlockingDep reports whether bead b has at least one unresolved
// blocking dependency. A dependency is blocking when its Type is "blocks" or
// "conditional-blocks" AND its DependsOnID is present in openBeadIDs (i.e. not
// yet closed). Parent-child deps and dangling deps (DependsOnID absent from
// openBeadIDs) are never considered blocking.
func hasUnresolvedBlockingDep(b protocol.Bead, openBeadIDs map[string]bool) bool {
	for _, dep := range b.Dependencies {
		if dep.Type != "blocks" && dep.Type != "conditional-blocks" {
			continue
		}
		if openBeadIDs[dep.DependsOnID] {
			return true
		}
	}
	return false
}

// recordAssignmentFailure marks a bead as having failed assignment (worktree
// creation error, missing acceptance criteria, etc). The bead will be skipped
// for worktreeFailureCooldown to prevent infinite retry loops.
func (d *Dispatcher) recordAssignmentFailure(beadID string) {
	d.mu.Lock()
	d.worktreeFailures[beadID] = d.nowFunc()
	d.mu.Unlock()
}

// checkPriorityContention is no longer used. Priority contention is now handled
// by the preemption system (oro-wofg). Removed in oro-721i.

// assignBead creates a worktree and sends ASSIGN to the worker.
// If memories exist for the bead's description, they are included in the
// AssignPayload.MemoryContext field for cross-session continuity.
// checkBeadReady validates bead ID and acceptance criteria. Returns title,
// acceptance, and true if the bead is ready for assignment. Escalates to
// manager if AC is missing.
func (d *Dispatcher) checkBeadReady(ctx context.Context, bead protocol.Bead, workerID string) (title, acceptance string, ok bool) {
	if err := protocol.ValidateBeadID(bead.ID); err != nil {
		_ = d.logEvent(ctx, "invalid_bead_id", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return "", "", false
	}
	title, acceptance, status := d.lookupBeadDetail(ctx, bead.ID, workerID)
	if status == "closed" || status == "in_progress" {
		_ = d.logEvent(ctx, "bead_not_ready_before_assign", "dispatcher", bead.ID, workerID,
			fmt.Sprintf("bead status %q — skipping assignment", status))
		return title, acceptance, false
	}
	if acceptance == "" {
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMissingAC, bead.ID, "no acceptance criteria — spawning AC writer", ""), bead.ID, workerID)
		d.recordAssignmentFailure(bead.ID) // 60-second cooldown prevents re-triggering
		return title, "", false            // skip assignment this cycle
	}
	if modules := protocol.CountDistinctModules(acceptance); modules > 2 {
		// Epics are expected to span multiple modules; skip the oversized check.
		// Also skip if the bead already has children — it was decomposed externally.
		isEpic := bead.Type == "epic"
		hasChildren, _ := d.beads.HasChildren(ctx, bead.ID)
		if !isEpic && !hasChildren {
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscOversizedBead, bead.ID,
				fmt.Sprintf("touches %d modules — needs decomposition", modules), ""), bead.ID, workerID)
			d.recordAssignmentFailure(bead.ID)
			return title, "", false
		}
	}
	return title, acceptance, true
}

func (d *Dispatcher) assignBead(ctx context.Context, w *trackedWorker, bead protocol.Bead) error { //nolint:funlen,gocognit,gocyclo // orchestration logic, splitting would obscure flow
	if strings.TrimSpace(bead.ID) == "" {
		return fmt.Errorf("assignBead: empty bead ID")
	}

	title, acceptance, ok := d.checkBeadReady(ctx, bead, w.id)
	if !ok {
		return nil
	}

	// Epic routing: check children before proceeding (requires I/O, must be outside lock).
	isEpicDecomp, skip := d.checkEpicAssignable(ctx, bead, w.id)
	if skip {
		return nil
	}

	// Atomically claim this bead for assignment (oro-ptp2: prevents race condition).
	// If another concurrent assignBead call already claimed it, abort.
	d.mu.Lock()
	if d.assigningBeads[bead.ID] {
		// Another assignment is already in progress for this bead
		d.mu.Unlock()
		_ = d.logEvent(ctx, "assignment_race_detected", "dispatcher", bead.ID, w.id,
			"bead already being assigned by another worker")
		return nil
	}
	// Belt-and-suspenders: check if a worker already completed assignment for
	// this bead. assigningBeads is ephemeral (cleared on completion), so a slow
	// goroutine could arrive after the flag is gone. This check catches that
	// case by inspecting the persistent worker state under the same lock.
	for _, w2 := range d.workers {
		if w2.beadID == bead.ID && w2.state == protocol.WorkerBusy {
			d.mu.Unlock()
			_ = d.logEvent(ctx, "assignment_race_detected", "dispatcher", bead.ID, w.id,
				fmt.Sprintf("bead already assigned to worker %s", w2.id))
			return nil
		}
	}
	d.assigningBeads[bead.ID] = true
	delete(d.escalatedBeads, bead.ID)
	d.mu.Unlock()

	// Mark bead as in_progress BEFORE worktree creation.
	// This updates external state so other dispatchers see the bead is claimed.
	if err := d.beads.Update(ctx, bead.ID, "in_progress"); err != nil {
		_ = d.logEvent(ctx, "update_status_failed", "dispatcher", bead.ID, w.id, err.Error())
		d.recordAssignmentFailure(bead.ID)
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return nil
	}

	// Check if a worktree already exists for this bead (from previous worker timeout/kill).
	// If it exists, reuse it to preserve uncommitted changes (oro-1eo8).
	d.mu.Lock()
	existingWorktree := d.worktreeByBead[bead.ID]
	d.mu.Unlock()

	var worktree, branch string
	var err error
	// Resolve the base/target branch for this bead.
	// resolveEpicBranch walks the parent chain to find the actual epic ancestor —
	// bead.Epic maps to the JSON "parent" field and may point to a non-epic bead.
	baseBranch, resolvedEpicID, resolveErr := resolveEpicBranch(ctx, d.beads, bead.Epic, d.cfg.DefaultBranch)
	if resolveErr != nil {
		_ = d.logEvent(ctx, "epic_branch_resolve_error", "dispatcher", bead.ID, w.id, resolveErr.Error())
		d.recordAssignmentFailure(bead.ID)
		_ = d.beads.Update(ctx, bead.ID, "ready")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return nil
	}
	if baseBranch != "main" {
		// Escalate if the epic branch does not exist yet.
		exists, beErr := d.worktrees.BranchExists(ctx, baseBranch)
		if beErr != nil || !exists {
			reason := fmt.Sprintf("epic branch %q not found for bead %s", baseBranch, bead.ID)
			if beErr != nil {
				reason = fmt.Sprintf("checking epic branch %q: %v", baseBranch, beErr)
			}
			_ = d.logEvent(ctx, "epic_branch_missing", "dispatcher", bead.ID, w.id, reason)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, bead.ID, "epic branch missing", reason), bead.ID, w.id)
			_ = d.beads.Update(ctx, bead.ID, "ready")
			d.mu.Lock()
			delete(d.assigningBeads, bead.ID)
			d.mu.Unlock()
			return nil
		}
	}
	targetBranch := baseBranch

	if existingWorktree != "" && !d.worktrees.Exists(ctx, existingWorktree) {
		// Stale entry — the worktree was removed externally after the previous
		// worker timed out. Clear it so we fall through to create a fresh one.
		stalePath := existingWorktree
		existingWorktree = ""
		d.mu.Lock()
		delete(d.worktreeByBead, bead.ID)
		d.mu.Unlock()
		_ = d.logEvent(ctx, "stale_worktree_cleared", "dispatcher", bead.ID, w.id,
			fmt.Sprintf(`{"stale_path":%q}`, stalePath))
	}

	if existingWorktree != "" {
		// Reuse existing worktree.
		worktree = existingWorktree
		branch = protocol.BranchPrefix + bead.ID
		_ = d.logEvent(ctx, "worktree_reused", "dispatcher", bead.ID, w.id,
			fmt.Sprintf(`{"worktree":%q}`, worktree))
	} else {
		// Create new worktree, branching from the resolved base branch.
		worktree, branch, err = d.worktrees.Create(ctx, bead.ID, baseBranch)
		if err != nil {
			_ = d.logEvent(ctx, "worktree_error", "dispatcher", bead.ID, w.id, err.Error())
			d.recordAssignmentFailure(bead.ID)
			// Revert status since assignment failed
			_ = d.beads.Update(ctx, bead.ID, "ready")
			d.mu.Lock()
			delete(d.assigningBeads, bead.ID)
			d.mu.Unlock()
			return nil
		}
		// Store new worktree for potential reuse on respawn (oro-1eo8).
		d.mu.Lock()
		d.worktreeByBead[bead.ID] = worktree
		d.mu.Unlock()
	}

	_ = d.createAssignment(ctx, bead.ID, w.id, worktree)
	_ = d.logEvent(ctx, "assign", "dispatcher", bead.ID, w.id,
		fmt.Sprintf(`{"worktree":%q,"branch":%q}`, worktree, branch))

	var memCtx string
	if d.memories != nil {
		memCtx, _ = memory.ForPrompt(ctx, d.memories, nil, buildSearchQuery(bead.Title, bead.Labels), 0)
	}
	var codeCtx string
	if d.codeIndex != nil {
		ctx5s, cancel5s := context.WithTimeout(ctx, 5*time.Second)
		defer cancel5s()
		results, _ := d.codeIndex.Search(ctx5s, bead.Title, 5)
		if len(results) > 0 {
			codeCtx = formatSearchResults(results)
		}
	}

	// Call estimator if bead needs estimation (no explicit model and no estimate yet)
	if bead.Model == "" && bead.EstimatedMinutes == 0 && d.estimator != nil {
		bead.EstimatedMinutes = d.estimator.Estimate(ctx, bead.Title, acceptance)
	}

	resolvedModel := bead.ResolveModel()
	if isEpicDecomp {
		resolvedModel = protocol.ModelOpus
	}
	d.mu.Lock()
	w.state = protocol.WorkerBusy
	w.beadID = bead.ID
	w.epicID = resolvedEpicID // actual epic ancestor ID for auto-close on merge
	w.isEpicDecomp = isEpicDecomp
	w.worktree = worktree
	w.baseBranch = baseBranch
	w.targetBranch = targetBranch
	w.model = resolvedModel
	w.lastProgress = d.nowFunc()
	err = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:              bead.ID,
			Worktree:            worktree,
			Model:               resolvedModel,
			MemoryContext:       memCtx,
			CodeSearchContext:   codeCtx,
			Title:               title,
			AcceptanceCriteria:  acceptance,
			IsEpicDecomposition: isEpicDecomp,
			TargetBranch:        targetBranch,
		},
	})
	if err != nil {
		// Socket is dead — remove worker entirely to prevent tryAssign from
		// cycling beads through a zombie (oro-e2jk). Same fix as
		// bead_closed_externally path.
		_ = w.conn.Close()
		delete(d.workers, w.id)
		delete(d.worktreeByBead, bead.ID) // clear stale entry so next assignment creates a fresh worktree (oro-fhn3)
	}
	// Clear assignment-in-progress flag now that worker state is updated (oro-ptp2).
	delete(d.assigningBeads, bead.ID)
	d.mu.Unlock()
	if err != nil {
		_ = d.worktrees.Remove(ctx, worktree)
		_ = d.logEvent(ctx, "worktree_cleanup", "dispatcher", bead.ID, w.id, err.Error())
	}
	return nil
}

// checkEpicAssignable determines whether an epic bead should proceed to assignment.
// Returns (isEpicDecomp=true, skip=false) when the epic has no children and should
// be assigned for decomposition. Returns (false, true) to skip in all other cases:
// epic with open children (not ready), epic with all children closed (auto-closed here),
// or any HasChildren/AllChildrenClosed error. For non-epic beads both values are false.
func (d *Dispatcher) checkEpicAssignable(ctx context.Context, bead protocol.Bead, workerID string) (isEpicDecomp, skip bool) {
	if bead.Type != "epic" {
		return false, false
	}
	hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_has_children_error", "dispatcher", bead.ID, workerID, err.Error())
		return false, true
	}
	if !hasChildren {
		return true, false // no children → assign for decomposition
	}
	// Epic has children: auto-close if all done, otherwise skip.
	allClosed, err := d.beads.AllChildrenClosed(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_all_children_closed_error", "dispatcher", bead.ID, workerID, err.Error())
		return false, true
	}
	if allClosed {
		if closeErr := d.beads.Close(ctx, bead.ID, "All children completed"); closeErr != nil {
			_ = d.logEvent(ctx, "epic_auto_close_failed", "dispatcher", bead.ID, workerID, closeErr.Error())
		} else {
			_ = d.logEvent(ctx, "epic_auto_closed_on_assign", "dispatcher", bead.ID, workerID, "")
		}
	}
	return false, true
}

// lookupBeadDetail retrieves the title, acceptance criteria, and status for a bead (best-effort).
func (d *Dispatcher) lookupBeadDetail(ctx context.Context, beadID, workerID string) (title, acceptance, status string) {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		bnfErr := &protocol.BeadNotFoundError{BeadID: beadID}
		_ = d.logEvent(ctx, "bead_lookup_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, bnfErr.Error()))
		return "", "", ""
	}
	if detail != nil {
		return detail.Title, detail.AcceptanceCriteria, detail.Status
	}
	return "", "", ""
}

// workerStatus holds per-worker health info for the enriched status response.
type workerStatus struct {
	ID                string  `json:"id"`
	State             string  `json:"state"`
	BeadID            string  `json:"bead_id,omitempty"`
	LastProgressSecs  float64 `json:"last_progress_secs"`
	LastHeartbeatSecs float64 `json:"last_heartbeat_secs"`
	ContextPct        int     `json:"context_pct"`
}

// statusResponse is the JSON structure returned by the status directive.
type statusResponse struct {
	State       string            `json:"state"`
	PID         int               `json:"pid"`
	WorkerCount int               `json:"worker_count"`
	QueueDepth  int               `json:"queue_depth"`
	Assignments map[string]string `json:"assignments"`
	FocusedEpic string            `json:"focused_epic,omitempty"`

	// Enriched fields (oro-vii8.1)
	Workers             []workerStatus `json:"workers"`
	ActiveCount         int            `json:"active_count"`
	IdleCount           int            `json:"idle_count"`
	TargetCount         int            `json:"target_count"`
	UptimeSeconds       float64        `json:"uptime_seconds"`
	PendingHandoffCount int            `json:"pending_handoff_count"`
	AttemptCounts       map[string]int `json:"attempt_counts,omitempty"`
	ProgressTimeoutSecs float64        `json:"progress_timeout_secs"`
}

// applyDirective transitions the dispatcher state machine and returns a detail
// string for the ACK response. Returns an error for invalid args (e.g. scale).
//
//nolint:gocyclo // dispatcher routing function - complexity is inherent to the pattern
func (d *Dispatcher) applyDirective(dir protocol.Directive, args string) (string, error) {
	switch dir {
	case protocol.DirectiveScale:
		return d.applyScaleDirective(args)
	case protocol.DirectiveKillWorker:
		return d.applyKillWorker(args)
	case protocol.DirectiveSpawnFor:
		return d.applySpawnFor(args)
	case protocol.DirectiveRestartWorker:
		return d.applyRestartWorker(args)
	case protocol.DirectivePreempt:
		return d.applyPreempt(args)
	case protocol.DirectivePendingEscalations:
		return d.applyPendingEscalations()
	case protocol.DirectiveAckEscalation:
		return d.applyAckEscalation(args)
	case protocol.DirectiveHealth:
		return d.applyHealth()
	case protocol.DirectiveWorkerLogs:
		return d.applyWorkerLogs(args)
	case protocol.DirectiveMaxWorkers:
		return d.applyMaxWorkersDirective(args)
	case protocol.DirectiveStart:
		return d.applyStart()
	case protocol.DirectiveStop:
		return "", fmt.Errorf("stop directive disabled; use 'oro stop' for graceful shutdown")
	case protocol.DirectivePause:
		return d.applyPause()
	case protocol.DirectiveResume:
		return d.applyResume()
	case protocol.DirectiveStatus:
		return d.applyStatus()
	case protocol.DirectiveFocus:
		return d.applyFocus(args)
	case protocol.DirectiveShutdown:
		// Reject shutdown via UDS directive — agents can bypass ORO_ROLE guards.
		// Legitimate shutdown uses SIGINT (oro stop) which the daemon always honors.
		return "", fmt.Errorf("shutdown directive rejected; use 'oro stop' (sends SIGINT)")
	case protocol.DirectiveRestartDaemon:
		return d.applyRestartDaemon()
	default:
		return fmt.Sprintf("applied %s", dir), nil
	}
}

// applyStart transitions the dispatcher to running state.
func (d *Dispatcher) applyStart() (string, error) {
	d.setState(StateRunning)
	return "started", nil
}

// applyPause transitions the dispatcher to paused state.
func (d *Dispatcher) applyPause() (string, error) {
	d.setState(StatePaused)
	return "paused", nil
}

// applyResume transitions the dispatcher from paused to running.
func (d *Dispatcher) applyResume() (string, error) {
	if d.GetState() == StateRunning {
		return "already running", nil
	}
	d.setState(StateRunning)
	return "resumed", nil
}

// applyStatus returns the dispatcher status JSON, throttled to avoid redundant
// rebuilds when the manager sends bursts of status requests. If a cached
// response exists and was built within statusThrottleWindow, it is returned
// immediately. Otherwise the status is rebuilt and cached.
func (d *Dispatcher) applyStatus() (string, error) {
	now := d.nowFunc()
	d.mu.Lock()
	cached := d.lastStatusJSON
	elapsed := now.Sub(d.lastStatusTime)
	d.mu.Unlock()
	if cached != "" && elapsed < statusThrottleWindow {
		return cached, nil
	}
	result := d.buildStatusJSON()
	d.mu.Lock()
	d.lastStatusTime = now
	d.lastStatusJSON = result
	d.mu.Unlock()
	return result, nil
}

// applyRestartDaemon initiates graceful shutdown for daemon restart.
// It closes shutdownCh to trigger the graceful shutdown sequence in Run(),
// which sends PREPARE_SHUTDOWN to all workers and exits cleanly.
func (d *Dispatcher) applyRestartDaemon() (string, error) {
	select {
	case <-d.shutdownCh:
		// Already closed
		return "restart already in progress", nil
	default:
		close(d.shutdownCh)
		return "restarting daemon", nil
	}
}

// applyFocus sets the focused epic and resumes the dispatcher if paused.
func (d *Dispatcher) applyFocus(args string) (string, error) {
	d.mu.Lock()
	d.focusedEpic = args
	d.mu.Unlock()
	if d.GetState() != StateRunning {
		d.setState(StateRunning)
	}
	if args == "" {
		return "focus cleared", nil
	}
	return fmt.Sprintf("focused on %s", args), nil
}

// buildStatusJSON constructs the status response JSON string.
// snapshotWorkers builds the per-worker status slice, assignments map, and
// active/idle counts. Caller must hold d.mu.
func (d *Dispatcher) snapshotWorkers(now time.Time) (workers []workerStatus, assignments map[string]string, active, idle int) {
	assignments = make(map[string]string, len(d.workers))
	workers = make([]workerStatus, 0, len(d.workers))
	for id, w := range d.workers {
		if w.beadID != "" {
			assignments[id] = w.beadID
		}
		var progressSecs float64
		if !w.lastProgress.IsZero() {
			progressSecs = now.Sub(w.lastProgress).Seconds()
		}
		var heartbeatSecs float64
		if !w.lastSeen.IsZero() {
			heartbeatSecs = now.Sub(w.lastSeen).Seconds()
		}
		workers = append(workers, workerStatus{
			ID:                id,
			State:             string(w.state),
			BeadID:            w.beadID,
			LastProgressSecs:  progressSecs,
			LastHeartbeatSecs: heartbeatSecs,
			ContextPct:        w.contextPct,
		})
		if w.state == protocol.WorkerBusy || w.state == protocol.WorkerReserved {
			active++
		} else {
			idle++
		}
	}
	return workers, assignments, active, idle
}

// calculateLiveQueueDepth returns the count of ready beads that are not assigned to workers.
func calculateLiveQueueDepth(readyBeads []protocol.Bead, workers map[string]*trackedWorker) int {
	// Build set of assigned bead IDs.
	assignedBeadIDs := make(map[string]bool)
	for _, w := range workers {
		if w.beadID != "" {
			assignedBeadIDs[w.beadID] = true
		}
	}

	// Count ready beads that are not assigned.
	queueDepth := 0
	for _, bead := range readyBeads {
		if !assignedBeadIDs[bead.ID] {
			queueDepth++
		}
	}
	return queueDepth
}

func (d *Dispatcher) buildStatusJSON() string {
	now := d.nowFunc()

	// Fetch ready beads to determine which attempt counts are valid.
	ctx := context.Background()
	readyBeads, err := d.beads.Ready(ctx)
	if err != nil {
		readyBeads = nil // Continue with empty ready list on error.
	}

	d.mu.Lock()
	workers, assignments, activeCount, idleCount := d.snapshotWorkers(now)

	// Calculate live queue depth (ready beads minus assigned beads).
	queueDepth := calculateLiveQueueDepth(readyBeads, d.workers)

	// Build set of active bead IDs (assigned to workers OR in ready queue).
	activeBeadIDs := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" {
			activeBeadIDs[w.beadID] = true
		}
	}
	for _, bead := range readyBeads {
		activeBeadIDs[bead.ID] = true
	}

	// Filter attempt counts to only include active beads.
	var attemptCounts map[string]int
	if len(d.attemptCounts) > 0 {
		attemptCounts = make(map[string]int)
		for beadID, count := range d.attemptCounts {
			if activeBeadIDs[beadID] {
				attemptCounts[beadID] = count
			}
		}
	}

	resp := statusResponse{
		State:               string(d.state),
		PID:                 os.Getpid(),
		WorkerCount:         len(d.workers),
		QueueDepth:          queueDepth,
		Assignments:         assignments,
		FocusedEpic:         d.focusedEpic,
		Workers:             workers,
		ActiveCount:         activeCount,
		IdleCount:           idleCount,
		TargetCount:         d.targetWorkers,
		UptimeSeconds:       now.Sub(d.startTime).Seconds(),
		PendingHandoffCount: len(d.pendingHandoffs),
		AttemptCounts:       attemptCounts,
		ProgressTimeoutSecs: d.cfg.ProgressTimeout.Seconds(),
	}
	d.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}

// applyScaleDirective parses the target count from args, stores it, and
// calls reconcileScale. Returns the ACK detail string.
func (d *Dispatcher) applyScaleDirective(args string) (string, error) {
	target, err := strconv.Atoi(args)
	if err != nil {
		return "", fmt.Errorf("invalid scale args %q: %w", args, err)
	}
	if target < 0 {
		return "", fmt.Errorf("invalid scale target %d: must be non-negative", target)
	}

	d.mu.Lock()
	if maxW := d.cfg.MaxWorkers; maxW > 0 && target > maxW {
		target = maxW
	}
	d.targetWorkers = target
	d.unexpectedManagedExits = 0
	connected := len(d.workers)
	d.mu.Unlock()

	detail := d.reconcileScale()
	if detail == "" {
		detail = fmt.Sprintf("target=%d, current=%d, no change", target, connected)
	}
	return detail, nil
}

// applyMaxWorkersDirective sets the maximum worker pool size at runtime.
// It updates cfg.MaxWorkers, clamps targetWorkers to the new ceiling if needed,
// and calls reconcileScale to enforce the updated limit immediately.
func (d *Dispatcher) applyMaxWorkersDirective(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker count required")
	}
	n, err := strconv.Atoi(args)
	if err != nil {
		return "", fmt.Errorf("invalid worker count %q: %w", args, err)
	}
	if n < 0 {
		return "", fmt.Errorf("worker count must be non-negative, got %d", n)
	}

	d.mu.Lock()
	d.cfg.MaxWorkers = n
	if d.targetWorkers > n {
		d.targetWorkers = n
	}
	d.mu.Unlock()

	d.reconcileScale()
	return fmt.Sprintf("max_workers=%d", n), nil
}

// applyKillWorker terminates a specific worker, cleans up its worktree,
// resets its bead to open, and clears bead tracking. Decrements targetWorkers
// only for managed workers. Returns an error if args is empty or the worker
// ID is not found.
func (d *Dispatcher) applyKillWorker(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}

	// Capture fields before removing worker.
	beadID := w.beadID
	managed := w.managed

	// Close connection and remove worker from pool.
	_ = w.conn.Close()
	delete(d.workers, workerID)

	// Decrement target count only for managed workers; external workers are
	// not counted against targetWorkers.
	if managed && d.targetWorkers > 0 {
		d.targetWorkers--
	}
	d.mu.Unlock()

	// DO NOT remove the worktree here - preserve it for respawn reuse (oro-1eo8).
	// The worktree will be reused if the same bead is reassigned, or cleaned up
	// on successful completion or explicit shutdown.

	// Reset bead to open so it can be reassigned.
	if beadID != "" {
		if err := d.beads.Update(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "kill_worker_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		d.clearBeadTracking(beadID)
		_ = d.completeAssignment(ctx, beadID)
		_ = d.logEvent(ctx, "worker_killed", "dispatcher", beadID, workerID,
			`{"reason":"kill-worker directive"}`)
	}

	return fmt.Sprintf("worker %s killed", workerID), nil
}

// applySpawnFor spawns a dedicated worker for a specific bead. The bead is
// added to priorityBeads so tryAssign assigns it before normal queue ordering.
func (d *Dispatcher) applySpawnFor(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("bead ID required")
	}
	beadID := args

	d.mu.Lock()
	for _, w := range d.workers {
		if w.beadID == beadID {
			workerID := w.id
			d.mu.Unlock()
			return "", fmt.Errorf("bead %s already assigned to %s", beadID, workerID)
		}
	}
	d.priorityBeads[beadID] = true
	d.targetWorkers++
	d.mu.Unlock()

	if d.procMgr == nil {
		d.mu.Lock()
		delete(d.priorityBeads, beadID)
		d.targetWorkers--
		d.mu.Unlock()
		return "", fmt.Errorf("no process manager configured")
	}

	newID := fmt.Sprintf("worker-spawnfor-%d", d.nowFunc().UnixNano())
	if _, err := d.procMgr.Spawn(newID); err != nil {
		d.mu.Lock()
		delete(d.priorityBeads, beadID)
		d.targetWorkers--
		d.mu.Unlock()
		return "", fmt.Errorf("spawn failed: %w", err)
	}

	d.mu.Lock()
	d.pendingManagedIDs[newID] = true
	d.mu.Unlock()

	_ = d.logEvent(context.Background(), "spawn_for", "dispatcher", beadID, newID, "")
	return fmt.Sprintf("spawned worker %s for bead %s", newID, beadID), nil
}

// applyRestartWorker terminates a specific worker, returns its bead to the
// ready queue, spawns a new worker with the same ID, and keeps targetWorkers
// unchanged. Returns an error if args is empty, the worker ID is not found,
// or spawning the new worker fails.
func (d *Dispatcher) applyRestartWorker(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}

	// Capture bead ID and managed flag before removing worker.
	beadID := w.beadID
	wasManaged := w.managed

	// Close connection and remove worker from pool
	_ = w.conn.Close()
	delete(d.workers, workerID)

	// If the original worker was managed, record the ID so registerWorker
	// sets managed=true when the respawned process connects.
	if wasManaged {
		d.pendingManagedIDs[workerID] = true
	}

	// Target count remains unchanged (unlike kill-worker)
	procMgr := d.procMgr
	d.mu.Unlock()

	// Return bead to queue by completing the assignment
	if beadID != "" {
		_ = d.completeAssignment(ctx, beadID)
		_ = d.logEvent(ctx, "worker_restarted", "dispatcher", beadID, workerID,
			`{"reason":"restart-worker directive"}`)
	}

	// Spawn new worker process with same ID
	if procMgr != nil {
		_, err := procMgr.Spawn(workerID)
		if err != nil {
			_ = d.logEvent(ctx, "worker_spawn_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
			return "", fmt.Errorf("spawn new worker: %w", err)
		}
	}

	return fmt.Sprintf("worker %s restarted", workerID), nil
}

// applyPreempt gracefully preempts a worker for higher-priority work.
// Unlike restart-worker, this sends a PREEMPT message to allow the worker
// to complete its current operation cleanly before stopping.
func (d *Dispatcher) applyPreempt(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}

	// Mark worker as preempting
	w.state = protocol.WorkerPreempting

	// Send PREEMPT message to worker
	msg := protocol.Message{
		Type: protocol.MsgPreempt,
	}
	if err := w.encoder.Encode(msg); err != nil {
		d.mu.Unlock()
		return "", fmt.Errorf("send preempt message: %w", err)
	}

	beadID := w.beadID
	d.mu.Unlock()

	// Log the preemption event
	if beadID != "" {
		_ = d.logEvent(ctx, "worker_preempted", "dispatcher", beadID, workerID,
			`{"reason":"preempt directive"}`)
	}

	return fmt.Sprintf("worker %s preempted", workerID), nil
}

// maybeAutoScale increases targetWorkers when assignable beads exist but no
// idle workers are available. Scales up to min(queue depth, MaxWorkers).
func (d *Dispatcher) maybeAutoScale(ctx context.Context, queueDepth, idleCount int) {
	if queueDepth == 0 || idleCount > 0 {
		return
	}

	d.mu.Lock()
	currentTarget := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	d.mu.Unlock()

	if currentTarget >= maxWorkers {
		return
	}

	// Scale to min(queue depth, MaxWorkers)
	newTarget := queueDepth
	if newTarget > maxWorkers {
		newTarget = maxWorkers
	}

	if newTarget > currentTarget {
		d.mu.Lock()
		d.targetWorkers = newTarget
		d.mu.Unlock()
		d.reconcileScale()
		_ = d.logEvent(ctx, "auto_scale", "dispatcher", "", "",
			fmt.Sprintf("scaled to %d workers (queue depth: %d)", newTarget, queueDepth))
	}
}

// reconcileScale compares target vs connected managed workers and spawns or
// shuts down managed workers to reach the target. Unmanaged (externally
// connected) workers are invisible to scaling in all modes.
//
// Uses atomic flag to prevent concurrent execution. If already running, returns
// immediately to avoid duplicate spawns. See oro-ovpc.1.
func (d *Dispatcher) reconcileScale() string {
	// Use atomic CAS to ensure only one reconcileScale runs at a time (oro-ovpc.1).
	// If another call is in progress, return immediately - the running call will
	// handle the reconciliation. This prevents duplicate spawns without deadlock.
	if !d.reconcilingScale.CompareAndSwap(false, true) {
		return "" // already reconciling
	}
	defer d.reconcilingScale.Store(false)

	d.mu.Lock()
	target := d.targetWorkers
	// Count both connected managed workers AND pending spawns (oro-ovpc).
	// Without counting pending, concurrent reconcileScale calls both see
	// managedCount=0 and spawn duplicates before workers connect.
	managedCount := len(d.pendingManagedIDs)
	for _, w := range d.workers {
		if w.managed {
			managedCount++
		}
	}
	// Guard: cap at 2*target using only managed workers (connected + pending +
	// exits) to prevent runaway crash-respawn loops (oro-135n, oro-kdne).
	// Unmanaged (orphaned) workers are excluded so they cannot block managed
	// worker spawning.
	managedExits := d.unexpectedManagedExits
	d.mu.Unlock()

	switch {
	case managedCount < target:
		if managedCount+managedExits >= 2*target {
			return fmt.Sprintf("target=%d, managed=%d, exits=%d, managed+exits %d >= 2*target %d — cap reached, skipping scaleUp",
				target, managedCount, managedExits, managedCount+managedExits, 2*target)
		}
		return d.scaleUp(target, managedCount)
	case managedCount > target:
		return d.scaleDown(target, managedCount)
	default:
		return ""
	}
}

// scaleUp spawns (target - connected) new worker processes.
func (d *Dispatcher) scaleUp(target, connected int) string {
	toSpawn := target - connected
	if d.procMgr == nil {
		return fmt.Sprintf("target=%d, need %d workers but no ProcessManager configured", target, toSpawn)
	}

	spawned := 0
	for i := 0; i < toSpawn; i++ {
		id := fmt.Sprintf("worker-%d-%d", time.Now().UnixNano(), i)
		if _, err := d.procMgr.Spawn(id); err != nil {
			continue
		}
		// Record as managed so registerWorker sets managed=true when it connects.
		d.mu.Lock()
		d.pendingManagedIDs[id] = true
		d.mu.Unlock()
		spawned++
	}
	return fmt.Sprintf("target=%d, spawning %d", target, spawned)
}

// scaleDown initiates graceful shutdown for excess managed workers, preferring
// idle workers first, then newest busy workers. Unmanaged workers are skipped.
func (d *Dispatcher) scaleDown(target, connected int) string {
	toRemove := connected - target

	d.mu.Lock()
	// Partition managed workers into idle and busy — unmanaged are excluded.
	var idle, busy []string
	for id, w := range d.workers {
		if !w.managed {
			continue
		}
		if w.state == protocol.WorkerIdle {
			idle = append(idle, id)
		} else {
			busy = append(busy, id)
		}
	}
	d.mu.Unlock()

	// Build removal list: idle first, then busy (newest = end of slice).
	var victims []string
	victims = append(victims, idle...)
	victims = append(victims, busy...)

	// Trim to the number we need to remove.
	if len(victims) > toRemove {
		victims = victims[:toRemove]
	}

	for _, id := range victims {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	return fmt.Sprintf("target=%d, shutting down %d", target, len(victims))
}

// heartbeatLoop, checkHeartbeats → worker_pool.go

// --- SQLite helpers ---

func (d *Dispatcher) logEvent(ctx context.Context, evType, source, beadID, workerID, payload string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
		evType, source, beadID, workerID, payload)
	if err != nil {
		return fmt.Errorf("log event: %w", err)
	}
	return nil
}

// logEventLocked is logEvent but expects the caller already holds d.mu. It runs
// the SQL in a goroutine to avoid blocking while holding the lock.
func (d *Dispatcher) logEventLocked(ctx context.Context, evType, source, beadID, workerID, payload string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
		evType, source, beadID, workerID, payload)
	if err != nil {
		return fmt.Errorf("log event: %w", err)
	}
	return nil
}

// escalate sends a message to the Manager via the escalator and logs any
// delivery failures to the events table. This prevents silent failures when
// the tmux session is dead.
//
// For escalation types that have playbooks (STUCK_WORKER, MERGE_CONFLICT,
// MISSING_AC), it also spawns a one-shot claude -p agent to take corrective
// action autonomously.
func (d *Dispatcher) escalate(ctx context.Context, msg, beadID, workerID string) {
	// Extract escalation type for database storage (separate from one-shot determination).
	dbEscType := extractEscalationType(msg)

	// Persist escalation to SQLite before attempting tmux delivery.
	var escalationID int64
	if res, err := d.db.ExecContext(ctx,
		`INSERT INTO escalations (type, bead_id, worker_id, message) VALUES (?, ?, ?, ?)`,
		dbEscType, beadID, workerID, msg); err == nil {
		escalationID, _ = res.LastInsertId()
	}

	if err := d.escalator.Escalate(ctx, msg); err != nil {
		_ = d.logEvent(ctx, "escalation_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q,"message":%q}`, err.Error(), msg))
	}

	// Spawn one-shot manager agent for actionable escalation types.
	// Only spawn for types with a one-shot playbook (use parseEscalationType, not extractEscalationType).
	if d.ops != nil {
		if oneShot := parseEscalationType(msg); oneShot != "" {
			d.spawnEscalationOneShot(ctx, escalationID, oneShot, beadID, workerID, msg)
		}
	}
}

// applyPendingEscalations returns all unacked escalations as JSON.
func (d *Dispatcher) applyPendingEscalations() (string, error) {
	rows, err := d.db.QueryContext(context.Background(),
		`SELECT id, type, bead_id, worker_id, message, status, created_at, retry_count
		 FROM escalations WHERE status = 'pending' ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("query pending escalations: %w", err)
	}
	defer rows.Close()

	var escs []protocol.Escalation
	for rows.Next() {
		var e protocol.Escalation
		if err := rows.Scan(&e.ID, &e.Type, &e.BeadID, &e.WorkerID, &e.Message, &e.Status, &e.CreatedAt, &e.RetryCount); err != nil {
			return "", fmt.Errorf("scan escalation: %w", err)
		}
		escs = append(escs, e)
	}

	b, err := json.Marshal(escs)
	if err != nil {
		return "", fmt.Errorf("marshal escalations: %w", err)
	}
	return string(b), nil
}

// applyAckEscalation marks an escalation as acknowledged by ID.
func (d *Dispatcher) applyAckEscalation(args string) (string, error) {
	id := strings.TrimSpace(args)
	if id == "" {
		return "", fmt.Errorf("ack-escalation requires an escalation ID")
	}

	res, err := d.db.ExecContext(context.Background(),
		`UPDATE escalations SET status = 'acked', acked_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		id)
	if err != nil {
		return "", fmt.Errorf("ack escalation: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Sprintf("escalation %s not found or already acked", id), nil
	}
	return fmt.Sprintf("acked escalation %s", id), nil
}

// escalationRetryLoop periodically re-delivers unacked escalations via tmux.
// Runs every EscalationRetryInterval (default 2 minutes), retries up to 5 times.
// Each iteration is wrapped in a defer/recover so a panic inside the body
// logs a goroutine_panic event and restarts the loop after exponential backoff.
func (d *Dispatcher) escalationRetryLoop(ctx context.Context) {
	interval := d.escalationRetryInterval
	if interval == 0 {
		interval = 2 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		exit := func() (shouldExit bool) {
			defer func() {
				if r := recover(); r != nil {
					if d.handleLoopPanic(ctx, r, &restartCount, &lastPanicTime) {
						shouldExit = true
					}
				}
			}()
			select {
			case <-ctx.Done():
				return true
			case <-ticker.C:
				d.callRetryPendingEscalations(ctx)
			}
			return false
		}()
		if exit {
			return
		}
	}
}

func (d *Dispatcher) retryPendingEscalations(ctx context.Context) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, type, bead_id, message FROM escalations
		 WHERE status = 'pending' AND retry_count < 5
		 ORDER BY id`)
	if err != nil {
		return
	}
	defer rows.Close()

	// Collect all escalations first (can't update while iterating)
	type pendingEscalation struct {
		id      int64
		escType string
		beadID  string
		msg     string
	}
	var pending []pendingEscalation

	for rows.Next() {
		var id int64
		var escType, msg string
		var beadID sql.NullString
		if err := rows.Scan(&id, &escType, &beadID, &msg); err != nil {
			continue
		}

		beadIDStr := ""
		if beadID.Valid {
			beadIDStr = beadID.String
		}
		pending = append(pending, pendingEscalation{
			id:      id,
			escType: escType,
			beadID:  beadIDStr,
			msg:     msg,
		})
	}
	_ = rows.Close() // #nosec G104 - defer handles cleanup on error

	// Process escalations after closing the query
	for _, esc := range pending {
		// Check if the underlying condition is resolved
		if !d.shouldRetryEscalation(ctx, esc.escType, esc.beadID) {
			// Condition resolved - auto-ack the escalation
			_, _ = d.db.ExecContext(ctx,
				`UPDATE escalations SET status = 'acked', acked_at = datetime('now') WHERE id = ?`,
				esc.id)
			continue
		}

		// Condition still holds - retry the escalation
		_ = d.escalator.Escalate(ctx, esc.msg)
		_, _ = d.db.ExecContext(ctx,
			`UPDATE escalations SET retry_count = retry_count + 1, last_retry_at = datetime('now') WHERE id = ?`,
			esc.id)
	}
}

// shouldRetryEscalation checks if an escalation's underlying condition still
// holds. Returns false if the condition is resolved (preventing spam), true
// if the escalation should be retried.
//
// Edge cases:
//   - Empty beadID + WORKER_CRASH: auto-ack — stale alert from a prev-session
//     worker with no bead assigned, stops the 2-minute replay loop (oro-p2ey)
//   - Empty beadID (other types): always retry (no bead context to check)
//   - beads.Show error: always retry (don't suppress on error)
//   - Unknown escType: always retry (don't block future escalation types)
func (d *Dispatcher) shouldRetryEscalation(ctx context.Context, escType, beadID string) bool {
	// Empty beadID: WORKER_CRASH auto-acks (stale prev-session alert, oro-p2ey);
	// all other types retry because there's no bead context to check.
	if beadID == "" {
		return protocol.EscalationType(escType) != protocol.EscWorkerCrash
	}

	// Check per-type conditions — helpers live in escalation_precheck.go
	switch protocol.EscalationType(escType) {
	case protocol.EscMissingAC:
		return d.retryMissingAC(ctx, beadID)
	case protocol.EscStuckWorker:
		return d.retryStuckWorker(beadID)
	case protocol.EscWorkerCrash, protocol.EscStuck:
		return d.retryBeadStillAssigned(ctx, beadID)
	case protocol.EscMergeConflict:
		return d.retryMergeConflict(ctx, beadID)
	case protocol.EscPriorityContention:
		return d.retryPriorityContention(ctx, beadID)
	case protocol.EscOversizedBead:
		return d.retryOversizedBead(ctx, beadID)
	case protocol.EscMergeComplete:
		return false
	default:
		return true
	}
}

// parseEscalationType extracts the escalation type from a formatted
// [ORO-DISPATCH] message. Returns empty string if not a recognized type
// that has a one-shot playbook.
func parseEscalationType(msg string) string {
	// Format: [ORO-DISPATCH] TYPE: bead-id — summary.
	const prefix = "[ORO-DISPATCH] "
	_, after, found := strings.Cut(msg, prefix)
	if !found {
		return ""
	}
	escType, _, found := strings.Cut(after, ":")
	if !found {
		return ""
	}
	switch protocol.EscalationType(escType) {
	case protocol.EscStuckWorker, protocol.EscMergeConflict,
		protocol.EscPriorityContention, protocol.EscMissingAC:
		return escType
	default:
		return ""
	}
}

// extractEscalationType extracts the escalation type from a formatted message
// without checking if it has a one-shot playbook. Used for database storage.
func extractEscalationType(msg string) string {
	// Format: [ORO-DISPATCH] TYPE: bead-id — summary.
	const prefix = "[ORO-DISPATCH] "
	_, after, found := strings.Cut(msg, prefix)
	if !found {
		return ""
	}
	escType, _, found := strings.Cut(after, ":")
	if !found {
		return ""
	}
	return escType
}

// spawnEscalationOneShot launches a one-shot claude -p process to handle
// the escalation. The result is logged asynchronously.
func (d *Dispatcher) spawnEscalationOneShot(ctx context.Context, escalationID int64, escType, beadID, workerID, msg string) {
	// Look up bead details for context (best-effort).
	var beadTitle, beadContext string
	if beadID != "" {
		if detail, err := d.beads.Show(ctx, beadID); err == nil && detail != nil {
			beadTitle = detail.Title
			beadContext = detail.Description
		}
	}

	// Look up worktree path, falling back to "." if not found.
	d.mu.Lock()
	workdir := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if workdir == "" {
		workdir = "."
	}

	var resultCh <-chan ops.Result
	if protocol.EscalationType(escType) == protocol.EscMissingAC {
		// Dedup guard: skip if a WriteAC agent is already running for this bead.
		if d.ops.HasActiveForBead(beadID) {
			return
		}
		resultCh = d.ops.WriteAC(ctx, ops.WriteACOpts{
			BeadID:          beadID,
			BeadTitle:       beadTitle,
			BeadDescription: beadContext,
			Workdir:         workdir,
		})
	} else {
		resultCh = d.ops.Escalate(ctx, ops.EscalationOpts{
			EscalationType: escType,
			BeadID:         beadID,
			BeadTitle:      beadTitle,
			BeadContext:    beadContext,
			RecentHistory:  msg,
			Workdir:        workdir,
		})
	}

	d.safeGo(func() {
		d.handleEscalationResult(ctx, escalationID, escType, beadID, workerID, resultCh)
	})
}

// handleEscalationResult logs the one-shot escalation agent's outcome.
// If the one-shot fails (timeout, error, or non-zero exit), it escalates
// to the persistent manager for manual intervention.
func (d *Dispatcher) handleEscalationResult(ctx context.Context, escalationID int64, escType, beadID, workerID string, resultCh <-chan ops.Result) {
	result := <-resultCh
	if result.Err != nil {
		_ = d.logEvent(ctx, "oneshot_escalation_failed", "ops", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"error":%q}`, escType, result.Err.Error()))

		// Escalate to persistent manager when one-shot fails.
		failMsg := fmt.Sprintf("[ORO-DISPATCH] ONESHOT_FAILED: %s — One-shot %s agent failed: %v",
			beadID, escType, result.Err)
		if err := d.escalator.Escalate(ctx, failMsg); err != nil {
			_ = d.logEvent(ctx, "escalation_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q,"message":%q}`, err.Error(), failMsg))
		}
		return
	}
	_ = d.logEvent(ctx, "oneshot_escalation_complete", "ops", beadID, workerID,
		fmt.Sprintf(`{"type":%q,"verdict":%q,"feedback":%q}`, escType, result.Verdict, result.Feedback))

	// Ack the escalation in the persistent queue so the retry loop doesn't re-deliver it.
	if escalationID > 0 {
		res, err := d.db.ExecContext(ctx,
			`UPDATE escalations SET status='acked', acked_at=datetime('now') WHERE id=? AND status='pending'`,
			escalationID)
		if err != nil {
			_ = d.logEvent(ctx, "escalation_ack_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"escalation_id":%d,"error":%q}`, escalationID, err.Error()))
		} else {
			n, _ := res.RowsAffected()
			_ = d.logEvent(ctx, "escalation_acked", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"escalation_id":%d,"rows_affected":%d}`, escalationID, n))
		}
	}
}

func (d *Dispatcher) createAssignment(ctx context.Context, beadID, workerID, worktree string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		return fmt.Errorf("create assignment: %w", err)
	}
	return nil
}

// persistBeadCount updates a counter column on the active assignment row for a bead.
// column must be one of "attempt_count" or "handoff_count". This is a best-effort
// operation: errors are logged but do not propagate.
func (d *Dispatcher) persistBeadCount(ctx context.Context, beadID, column string, value int) {
	if d.db == nil {
		return
	}
	// Allowlist columns to prevent SQL injection.
	switch column {
	case "attempt_count", "handoff_count":
	default:
		return
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE assignments SET %s=? WHERE bead_id=? AND status='active'`, column),
		value, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "persist_count_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"column":%q,"value":%d,"error":%q}`, column, value, err.Error()))
	}
}

// resetOrphanedBeads resets any in_progress beads back to open on startup.
// This handles crash recovery: if the dispatcher crashed while beads were
// in_progress, they would remain stuck in that state without this reset.
// Errors are non-fatal — logged via logEvent and startup continues.
func (d *Dispatcher) resetOrphanedBeads(ctx context.Context) {
	beads, err := d.beads.InProgress(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "startup_reset_list_failed", "dispatcher", "", "", err.Error())
		return
	}
	for _, b := range beads {
		if updateErr := d.beads.Update(ctx, b.ID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "startup_reset_bead_failed", "dispatcher", b.ID, "", updateErr.Error())
		}
	}
}

// restoreState reconstructs the in-memory attemptCounts and handoffCounts maps
// from active assignments persisted in SQLite. This ensures tracking state
// survives a dispatcher restart.
func (d *Dispatcher) restoreState(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx,
		`SELECT bead_id, attempt_count, handoff_count FROM assignments WHERE status='active'`)
	if err != nil {
		return fmt.Errorf("query active assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	d.mu.Lock()
	defer d.mu.Unlock()

	for rows.Next() {
		var beadID string
		var attemptCount, handoffCount int
		if err := rows.Scan(&beadID, &attemptCount, &handoffCount); err != nil {
			return fmt.Errorf("scan assignment: %w", err)
		}
		if attemptCount > 0 {
			d.attemptCounts[beadID] = attemptCount
		}
		if handoffCount > 0 {
			d.handoffCounts[beadID] = handoffCount
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate assignments: %w", err)
	}
	return nil
}

func (d *Dispatcher) completeAssignment(ctx context.Context, beadID string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE bead_id=? AND status='active'`,
		beadID)
	if err != nil {
		return fmt.Errorf("complete assignment: %w", err)
	}
	return nil
}

func (d *Dispatcher) pendingCommands(ctx context.Context) ([]protocol.CommandRow, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, directive, args, status, created_at, COALESCE(processed_at, '') FROM commands WHERE status='pending' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query pending commands: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cmds []protocol.CommandRow
	for rows.Next() {
		var c protocol.CommandRow
		if err := rows.Scan(&c.ID, &c.Directive, &c.Args, &c.Status, &c.CreatedAt, &c.ProcessedAt); err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		cmds = append(cmds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate commands: %w", err)
	}
	return cmds, nil
}

func (d *Dispatcher) markCommandProcessed(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE commands SET status='processed', processed_at=datetime('now') WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("mark command processed: %w", err)
	}
	return nil
}

// sendToWorker, maxPendingMessages → worker_pool.go

// shutdownSequence orchestrates the four-phase graceful shutdown:
//  1. Cancel ops agents and abort in-flight merges (safe before worker stop).
//  2. Send PREPARE_SHUTDOWN to all workers, wait for drain or force-kill.
//     3b. Reset all active assignments back to open so beads are re-assignable on restart.
//  3. Remove worktrees and flush bead state (safe after workers are stopped).
func (d *Dispatcher) shutdownSequence() {
	// Phase 1: Cancel ops agents and abort in-flight merges.
	d.shutdownCancelOps()

	// Phase 2: Send PREPARE_SHUTDOWN to all workers and wait for them to drain.
	// Collect worker IDs and worktree paths under lock BEFORE the wait loop,
	// because workers will be deleted from the map as they disconnect.
	d.mu.Lock()
	workerIDs := make([]string, 0, len(d.workers))
	var worktreePaths []string
	for id, w := range d.workers {
		workerIDs = append(workerIDs, id)
		if w.worktree != "" {
			worktreePaths = append(worktreePaths, w.worktree)
		}
	}
	d.mu.Unlock()

	for _, id := range workerIDs {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	d.shutdownWaitForWorkers()

	// Phase 3b: Reset in-progress beads to open so they become re-assignable
	// on the next dispatcher start. Best-effort: log warnings on failure, continue.
	d.shutdownResetActiveBeads()

	// Phase 3: Workers are stopped — now safe to remove worktrees and flush state.
	d.shutdownRemoveWorktrees(worktreePaths)
}

// shutdownWaitForWorkers → worker_pool.go

// shutdownCancelOps cancels active ops agents and aborts in-flight merges.
// Safe to call before workers are stopped.
func (d *Dispatcher) shutdownCancelOps() {
	for _, taskID := range d.ops.Active() {
		if err := d.ops.Cancel(taskID); err == nil {
			_ = d.logEvent(context.Background(), "ops_cancelled", "dispatcher", "", "", taskID)
		}
	}
	_ = d.merger.AbortAll()
}

// shutdownRemoveWorktrees removes the given worktrees and flushes bead state.
// Must be called AFTER all workers have been stopped so their working
// directories are no longer in use.
func (d *Dispatcher) shutdownRemoveWorktrees(paths []string) {
	// Remove worktrees best-effort (don't block shutdown).
	ctx := context.Background()
	for _, p := range paths {
		if err := d.worktrees.Remove(ctx, p); err != nil {
			_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", "", "", err.Error())
		} else {
			_, _, _ = d.logEvent, ctx, p
		}
	}

	// Flush bead state to disk before exiting.
	if err := d.beads.Sync(ctx); err != nil {
		_ = d.logEvent(ctx, "bead_sync_failed", "dispatcher", "", "", err.Error())
	} else {
		_ = d.logEvent(ctx, "bead_synced", "dispatcher", "", "", "")
	}
}

// shutdownResetActiveBeads queries active assignments and resets each bead to
// "open" so it becomes re-assignable on next dispatcher start. Best-effort:
// failures are logged but do not block shutdown.
//
// It uses d.shutdownRunner (anchored to cfg.RepoRoot) so that `bd update`
// always runs from the repository root, not from a worker worktree that may
// lack a .beads/ database.
func (d *Dispatcher) shutdownResetActiveBeads() {
	ctx := context.Background()
	rows, err := d.db.QueryContext(ctx, `SELECT bead_id FROM assignments WHERE status='active'`)
	if err != nil {
		_ = d.logEvent(ctx, "shutdown_reset_query_failed", "dispatcher", "", "", err.Error())
		return
	}
	defer func() { _ = rows.Close() }()

	// Use a CLIBeadSource backed by the shutdown runner so bd commands are
	// executed from the repo root regardless of the process working directory.
	rootBeads := NewCLIBeadSource(d.shutdownRunner)

	for rows.Next() {
		var beadID string
		if scanErr := rows.Scan(&beadID); scanErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_scan_failed", "dispatcher", "", "", scanErr.Error())
			continue
		}
		if updateErr := rootBeads.Update(ctx, beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_bead_failed", "dispatcher", beadID, "", updateErr.Error())
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = d.logEvent(ctx, "shutdown_reset_rows_failed", "dispatcher", "", "", rowsErr.Error())
	}
}

// cancelOpsAgents cancels all in-flight ops agents for the given bead and logs the result.
func (d *Dispatcher) cancelOpsAgents(ctx context.Context, beadID, workerID, reason string) {
	if n, err := d.ops.CancelForBead(beadID); n > 0 {
		_ = d.logEvent(ctx, "ops_agents_cancelled", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"count":%d,"reason":%q}`, n, reason))
		if err != nil {
			_ = d.logEvent(ctx, "ops_cancel_error", "dispatcher", beadID, workerID, err.Error())
		}
	}
}

// handleQGExhausted handles the case when quality gate retries are exhausted.
// It releases the worker, cancels in-flight ops, marks the bead as exhausted,
// then spawns a Decompose ops agent as a fallback. If Decompose succeeds
// (VerdictResolved), no P0 bug is created. If it fails, handleQGExhaustedFallback
// runs the existing P0 bug + EscStuck escalation path.
func (d *Dispatcher) handleQGExhausted(ctx context.Context, workerID, beadID, qgOutput string, attempt int, qgErr *protocol.QualityGateError) {
	d.persistBeadCount(ctx, beadID, "attempt_count", attempt)

	_ = d.completeAssignment(ctx, beadID)

	// Release the worker so checkHeartbeats won't find a stale busy worker
	// and call clearBeadTracking (which would wipe exhaustedBeads).
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.worktree = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	d.mu.Unlock()

	// Cancel any in-flight ops agents for this bead to prevent stale escalations.
	d.cancelOpsAgents(ctx, beadID, workerID, "qg_exhausted")

	// Release worker and mark bead as exhausted atomically.
	// Worker must be released to prevent heartbeat/progress timeout from
	// calling clearBeadTracking and wiping exhaustedBeads.
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	// Clear tracking maps and set exhaustedBeads within same lock.
	delete(d.attemptCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	delete(d.escalatedBeads, beadID)
	delete(d.worktreeFailures, beadID)
	delete(d.assigningBeads, beadID)
	// Set exhaustedBeads before spawning goroutine to block re-assignment.
	d.exhaustedBeads[beadID] = true
	d.mu.Unlock()

	// Spawn a Decompose agent as a fallback. On VerdictResolved, no P0 bug is
	// created. On VerdictFailed (or error), the existing P0 + escalation path runs.
	d.safeGo(func() {
		ch := d.ops.Decompose(ctx, ops.DecomposeOpts{BeadID: beadID, QGOutput: qgOutput})
		result := <-ch
		d.handleDecomposeResult(ctx, workerID, beadID, result, qgOutput, attempt, qgErr)
	})
}

// handleDecomposeResult processes the outcome of a Decompose ops agent.
// On VerdictResolved, it logs success and returns without creating a P0 bug.
// On VerdictFailed or error, it calls handleQGExhaustedFallback to run the
// standard P0 bug + EscStuck escalation path.
func (d *Dispatcher) handleDecomposeResult(ctx context.Context, workerID, beadID string, result ops.Result, qgOutput string, attempt int, qgErr *protocol.QualityGateError) {
	if result.Err != nil {
		_ = d.logEvent(ctx, "decompose_error", "ops", beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, result.Err.Error()))
		d.handleQGExhaustedFallback(ctx, workerID, beadID, qgOutput, attempt, qgErr)
		return
	}
	if result.Verdict != ops.VerdictResolved {
		_ = d.logEvent(ctx, "decompose_failed", "ops", beadID, workerID,
			fmt.Sprintf(`{"verdict":%q,"feedback":%q}`, result.Verdict, result.Feedback))
		d.handleQGExhaustedFallback(ctx, workerID, beadID, qgOutput, attempt, qgErr)
		return
	}
	_ = d.logEvent(ctx, "decompose_resolved", "ops", beadID, workerID,
		fmt.Sprintf(`{"feedback":%q}`, result.Feedback))
}

// handleQGExhaustedFallback creates a P0 bug bead and escalates to the manager.
// Called when the Decompose agent fails or returns VerdictFailed after QG retries
// are exhausted.
func (d *Dispatcher) handleQGExhaustedFallback(ctx context.Context, workerID, beadID, qgOutput string, attempt int, qgErr *protocol.QualityGateError) {
	// Create a P0 bug bead so the failure is tracked as actionable work.
	p0Title := fmt.Sprintf("P0: QG exhausted for %s", beadID)
	p0Desc := fmt.Sprintf("Quality gate failed %d times. Last output:\n%s", attempt, qgOutput)
	newID, createErr := d.beads.Create(ctx, p0Title, "bug", 0, p0Desc, beadID, "")
	if createErr != nil {
		_ = d.logEvent(ctx, "p0_bead_create_failed", workerID, beadID, workerID, createErr.Error())
	} else {
		_ = d.logEvent(ctx, "p0_bead_created", workerID, beadID, workerID,
			fmt.Sprintf(`{"new_bead_id":%q}`, newID))
	}

	_ = d.logEvent(ctx, "qg_retry_escalated", workerID, beadID, workerID,
		fmt.Sprintf(`{"attempts":%d,"error":%q}`, attempt, qgErr.Error()))
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
		fmt.Sprintf("quality gate failed %d times", attempt), qgOutput), beadID, workerID)
}

// formatSearchResults formats code search results into markdown for prompt injection.
// When a result has a non-empty Reason, it is included as a relevance note.
func formatSearchResults(results []SearchResult) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "### %s:%d-%d\n```\n%s\n```\n",
			r.FilePath, r.StartLine, r.EndLine, r.Content)
		if r.Reason != "" {
			fmt.Fprintf(&b, "_Relevance: %s_\n", r.Reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// buildSearchQuery combines a bead title and labels into a single search string.
// Labels are appended after the title, separated by spaces.
// Empty labels are ignored. If title is empty, only labels are joined.
func buildSearchQuery(title string, labels []string) string {
	parts := make([]string, 0, 1+len(labels))
	if title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, labels...)
	return strings.Join(parts, " ")
}

// LLMEstimator uses Claude API to estimate bead completion time.
type LLMEstimator struct {
	apiKey string
}

// NewLLMEstimator creates a new LLMEstimator with the given API key.
func NewLLMEstimator(apiKey string) *LLMEstimator {
	return &LLMEstimator{apiKey: apiKey}
}

// messageRequest is the request body for the Anthropic API.
type messageRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// messageResponse is the response body from the Anthropic API.
type messageResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Estimate calls Claude to estimate the time (in minutes) required for a bead.
// Returns 0 if estimation fails or if the model decides the task is quick/trivial.
func (e *LLMEstimator) Estimate(ctx context.Context, title, acceptance string) int {
	if e.apiKey == "" {
		return 0
	}

	prompt := fmt.Sprintf(`Estimate the time required to complete this software engineering task in minutes.

Task Title: %s

Acceptance Criteria:
%s

Return ONLY a single integer (the number of minutes). If you cannot estimate, return 0.
Be conservative - when in doubt, estimate higher rather than lower.`, title, acceptance)

	resp, err := e.callAPI(ctx, prompt)
	if err != nil || resp == nil {
		return 0
	}

	return e.parseResponse(resp)
}

// callAPI makes a request to the Anthropic API (using https://api.anthropic.com which is a trusted endpoint).
func (e *LLMEstimator) callAPI(ctx context.Context, prompt string) (*messageResponse, error) { //nolint:gosec // SSRF: endpoint is hardcoded, not user-controlled
	reqBody := messageRequest{
		Model:     "claude-opus-4-1",
		MaxTokens: 10,
	}
	reqBody.Messages = append(reqBody.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: prompt})

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", e.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // URL is hardcoded to Anthropic API endpoint
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var respData messageResponse
	if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &respData, nil
}

// parseResponse extracts the estimated minutes from the API response.
func (e *LLMEstimator) parseResponse(resp *messageResponse) int {
	if resp == nil || len(resp.Content) == 0 || resp.Content[0].Text == "" {
		return 0
	}

	text := strings.TrimSpace(resp.Content[0].Text)
	minutes, err := strconv.Atoi(text)
	if err != nil || minutes < 0 {
		return 0
	}

	return minutes
}

// ConnectedWorkers, TargetWorkers, WorkerInfo, WorkerModel → worker_pool.go
