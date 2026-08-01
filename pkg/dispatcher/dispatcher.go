// Package dispatcher implements the Oro orchestrator — the core coordination
// engine that composes protocol, merge, worker, and ops packages into a
// unified runtime. The Dispatcher manages a UDS server for worker connections,
// SQLite WAL for runtime state, a priority queue from oro task ready, worker
// lifecycle supervision, merge execution, ops agent spawning, command
// processing, and escalation to the Manager.
//
// The Dispatcher is INERT until it receives a "start" directive. After that
// it runs autonomously, polling for work and assigning tasks to idle workers.
package dispatcher

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oro/pkg/cards"
	embeddings "oro/pkg/embed"
	"oro/pkg/factoryhealth"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/web"
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

// qgOriginalReopenDeferDuration keeps deterministic QG-exhausted beads out of
// the ready queue long enough for another task or an operator to make progress.
const qgOriginalReopenDeferDuration = time.Hour

// reviewRateLimitDeferDuration keeps a bead out of the ready queue after the
// reviewer exhausts its five-hour usage window. Reassigning immediately would
// start another reviewer in the same rate-limited window.
const reviewRateLimitDeferDuration = time.Hour

const maxCodeSearchContextSize = 128 * 1024

// ErrSemanticDisabled is returned by WaitForEmbedder when semantic search has
// not been configured (embedderReady channel was never created).
var ErrSemanticDisabled = errors.New("semantic search disabled")

// ErrEmbedderUnavailable is returned by WaitForEmbedder when the embedder
// goroutine completed but the embedder could not be initialised.
var ErrEmbedderUnavailable = errors.New("embedder unavailable")

var errStorageAdmissionPaused = errors.New("storage admission paused")

// --- Domain types ---

// Bead, BeadDetail, and model constants are now in pkg/protocol/types.go

// --- Interfaces for testability ---

// Reranker re-scores a set of candidate documents against a query.
type Reranker interface {
	Rerank(query string, docs []string) []float64
}

// Embedder computes dense embedding vectors for semantic memory.
type Embedder interface {
	Embed(text string) []float32
	Dim() int
	Name() string
}

var (
	dreamDeleteRe = regexp.MustCompile(`^\[DELETE\]\s+(\d+)$`)
	dreamCreateRe = regexp.MustCompile(`^\[CREATE\]\s+type=(\w+)(?:\s+tags=([^\s:]+))?:\s+(.+)$`)
	dreamMergeRe  = regexp.MustCompile(`^\[MERGE\]\s+(\d+)\s+(\d+)\s+type=(\w+)(?:\s+tags=([^\s:]+))?:\s+(.+)$`)
)

// ParseDreamActions scans dream output and extracts memory mutation requests.
func ParseDreamActions(output string) []DreamAction {
	var actions []DreamAction
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if action, ok := parseDreamLine(line); ok {
			actions = append(actions, action)
		}
	}
	return actions
}

func parseDreamLine(line string) (DreamAction, bool) {
	if m := dreamDeleteRe.FindStringSubmatch(line); m != nil {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return DreamAction{}, false
		}
		return DreamAction{Kind: "DELETE", ID: id}, true
	}
	if m := dreamCreateRe.FindStringSubmatch(line); m != nil {
		params := protocol.MemoryInsertParams{
			Type:    m[1],
			Content: m[3],
			Source:  "dreamer",
		}
		if m[2] != "" {
			params.Tags = strings.Split(m[2], ",")
		}
		return DreamAction{Kind: "CREATE", Params: params}, true
	}
	if m := dreamMergeRe.FindStringSubmatch(line); m != nil {
		id1, err1 := strconv.ParseInt(m[1], 10, 64)
		id2, err2 := strconv.ParseInt(m[2], 10, 64)
		if err1 != nil || err2 != nil {
			return DreamAction{}, false
		}
		params := protocol.MemoryInsertParams{
			Type:    m[3],
			Content: m[5],
			Source:  "dreamer",
		}
		if m[4] != "" {
			params.Tags = strings.Split(m[4], ",")
		}
		return DreamAction{Kind: "MERGE", IDs: []int64{id1, id2}, Params: params}, true
	}
	return DreamAction{}, false
}

// Option customizes Dispatcher construction without changing existing call sites.
type Option func(*Dispatcher)

// WithMemoryServices wires memory behavior through dispatcher-owned interfaces.
func WithMemoryServices(services MemoryServices) Option {
	return func(d *Dispatcher) {
		d.memories = services.Store
		d.memoryServices = services
	}
}

// --- Dispatcher ---

// Dispatcher is the main orchestrator. Worker management and bead tracking
// are factored into embedded WorkerPool and BeadTracker structs whose fields
// are promoted so that existing callers (including tests) can access them
// directly (e.g. d.workers, d.attemptCounts). Both embedded structs share
// the Dispatcher-level mu for synchronisation.
type Dispatcher struct {
	cfg            Config
	db             *sql.DB
	remoteGates    *Store
	merger         *merge.Coordinator
	ops            *ops.Spawner
	beads          DeferredStore
	worktrees      WorktreeManager
	escalator      Escalator
	memories       MemoryStore
	memoryServices MemoryServices
	cardStore      cards.Store // dual-write mirror; nil means D.3 shim disabled
	codeIndex      CodeIndex   // interface for FTS5 code search (nil means no search)
	// beadSourceMode is the normalized ORO_BEADSOURCE_MODE captured at startup.
	// It controls whether the dispatcher watches a filesystem task-data source
	// or the native SQLite store.
	beadSourceMode string

	// embedder fields — populated by the warm-up goroutine (next bead).
	// embedderReady == nil means semantic search is disabled for this session.
	embedder        Embedder
	embedderReady   chan struct{}
	embedderErr     error
	embedderFactory func(modelDir string) (Embedder, error)

	// reranker fields — populated lazily on first RerankByIDsRequest (sync.Once-guarded).
	// rerankerFactory == nil means reranker is unavailable for this session.
	reranker        Reranker
	rerankerOnce    sync.Once
	rerankerErr     error
	rerankerFactory func(modelDir string) (Reranker, error)
	procMgr         ProcessManager
	acceptance      AcceptanceRunner // runs epic acceptance test commands
	qgRunner        QGRunner         // runs quality gate before merge (defaults to &ShellQGRunner{})
	qgBaselineCache map[string]qgBaseline
	// presubmitCandidates holds independent local validation plans. Its
	// semaphore scopes capacity to each action's declared resource class.
	presubmitCandidates   chan presubmitCandidate
	presubmitSemaphore    *QGSemaphore
	presubmitActionRunner func(context.Context, PresubmitAction) error
	paneRestarter         PaneRestarter      // restarts named tmux panes (nil means no restart)
	estimator             BeadEstimator      // estimates bead completion time (nil means no estimation)
	sseBroadcaster        web.SSEBroadcaster // broadcasts server-sent events (never nil, initialized in New)
	// WorkerPool holds the connected-worker registry (embedded for field promotion).
	WorkerPool
	// BeadTracker holds per-bead counters and mappings (embedded for field promotion).
	BeadTracker

	mu               sync.Mutex
	reconcilingScale atomic.Bool // prevents concurrent reconcileScale() calls (oro-ovpc.1)

	state                       State
	pauseSource                 string
	pauseReason                 string
	listener                    net.Listener
	focusedEpic                 string
	focusVersion                uint64
	epicQGWorktreeSeq           uint64
	targetWorkers               int
	explicitScaleTarget         bool
	completionsSinceConsolidate int // counts completed beads since last context consolidation
	beadsSinceDream             int // counts completed beads since last dream trigger
	// mergesSinceJanitor tracks completed merges until the janitor has enough
	// work to justify a scan. janitorRunsSinceAudit tracks eligible cycles so
	// an audit can replace the configured periodic janitor run.
	mergesSinceJanitor    uint64
	janitorRunsSinceAudit uint64
	janitorSpawnFn        func(context.Context)                           // test hook; nil records the scheduled janitor run
	auditSpawnFn          func(context.Context)                           // test hook; nil records the scheduled audit run
	auditResultFn         func(context.Context, ops.AuditOpts) ops.Result // test hook; nil runs the ops audit
	cleanlinessRoleMu     sync.Mutex
	cleanlinessCycleMu    sync.Mutex

	// dreamExecuteFn, if non-nil, is called by handleDreamResult instead of
	// memoryServices.ExecuteDream. Tests inject this to capture calls.
	dreamExecuteFn func(ctx context.Context, actions []DreamAction, store MemoryStore, logFn func(string)) error

	// repoRoot is the effective repository root (cfg.RepoRoot with cwd fallback).
	// Used as the target directory for git operations on the primary repo (e.g. epic FF merge).
	repoRoot string

	// shutdownRunner is the CommandRunner used for repo-root git and recovery
	// commands. Initialised by New() to &ExecCommandRunner{Dir: cfg.RepoRoot};
	// overridable in tests.
	shutdownRunner   CommandRunner
	shutdownRunnerMu sync.RWMutex

	// beadsDir is the internal task data directory to watch when using a
	// filesystem-backed source (defaults to protocol.BeadsDir).
	beadsDir string

	// panesDir is the directory to watch for pane context_pct files (defaults to ~/.oro/panes)
	panesDir string

	// signaledPanes tracks which panes have been signaled to avoid re-signaling
	signaledPanes map[string]bool

	// paneStates tracks per-pane restart state (lastRestartAt, restartCount, restarting flag)
	paneStates map[string]*paneState

	// startTime records when Run() was called (for uptime).
	startTime time.Time

	// checkpoints tracks the in-flight checkpoint state per bead (§9.3).
	checkpoints *checkpointTracker

	// cachedQueueDepth stores the last-known count from beads.Ready() in the assign loop.
	cachedQueueDepth  int
	cachedIdleWorkers int
	lastCycleScanAt   time.Time
	escalatedCycles   map[string]bool

	// lastRecoveryAssignmentBlockLog throttles noisy assignment-block events while
	// open recovery quarantines keep automation stopped.
	lastRecoveryAssignmentBlockLog time.Time
	assignmentFrozenByQuarantine   bool
	blockingRecoveryQuarantines    int
	assignmentFreezeReason         string

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

	// Review artifact maintenance is serialized so overlapping scheduled ticks
	// cannot delete or acknowledge the same artifact concurrently.
	reviewArtifactPruneMu     sync.Mutex
	reviewArtifactRetention   time.Duration
	reviewMaintenanceInterval time.Duration

	// escalationRetryInterval controls how often escalationRetryLoop fires.
	// Defaults to 2 minutes; tests may set this to a shorter value.
	escalationRetryInterval time.Duration

	// loopPanicBackoffFn, if non-nil, overrides the exponential backoff duration
	// used by the panic-restart wrappers in assignLoop/heartbeatLoop/escalationRetryLoop.
	// Intended for tests that need deterministic, fast backoff values.
	loopPanicBackoffFn func(restartCount int) time.Duration

	// transientBackoffFn, if non-nil, overrides the per-retry backoff duration
	// used by handleTransientQGFailure. Tests inject this to avoid real sleeps.
	transientBackoffFn func(count int) time.Duration

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

	// pendingManagedSince records when pending managed workers were spawned. It
	// lets reconciliation discard workers that exited before their first heartbeat.
	pendingManagedSince map[string]time.Time

	// pendingWorkerTargets tracks spawn-for worker IDs until they connect. The
	// target is transferred to trackedWorker.targetBeadID in registerWorker.
	pendingWorkerTargets map[string]string

	// pendingSpawnForWorkers tracks one-shot spawn-for worker IDs until they
	// connect. These workers are managed for process cleanup but do not count
	// toward the general worker pool target.
	pendingSpawnForWorkers map[string]bool

	// pendingExternalIDs tracks worker IDs reserved by `oro worker launch`
	// before the external worker process connects. These are not managed by the
	// dispatcher, but they consume MaxWorkers capacity while pending.
	pendingExternalIDs map[string]bool

	// pendingExternalSince records when external launch reservations were made
	// so stale reservations do not strand capacity forever.
	pendingExternalSince map[string]time.Time
	pendingQGRetries     map[string]QGRetryContext

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

	// httpServer is the HTTP server started when WebEnabled=true. Set in Run() before
	// the safeGo goroutine starts. Nil when WebEnabled=false or before Run() sets it.
	httpServer *http.Server

	// shutdownCh is closed when a shutdown directive is received, causing Run() to exit.
	shutdownCh chan struct{}
	// shutdownAuthorized gates whether SIGTERM is honored by the signal handler.
	shutdownAuthorized atomic.Bool

	// wg tracks all goroutines spawned by Run() to ensure graceful shutdown
	wg sync.WaitGroup

	// acceptSem limits concurrent connection handlers in acceptLoop
	acceptSem chan struct{}

	// lastStatusTime, lastStatusJSON, and lastStatusStorageKey implement throttling for status
	// directives. If a status request arrives within statusThrottleWindow
	// of the previous one, the cached JSON is returned unless storage changed.
	lastStatusTime       time.Time
	lastStatusJSON       string
	lastStatusStorageKey string
}

