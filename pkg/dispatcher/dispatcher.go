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
	nowFunc                 func() time.Time
	epicAdmissionRenewEvery time.Duration

	// testUnlockHook, if non-nil, is called after releasing the lock in
	// registerWorker/handleQGFailure (before memory.ForPrompt). Tests use
	// this to inject a synchronization point that guarantees a concurrent
	// deletion occurs during the unlock window.
	testUnlockHook func()

	// testPanePollDone, if non-nil, is called after each pane monitor poll
	// iteration completes. Tests use this to synchronize without time.Sleep.
	testPanePollDone func()

	// testLegacyReconnectClaimedHook, if non-nil, is called after the durable
	// legacy reconnect claim commits and before in-memory ownership is restored.
	// Tests use this to inject a concurrent durable ownership transfer.
	testLegacyReconnectClaimedHook func()

	// testLegacyReconnectVerifiedHook, if non-nil, is called after durable
	// canonical verification and before in-memory ownership is restored.
	testLegacyReconnectVerifiedHook func()

	// testLegacyReconnectRequeuedHook, if non-nil, is called after the legacy
	// assignment is durably requeued and before the authoritative bead reopens.
	testLegacyReconnectRequeuedHook func()
	// testLegacyReconnectAdmissionHook, if non-nil, is called after the legacy
	// drain check and before assignment admission. Tests use it to reproduce
	// reconnect/READY lock-order interleavings.
	testLegacyReconnectAdmissionHook func()
	// testCanonicalReconnectAdmissionHook, if non-nil, is called after the
	// canonical reconnect reserves assignment admission and before d.mu.
	testCanonicalReconnectAdmissionHook func()

	// Review artifact maintenance is serialized so overlapping scheduled ticks
	// cannot delete or acknowledge the same artifact concurrently.
	assignmentAdmissionMu     sync.Mutex
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
	if resolved.ReviewEvidenceDir == "" {
		resolved.ReviewEvidenceDir, _ = filepath.Abs(filepath.Join(rootDir, protocol.OroDir, "review-evidence"))
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
		epicAdmissionRenewEvery:   epicBranchAdmissionLeaseRenewInterval,
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
var errPreMergeQGAlreadyHandled = errors.New("pre-merge QG failure already handled")

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
	EpicBranchBlocksOpen         int                          `json:"epic_branch_blocks_open"`
	EpicBranchLeasesActive       int                          `json:"epic_branch_leases_active"`
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

var errResolvedPreservedMismatch = errors.New("resolved preserved branch/worktree mismatch")

// the active assignment row is used as the source of truth (oro-fksf).
// ConnectedWorkers, TargetWorkers, WorkerInfo, WorkerModel → worker_pool.go