func (d *Dispatcher) qgMutationBase(targetBranch string) string {
	if targetBranch != "" {
		return targetBranch
	}
	if d.cfg.DefaultBranch != "" {
		return d.cfg.DefaultBranch
	}
	return "main"
}

func (d *Dispatcher) updateBeadStatus(ctx context.Context, id, status string) error {
	return updateBeadStatus(ctx, d.beads, id, status)
}

// New creates a Dispatcher. It does NOT start listening or polling — call Run().
// Returns nil and an error if the Config is invalid after applying defaults.
// codeIdx may be nil to disable code search context injection.
//
//nolint:funlen // factory initialization
func New(cfg Config, db *sql.DB, merger *merge.Coordinator, opsSpawner *ops.Spawner, beads DeferredStore, wt WorktreeManager, esc Escalator, codeIdx CodeIndex, opts ...Option) (*Dispatcher, error) {
	resolved := cfg.withDefaults()
	if err := resolved.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	// Determine the effective repo root for oro task commands.
	// Falls back to the process working directory when RepoRoot is not set.
	rootDir, beadsDir := resolved.RepoRoot, resolved.BeadsDir
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}
	if beadsDir == "" {
		beadsDir = protocol.BeadsDir
	}
	var cardStore cards.Store
	if store, err := cards.NewStore(db); err == nil {
		cardStore = store
	}
	beadSourceMode := normalizeBeadSourceModeForPrimary(os.Getenv("ORO_BEADSOURCE_MODE"), beads)
	selectedBeads, err := selectStore(context.Background(), beadSourceMode, beads, db)
	if err != nil {
		return nil, err
	}
	var remoteGates *Store
	if db != nil {
		remoteGates, err = NewStore(context.Background(), db)
		if err != nil {
			return nil, err
		}
	}
	d := &Dispatcher{
		cfg:            resolved,
		db:             db,
		remoteGates:    remoteGates,
		merger:         merger,
		ops:            opsSpawner,
		beads:          selectedBeads,
		worktrees:      wt,
		escalator:      esc,
		cardStore:      cardStore,
		codeIndex:      codeIdx,
		beadSourceMode: beadSourceMode,
		embedderReady:  defaultEmbedderReady(resolved),
		embedderFactory: func(modelDir string) (Embedder, error) {
			return embeddings.NewEmbedder(modelDir)
		},
		rerankerFactory:     defaultRerankerFactory(resolved),
		repoRoot:            rootDir,
		shutdownRunner:      &ExecCommandRunner{Dir: rootDir},
		acceptance:          &ShellAcceptanceRunner{},
		estimator:           resolved.Estimator,
		qgRunner:            &ShellQGRunner{},
		qgBaselineCache:     make(map[string]qgBaseline),
		presubmitCandidates: make(chan presubmitCandidate),
		presubmitSemaphore:  newPresubmitSemaphore(),
		sseBroadcaster:      web.NewSSEBroadcaster(),
		state:               StateInert,
		targetWorkers:       resolved.InitialWorkers,
		explicitScaleTarget: resolved.AllowZeroWorkers &&
			resolved.InitialWorkers == 0 && resolved.MaxWorkers > 0,
		WorkerPool: WorkerPool{
			workers: make(map[string]*trackedWorker),
		},
		BeadTracker: BeadTracker{
			rejectionCounts:        make(map[string]int),
			reviewBlockedCounts:    make(map[string]int),
			handoffCounts:          make(map[string]int),
			attemptCounts:          make(map[string]int),
			transientCounts:        make(map[string]int),
			checkpointCounts:       make(map[string]int),
			pendingHandoffs:        make(map[string]*pendingHandoff),
			qgStuckTracker:         make(map[string]*qgHistory),
			escalatedBeads:         make(map[string]bool),
			worktreeFailures:       make(map[string]time.Time),
			exhaustedBeads:         make(map[string]bool),
			assigningBeads:         make(map[string]bool),
			mergingBeads:           make(map[string]bool),
			worktreeByBead:         make(map[string]string),
			epicMergeFailed:        make(map[string]bool),
			epicCloseInFlight:      make(map[string]bool),
			processedExternalClose: make(map[string]bool),
			epicSkipLogged:         make(map[string]bool),
		},
		priorityBeads:             make(map[string]bool),
		pendingManagedIDs:         make(map[string]bool),
		pendingManagedSince:       make(map[string]time.Time),
		pendingWorkerTargets:      make(map[string]string),
		pendingSpawnForWorkers:    make(map[string]bool),
		pendingExternalIDs:        make(map[string]bool),
		pendingExternalSince:      make(map[string]time.Time),
		pendingQGRetries:          make(map[string]QGRetryContext),
		workerReadyCh:             make(chan struct{}, 1),
		shutdownCh:                make(chan struct{}),
		beadsDir:                  beadsDir,
		panesDir:                  defaultPanesDir(),
		signaledPanes:             make(map[string]bool),
		paneStates:                make(map[string]*paneState),
		escalatedCycles:           make(map[string]bool),
		checkpoints:               newCheckpointTracker(),
		nowFunc:                   time.Now,
		reviewArtifactRetention:   defaultReviewArtifactRetention,
		reviewMaintenanceInterval: defaultReviewMaintenanceInterval,
		acceptSem:                 make(chan struct{}, 100), // limit to 100 concurrent connection handlers
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d, nil
}

func defaultEmbedderReady(cfg Config) chan struct{} {
	if cfg.SemanticModelDir == "" {
		return nil
	}
	return make(chan struct{})
}

func defaultRerankerFactory(cfg Config) func(string) (Reranker, error) {
	if cfg.RerankerModelDir == "" {
		return nil
	}
	return func(modelDir string) (Reranker, error) {
		return embeddings.NewReranker(modelDir)
	}
}

func defaultPanesDir() string {
	if oroHome := os.Getenv("ORO_HOME"); oroHome != "" {
		return filepath.Join(oroHome, "panes")
	}
	return filepath.Join(os.Getenv("HOME"), ".oro", "panes")
}

// EmbedderWaiter is satisfied by Dispatcher and any type that exposes the
// embedder warm-up gate. Callers (e.g. semantic-search HTTP handlers) use this
// interface to block until the embedder is ready without importing the full Dispatcher.
type EmbedderWaiter interface {
	WaitForEmbedder(ctx context.Context) (Embedder, error)
}

// WaitForEmbedder blocks until the embedder warm-up goroutine signals readiness,
// then returns the initialised Embedder. It returns ErrSemanticDisabled immediately
// when embedderReady is nil (semantic search not configured). ctx cancellation is
// honoured — the caller receives ctx.Err() if the context is cancelled first.
func (d *Dispatcher) WaitForEmbedder(ctx context.Context) (Embedder, error) {
	if d.embedderReady == nil {
		return nil, ErrSemanticDisabled
	}
	select {
	case <-d.embedderReady:
		if d.embedderErr != nil {
			return nil, d.embedderErr
		}
		return d.embedder, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for embedder: %w", ctx.Err())
	}
}

// warmupEmbedder loads the BGE model via embedderFactory and closes embedderReady
// when done (success or failure). embedderReady == nil means semantic search is
// disabled — warmupEmbedder returns immediately in that case. Intended to run in
// a goroutine spawned by Run() after the context is available.
func (d *Dispatcher) warmupEmbedder(ctx context.Context) {
	if d.embedderReady == nil {
		return
	}
	var once sync.Once
	closeReady := func() { once.Do(func() { close(d.embedderReady) }) }
	defer closeReady()
	defer func() {
		if r := recover(); r != nil {
			d.embedderErr = fmt.Errorf("embedder factory panic: %v", r)
		}
	}()
	select {
	case <-ctx.Done():
		d.embedderErr = ctx.Err()
		return
	default:
	}
	emb, err := d.embedderFactory(d.cfg.SemanticModelDir)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			fmt.Fprintf(os.Stderr, "BGE model missing at %s — run `oro models prefetch` to download\n", pathErr.Path)
		}
		d.embedderErr = ErrEmbedderUnavailable
		return
	}
	d.embedder = emb
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

// notifyAssignLoop wakes the assign loop so it calls tryAssign immediately.
// Non-blocking: if the channel already has a pending signal the send is dropped
// (a tryAssign is already queued and this signal is redundant).
func (d *Dispatcher) notifyAssignLoop() {
	select {
	case d.workerReadyCh <- struct{}{}:
	default:
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

// commandRunner returns the recovery command runner as a stable snapshot.
// Connection cleanup runs independently from test setup and must not race a
// test-specific runner swap.
func (d *Dispatcher) commandRunner() CommandRunner {
	d.shutdownRunnerMu.RLock()
	defer d.shutdownRunnerMu.RUnlock()
	return d.shutdownRunner
}

func (d *Dispatcher) setCommandRunner(r CommandRunner) {
	d.shutdownRunnerMu.Lock()
	defer d.shutdownRunnerMu.Unlock()
	d.shutdownRunner = r
}

// GetConfig returns the dispatcher's resolved Config. Intended for tests
// that need to verify flag values were wired through to the dispatcher.
//
//oro:testonly
func (d *Dispatcher) GetConfig() Config {
	return d.cfg
}

// healthzHandler serves GET /healthz. Returns 200 when the dispatcher is in
// StateRunning, 503 otherwise. Used by load-balancers and readiness probes.
func (d *Dispatcher) healthzHandler(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	state := d.state
	d.mu.Unlock()
	if state == StateRunning {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

// startHTTPServer registers HTTP handlers, stores the *http.Server on d, and
// launches it via safeGo. Bind failures are logged as web_server_bind_failed
// events and are non-fatal — the dispatcher continues without a web interface.
func (d *Dispatcher) startHTTPServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", d.healthzHandler)
	dashHandler := web.NewHandler(d, web.Content)
	mux.Handle("/", dashHandler)
	srv := &http.Server{
		Addr:              d.cfg.WebAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, //nolint:mnd // standard defensive timeout (G112)
		WriteTimeout:      30 * time.Second, //nolint:mnd // prevent slow-write resource exhaustion
		IdleTimeout:       60 * time.Second, //nolint:mnd // reclaim idle keep-alive connections
	}
	d.mu.Lock()
	d.httpServer = srv
	d.mu.Unlock()
	d.safeGo(func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = d.logEvent(context.Background(), "web_server_bind_failed", "dispatcher", "", "", err.Error())
		}
	})
}

// Run starts the Dispatcher event loop. It:
//  1. Initializes the SQLite schema
//  2. Starts the UDS listener
//  3. Polls for commands (directives) and ready beads
//  4. Monitors worker heartbeats
//
// Run blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	// Defer-recover so a panic anywhere in Run() (or its synchronous callees)
	// leaves a breadcrumb on disk before the process dies. Background loops
	// are wrapped in safeGo and have their own panic handling; this catches
	// the rest. Re-panic so callers / Go runtime still see the panic
	// (oro-zxxn — silent dispatcher death gave us nothing to triage from).
	defer func() {
		if r := recover(); r != nil {
			d.writeExitMarker("panic", fmt.Sprint(r), debug.Stack())
			panic(r)
		}
	}()

	lock, err := acquirePIDLock(d.cfg.DBPath)
	if err != nil {
		d.writeExitMarker("fatal", "acquirePIDLock: "+err.Error(), nil)
		return err
	}
	defer func() { _ = lock.release() }()
	lockRefreshCtx, cancelLockRefresh := context.WithCancel(ctx)
	defer cancelLockRefresh()
	go lock.refreshLoop(lockRefreshCtx, pidLockMaxAge/2)

	d.mu.Lock()
	d.startTime = d.nowFunc()
	d.mu.Unlock()

	if err := d.startupRecovery(ctx); err != nil {
		d.writeExitMarker("fatal", "startupRecovery: "+err.Error(), nil)
		return err
	}

	ln, err := d.openSocket()
	if err != nil {
		d.writeExitMarker("fatal", "openSocket: "+err.Error(), nil)
		return err
	}

	d.spawnBackgroundLoops(ctx, ln)

	d.safeGo(func() { d.staleAssignmentSweepLoop(ctx) })

	exitReason := "shutdownCh"
	select {
	case <-ctx.Done():
		exitReason = "ctx_done"
	case <-d.shutdownCh:
	}

	_ = ln.Close()
	d.shutdownWithTimeout()
	d.writeExitMarker("normal", exitReason, nil)
	return nil
}

// writeExitMarker appends a timestamped line to dispatcher.exit.log alongside
// the dispatcher DB. Last-resort breadcrumb when the dispatcher dies — events
// table writes can fail during shutdown if SQLite is hosed, but a plain
// os.OpenFile + Write is robust. Filed by oro-zxxn after a silent dispatcher
// death on 2026-05-05 left no triage signal.
//
// kind is one of: "panic", "normal", "fatal". detail is a short human reason
// (panic message, "ctx_done", "openSocket: ...", etc). stack is optional —
// pass debug.Stack() inside a recover to capture goroutine state.
func (d *Dispatcher) writeExitMarker(kind, detail string, stack []byte) {
	if d == nil || d.cfg.DBPath == "" || d.cfg.DBPath == ":memory:" {
		return
	}
	path := filepath.Join(filepath.Dir(d.cfg.DBPath), "dispatcher.exit.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path derived from trusted d.cfg.DBPath set at dispatcher startup
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	pid := os.Getpid()
	_, _ = fmt.Fprintf(f, "%s pid=%d kind=%s detail=%q\n", ts, pid, kind, detail)
	if len(stack) > 0 {
		_, _ = f.Write(stack)
		if stack[len(stack)-1] != '\n' {
			_, _ = f.WriteString("\n")
		}
	}
	_, _ = f.WriteString("---\n")
}

// startupRecovery initializes the schema, prunes orphaned worktrees, and runs
// state-restoration / orphaned-bead reconciliation. Errors from prune and
// reconciliation are logged but non-fatal — only schema or state-restore
// failures abort startup.
func (d *Dispatcher) startupRecovery(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		return fmt.Errorf("init bead schema: %w", err)
	}
	if pruneErr := d.worktrees.Prune(ctx); pruneErr != nil {
		_ = d.logEvent(ctx, "worktree_prune_failed", "dispatcher", "", "", pruneErr.Error())
	}
	d.logAssignmentInvariantViolations(ctx)
	d.detectAndResolveDuplicateActiveAssignments(ctx)
	if err := d.reconcileOpsRunsOnStartup(ctx); err != nil {
		return fmt.Errorf("reconcile ops runs: %w", err)
	}
	if err := d.routePendingRoutableEscalations(ctx); err != nil {
		return fmt.Errorf("route pending routable escalations: %w", err)
	}

	recoverableBeads, recoveryStats, err := d.restoreState(ctx)
	if err != nil {
		return fmt.Errorf("restore state: %w", err)
	}
	autoResolved := d.autoResolveEmptySafeRecoveryQuarantines(ctx)
	reopened, skipped := d.resetOrphanedBeads(ctx, recoverableBeads)
	_ = d.logEvent(ctx, "startup_reconciliation_summary", "dispatcher", "", "",
		fmt.Sprintf(`{"recovered_attempts":%d,"quarantined_assignments":%d,"auto_resolved_quarantines":%d,"retired_closed_assignments":%d,"reopened_beads":%d,"skipped_in_progress":%d}`,
			recoveryStats.recoverable, recoveryStats.quarantined, autoResolved, recoveryStats.retiredClosed, reopened, skipped))
	if d.shouldRunZombieDeferredRepair() {
		if fixed, err := d.detectZombieDeferred(ctx); err == nil && fixed > 0 {
			_ = d.logEvent(ctx, "startup_zombie_defer_summary", "dispatcher", "", "",
				fmt.Sprintf(`{"fixed":%d}`, fixed))
		}
	}
	return nil
}

func (d *Dispatcher) shouldRunZombieDeferredRepair() bool {
	mode := strings.ToLower(strings.TrimSpace(d.beadSourceMode))
	return mode != "sqlite" && mode != "shadow"
}

// openSocket cleans any stale socket, binds the UDS listener with 0600
// permissions (owner-only), and stashes the listener on the dispatcher.
func (d *Dispatcher) openSocket() (net.Listener, error) {
	if err := cleanStaleSocket(d.cfg.SocketPath); err != nil {
		return nil, fmt.Errorf("stale socket check %s: %w", d.cfg.SocketPath, err)
	}
	ln, err := net.Listen("unix", d.cfg.SocketPath) //nolint:noctx // UDS bind is instant
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", d.cfg.SocketPath, err)
	}
	if err := os.Chmod(d.cfg.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket %s: %w", d.cfg.SocketPath, err)
	}
	d.mu.Lock()
	d.listener = ln
	d.mu.Unlock()
	return ln, nil
}

// spawnBackgroundLoops starts the accept/assign/heartbeat/pane/escalation
// goroutines (each via safeGo for panic recovery) and the HTTP server when
// WebEnabled is true.
func (d *Dispatcher) spawnBackgroundLoops(ctx context.Context, ln net.Listener) {
	d.safeGo(func() { d.acceptLoop(ctx, ln) })
	d.safeGo(func() { d.assignLoop(ctx) })
	d.safeGo(func() { d.heartbeatLoop(ctx) })
	d.safeGo(func() { d.paneMonitorLoop(ctx) })
	d.safeGo(func() { d.escalationRetryLoop(ctx) })
	d.safeGo(func() { d.reviewMaintenanceLoop(ctx) })
	d.safeGo(func() { d.runPresubmitScheduler(ctx) })
	d.safeGo(func() { d.storageControllerLoop(ctx) })
	// oro-pcp9 replaces the package-level RunSweepLoop(..., SweepConfig{}) call with
	// the method form so the sweep honours d.cfg.SweepConfig instead of zero values.
	// storageControllerLoop is main-only and unaffected, so both loops start.
	d.safeGo(func() { d.runSweepLoop(ctx, d.cfg.SweepConfig) })
	if d.cfg.WebEnabled {
		d.startHTTPServer()
	}
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

	// Shut down HTTP server (if running) so its safeGo goroutine can exit
	// before wg.Wait() below.
	d.mu.Lock()
	srv := d.httpServer
	d.mu.Unlock()
	if srv != nil {
		_ = srv.Shutdown(shutdownCtx) //nolint:contextcheck // shutdownCtx is the right scope here
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

// connCloseCleanup runs the deferred connection teardown for handleConn.
// It guards against clobbering a reconnected worker: only cleans up if the
// stored conn still matches the one this goroutine was serving.
// workerID is captured by reference in the defer so it holds its final value.
// connCloseState is the snapshot connCloseCleanup takes under d.mu before it
// dispatches the unlocked cleanup work.
type connCloseState struct {
	beadID        string
	assignmentID  int64
	worktree      string
	baseBranch    string
	retryContext  QGRetryContext
	retryPending  bool
	retrySnapshot workerAssignmentSnapshot
	preempted     bool
}

// takeConnCloseState performs connCloseCleanup's locked phase: it verifies the
// connection still owns the worker row, snapshots the assignment, and removes the
// worker. proceed is false when there is nothing further to clean up; notify is
// true when the caller must still wake the assign loop. Extracted to keep
// connCloseCleanup under the funlen limit; behaviour is unchanged.
func (d *Dispatcher) takeConnCloseState(workerID string, conn net.Conn) (connCloseState, bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	w, exists := d.workers[workerID]
	if !exists || w.conn != conn {
		return connCloseState{}, false, false
	}
	if w.spawnFor && w.state == protocol.WorkerShuttingDown {
		w.lastSeen = d.nowFunc()
		return connCloseState{}, false, true
	}

	st := connCloseState{
		beadID:       w.beadID,
		assignmentID: w.assignmentID,
		worktree:     w.worktree,
		baseBranch:   w.baseBranch,
		retrySnapshot: workerAssignmentSnapshot{
			execution:    w.execution,
			worktree:     w.worktree,
			runtime:      w.runtime,
			model:        w.model,
			reasoning:    w.reasoning,
			epicID:       w.epicID,
			baseBranch:   w.baseBranch,
			targetBranch: w.targetBranch,
		},
		preempted: w.state == protocol.WorkerPreempting,
	}
	st.retryContext, st.retryPending = d.pendingQGRetries[workerID]
	if st.preempted && st.beadID != "" {
		// Keep the bead reserved while its durable assignment is terminalized.
		// Without this guard a concurrently idle replacement can create a second
		// active assignment after the worker is removed but before cleanup runs.
		d.assigningBeads[st.beadID] = true
	}
	delete(d.workers, workerID)
	return st, true, false
}

func (d *Dispatcher) connCloseCleanup(workerID string, conn net.Conn) {
	if workerID == "" {
		return
	}
	st, proceed, notify := d.takeConnCloseState(workerID, conn)
	if !proceed {
		if notify {
			d.notifyAssignLoop()
		}
		return
	}
	beadID, assignmentID, worktree, baseBranch := st.beadID, st.assignmentID, st.worktree, st.baseBranch
	retryContext, retryPending := st.retryContext, st.retryPending
	retrySnapshot := st.retrySnapshot
	preempted := st.preempted

	if preempted && beadID != "" {
		d.reconcilePreemptedDisconnect(workerID, beadID, assignmentID, worktree)
		return
	}
	if retryPending {
		if err := d.restoreQGRetryHandoff(context.Background(), workerID, beadID, assignmentID, retryContext, retrySnapshot); err != nil {
			_ = d.logEvent(context.Background(), "qg_retry_feedback_restore_failed", "dispatcher", beadID, workerID, err.Error())
			return
		}
		d.notifyAssignLoop()
		return
	}

	if beadID != "" {
		if d.quarantineDisconnectedPreservedAssignment(context.Background(), workerID, beadID, assignmentID, worktree, baseBranch, "") {
			d.clearBeadTracking(beadID)
			d.notifyAssignLoop()
			return
		}
		d.clearBeadTracking(beadID)
		d.safeGo(func() {
			_ = d.updateBeadStatus(context.Background(), beadID, "open")
		})
	}

	// Wake the assign loop so reconcileScale can spawn a replacement immediately
	// rather than waiting for the next fsnotify event or fallback tick.
	d.notifyAssignLoop()
}

func (d *Dispatcher) quarantineDisconnectedPreservedAssignment(ctx context.Context, workerID, beadID string, assignmentID int64, worktree, baseBranch, cause string) bool {
	if assignmentID <= 0 {
		return false
	}
	active, err := d.assignmentActive(ctx, assignmentID, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "disconnected_assignment_lookup_failed", "dispatcher", beadID, workerID, err.Error())
		return true
	}
	if !active {
		return false
	}
	blocked, details, err := d.recoveryWorkBlocked(ctx, beadID, worktree, baseBranch)
	if err == nil && !blocked {
		return false
	}
	if err != nil {
		details = appendRecoveryDetail(details, "error: "+err.Error())
	}
	if details == "" {
		details = "disconnected worker left recovery state requiring preservation"
	}
	if cause != "" {
		details = appendRecoveryDetail(details, cause)
	}
	_, err = d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: assignmentID,
		WorkerID:     workerID,
		Worktree:     worktree,
		Branch:       protocol.BranchPrefix + beadID,
		Reason:       "stale_active_assignment",
		Details:      details,
	})
	if err != nil {
		_ = d.logEvent(ctx, "disconnected_assignment_quarantine_failed", "dispatcher", beadID, workerID, err.Error())
		return true
	}
	if err := d.updateBeadStatus(ctx, beadID, "blocked"); err != nil {
		_ = d.logEvent(ctx, "disconnected_assignment_block_failed", "dispatcher", beadID, workerID, err.Error())
		if restoreErr := d.restoreDisconnectedAssignmentActive(ctx, assignmentID); restoreErr != nil {
			_ = d.logEvent(ctx, "disconnected_assignment_restore_failed", "dispatcher", beadID, workerID, restoreErr.Error())
		}
	}
	return true
}

func (d *Dispatcher) assignmentActive(ctx context.Context, assignmentID int64, beadID string) (bool, error) {
	var active bool
	err := d.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM assignments WHERE id=? AND bead_id=? AND status='active')`, assignmentID, beadID).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("lookup disconnected assignment: %w", err)
	}
	return active, nil
}

func (d *Dispatcher) restoreDisconnectedAssignmentActive(ctx context.Context, assignmentID int64) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='active', completed_at=NULL WHERE id=? AND status='quarantined'`, assignmentID)
	if err != nil {
		return fmt.Errorf("restore disconnected assignment active: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("restore disconnected assignment active rows: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("restore disconnected assignment active: assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
}

func (d *Dispatcher) reconcilePreemptedDisconnect(workerID, beadID string, assignmentID int64, worktree string) {
	ctx := context.Background()
	if !d.terminalizePreemptedDisconnect(ctx, workerID, beadID, assignmentID, worktree) {
		return
	}
	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "preempt_disconnect_bead_reset_failed", "dispatcher", beadID, workerID, err.Error())
		}
	}
	d.clearBeadTracking(beadID)
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
	d.notifyAssignLoop()
}

func (d *Dispatcher) terminalizePreemptedDisconnect(ctx context.Context, workerID, beadID string, assignmentID int64, worktree string) bool {
	err := d.completeAssignment(ctx, assignmentID, beadID)
	if err == nil {
		return true
	}
	_ = d.logEvent(ctx, "preempt_disconnect_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	_, quarantineErr := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: assignmentID,
		WorkerID:     workerID,
		Worktree:     worktree,
		Branch:       protocol.BranchPrefix + beadID,
		Reason:       "preempted_worker_disconnect",
		Details:      err.Error(),
	})
	if quarantineErr == nil {
		return true
	}
	_ = d.logEvent(ctx, "preempt_disconnect_assignment_quarantine_failed", "dispatcher", beadID, workerID, quarantineErr.Error())
	return false
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
		d.connCloseCleanup(workerID, conn)
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

		// Handle RERANK_BY_IDS_REQUEST from short-lived callers (e.g. search pipeline).
		if msg.Type == protocol.MsgRerankByIDsRequest {
			d.handleRerankByIDsWithResponse(ctx, conn, msg)
			return
		}

		if d.handleWorkRequestConn(ctx, conn, msg) {
			return
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
	case protocol.MsgCheckpointAck:
		d.handleCheckpointAck(ctx, workerID, msg)
	case protocol.MsgCapabilityRefreshACK:
		d.handleCapabilityRefreshAck(ctx, workerID, msg)
	}
}

func (d *Dispatcher) handleHeartbeat(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Heartbeat == nil {
		return
	}
	contextIncreased := false
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.lastSeen = d.nowFunc()
		contextIncreased = msg.Heartbeat.ContextPct > w.contextPct
		w.contextPct = msg.Heartbeat.ContextPct
	}
	d.mu.Unlock()
	if contextIncreased {
		d.recordWorkerProgress(ctx, workerID, msg.Heartbeat.BeadID, "context_pct_increase")
	}

	d.broadcastEvent("heartbeat", msg.Heartbeat.BeadID, workerID)

	// Trigger a checkpoint when context usage crosses the configured threshold
	// and no checkpoint is already in-flight for this bead (§9.3).
	if d.cfg.CheckpointThreshold > 0 &&
		msg.Heartbeat.ContextPct >= d.cfg.CheckpointThreshold &&
		msg.Heartbeat.BeadID != "" &&
		d.checkpoints.get(msg.Heartbeat.BeadID) == nil {
		d.triggerCheckpoint(ctx, msg.Heartbeat.BeadID, workerID, msg.Heartbeat.ContextPct)
	}
}

func (d *Dispatcher) handleTypeChangedToEpic(ctx context.Context, workerID, beadID string, release doneWorkerRelease) bool {
	if release.isEpicDecomp {
		return false
	}
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil || detail.Type != "epic" {
		return false
	}
	_ = d.logEvent(ctx, "type_changed_to_epic", workerID, beadID, workerID, "")
	d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: release.assignmentID,
		WorkerID:     workerID,
		Worktree:     release.worktree,
		Branch:       release.branch,
		Reason:       "type_changed_to_epic",
		Details:      "worker completed a task that was promoted to epic before merge",
	})
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, release.assignmentID)
	return true
}

func (d *Dispatcher) completeManualIntegration(ctx context.Context, beadID, workerID string, release doneWorkerRelease) {
	if err := d.completeAssignment(ctx, release.assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "manual_integration_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	}
	if err := d.updateBeadStatus(ctx, beadID, "blocked"); err != nil {
		_ = d.logEvent(ctx, "manual_integration_status_failed", "dispatcher", beadID, workerID, err.Error())
	}
	detail := fmt.Sprintf(`{"branch":%q,"worktree":%q,"target_branch":%q}`, release.branch, release.worktree, release.targetBranch)
	_ = d.logEvent(ctx, "manual_integration_required", "dispatcher", beadID, workerID, detail)
	d.escalate(ctx, fmt.Sprintf("[ORO-DISPATCH] MANUAL_INTEGRATION: %s — review and merge %s from %s.", beadID, release.branch, release.worktree), beadID, workerID)
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, release.assignmentID)
}

type doneWorkerRelease struct {
	worktree     string
	branch       string
	epicID       string
	targetBranch string
	assignmentID int64
	isEpicDecomp bool
	ok           bool
}

func (d *Dispatcher) releaseWorkerAfterDoneLocked(workerID, beadID string) doneWorkerRelease {
	w, ok := d.workers[workerID]
	if !ok {
		return doneWorkerRelease{}
	}

	release := doneWorkerRelease{
		worktree:     w.worktree,
		branch:       protocol.BranchPrefix + beadID,
		epicID:       w.epicID,
		targetBranch: w.targetBranch,
		assignmentID: w.assignmentID,
		isEpicDecomp: w.isEpicDecomp,
		ok:           true,
	}
	spawnFor := w.spawnFor

	if spawnFor {
		w.state = protocol.WorkerShuttingDown
	} else {
		w.state = protocol.WorkerReserved
	}

	if spawnFor {
		d.shutdownCompletedSpawnForWorkerLocked(w)
	}
	return release
}

func (d *Dispatcher) releaseWorkerAfterDoneTerminal(workerID, beadID string, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID || w.assignmentID != assignmentID {
		return
	}
	if w.spawnFor {
		w.state = protocol.WorkerShuttingDown
		d.shutdownCompletedSpawnForWorkerLocked(w)
	} else {
		w.state = protocol.WorkerIdle
	}
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.targetBeadID = ""
	w.lastProgress = d.nowFunc()
	d.notifyAssignLoop()
}

// storeRejectionFeedback persists reviewer feedback in the rejection_history
// table (not memories), so rejections accumulate across retry cycles without
// polluting the memory search index. Best-effort: errors are silently ignored.
func (d *Dispatcher) storeRejectionFeedback(ctx context.Context, beadID, feedback string) {
	if d.memoryServices.InsertRejection == nil || feedback == "" {
		return
	}
	_ = d.memoryServices.InsertRejection(ctx, beadID, "", feedback)
}

// buildRejectionMemoryContext stores the current reviewer feedback in
// rejection_history and returns a MemoryContext containing current and prior
// rejection feedback. General bead knowledge is supplied through Cards.
func (d *Dispatcher) buildRejectionMemoryContext(ctx context.Context, beadID, feedback string) string {
	// Fetch prior rejections BEFORE storing the current one so "prior"
	// truly means prior and the current feedback doesn't appear twice.
	var priorCtx string
	if d.memoryServices.GetRejections != nil {
		if rejections, err := d.memoryServices.GetRejections(ctx, beadID); err == nil && len(rejections) > 0 {
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
		return priorCtx
	}

	rejectionSection := fmt.Sprintf("## Review Rejection Feedback\n%s", feedback)

	parts := []string{rejectionSection}
	if priorCtx != "" {
		parts = append(parts, priorCtx)
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

// classifyQGError returns "systemic" for persistent environment failures (e.g.
// missing quality_gate.sh) and "transient" for recoverable interruptions (e.g.
// context cancellation). Systemic errors trigger work preservation; transient
// errors proceed with standard cleanup.
func classifyQGError(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "systemic"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "transient"
	}
	return "systemic"
}

// handlePreMergeQGError classifies a pre-merge QG infrastructure error, records the
// classification before any cleanup, and performs class-appropriate cleanup.
// Systemic errors preserve the agent branch for human discovery; transient errors
// proceed with full cleanup. Always returns false.
func (d *Dispatcher) handlePreMergeQGError(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, err error) bool {
	class := classifyQGError(err)

	// Record classification before any escalation or cleanup so it is observable
	// even if subsequent steps fail.
	_ = d.logEvent(ctx, "pre_merge_qg_error_classified", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"class":%q,"error":%q}`, class, err.Error()))

	if class == "systemic" {
		d.handleSystemicPreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, err)
		return false
	}

	// Transient: standard escalation and full cleanup.
	d.escalate(ctx,
		protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge QG error", err.Error()),
		beadID, workerID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
		_ = d.requeueAssignment(ctx, assignmentID)
	} else {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q,"reason":"external_close_without_merge_proof"}`, protocol.BranchPrefix+beadID, worktree))
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

func (d *Dispatcher) handleSystemicPreMergeQGError(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, err error) {
	branch := protocol.BranchPrefix + beadID
	_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"worktree":%q}`, branch, worktree))
	d.escalate(ctx,
		protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge QG systemic error", err.Error()),
		beadID, workerID)
	d.finalizeSystemicPreMergeQGError(ctx, beadID, workerID, assignmentID)
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
}

func (d *Dispatcher) finalizeSystemicPreMergeQGError(ctx context.Context, beadID, workerID string, assignmentID int64) {
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if updateErr := d.updateBeadStatus(ctx, beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "pre_merge_qg_reopen_failed", "dispatcher", beadID, workerID, updateErr.Error())
		}
		if requeueErr := d.requeueAssignment(ctx, assignmentID); requeueErr != nil {
			_ = d.logEvent(ctx, "pre_merge_qg_requeue_failed", "dispatcher", beadID, workerID, requeueErr.Error())
		}
		return
	}
	_ = d.completeAssignment(ctx, assignmentID, beadID)
}

// handlePreMergeQGFailure classifies the pre-merge QG failure output, records the
// occurrence, handles cleanup, and returns false so the caller aborts the merge.
// For deterministic failures it records via RecordQGFailureOccurrence; for systemic
// failures it creates or reuses an infra incident. In both cases the original bead
// is reopened unless it was externally closed before this handler runs.
func (d *Dispatcher) handlePreMergeQGFailure(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, qgOutput string) bool {
	qgFingerprint, qgSummary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	rec := QGFailureRecord{
		ID:           fmt.Sprintf("%s:%s:%d:pre-merge", beadID, workerID, assignmentID),
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "pre-merge",
		Fingerprint:  qgFingerprint,
		Summary:      qgSummary,
		Output:       qgOutput,
	}
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{RetryExhausted: true})

	_ = d.logEvent(ctx, "qg_failed", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"output":%q,"fingerprint":%q,"class":%q,"decision":%q}`,
			qgOutput, qgFingerprint, cls.Class, cls.Decision))

	if cls.Decision == QGFailureDecisionCreateOrReuseInfra {
		d.recordPreMergeInfraIncident(ctx, rec, cls)
	} else {
		d.recordPreMergeDeterministicFailure(ctx, rec, cls)
	}

	// Only requeue if not already closed on main — a stale QG failure must
	// not reopen a bead that was successfully merged externally.
	if d.shouldReopenQGOriginal(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
		_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q}`, protocol.BranchPrefix+beadID, worktree))
		_ = d.requeueAssignment(ctx, assignmentID)
	} else {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "pre_merge_qg_work_preserved", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q,"reason":"external_close_without_merge_proof"}`, protocol.BranchPrefix+beadID, worktree))
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

// recordPreMergeInfraIncident creates or reuses the infra incident for a
// systemic pre-merge QG failure and logs a qg_infra_incident_reused event.
func (d *Dispatcher) recordPreMergeInfraIncident(ctx context.Context, rec QGFailureRecord, cls QGFailureClassification) {
	incident, err := d.createOrReuseQGInfraIncident(ctx, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
		return
	}
	_ = d.logEvent(ctx, "qg_infra_incident_reused", "dispatcher", rec.BeadID, rec.WorkerID,
		fmt.Sprintf(`{"incident_id":%d,"class":%q,"fingerprint":%q}`, incident.ID, cls.Class, rec.Fingerprint))
}

// recordPreMergeDeterministicFailure records the QG occurrence and links it to
// the originating bead. Errors at either step are logged as separate events so
// they remain debuggable without altering the reopen path.
func (d *Dispatcher) recordPreMergeDeterministicFailure(ctx context.Context, rec QGFailureRecord, cls QGFailureClassification) {
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
		return
	}
	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", "dispatcher", rec.BeadID, rec.WorkerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), rec.Fingerprint, incident.ID))
	}
}

func (d *Dispatcher) guardQGRegression(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, targetBranch string) bool {
	if !d.cfg.RegressionRevert {
		return true
	}
	base, ok := d.takeQGBaselineForBead(beadID)
	if !ok {
		return true
	}

	regression, err := d.detectQGRegression(ctx, base, worktree, d.qgMutationBase(targetBranch))
	if err != nil {
		return d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, err)
	}
	if regression == (qgRegression{}) {
		return true
	}

	if err := d.revertRegressedRetry(ctx, base, worktree); err != nil {
		_ = d.logEvent(ctx, "qg_regression_revert_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"test":%q,"error":%q}`, regression.TestName, err.Error()))
		d.escalate(ctx,
			protocol.FormatEscalation(protocol.EscStuck, beadID, "QG_REGRESSION_REVERT_FAILED", err.Error()),
			beadID, workerID)
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return false
	}

	_ = d.logEvent(ctx, "qg_regression_reverted", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"test":%q,"baseline_passed":%t,"current_passed":%t}`,
			regression.TestName, regression.BaselinePassed, regression.CurrentPassed))
	if d.shouldReopenQGOriginal(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
		_ = d.requeueAssignment(ctx, assignmentID)
	} else {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

// checkPreMergeQG runs the local pre-merge quality gate before merging. Mutation
// testing is opt-in so local branch merges do not pay that cost by default.
// It returns true when the gate passes and the merge should proceed. On failure
// or error it handles cleanup and returns false so the caller can return early.
var errPreMergeQGAlreadyHandled = errors.New("pre-merge QG failure already handled")

type preMergeQGFailureError struct {
	output string
}

func (e *preMergeQGFailureError) Error() string {
	return "pre-merge quality gate failed"
}

type preMergeQGRunError struct {
	err error
}

func (e *preMergeQGRunError) Error() string {
	return fmt.Sprintf("run pre-merge quality gate: %v", e.err)
}

func (e *preMergeQGRunError) Unwrap() error {
	return e.err
}

// runPreMergeQG executes the dispatcher quality gate for a final candidate
// worktree. It leaves failure handling to its caller, except for regression
// protection, which already performs the required recovery itself.
func (d *Dispatcher) runPreMergeQG(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, targetBranch string) error {
	if err := d.observeStorageController(ctx); err != nil {
		return err
	}
	if !d.storageAdmissionAllowed() {
		return errStorageAdmissionPaused
	}
	mutationBase := d.qgMutationBase(targetBranch)
	if !d.guardQGRegression(ctx, beadID, workerID, worktree, assignmentID, mutationBase) {
		return errPreMergeQGAlreadyHandled
	}
	qgPassed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, mutationBase)
	if qgErr != nil {
		return &preMergeQGRunError{err: qgErr}
	}
	if !qgPassed {
		return &preMergeQGFailureError{output: qgOutput}
	}
	return nil
}

// checkPreMergeQG preserves the direct local-gate entry point used by the
// existing lifecycle checks. Dispatcher merges invoke runPreMergeQG through
// merge.Opts.PreFFCheck so the gate sees the rebased worktree.
func (d *Dispatcher) checkPreMergeQG(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, targetBranch string) bool {
	err := d.runPreMergeQG(ctx, beadID, workerID, worktree, assignmentID, targetBranch)
	if err == nil {
		return true
	}
	if errors.Is(err, errPreMergeQGAlreadyHandled) {
		return false
	}
	var qgFailure *preMergeQGFailureError
	if errors.As(err, &qgFailure) {
		return d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgFailure.output)
	}
	var qgRunErr *preMergeQGRunError
	if errors.As(err, &qgRunErr) {
		return d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, qgRunErr.err)
	}
	return d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, err)
}

func (d *Dispatcher) handlePreFFCheckError(ctx context.Context, beadID, workerID, worktree string, assignmentID int64, err error) bool {
	var preFFErr *merge.PreFFCheckError
	if !errors.As(err, &preFFErr) {
		return false
	}
	if errors.Is(preFFErr, errPreMergeQGAlreadyHandled) {
		return true
	}
	var qgFailure *preMergeQGFailureError
	if errors.As(preFFErr, &qgFailure) {
		d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgFailure.output)
		return true
	}
	var qgRunErr *preMergeQGRunError
	if errors.As(preFFErr, &qgRunErr) {
		d.handlePreMergeQGError(ctx, beadID, workerID, worktree, assignmentID, qgRunErr.err)
		return true
	}
	return false
}

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

	if mergeErr := d.advanceTargetToEpic(ctx, epicBranch, targetBranch); mergeErr != nil {
		if recoverErr := d.recoverEpicDivergence(ctx, epicID, workerID, epicBranch, targetBranch, mergeErr); recoverErr != nil {
			return recoverErr
		}
	}

	_ = d.logEvent(ctx, "epic_ff_merged", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"branch":%q}`, epicBranch))

	if delErr := d.worktrees.DeleteBranch(ctx, epicBranch); delErr != nil {
		_ = d.logEvent(ctx, "epic_branch_delete_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, delErr.Error()))
	}
	return nil
}

// advanceTargetToEpic fast-forwards targetBranch to the tip of epicBranch. For
// the HEAD branch it uses an ff-only merge so the working tree advances; for any
// other target it advances the ref directly (no checkout required).
// to the Manager instead of re-assigning the bead to the worker.
const maxQGRetries = 3

func (d *Dispatcher) handleShutdownApproved(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ShutdownApproved == nil {
		return
	}

	_ = d.logEvent(ctx, "shutdown_approved", workerID, "", workerID, "")

	// Send hard SHUTDOWN to finalize
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var beadID string
	var assignmentID int64
	if ok {
		w.shutdownApproved = true
		beadID = w.beadID // capture before clearing
		assignmentID = w.assignmentID
		if w.shutdownReason == shutdownReasonScaleDown || w.spawnFor {
			sendShutdownWithoutBuffering(w)
		} else {
			_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		}
		w.markShuttingDownWithoutAssignment()
	}
	d.mu.Unlock()

	// Requeue any in-flight bead so it can be reassigned.
	if beadID != "" {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "scale_down_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		d.clearBeadTracking(beadID)
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		_ = d.logEvent(ctx, "bead_requeued_scale_down", "dispatcher", beadID, workerID,
			`{"reason":"shutdown_approved"}`)
	}
}

// GracefulShutdownWorker, shutdownWaitLoop, handleShutdownTimeout, checkShutdownApproved → worker_pool.go

// --- Priority queue / assignment loop ---
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
	Managed           bool    `json:"managed"`
	SpawnFor          bool    `json:"spawn_for,omitempty"`
	TargetBeadID      string  `json:"target_bead_id,omitempty"`
}

// QGFailureStatus summarises open quality-gate failure incidents for the
// dispatcher status response.
type QGFailureStatus struct {
	OpenIncidents      int
	Occurrences30m     int
	TopFingerprints    []string
	RecentFingerprints []string
}

type qgIncidentStatusRow struct {
	ID          int64
	Fingerprint string
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
	MaxWorkers          int            `json:"max_workers"`
	ManagedCount        int            `json:"managed_count"`
	UnmanagedCount      int            `json:"unmanaged_count"`
	PendingWorkerCount  int            `json:"pending_worker_count"`
	UptimeSeconds       float64        `json:"uptime_seconds"`
	PendingHandoffCount int            `json:"pending_handoff_count"`
	AttemptCounts       map[string]int `json:"attempt_counts,omitempty"`
	ProgressTimeoutSecs float64        `json:"progress_timeout_secs"`

	// QG failure incident fields
	QGFailureIncidentsOpen       int                          `json:"qg_failure_incidents_open"`
	QGFailureOccurrences30m      int                          `json:"qg_failure_occurrences_30m"`
	QGFailureTopFingerprints     []string                     `json:"qg_failure_top_fingerprints,omitempty"`
	AssignmentFrozenByQuarantine bool                         `json:"assignment_frozen_by_quarantine"`
	BlockingRecoveryQuarantines  int                          `json:"blocking_recovery_quarantines,omitempty"`
	AssignmentFreezeReason       string                       `json:"assignment_freeze_reason,omitempty"`
	Health                       *factoryhealth.FactoryHealth `json:"health,omitempty"`
}

// applyDirective transitions the dispatcher state machine and returns a detail
// string for the ACK response. Returns an error for invalid args (e.g. scale).
func (d *Dispatcher) recordWorkerProgress(ctx context.Context, workerID, beadID, source string) {
	_ = d.logEvent(ctx, "worker_progress", source, beadID, workerID, "")
}

func (d *Dispatcher) logEvent(ctx context.Context, evType, source, beadID, workerID, payload string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
		evType, source, beadID, workerID, payload)
	if err != nil {
		return fmt.Errorf("log event: %w", err)
	}
	d.broadcastEvent(evType, beadID, workerID)
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
	d.broadcastEvent(evType, beadID, workerID)
	return nil
}

func (d *Dispatcher) broadcastEvent(evType, beadID, workerID string) {
	if d.sseBroadcaster != nil {
		d.sseBroadcaster.Send(evType, beadID, workerID)
	}
}

// escalate sends a message to the Manager via the escalator and logs any
// delivery failures to the events table. This prevents silent failures when
// the tmux session is dead.
//
// For escalation types that have playbooks (STUCK_WORKER, MERGE_CONFLICT,
// MISSING_AC), it also spawns a one-shot claude -p agent to take corrective
// action autonomously.
var errDecomposeValidationUnavailable = errors.New("decompose validation unavailable")

func (d *Dispatcher) pruneStaleAgentBranches(ctx context.Context) {
	if d.repoRoot == "" {
		return
	}
	out, err := d.commandRunner().Run(ctx, "git", "-C", d.repoRoot, "branch", "--list", "agent/*")
	if err != nil {
		_ = d.logEvent(ctx, "startup_prune_branches_list_failed", "dispatcher", "", "", err.Error())
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "*+"))
		if branch == "" {
			continue
		}
		if _, delErr := d.commandRunner().Run(ctx, "git", "-C", d.repoRoot, "branch", "-d", branch); delErr != nil {
			_ = d.logEvent(ctx, "startup_prune_branch_delete_failed", "dispatcher", "", "", branch+": "+delErr.Error())
		}
	}
}

// deleteStaleAgentBranch deletes agent/<beadID> if it exists and is already
// merged into targetBranch, logging the outcome.
// Called before worktree.Create to ensure the new worktree always branches from
// the resolved assignment target HEAD.
// If git cannot safely delete the branch, the branch is recovery-quarantined
// and assignment aborts. Startup/retry recovery must preserve ambiguous branch
// state instead of force-deleting or removing the checked-out worktree.
var errResolvedPreservedMismatch = errors.New("resolved preserved branch/worktree mismatch")

func (d *Dispatcher) deleteStaleAgentBranch(ctx context.Context, beadID, workerID, targetBranch string) error {
	branch := protocol.BranchPrefix + beadID
	if targetBranch == "" {
		targetBranch = d.cfg.DefaultBranch
	}
	exists, err := d.worktrees.BranchExists(ctx, branch)
	if err != nil {
		return fmt.Errorf("check stale branch %s exists: %w", branch, err)
	}
	if !exists {
		return nil
	}
	preservedAssignmentID, preserved, err := d.resolvedPreservedMismatchForRequeuedBead(ctx, beadID)
	if err != nil {
		return fmt.Errorf("check stale branch %s preserved recovery state: %w", branch, err)
	}
	if preserved {
		_ = d.logEvent(ctx, "stale_agent_branch_cleanup_suppressed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"assignment_id":%d,"reason":"resolved_preserved_mismatch"}`,
				branch, preservedAssignmentID))
		return fmt.Errorf("%w: assignment %d", errResolvedPreservedMismatch, preservedAssignmentID)
	}
	err = d.worktrees.DeleteBranchMergedInto(ctx, branch, targetBranch)
	if err == nil {
		_ = d.logEvent(ctx, "stale_agent_branch_deleted", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target_branch":%q}`, branch, targetBranch))
		return nil
	}
	reason := "unsafe_stale_branch"
	if strings.Contains(strings.ToLower(err.Error()), "checked out") {
		reason = "branch_worktree_mismatch"
	}
	worktreePath := filepath.Join(d.repoRoot, ".worktrees", beadID)
	if _, qErr := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:   beadID,
		WorkerID: workerID,
		Worktree: worktreePath,
		Branch:   branch,
		Reason:   reason,
		Details:  err.Error(),
	}); qErr != nil {
		_ = d.logEvent(ctx, "stale_branch_quarantine_failed", "dispatcher", beadID, workerID, qErr.Error())
		return fmt.Errorf("delete stale branch %s: %w", branch, err)
	}
	_ = d.logEvent(ctx, "stale_agent_branch_quarantined", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"reason":%q,"error":%q}`, branch, reason, err.Error()))
	return fmt.Errorf("stale branch %s quarantined: %w", branch, err)
}

// resetOrphanedBeads resets recoverable dispatcher-owned in_progress beads back
// to open on startup. Human-owned in_progress beads are left untouched because
// they have no dispatcher-owned active assignment state to recover from.
// Errors are non-fatal — logged via logEvent and startup continues.
func (d *Dispatcher) resetOrphanedBeads(ctx context.Context, recoverable map[string]bool) (reopened, skipped int) {
	beads, err := d.beads.InProgress(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "startup_reset_list_failed", "dispatcher", "", "", err.Error())
		return 0, 0
	}
	for _, b := range beads {
		if !recoverable[b.ID] {
			skipped++
			continue
		}
		if updateErr := d.updateBeadStatus(ctx, b.ID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "startup_reset_bead_failed", "dispatcher", b.ID, "", updateErr.Error())
			continue
		}
		reopened++
	}
	return reopened, skipped
}

// restoreState reconstructs the in-memory attemptCounts and handoffCounts maps
// from recoverable active assignments persisted in SQLite. This ensures
// tracking state survives a dispatcher restart without reopening inconsistent
// rows that would destroy or overwrite recoverable work.
type startupRecoveryStats struct {
	recoverable   int
	quarantined   int
	retiredClosed int
}

type restoredAssignment struct {
	id           int64
	beadID       string
	worktree     string
	attemptCount int
	handoffCount int
}

type quarantinedAssignment struct {
	id       int64
	beadID   string
	workerID string
	worktree string
	branch   string
	reason   string
}

type retiredClosedAssignment struct {
	id     int64
	beadID string
}

func (d *Dispatcher) restoreState(ctx context.Context) (map[string]bool, startupRecoveryStats, error) {
	restored, quarantined, retiredClosed, err := d.loadActiveAssignments(ctx)
	if err != nil {
		return nil, startupRecoveryStats{}, err
	}
	d.processRetiredClosedAssignments(ctx, retiredClosed)
	d.processQuarantined(ctx, quarantined)
	recoverable := d.applyRestoredAssignments(restored)
	d.restoreInflightCheckpoints(ctx, restored)
	stats := startupRecoveryStats{
		recoverable:   len(restored),
		quarantined:   len(quarantined),
		retiredClosed: len(retiredClosed),
	}
	return recoverable, stats, nil
}

// loadActiveAssignments reads active and shutdown-requeued assignments and
// partitions them into restorable (worktree + branch present) and quarantinable
// sets. Generic completed assignments are intentionally ignored.
func (d *Dispatcher) loadActiveAssignments(ctx context.Context) ([]restoredAssignment, []quarantinedAssignment, []retiredClosedAssignment, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, bead_id, worker_id, worktree, attempt_count, handoff_count FROM assignments WHERE status IN ('active', 'requeued')`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query active assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		restored      []restoredAssignment
		quarantined   []quarantinedAssignment
		retiredClosed []retiredClosedAssignment
	)
	for rows.Next() {
		var a restoredAssignment
		var workerID string
		if err := rows.Scan(&a.id, &a.beadID, &workerID, &a.worktree, &a.attemptCount, &a.handoffCount); err != nil {
			return nil, nil, nil, fmt.Errorf("scan assignment: %w", err)
		}
		if d.assignmentHasRetirableClosedBead(ctx, a.beadID, a) {
			retiredClosed = append(retiredClosed, retiredClosedAssignment{
				id:     a.id,
				beadID: a.beadID,
			})
			continue
		}
		if reason := d.classifyAssignment(ctx, a); reason != "" {
			if reason == "branch_worktree_mismatch" && d.resolvedPreservedMismatchAssignment(ctx, a.id) {
				continue
			}
			quarantined = append(quarantined, quarantinedAssignment{
				id:       a.id,
				beadID:   a.beadID,
				workerID: workerID,
				worktree: a.worktree,
				branch:   protocol.BranchPrefix + a.beadID,
				reason:   reason,
			})
			continue
		}
		restored = append(restored, a)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterate assignments: %w", err)
	}
	return restored, quarantined, retiredClosed, nil
}

func (d *Dispatcher) assignmentHasRetirableClosedBead(ctx context.Context, beadID string, a restoredAssignment) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "startup_closed_assignment_lookup_failed", "dispatcher", beadID, "", err.Error())
		return false
	}
	if detail == nil || !strings.EqualFold(detail.Status, "closed") {
		return false
	}
	switch d.classifyAssignment(ctx, a) {
	case "missing_worktree", "missing_worktree_path", "missing_branch":
		return true
	default:
		return false
	}
}

// classifyAssignment returns "" if the assignment is recoverable, or a
// quarantine reason otherwise: missing_worktree | missing_worktree_path |
// branch_check_failed | missing_branch | branch_worktree_mismatch.
func (d *Dispatcher) classifyAssignment(ctx context.Context, a restoredAssignment) string {
	branch := protocol.BranchPrefix + a.beadID
	switch {
	case a.worktree == "":
		return "missing_worktree"
	case !d.worktrees.Exists(ctx, a.worktree):
		return "missing_worktree_path"
	}
	exists, branchErr := d.worktrees.BranchExists(ctx, branch)
	switch {
	case branchErr != nil:
		return "branch_check_failed"
	case !exists:
		return "missing_branch"
	}
	currentBranch, currentErr := d.worktrees.CurrentBranch(ctx, a.worktree)
	if currentErr != nil || currentBranch != branch {
		return "branch_worktree_mismatch"
	}
	return ""
}

// processQuarantined records each unsafe recovery state in the durable
// recovery quarantine table and keeps the assignment visible as quarantined.
func (d *Dispatcher) processQuarantined(ctx context.Context, quarantined []quarantinedAssignment) {
	for _, q := range quarantined {
		if _, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
			BeadID:       q.beadID,
			AssignmentID: q.id,
			WorkerID:     q.workerID,
			Worktree:     q.worktree,
			Branch:       q.branch,
			Reason:       q.reason,
			Details:      "startup recovery could not prove branch/worktree consistency",
		}); err != nil {
			_ = d.logEvent(ctx, "startup_recovery_quarantine_failed", "dispatcher", q.beadID, q.workerID, err.Error())
			continue
		}
		_ = d.logEvent(ctx, "startup_recovery_quarantined", "dispatcher", q.beadID, "",
			fmt.Sprintf(`{"assignment_id":%d,"reason":%q}`, q.id, q.reason))
	}
}

func (d *Dispatcher) processRetiredClosedAssignments(ctx context.Context, retired []retiredClosedAssignment) {
	for _, assignment := range retired {
		if err := d.completeAssignment(ctx, assignment.id, assignment.beadID); err != nil {
			_ = d.logEvent(ctx, "startup_closed_assignment_retire_failed", "dispatcher", assignment.beadID, "", err.Error())
			continue
		}
		_ = d.logEvent(ctx, "startup_closed_assignment_retired", "dispatcher", assignment.beadID, "",
			fmt.Sprintf(`{"assignment_id":%d,"reason":"closed_empty_state"}`, assignment.id))
	}
}

func (d *Dispatcher) recoveryWorkBlocked(ctx context.Context, beadID, worktree, baseBranch string) (blocked bool, details string, err error) {
	if worktree == "" {
		return true, "missing worktree path", nil
	}
	if !d.worktrees.Exists(ctx, worktree) {
		return true, "worktree path missing: " + worktree, nil
	}

	dirty, dirtyStatus, dirtyErr := d.worktreeDirty(ctx, beadID, worktree)
	if dirty || dirtyErr != nil {
		return dirty, dirtyStatus, dirtyErr
	}
	return d.branchHasUnmergedWork(ctx, beadID, worktree, baseBranch)
}

func (d *Dispatcher) worktreeDirty(ctx context.Context, beadID, worktree string) (dirty bool, status string, err error) {
	out, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "status", "--porcelain")
	if err != nil {
		return false, "", fmt.Errorf("git status in %s: %w", worktree, err)
	}
	var remaining []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) >= 4 && line[:2] == "??" && isManagedQualityGateCachePath(beadID, strings.TrimSpace(line[3:])) {
			continue
		}
		if line != "" {
			remaining = append(remaining, line)
		}
	}
	status = strings.Join(remaining, "\n")
	return status != "", status, nil
}

func (d *Dispatcher) branchHasUnmergedWork(ctx context.Context, beadID, worktree, baseBranch string) (blocked bool, details string, err error) {
	if beadID == "" {
		return false, "", nil
	}
	if baseBranch == "" {
		baseBranch = d.cfg.DefaultBranch
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	branch := protocol.BranchPrefix + beadID
	out, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-list", "--count", baseBranch+".."+branch)
	if err != nil {
		return false, "", fmt.Errorf("git rev-list %s..%s in %s: %w", baseBranch, branch, worktree, err)
	}
	countText := strings.TrimSpace(string(out))
	if countText == "" {
		return false, "", nil
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return false, "", fmt.Errorf("parse git rev-list count %q for %s..%s: %w", countText, baseBranch, branch, err)
	}
	if count == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("%s has %d commit(s) not in %s", branch, count, baseBranch), nil
}

func appendRecoveryDetail(details, extra string) string {
	if details == "" {
		return extra
	}
	return details + "; " + extra
}

// staleAssignmentSweepLoop sweeps stale active assignments after a startup
// grace window, then keeps sweeping periodically for long-lived dispatcher
// sessions. The initial grace preserves time for workers from a surviving
// restart to reconnect before their assignments are considered stale.
func (d *Dispatcher) staleAssignmentSweepLoop(ctx context.Context) {
	graceWindow := 3 * d.cfg.HeartbeatTimeout
	select {
	case <-time.After(graceWindow):
	case <-ctx.Done():
		return
	case <-d.shutdownCh:
		return
	}
	d.abandonStaleActiveAssignments(ctx)

	ticker := time.NewTicker(d.cfg.HeartbeatTimeout)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.abandonStaleActiveAssignments(ctx)
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		}
	}
}

// abandonStaleActiveAssignments walks every status='active' assignment row
// and quarantines any whose worker_id is not currently in the connected pool.
// A silently dead worker can leave useful work in its worktree/branch, so the
// row stays visible as recovery-owned state until an operator resolves it.
//
// Filed by oro-tczh after a silent dispatcher death (oro-zxxn) left 9 dead
// workers' assignments still active. The new dispatcher's startupRecovery
// path only handled in_progress beads with recoverable worktrees and never
// abandoned the stranded rows; the queue silently dropped from many beads
// to one until manual SQL untangled it.
//
// Caller is responsible for the grace window — call after enough time has
// passed that any worker that was going to reconnect has done so.
func (d *Dispatcher) abandonStaleActiveAssignments(ctx context.Context) int {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, bead_id, worker_id, worktree FROM assignments WHERE status='active'`)
	if err != nil {
		_ = d.logEvent(ctx, "stale_assignment_scan_failed", "dispatcher", "", "", err.Error())
		return 0
	}
	type stale struct {
		id       int64
		beadID   string
		workerID string
		worktree string
	}
	var pending []stale
	for rows.Next() {
		var s stale
		if scanErr := rows.Scan(&s.id, &s.beadID, &s.workerID, &s.worktree); scanErr != nil {
			_ = rows.Close()
			_ = d.logEvent(ctx, "stale_assignment_scan_failed", "dispatcher", "", "", scanErr.Error())
			return 0
		}
		d.mu.Lock()
		_, connected := d.workers[s.workerID]
		d.mu.Unlock()
		if !connected {
			pending = append(pending, s)
		}
	}
	_ = rows.Close()

	abandoned := 0
	for _, s := range pending {
		branch := protocol.BranchPrefix + s.beadID
		if _, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
			BeadID:       s.beadID,
			AssignmentID: s.id,
			WorkerID:     s.workerID,
			Worktree:     s.worktree,
			Branch:       branch,
			Reason:       "stale_active_assignment",
			Details:      "active assignment belongs to a disconnected worker",
		}); err != nil {
			_ = d.logEvent(ctx, "stale_assignment_quarantine_failed", "dispatcher", s.beadID, s.workerID, err.Error())
			continue
		}
		abandoned++
		_ = d.logEvent(ctx, "stale_assignment_quarantined", "dispatcher", s.beadID, s.workerID,
			fmt.Sprintf(`{"assignment_id":%d,"branch":%q,"worktree":%q}`, s.id, branch, s.worktree))
	}
	return abandoned
}

// applyRestoredAssignments populates worktreeByBead/attemptCounts/handoffCounts
// from the recovered assignments and returns the set of recoverable bead IDs.
func (d *Dispatcher) applyRestoredAssignments(restored []restoredAssignment) map[string]bool {
	recoverable := make(map[string]bool, len(restored))
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range restored {
		recoverable[a.beadID] = true
		d.worktreeByBead[a.beadID] = a.worktree
		if a.attemptCount > 0 {
			d.attemptCounts[a.beadID] = a.attemptCount
		}
		if a.handoffCount > 0 {
			d.handoffCounts[a.beadID] = a.handoffCount
		}
	}
	return recoverable
}

func (d *Dispatcher) logAssignmentInvariantViolations(ctx context.Context) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT bead_id, COUNT(*) FROM assignments WHERE status='active' GROUP BY bead_id HAVING COUNT(*) > 1`)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var beadID string
		var activeCount int
		if err := rows.Scan(&beadID, &activeCount); err != nil {
			return
		}
		_ = d.logEvent(ctx, "assignment_invariant_violation", "dispatcher", beadID, "",
			fmt.Sprintf(`{"active_assignments":%d}`, activeCount))
	}
}

// releasePriorAssignment finalizes a worker's previous bead assignment when
// it is being reassigned to a different bead without a clean DONE. It returns
// the prior bead to a retryable state and preserves the branch/worktree so a
// later assignment can resume or inspect the abandoned attempt. Without this,
// a worker reassigned mid-run leaves the prior bead stuck in_progress with
// worker_id still pointing at the worker (oro-xqrh).
//
// The caller must NOT hold d.mu. Safe to call when the worker has no prior
// bead. If in-memory bead state was already cleared but assignmentID remains,
// the active assignment row is used as the source of truth (oro-fksf).
func (d *Dispatcher) shutdownSequence() {
	d.mu.Lock()
	d.state = StateStopping
	d.mu.Unlock()

	// Phase 1: Cancel ops agents and abort in-flight merges.
	d.shutdownCancelOps()

	// Phase 2: Send PREPARE_SHUTDOWN to all workers and wait for them to drain.
	// Collect worker IDs and worktree paths under lock BEFORE the wait loop,
	// because workers will be deleted from the map as they disconnect.
	d.mu.Lock()
	workerIDs := make([]string, 0, len(d.workers))
	for id := range d.workers {
		workerIDs = append(workerIDs, id)
	}
	d.mu.Unlock()

	for _, id := range workerIDs {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	d.shutdownWaitForWorkers()

	// Phase 3b: Reset in-progress beads to open so they become re-assignable
	// on the next dispatcher start. Best-effort: log warnings on failure, continue.
	d.shutdownResetActiveBeads()

	// Phase 3: Workers are stopped. Active assignment worktrees are preserved
	// as requeued recovery-owned state; no dispatcher-owned cleanup runs here.
	d.shutdownRemoveWorktrees(nil)
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

	// Bead state is persisted by the store implementation.
}

// shutdownResetActiveBeads queries active assignments and resets each bead to
// "open" so it becomes re-assignable on next dispatcher start. Best-effort:
// failures are logged but do not block shutdown.
func (d *Dispatcher) shutdownResetActiveBeads() {
	ctx := context.Background()
	rows, err := d.db.QueryContext(ctx, `SELECT id, bead_id, worker_id FROM assignments WHERE status='active'`)
	if err != nil {
		_ = d.logEvent(ctx, "shutdown_reset_query_failed", "dispatcher", "", "", err.Error())
		return
	}
	defer func() { _ = rows.Close() }()

	type shutdownAssignment struct {
		id       int64
		beadID   string
		workerID string
	}
	var assignments []shutdownAssignment
	active := make(map[string]bool)
	for rows.Next() {
		var (
			assignmentID int64
			beadID       string
			workerID     string
		)
		if scanErr := rows.Scan(&assignmentID, &beadID, &workerID); scanErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_scan_failed", "dispatcher", "", "", scanErr.Error())
			continue
		}
		active[beadID] = true
		assignments = append(assignments, shutdownAssignment{id: assignmentID, beadID: beadID, workerID: workerID})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = d.logEvent(ctx, "shutdown_reset_rows_failed", "dispatcher", "", "", rowsErr.Error())
	}
	_ = rows.Close()

	for _, assignment := range assignments {
		if updateErr := updateBeadStatus(ctx, d.beads, assignment.beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_bead_failed", "dispatcher", assignment.beadID, "", updateErr.Error())
			continue
		}
		if requeueErr := d.requeueAssignmentForShutdown(ctx, assignment.id); requeueErr != nil {
			_ = d.logEvent(ctx, "shutdown_assignment_requeue_failed", "dispatcher", assignment.beadID, assignment.workerID, requeueErr.Error())
			continue
		}
		_ = d.logEvent(ctx, "shutdown_assignment_requeued", "dispatcher", assignment.beadID, assignment.workerID,
			fmt.Sprintf(`{"assignment_id":%d}`, assignment.id))
	}

	inProgress, listErr := d.beads.InProgress(ctx)
	if listErr != nil {
		_ = d.logEvent(ctx, "shutdown_reset_in_progress_list_failed", "dispatcher", "", "", listErr.Error())
		return
	}
	for _, bead := range inProgress {
		if active[bead.ID] || bead.Type == "epic" {
			continue
		}
		if updateErr := updateBeadStatus(ctx, d.beads, bead.ID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "shutdown_reset_in_progress_bead_failed", "dispatcher", bead.ID, "", updateErr.Error())
		}
	}
}

func (d *Dispatcher) requeueAssignmentForShutdown(ctx context.Context, assignmentID int64) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='requeued', completed_at=datetime('now') WHERE id=? AND status='active'`,
		assignmentID)
	if err != nil {
		return fmt.Errorf("requeue assignment for shutdown: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr == nil && rows != 1 {
		return fmt.Errorf("requeue assignment for shutdown: assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
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

// handleRepeatedQGOutput is called when isQGStuck detects maxStuckCount
// consecutive identical QG outputs for a bead. It classifies the failure and
// routes to the appropriate cleanup path without generic escalation:
//   - QGFailureDecisionReopenOriginal  → handleClassifiedQGExhaustion (reopen bead)
//   - QGFailureDecisionCreateOrReuseInfra → handleSystemicQGExhaustion (infra incident)
//   - default (StopForTriage, etc.)    → complete assignment, release worker, log triage event
//
// All paths leave no active assignment, stale worker state, stale qgStuckTracker,
// or stranded original bead. Worker-facing sends are never attempted, so the
// function is safe to call even when the worker has already disconnected.
func (d *Dispatcher) handleRepeatedQGOutput(ctx context.Context, workerID, beadID string, rec QGFailureRecord, cls QGFailureClassification) {
	_ = d.logEvent(ctx, "qg_repeated_classified", workerID, beadID, workerID,
		fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q}`,
			cls.Class, cls.Decision, rec.Fingerprint))

	switch cls.Decision {
	case QGFailureDecisionReopenOriginal:
		d.handleClassifiedQGExhaustion(ctx, workerID, beadID, rec.AssignmentID, rec, cls)
	case QGFailureDecisionCreateOrReuseInfra:
		d.handleSystemicQGExhaustion(ctx, workerID, beadID, rec.AssignmentID, rec, cls)
	default:
		_ = d.completeAssignment(ctx, rec.AssignmentID, beadID)
		d.releaseWorkerAfterQGExhaustion(workerID, beadID)
		if d.shouldReopenQGOriginal(ctx, beadID) {
			if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
				_ = d.logEvent(ctx, "qg_repeated_reopen_failed", "dispatcher", beadID, workerID,
					fmt.Sprintf(`{"error":%q}`, err.Error()))
			}
		}
		_ = d.logEvent(ctx, "qg_repeated_triage", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q}`,
				cls.Class, cls.Decision, rec.Fingerprint))
	}
}

// handleQGExhausted handles the case when quality gate retries are exhausted.
// It classifies before creating any follow-up work: deterministic failures stay
// on the original bead, systemic failures reuse/create infra incidents, and
// low-confidence failures stop for triage without creating legacy QG P0 beads.
func (d *Dispatcher) handleQGExhausted(ctx context.Context, workerID, beadID string, assignmentID int64, qgOutput string, attempt int) {
	d.persistBeadCount(ctx, assignmentID, beadID, "attempt_count", attempt)
	rec := qgExhaustionRecord(workerID, beadID, assignmentID, qgOutput, attempt)
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{RetryExhausted: true})
	if cls.Decision == QGFailureDecisionReopenOriginal {
		d.handleClassifiedQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
		return
	}
	if cls.Decision == QGFailureDecisionCreateOrReuseInfra {
		d.handleSystemicQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
		return
	}
	d.handleTriageQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
}

func qgExhaustionRecord(workerID, beadID string, assignmentID int64, qgOutput string, attempt int) QGFailureRecord {
	qgFingerprint, qgSummary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	return QGFailureRecord{
		ID:           fmt.Sprintf("%s:%s:%d:%d", beadID, workerID, assignmentID, attempt),
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  qgFingerprint,
		Summary:      qgSummary,
		Output:       qgOutput,
	}
}

func (d *Dispatcher) handleSystemicQGExhaustion(ctx context.Context, workerID, beadID string, assignmentID int64, rec QGFailureRecord, cls QGFailureClassification) {
	incident, err := d.createOrReuseQGInfraIncident(ctx, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
	}
	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.releaseWorkerAfterQGExhaustion(workerID, beadID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "qg_original_reopen_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	_ = d.logEvent(ctx, "qg_infra_incident_reused", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"incident_id":%d,"class":%q,"fingerprint":%q}`, incident.ID, cls.Class, rec.Fingerprint))
}

func (d *Dispatcher) handleClassifiedQGExhaustion(ctx context.Context, workerID, beadID string, assignmentID int64, rec QGFailureRecord, cls QGFailureClassification) {
	rec = normalizeQGFailureRecord(rec)
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
	} else if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), rec.Fingerprint, incident.ID))
	}

	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.releaseWorkerAfterQGExhaustion(workerID, beadID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "qg_original_reopen_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		} else {
			d.deferReopenedQGOriginal(ctx, beadID, workerID, rec.Fingerprint)
		}
	}
	_ = d.logEvent(ctx, "qg_original_reopened", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q}`, cls.Class, cls.Decision, rec.Fingerprint))
}

func (d *Dispatcher) handleTriageQGExhaustion(ctx context.Context, workerID, beadID string, assignmentID int64, rec QGFailureRecord, cls QGFailureClassification) {
	rec = normalizeQGFailureRecord(rec)
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), rec.Fingerprint))
	} else if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), rec.Fingerprint, incident.ID))
	}

	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.releaseWorkerAfterQGExhaustion(workerID, beadID)
	if d.shouldReopenQGOriginal(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "qg_original_reopen_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		} else {
			d.deferReopenedQGOriginal(ctx, beadID, workerID, rec.Fingerprint)
		}
	}
	_ = d.logEvent(ctx, "qg_failure_triage_required", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"class":%q,"decision":%q,"fingerprint":%q,"reason":%q}`,
			cls.Class, cls.Decision, rec.Fingerprint, cls.Reason))
}

func (d *Dispatcher) deferReopenedQGOriginal(ctx context.Context, beadID, workerID, fingerprint string) {
	until := d.nowFunc().UTC().Add(qgOriginalReopenDeferDuration).Format(time.RFC3339)
	if err := d.beads.Defer(ctx, beadID, until); err != nil {
		d.mu.Lock()
		d.exhaustedBeads[beadID] = true
		d.mu.Unlock()
		_ = d.logEvent(ctx, "qg_original_defer_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), fingerprint))
		return
	}
	_ = d.logEvent(ctx, "qg_original_deferred", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"until":%q,"fingerprint":%q}`, until, fingerprint))
}

func (d *Dispatcher) releaseWorkerAfterQGExhaustion(workerID, beadID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerIdle
		w.assignmentID = 0
		w.beadID = ""
		w.epicID = ""
		w.isEpicDecomp = false
	}
	delete(d.attemptCounts, beadID)
	delete(d.transientCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.reviewBlockedCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	delete(d.escalatedBeads, beadID)
	delete(d.worktreeFailures, beadID)
	delete(d.assigningBeads, beadID)
	delete(d.exhaustedBeads, beadID)
}

func (d *Dispatcher) shouldReopenQGOriginal(ctx context.Context, beadID string) bool {
	return d.shouldReopenBead(ctx, beadID)
}

func (d *Dispatcher) shouldReopenBead(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		return true
	}
	return detail.Status != "closed"
}

// ConnectedWorkers, TargetWorkers, WorkerInfo, WorkerModel → worker_pool.go
