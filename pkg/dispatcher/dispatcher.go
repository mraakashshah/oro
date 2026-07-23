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
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"oro/pkg/agentmodel"
	"oro/pkg/beadstore"
	"oro/pkg/cards"
	embeddings "oro/pkg/embed"
	"oro/pkg/factoryhealth"
	"oro/pkg/leakscan"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/processenv"
	"oro/pkg/protocol"
	"oro/pkg/web"
	workerstream "oro/pkg/worker"

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

// MemoryRejection is dispatcher-owned rejection history read from the memory boundary.
type MemoryRejection struct {
	ID        int64
	BeadID    string
	WorkerID  string
	Feedback  string
	CreatedAt string
}

// DreamAction is a dispatcher-local memory mutation requested by a dream agent.
type DreamAction struct {
	Kind   string
	ID     int64
	IDs    []int64
	Params protocol.MemoryInsertParams
}

// MemoryStore is the dispatcher-owned subset of memory behavior.
type MemoryStore interface {
	Insert(ctx context.Context, m protocol.MemoryInsertParams) (int64, error)
	GetByID(ctx context.Context, id int64) (protocol.Memory, error)
	DumpAll(ctx context.Context) ([]protocol.Memory, error)
	HasEmbedder() bool
}

// MemoryServices contains memory-specific operations supplied by outer packages.
type MemoryServices struct {
	Store            MemoryStore
	InsertRejection  func(ctx context.Context, beadID, workerID, feedback string) error
	GetRejections    func(ctx context.Context, beadID string) ([]MemoryRejection, error)
	Consolidate      func(ctx context.Context) (merged, pruned int, err error)
	TrimSearchEvents func(ctx context.Context, maxAge time.Duration) (int64, error)
	ExecuteDream     func(ctx context.Context, actions []DreamAction, logFn func(string)) error
	HandoffInserter  func(cardStore cards.Store) LearningSink
}

// LearningSink persists worker-emitted learning candidates for card review.
type LearningSink interface {
	AppendLearningPending(ctx context.Context, beadID string, c cards.CardCandidate) (int64, error)
}

type noopLearningSink struct{}

// AppendLearningPending ignores pending learning writes when cards are unavailable.
func (noopLearningSink) AppendLearningPending(context.Context, string, cards.CardCandidate) (int64, error) {
	return 0, nil
}

type handoffLearningSink struct {
	cardStore cards.Store
}

// HandoffInserter adapts handoff memory params into pending card candidates.
func HandoffInserter(cardStore cards.Store) LearningSink {
	if cardStore == nil {
		return noopLearningSink{}
	}
	return handoffLearningSink{cardStore: cardStore}
}

// AppendLearningPending forwards handoff learning candidates to the card store.
func (s handoffLearningSink) AppendLearningPending(ctx context.Context, beadID string, c cards.CardCandidate) (int64, error) {
	id, err := s.cardStore.AppendLearningPending(ctx, beadID, c)
	if err != nil {
		return 0, fmt.Errorf("append handoff pending learning: %w", err)
	}
	return id, nil
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

// DeferredStore is the dispatcher-local extension for deferred bead repair.
type DeferredStore interface {
	beadstore.Store
	Defer(ctx context.Context, id, until string) error
	Undefer(ctx context.Context, id string) error
}

type dependencyStore interface {
	AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error
}

func selectStore(ctx context.Context, mode string, primary DeferredStore, db *sql.DB) (DeferredStore, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "cli":
		return primary, nil
	case "sqlite", "shadow":
		if db == nil {
			return nil, fmt.Errorf("select bead source %q: db is nil", mode)
		}
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			return nil, fmt.Errorf("select bead source %q: migrate bead schema: %w", mode, err)
		}
		if strings.EqualFold(strings.TrimSpace(mode), "shadow") {
			if _, err := db.ExecContext(ctx, protocol.MigrateKVStore); err != nil {
				return nil, fmt.Errorf("select bead source %q: migrate kv store: %w", mode, err)
			}
		}
		sqliteStore := beadstore.NewSQLiteStore(db)
		if strings.EqualFold(strings.TrimSpace(mode), "sqlite") {
			return sqliteStore, nil
		}
		shadowStartedAt, err := beadstore.LoadOrInitShadowStartedAt(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("select bead source %q: shadow started at: %w", mode, err)
		}
		return beadstore.NewShadowStore(primary, sqliteStore, beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
			logBeadstoreDivergence(ctx, db, event)
		}), beadstore.WithShadowStartedAt(shadowStartedAt)), nil
	default:
		return nil, fmt.Errorf("unknown %s %q", "ORO_BEADSOURCE_MODE", mode)
	}
}

func normalizeBeadSourceModeForPrimary(mode string, primary DeferredStore) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" && isSQLiteStore(primary) {
		return "sqlite"
	}
	if normalized == "sqlite" && !isSQLiteStore(primary) {
		return "cli"
	}
	return normalized
}

func isSQLiteStore(store DeferredStore) bool {
	_, ok := store.(*beadstore.SQLiteStore)
	return ok
}

func logBeadstoreDivergence(ctx context.Context, db *sql.DB, event beadstore.ShadowDivergence) {
	if db == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{
		"operation": event.Operation,
		"kind":      string(event.Kind),
		"reason":    event.Reason,
	})
	if err != nil {
		return
	}
	_, _ = db.ExecContext(ctx,
		`INSERT INTO events (type, source, payload) VALUES (?, ?, ?)`,
		"beadstore_divergence", "beadstore_shadow", string(payload))
}

func updateBeadStatus(ctx context.Context, beads beadstore.Store, id, status string) error {
	if err := beads.Update(ctx, id, beadstore.UpdateParams{Status: &status}); err != nil {
		return fmt.Errorf("update bead %s status to %s: %w", id, status, err)
	}
	return nil
}

// WorktreeManager creates and removes git worktrees.
type WorktreeManager interface {
	Create(ctx context.Context, beadID, baseBranch string) (path string, branch string, err error)
	Remove(ctx context.Context, path string) error
	Prune(ctx context.Context) error
	DeleteBranch(ctx context.Context, branch string) error
	DeleteBranchMergedInto(ctx context.Context, branch, targetBranch string) error
	ForceDeleteBranch(ctx context.Context, branch string) error
	BranchExists(ctx context.Context, branch string) (bool, error)
	MergeFFOnly(ctx context.Context, branch string, target string) (commitSHA string, err error)
	// UpdateBranchRef advances targetBranch to point at the tip of sourceBranch
	// without requiring sourceBranch to be checked out. Used when the target is
	// not the HEAD branch (i.e., not the branch checked out in the main worktree).
	UpdateBranchRef(ctx context.Context, targetBranch, sourceBranch string) error
	BranchHead(ctx context.Context, branch string) (string, error)
	GCClosedWorktrees(ctx context.Context, isBeadClosed func(string) bool) error
	// Exists reports whether the worktree at path is still present on disk.
	// Returns false if the path does not exist or cannot be accessed.
	Exists(ctx context.Context, path string) bool
	// CurrentBranch reports the branch checked out in the worktree.
	CurrentBranch(ctx context.Context, path string) (string, error)
	// RebaseOnto rebases branch onto onto using git rebase --onto.
	RebaseOnto(ctx context.Context, branch, onto string) error
	// PushBranch pushes branch to origin.
	PushBranch(ctx context.Context, branch string) error
	// CreateBranch creates a new branch named `name` starting from `from`.
	// If the branch already exists git returns a non-zero exit code; the
	// caller is responsible for deciding whether that is an error.
	CreateBranch(ctx context.Context, name string, from string) error
}

type existingWorktreeReusePreparer interface {
	PrepareExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) (fastForwarded bool, err error)
}

type existingWorktreeDivergedRebaser interface {
	RebaseDivergedExistingForReuse(ctx context.Context, worktree, branch, baseBranch string) error
}

type assignmentBaseBranchPreparer interface {
	PrepareBaseBranchForAssignment(ctx context.Context, branch, baseBranch string) (fastForwarded bool, err error)
}

type assignmentBaseBranchSafetyChecker interface {
	BaseBranchHasUniqueCommits(ctx context.Context, branch, baseBranch string) (bool, error)
}

// epicPreserveOutcome is the result of a deterministic epic-ancestry preserve
// merge. On any error the caller falls back regardless of outcome.
type epicPreserveOutcome int

const (
	// epicPreserveNoop means target's tip is already an ancestor of the epic
	// branch: nothing to do.
	epicPreserveNoop epicPreserveOutcome = iota
	// epicPreserveMerged means a new preserve commit was created and the epic
	// ref advanced to it via compare-and-swap.
	epicPreserveMerged
	// epicPreserveConflict means the merge could not be computed without a
	// content conflict; the caller must fall back to LLM recovery.
	epicPreserveConflict
)

// epicMergePreserver deterministically preserves both target and epic ancestry
// on the epic branch without an LLM worker or a checked-out worktree.
// Implemented by *GitWorktreeManager; worktree managers that do not implement
// it cause the dispatcher to fall back to ensureEpicRebaseChild.
type epicMergePreserver interface {
	// preserveEpicAncestry merges target into epicBranch so that both the epic
	// branch's current tip and target become ancestors of the epic branch,
	// advancing the epic ref transactionally (compare-and-swap). It never
	// checks out a worktree. Returns the new epic tip on epicPreserveMerged
	// (or the unchanged tip on epicPreserveNoop). Any failure before the ref
	// mutation leaves all refs untouched.
	preserveEpicAncestry(ctx context.Context, epicBranch, target string) (epicPreserveOutcome, string, error)
	// rollbackEpicPreserve reverts a preserve merge that failed post-merge
	// verification (e.g. the quality gate), advancing epicBranch from newOID
	// back to oldOID via compare-and-swap. It fails without mutating the ref
	// if epicBranch no longer points at newOID.
	rollbackEpicPreserve(ctx context.Context, epicBranch, oldOID, newOID string) error
}

// Escalator accepts escalation messages from dispatcher checks.
type Escalator interface {
	Escalate(ctx context.Context, msg string) error
}

// ProcessManager spawns and kills oro worker OS processes.
// Production implementations use exec.Command to run `oro worker`.
type ProcessManager interface {
	Spawn(id string) (*os.Process, error)
	Kill(id string) error
	// IsAlive reports whether the tracked process for id is still running.
	// Returns false if id is not tracked or the process has exited.
	IsAlive(id string) bool
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
// whether it passed. skipMutation true means mutation testing is skipped;
// false means the caller explicitly opted into mutation testing.
type QGRunner interface {
	Run(ctx context.Context, worktree string, skipMutation bool, mutationBase string) (passed bool, output string, err error)
}

// ShellQGRunner runs quality_gate.sh inside the worktree via bash. It looks
// for scripts/quality_gate.sh first, then quality_gate.sh at the repo root.
// It returns (true, output, nil) on exit 0, (false, output, nil) on non-zero
// exit, and (false, "", err) if the script cannot be found or launched.
type ShellQGRunner struct{}

// Run implements QGRunner using the same logic as worker.RunQualityGate but
// self-contained in the dispatcher package to avoid an import cycle.
func (r *ShellQGRunner) Run(ctx context.Context, worktree string, skipMutation bool, mutationBase string) (passed bool, output string, err error) {
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
	if output, conflictErr := qualityGateConflictMarkerOutput(scriptPath); conflictErr != nil {
		return false, "", conflictErr
	} else if output != "" {
		return false, output, nil
	}

	args := []string{scriptPath}
	if !skipMutation {
		args = append(args, "--mutation-testing")
	}
	cmd := exec.CommandContext(ctx, "bash", args...) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree
	cmd.Env = qgRunnerEnv(skipMutation, worktree, mutationBase)
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

func qualityGateConflictMarkerOutput(scriptPath string) (string, error) {
	file, err := os.Open(scriptPath) //nolint:gosec // path is the selected quality gate script inside a validated worktree.
	if err != nil {
		return "", fmt.Errorf("open quality gate script: %w", err)
	}
	defer func() { _ = file.Close() }()

	var b strings.Builder
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, "=======") || strings.HasPrefix(line, ">>>>>>>") {
			if b.Len() == 0 {
				b.WriteString("FAIL: quality_gate.sh contains unresolved git conflict markers\n")
			}
			b.WriteString(scriptPath)
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(lineNo))
			b.WriteByte(':')
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan quality gate script: %w", err)
	}
	return b.String(), nil
}

func qgRunnerEnv(skipMutation bool, worktree, mutationBase string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if processenv.StripQualityGateEnv(kv) {
			continue
		}
		env = append(env, kv)
	}
	if skipMutation {
		env = append(env, "ORO_SKIP_MUTATION=1")
	}
	if mutationBase != "" {
		env = append(env, "ORO_MUTATION_BASE="+mutationBase)
	}
	return processenv.ForWorkdir(env, worktree)
}

// --- Worker tracking ---

// WorkerState is now in pkg/protocol/types.go

// trackedWorker holds runtime state for a connected worker.
type trackedWorker struct {
	id               string
	conn             net.Conn
	state            protocol.WorkerState
	assignmentID     int64
	execution        WorkerExecutionContext
	beadID           string
	epicID           string // parent epic ID if the assigned bead is a child of an epic
	isEpicDecomp     bool   // true when worker is assigned an epic for decomposition (no merge on done)
	worktree         string
	baseBranch       string // branch the worktree was created from (main or epic/<epicID>)
	targetBranch     string // branch the worker's changes should merge into (same as baseBranch)
	runtime          string // resolved runtime for the current bead assignment
	model            string // resolved model for the current bead assignment
	reasoning        string // resolved Codex reasoning effort for the current bead assignment
	lastSeen         time.Time
	lastProgress     time.Time // last time meaningful progress was observed (DONE/READY_FOR_REVIEW/QG/first STATUS)
	contextPct       int       // context usage percentage from last heartbeat (0-100)
	encoder          *json.Encoder
	pendingMsgs      []protocol.Message // buffered messages for disconnected worker
	shutdownCancel   context.CancelFunc // cancels previous shutdown goroutine (1nf.5)
	shutdownApproved bool               // set by handleShutdownApproved; checked by checkShutdownApproved
	shutdownReason   string             // why graceful shutdown was requested
	managed          bool               // true if spawned by the dispatcher (vs externally connected)
	spawnFor         bool               // true for one-shot workers spawned by spawn-for
	targetBeadID     string             // set for spawn-for workers; only this bead may be assigned
	prevSession      bool               // true if worker ID predates this dispatcher's startTime (previous session)
	reviewDeadSince  time.Time          // set when ops review subprocess is detected dead; zero if review is active
}

func (w *trackedWorker) markShuttingDownWithoutAssignment() {
	w.state = protocol.WorkerShuttingDown
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	w.targetBeadID = ""
}

const directWorkerWriteTimeout = 250 * time.Millisecond

func sendToWorkerWithoutBuffering(w *trackedWorker, msg protocol.Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	data = append(data, '\n')

	if err := w.conn.SetWriteDeadline(time.Now().Add(directWorkerWriteTimeout)); err == nil {
		defer func() { _ = w.conn.SetWriteDeadline(time.Time{}) }()
	}
	_, _ = w.conn.Write(data)
}

func sendShutdownWithoutBuffering(w *trackedWorker) {
	sendToWorkerWithoutBuffering(w, protocol.Message{Type: protocol.MsgShutdown})
}

func sendPrepareShutdownWithoutBuffering(w *trackedWorker, timeout time.Duration) {
	sendToWorkerWithoutBuffering(w, protocol.Message{
		Type: protocol.MsgPrepareShutdown,
		PrepareShutdown: &protocol.PrepareShutdownPayload{
			Timeout: timeout,
		},
	})
}

type idleWorker struct {
	worker       *trackedWorker
	targetBeadID string
	spawnFor     bool
}

const shutdownReasonScaleDown = "scale_down"

// pendingHandoff holds context for a bead whose worker has been shut down
// during a ralph handoff. The next worker to connect will be assigned this
// bead+worktree instead of going through normal assignment.
type pendingHandoff struct {
	assignmentID   int64
	execution      WorkerExecutionContext
	beadID         string
	epicID         string // parent epic ID if the bead is a child of an epic
	worktree       string
	baseBranch     string // branch the worktree was created from (main or epic/<epicID>)
	targetBranch   string // branch the worker's changes should merge into (same as baseBranch)
	runtime        string
	model          string
	reasoning      string
	title          string   // bead title for memory search on respawn
	labels         []string // bead labels for memory search on respawn
	nextAction     string   // intent_summary from checkpoint_acked (§9.3); empty for ralph handoffs
	checkpointTurn int      // checkpoint respawn count for this bead (§9.3 step 9); 0 for ralph handoffs
}

type workerAssignmentSnapshot struct {
	execution    WorkerExecutionContext
	worktree     string
	runtime      string
	model        string
	reasoning    string
	epicID       string
	baseBranch   string
	targetBranch string
}

// --- Config ---

// Config holds Dispatcher configuration.
type Config struct {
	SocketPath              string        // UDS socket path.
	DBPath                  string        // SQLite database path.
	RepoRoot                string        // Absolute path to the repository root. Used so oro task commands run from the right directory even when the process is started from a worktree. Falls back to os.Getwd() if empty.
	BeadsDir                string        // Internal task data directory (defaults to protocol.BeadsDir when empty). Set from ProjectPaths.BeadsDir for stealth-mode support.
	MaxWorkers              int           // Worker pool ceiling for auto-scale (default 10).
	InitialWorkers          int           // Initial targetWorkers on startup (default: MaxWorkers).
	AllowZeroWorkers        bool          // When true, InitialWorkers=0 is treated as an explicit target (not auto-defaulted) so daemon-only manual-worker mode keeps a zero baseline. Combined with MaxWorkers>0 in New(), this also seeds explicitScaleTarget so maybeAutoScale will not raise the target from zero.
	HeartbeatTimeout        time.Duration // Worker heartbeat timeout (default 45s).
	ProgressTimeout         time.Duration // Max time without meaningful progress before STUCK_WORKER escalation (default 15m).
	PollInterval            time.Duration // oro task ready poll interval (default 10s).
	FallbackPollInterval    time.Duration // Fallback poll interval for fsnotify safety net (default 60s).
	CycleScanInterval       time.Duration // Dependency-cycle pre-flight scan interval (default 60s).
	ShutdownTimeout         time.Duration // Graceful shutdown timeout (default 10s).
	ConsolidateAfterN       int           // Trigger context consolidation after N completed beads (default 5).
	DreamInterval           int           // Spawn a dream memory-consolidation agent after N completed beads (default 10; 0 disables).
	GradeGateEnabled        bool          // When true, dream actions are queued as card proposals instead of directly applying memory mutations.
	JanitorInterval         int           // Run janitor after N completed merges; 0 disables it.
	JanitorIdleThreshold    int           // Require at most this many queued beads before janitor runs; 0 means only an empty queue.
	AuditEveryNJanitors     int           // Run audit every N janitor cycles; 0 disables periodic audit cadence.
	JanitorTopK             int           // Limit each janitor cycle to its top K findings; 0 uses the janitor's natural limit.
	JanitorEnabled          bool          // Enable janitor cycles. Enable flags intentionally default false.
	AuditEnabled            bool          // Enable audit counters driven by janitor cycles.
	PaneContextThreshold    int           // Context percentage threshold for pane handoff (default 60).
	PaneMonitorInterval     time.Duration // Pane context_pct poll interval (default 5s).
	PaneRestartCooldown     time.Duration // Min time between manager pane restarts (default 2m).
	PaneInactivityTimeout   time.Duration // Manager inactivity duration before restart (default 10m).
	ReviewTimeout           time.Duration // Max time a reviewing worker can stall before STUCK_WORKER escalation (default 15m).
	ReviewDeadGrace         time.Duration // Grace period before removing a reviewing worker whose ops review subprocess has exited (default 30s).
	ManualIntegration       bool          // If true, completed worker branches wait for manual coordinator integration instead of auto-merge.
	MutationTesting         bool          // If true, dispatcher quality gates run mutation-testing tiers. Defaults false.
	RegressionRevert        bool          // If true, QG retries capture a pre-retry baseline for regression-revert checks. Defaults true.
	LeakScan                LeakScanConfig
	Estimator               BeadEstimator // Optional bead complexity estimator for explicit injection.
	WorkerProgram           string        // Absolute path to worker-program.md. Defaults to <RepoRoot>/worker-program.md.
	ReviewPatterns          string        // Absolute path for review patterns. Populated from ProjectPaths.ReviewPatterns.
	ReviewPatternCandidates string        // Absolute path for review-pattern candidate inbox. Populated from ProjectPaths.ReviewPatternCandidates.
	DefaultBranch           string        // Base branch for worktree creation and epic FF merges (default "main"). Set via --base-branch flag.
	WebEnabled              bool          // Enable HTTP server for dashboard/health endpoints (default false).
	WebAddr                 string        // HTTP server listen address (default 127.0.0.1:4444 in withDefaults).
	SemanticModelDir        string        // Directory containing the BGE ONNX model files. Empty means semantic search is disabled.
	RerankerModelDir        string        // Directory containing the BGE reranker ONNX model files. Empty means reranker unavailable.
	// CheckpointThreshold is the context-usage percentage (0–100) at which the dispatcher
	// triggers a checkpoint for the assigned worker. 0 disables checkpoint signalling.
	// Default 75 (§9.3).
	CheckpointThreshold int
	// ContextSafety holds the configurable warning/checkpoint thresholds (§9.4).
	// Expressed as fractions in [0, 1]. Zero values fall back to package defaults.
	ContextSafety ContextSafetyConfig
	// StorageHealth observes the host-global storage control plane. A nil
	// observer leaves storage health unavailable.
	StorageHealth func(context.Context) *factoryhealth.StorageHealth
}

// LeakScanConfig controls the dispatcher's pre-merge secret scan.
type LeakScanConfig struct {
	Enabled        bool
	BlockOn        string
	EntropyMinBits float64
	EntropyAction  string
	AllowlistPath  string
}

// intDefault returns v if non-zero, otherwise dflt.
// Used in withDefaults to apply int field defaults without adding cyclomatic
// complexity (each if-block counts toward gocyclo limit).
func intDefault(v, dflt int) int {
	if v == 0 {
		return dflt
	}
	return v
}

func durationDefault(v, dflt time.Duration) time.Duration {
	if v == 0 {
		return dflt
	}
	return v
}

func boolDefault(v, dflt bool) bool {
	if !v {
		return dflt
	}
	return v
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

func shouldDefaultWorkerCounts(c Config) bool {
	return !c.AllowZeroWorkers || c.InitialWorkers != 0
}

func (c *Config) withDefaults() Config {
	out := *c
	if shouldDefaultWorkerCounts(out) {
		out.InitialWorkers, out.MaxWorkers = defaultWorkerCounts(out.InitialWorkers, out.MaxWorkers)
	}
	out.HeartbeatTimeout = durationDefault(out.HeartbeatTimeout, 45*time.Second)
	out.ProgressTimeout = durationDefault(out.ProgressTimeout, 10*time.Minute)
	out.PollInterval = durationDefault(out.PollInterval, 10*time.Second)
	out.FallbackPollInterval = durationDefault(out.FallbackPollInterval, 60*time.Second)
	out.CycleScanInterval = durationDefault(out.CycleScanInterval, 60*time.Second)
	out.ShutdownTimeout = durationDefault(out.ShutdownTimeout, 10*time.Second)
	out.ConsolidateAfterN = intDefault(out.ConsolidateAfterN, 5)
	out.PaneContextThreshold = intDefault(out.PaneContextThreshold, 40)
	// DreamInterval is intentionally NOT defaulted here: 0 means "disabled"
	// and must survive withDefaults. Production sets it explicitly in cmd_start.go.
	out.PaneMonitorInterval = durationDefault(out.PaneMonitorInterval, 5*time.Second)
	out.PaneRestartCooldown = durationDefault(out.PaneRestartCooldown, 2*time.Minute)
	out.PaneInactivityTimeout = durationDefault(out.PaneInactivityTimeout, 10*time.Minute)
	out.ReviewTimeout = durationDefault(out.ReviewTimeout, 15*time.Minute)
	out.ReviewDeadGrace = durationDefault(out.ReviewDeadGrace, 30*time.Second)
	out.RegressionRevert = boolDefault(out.RegressionRevert, true)
	out.CheckpointThreshold = intDefault(out.CheckpointThreshold, 75)
	if out.DefaultBranch == "" {
		out.DefaultBranch = "main"
	}
	if out.WebAddr == "" {
		out.WebAddr = "127.0.0.1:4444"
	}
	return out
}

// validate checks that all Config values are valid, including required
// durations, non-negative counts, and compatible feature flags. Call this
// AFTER withDefaults().
func (c Config) validate() error {
	if c.MaxWorkers < 0 {
		return fmt.Errorf("MaxWorkers must be non-negative, got %d", c.MaxWorkers)
	}
	if c.JanitorInterval < 0 {
		return fmt.Errorf("JanitorInterval must be non-negative, got %d", c.JanitorInterval)
	}
	if c.JanitorIdleThreshold < 0 {
		return fmt.Errorf("JanitorIdleThreshold must be non-negative, got %d", c.JanitorIdleThreshold)
	}
	if c.AuditEveryNJanitors < 0 {
		return fmt.Errorf("AuditEveryNJanitors must be non-negative, got %d", c.AuditEveryNJanitors)
	}
	if c.JanitorTopK < 0 {
		return fmt.Errorf("JanitorTopK must be non-negative, got %d", c.JanitorTopK)
	}
	if c.AuditEnabled && !c.JanitorEnabled {
		return fmt.Errorf("AuditEnabled requires JanitorEnabled because audit counters are driven by janitor cycles")
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
	if c.CycleScanInterval <= 0 {
		return fmt.Errorf("CycleScanInterval must be positive, got %v", c.CycleScanInterval)
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

	// lastStatusTime and lastStatusJSON implement throttling for status
	// directives. If a status request arrives within statusThrottleWindow
	// of the previous one, the cached JSON is returned without rebuilding.
	lastStatusTime time.Time
	lastStatusJSON string
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
		priorityBeads:          make(map[string]bool),
		pendingManagedIDs:      make(map[string]bool),
		pendingManagedSince:    make(map[string]time.Time),
		pendingWorkerTargets:   make(map[string]string),
		pendingSpawnForWorkers: make(map[string]bool),
		pendingExternalIDs:     make(map[string]bool),
		pendingExternalSince:   make(map[string]time.Time),
		workerReadyCh:          make(chan struct{}, 1),
		shutdownCh:             make(chan struct{}),
		beadsDir:               beadsDir,
		panesDir:               defaultPanesDir(),
		signaledPanes:          make(map[string]bool),
		paneStates:             make(map[string]*paneState),
		escalatedCycles:        make(map[string]bool),
		checkpoints:            newCheckpointTracker(),
		nowFunc:                time.Now,
		acceptSem:              make(chan struct{}, 100), // limit to 100 concurrent connection handlers
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
	d.safeGo(func() { d.runPresubmitScheduler(ctx) })
	d.safeGo(func() { RunSweepLoop(ctx, d.beads, d.db, SweepConfig{}) })
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

// connCloseCleanup runs the deferred connection teardown for handleConn.
// It guards against clobbering a reconnected worker: only cleans up if the
// stored conn still matches the one this goroutine was serving.
// workerID is captured by reference in the defer so it holds its final value.
func (d *Dispatcher) connCloseCleanup(workerID string, conn net.Conn) {
	if workerID == "" {
		return
	}
	d.mu.Lock()
	w, exists := d.workers[workerID]
	if !exists || w.conn != conn {
		d.mu.Unlock()
		return
	}
	if w.spawnFor && w.state == protocol.WorkerShuttingDown {
		w.lastSeen = d.nowFunc()
		d.mu.Unlock()
		d.notifyAssignLoop()
		return
	}
	beadID := w.beadID
	assignmentID := w.assignmentID
	worktree := w.worktree
	baseBranch := w.baseBranch
	preempted := w.state == protocol.WorkerPreempting
	if preempted && beadID != "" {
		// Keep the bead reserved while its durable assignment is terminalized.
		// Without this guard a concurrently idle replacement can create a second
		// active assignment after the worker is removed but before cleanup runs.
		d.assigningBeads[beadID] = true
	}
	delete(d.workers, workerID)
	d.mu.Unlock()

	if preempted && beadID != "" {
		d.reconcilePreemptedDisconnect(workerID, beadID, assignmentID, worktree)
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

func (d *Dispatcher) handleStatus(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Status == nil {
		return
	}
	d.touchProgress(workerID)
	evType := "status"
	if msg.Status.State == "qg_retry_received" {
		evType = "qg_retry_received"
	}
	payload := fmt.Sprintf(`{"state":%q,"result":%q}`, msg.Status.State, msg.Status.Result)
	if evType == "qg_retry_received" {
		_ = d.logEvent(ctx, evType, workerID, msg.Status.BeadID, workerID, payload)
		return
	}
	d.broadcastEvent(evType, msg.Status.BeadID, workerID)
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

	d.mu.Lock()
	release := d.releaseWorkerAfterDoneLocked(workerID, beadID)
	d.mu.Unlock()
	d.assignPendingHandoffsToIdleWorkers()

	if !release.ok || release.worktree == "" {
		return
	}

	// Clear tracking state for completed bead.
	d.clearBeadTracking(beadID)

	// Re-check bead type: if a task bead was promoted to an epic mid-flight,
	// skip merge to avoid landing decomposition work as a finished task.
	// Show errors are best-effort — fall through to the normal merge path.
	if d.handleTypeChangedToEpic(ctx, workerID, beadID, release) {
		return
	}

	if release.isEpicDecomp {
		// Epic decomposition complete — skip merge/close; just clean up the worktree.
		_ = d.logEvent(ctx, "epic_decomp_done", workerID, beadID, workerID, "")
		if err := d.completeAssignment(ctx, release.assignmentID, beadID); err != nil {
			_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		}
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "epic_decomp_reopen_failed", "dispatcher", beadID, workerID, err.Error())
		}
		d.safeGo(func() {
			if err := d.worktrees.Remove(ctx, release.worktree); err != nil {
				_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
			}
		})
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, release.assignmentID)
		return
	}

	if d.cfg.ManualIntegration {
		d.completeManualIntegration(ctx, beadID, workerID, release)
		return
	}

	// Merge in background
	d.safeGo(func() {
		d.mergeAndComplete(ctx, beadID, workerID, release.worktree, release.branch, release.epicID, release.targetBranch, release.assignmentID)
	})
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

func (d *Dispatcher) shutdownCompletedSpawnForWorkerLocked(w *trackedWorker) {
	sendShutdownWithoutBuffering(w)
	w.markShuttingDownWithoutAssignment()
}

// handleQGStuckDetected handles the case where a bead has produced the same QG
// output enough consecutive times to be considered stuck. The repeated identical
// output proves the current approach isn't working, so we classify with
// RetryExhausted=true so deterministic failures get ReopenOriginal routing.
func (d *Dispatcher) handleQGStuckDetected(ctx context.Context, workerID, beadID, qgOutput, qgFingerprint, qgSummary string) {
	_ = d.logEvent(ctx, "qg_stuck_detected", workerID, beadID, workerID,
		fmt.Sprintf(`{"repeated_count":%d}`, maxStuckCount))
	d.mu.Lock()
	assignmentID := d.assignmentIDLocked(workerID, beadID)
	d.mu.Unlock()
	stuckRec := QGFailureRecord{
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  qgFingerprint,
		Summary:      qgSummary,
		Output:       qgOutput,
	}
	stuckCls := d.classifyQGFailure(ctx, stuckRec, QGFailureHistory{RetryExhausted: true})
	d.handleRepeatedQGOutput(ctx, workerID, beadID, stuckRec, stuckCls)
}

// handleQGFailure processes a quality-gate failure: checks for stuck detection
// (repeated identical outputs), increments the attempt counter, escalates if
// either cap is reached, or re-assigns with feedback.
func (d *Dispatcher) handleQGFailure(ctx context.Context, workerID, beadID, qgOutput string) {
	d.touchProgress(workerID)

	qg := d.evaluateQGFailure(ctx, workerID, beadID, qgOutput)
	d.logQGFailureRejection(ctx, workerID, beadID, qg)

	// Check stuck detection: hash QGOutput and track consecutive identical hashes.
	if d.isQGStuck(beadID, qgOutput) {
		d.handleQGStuckDetected(ctx, workerID, beadID, qgOutput, qg.record.Fingerprint, qg.record.Summary)
		return
	}

	// Transient and flaky failures use backoff retry — they do not increment
	// attemptCounts and therefore do not burn the worker-fix retry budget.
	if qg.classification.Decision == QGFailureDecisionBackoffRetry {
		d.handleTransientQGFailure(ctx, workerID, beadID, qg.record, qg.classification)
		return
	}

	retry := d.reserveQGRetryAttempt(workerID, beadID, qg.err)
	if retry.exhausted {
		d.handleQGExhausted(ctx, workerID, beadID, retry.assignmentID, qgOutput, retry.attempt)
		return
	}

	d.recordQGFailureIncident(ctx, workerID, beadID, retry.assignmentID, retry.attempt, qgOutput, qg.record.Fingerprint, qg.record.Summary, qg.classification)
	d.persistBeadCount(ctx, retry.assignmentID, beadID, "attempt_count", retry.attempt)
	d.qgRetryWithReservation(ctx, workerID, beadID, qgOutput, retry.attempt)
}

type qgFailureEvaluation struct {
	err            *protocol.QualityGateError
	record         QGFailureRecord
	classification QGFailureClassification
}

func (d *Dispatcher) evaluateQGFailure(ctx context.Context, workerID, beadID, qgOutput string) qgFailureEvaluation {
	fingerprint, summary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	record := QGFailureRecord{
		BeadID:      beadID,
		WorkerID:    workerID,
		Component:   "worker",
		Fingerprint: fingerprint,
		Summary:     summary,
		Output:      qgOutput,
	}
	return qgFailureEvaluation{
		err: &protocol.QualityGateError{
			BeadID:   beadID,
			WorkerID: workerID,
			Output:   qgOutput,
		},
		record:         record,
		classification: d.classifyQGFailure(ctx, record, QGFailureHistory{}),
	}
}

func (d *Dispatcher) logQGFailureRejection(ctx context.Context, workerID, beadID string, qg qgFailureEvaluation) {
	_ = d.logEvent(ctx, "quality_gate_rejected", workerID, beadID, workerID,
		fmt.Sprintf(`{"reason":"QualityGatePassed=false","error":%q,"fingerprint":%q,"summary":%q,"class":%q,"decision":%q,"confidence":%q,"classification_reason":%q}`,
			qg.err.Error(), qg.record.Fingerprint, qg.record.Summary, qg.classification.Class, qg.classification.Decision, qg.classification.Confidence, qg.classification.Reason))
}

type qgRetryAttempt struct {
	attempt      int
	assignmentID int64
	exhausted    bool
}

func (d *Dispatcher) reserveQGRetryAttempt(workerID, beadID string, qgErr *protocol.QualityGateError) qgRetryAttempt {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.attemptCounts[beadID]++
	attempt := d.attemptCounts[beadID]
	qgErr.Attempt = attempt
	assignmentID := d.assignmentIDLocked(workerID, beadID)
	if attempt >= maxQGRetries {
		return qgRetryAttempt{attempt: attempt, assignmentID: assignmentID, exhausted: true}
	}
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerReserved
	}
	return qgRetryAttempt{attempt: attempt, assignmentID: assignmentID}
}

func (d *Dispatcher) recordQGFailureIncident(ctx context.Context, workerID, beadID string, assignmentID int64, attempt int, output, fingerprint, summary string, cls QGFailureClassification) {
	rec := QGFailureRecord{
		ID:           fmt.Sprintf("%s:%s:%d:%d", beadID, workerID, assignmentID, attempt),
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  fingerprint,
		Summary:      summary,
		Output:       output,
	}
	incident, err := RecordQGFailureOccurrence(ctx, d.db, rec, cls)
	if err != nil {
		_ = d.logEvent(ctx, "qg_failure_record_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q}`, err.Error(), fingerprint))
		return
	}
	if err := d.linkQGFailureToBeads(ctx, incident, rec, cls); err != nil {
		_ = d.logEvent(ctx, "qg_failure_link_failed", workerID, beadID, workerID,
			fmt.Sprintf(`{"error":%q,"fingerprint":%q,"incident_id":%d}`, err.Error(), fingerprint, incident.ID))
	}
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
	snap := d.opusEscalationSnapshotLocked(workerID)
	d.mu.Unlock()

	var payload *protocol.AssignPayload
	success := d.withReservation(workerID,
		// I/O function: build full payload outside lock.
		func() string {
			if d.cfg.RegressionRevert {
				if _, err := d.seedQGBaselineFromFailure(ctx, beadID, snap.worktree, qgOutput); err != nil {
					_ = d.logEvent(ctx, "qg_baseline_capture_failed", workerID, beadID, workerID,
						fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
				}
			}
			payload = d.buildAssignPayload(ctx, &snap, attempt, qgOutput, "", snap.execution)
			return ""
		},
		// Assign function: update state and send message under lock.
		func(w *trackedWorker, memCtx string) bool {
			// Escalate runtime+model+reasoning together.
			w.runtime, w.model, w.reasoning = agentmodel.ResolveForRole("worker_escalation")
			payload.Runtime = w.runtime
			payload.Model = w.model // sync with live escalated value
			payload.Reasoning = w.reasoning

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
				_ = d.completeAssignment(ctx, w.assignmentID, beadID)
				return false
			}
			_ = d.logEventLocked(ctx, "qg_retry_assign_sent", workerID, beadID, workerID,
				fmt.Sprintf(`{"attempt":%d,"model":%q}`, attempt, payload.Model))
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

func (d *Dispatcher) checkPreMergeLeaks(ctx context.Context, beadID, workerID, worktree, branch, targetBranch string, assignmentID int64) bool {
	cfg := d.cfg.LeakScan
	if !cfg.Enabled {
		return true
	}
	target := targetBranch
	if target == "" {
		target = d.cfg.DefaultBranch
	}
	diff, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "diff", target+".."+branch)
	if err != nil {
		_ = d.logEvent(ctx, "pre_merge_leakscan_error", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q,"error":%q}`, branch, target, err.Error()))
		return true
	}
	allow, err := d.loadLeakScanAllowlist()
	if err != nil {
		_ = d.logEvent(ctx, "pre_merge_leakscan_error", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q,"error":%q}`, branch, target, err.Error()))
		return true
	}
	result := scanPreMergeDiff(string(diff), cfg, allow)
	if len(result.Matches) == 0 {
		return true
	}
	summary := leakscan.Summarize(result)
	_ = d.logEvent(ctx, "pre_merge_leakscan_warn", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"target":%q,"matches":%q}`, branch, target, summary))
	if !preMergeLeakShouldBlock(cfg.BlockOn, result) {
		return true
	}
	return d.blockPreMergeLeak(ctx, beadID, workerID, worktree, branch, assignmentID, summary)
}

func scanPreMergeDiff(diff string, cfg LeakScanConfig, allow leakscan.Allowlist) leakscan.Result {
	if cfg.EntropyMinBits == 0 {
		return leakscan.ScanDiff(diff, leakscan.DefaultPatterns(), allow)
	}
	return leakscan.ScanDiffWithMinEntropy(diff, leakscan.DefaultPatterns(), allow, cfg.EntropyMinBits)
}

func (d *Dispatcher) loadLeakScanAllowlist() (leakscan.Allowlist, error) {
	if d.cfg.LeakScan.AllowlistPath == "" {
		return leakscan.Allowlist{}, nil
	}
	allow, err := leakscan.LoadAllowlist(d.cfg.LeakScan.AllowlistPath)
	if err != nil {
		return leakscan.Allowlist{}, fmt.Errorf("load pre-merge leakscan allowlist: %w", err)
	}
	return allow, nil
}

func preMergeLeakShouldBlock(blockOn string, result leakscan.Result) bool {
	switch strings.ToLower(strings.TrimSpace(blockOn)) {
	case "", "none":
		return false
	case "critical":
		for _, match := range result.Matches {
			if match.Severity == leakscan.SeverityCritical && match.Action == leakscan.ActionBlock {
				return true
			}
		}
		return false
	default:
		return result.ShouldBlock
	}
}

func (d *Dispatcher) blockPreMergeLeak(ctx context.Context, beadID, workerID, worktree, branch string, assignmentID int64, summary string) bool {
	_ = d.logEvent(ctx, "merge_blocked_secret_leak", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"worktree":%q,"matches":%q}`, branch, worktree, summary))
	d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: assignmentID,
		WorkerID:     workerID,
		Worktree:     worktree,
		Branch:       branch,
		Reason:       "pre_merge_secret_leak",
		Details:      summary,
	})
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "pre-merge secret leak", summary), beadID, workerID)
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	return false
}

// checkEpicQG creates a temporary worktree from epicBranch, runs the local
// quality gate against it with mutation testing disabled unless configured, and cleans up
// the worktree on completion. It returns true when the gate passes and
// tryCloseEpic should proceed to completeEpicClose. On failure or error it
// handles logging/escalation and returns false.
func (d *Dispatcher) checkEpicQG(ctx context.Context, epicID, workerID, epicBranch, targetBranch string) bool {
	wtID := d.epicQGWorktreeID(epicID)
	worktree, _, err := d.worktrees.Create(ctx, wtID, epicBranch)
	if err != nil {
		_ = d.logEvent(ctx, "epic_qg_worktree_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, err.Error()))
		return d.handleEpicQGInfraFailure(ctx, epicID, workerID, epicBranch, err)
	}
	defer func() { _ = d.worktrees.Remove(context.Background(), worktree) }()

	passed, qgOutput, qgErr := d.qgRunner.Run(ctx, worktree, !d.cfg.MutationTesting, d.qgMutationBase(targetBranch))
	if qgErr != nil {
		_ = d.logEvent(ctx, "epic_qg_error", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"error":%q}`, qgErr.Error()))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, epicID, "epic QG error", qgErr.Error()), epicID, workerID)
		return d.handleEpicQGInfraFailure(ctx, epicID, workerID, epicBranch, qgErr)
	}
	if !passed {
		return d.handleEpicQGFailure(ctx, epicID, workerID, epicBranch, qgOutput)
	}
	return true
}

func (d *Dispatcher) epicQGWorktreeID(epicID string) string {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(atomic.AddUint64(&d.epicQGWorktreeSeq, 1), 36)
	maxPrefixLen := 63 - len("-qg-") - len(suffix)
	if maxPrefixLen < 1 {
		maxPrefixLen = 1
	}
	prefix := epicID
	if len(prefix) > maxPrefixLen {
		prefix = strings.TrimRight(prefix[:maxPrefixLen], "-._")
	}
	if prefix == "" {
		prefix = "q"
	}
	return prefix + "-qg-" + suffix
}

// handleEpicQGFailure classifies a QG failure on an epic branch and takes the
// appropriate action:
//   - systemic/flaky → record or reuse the infra incident; no epic-specific fix bead.
//   - deterministic/unknown → create one targeted fix bead per (epic, fingerprint);
//     subsequent calls with the same fingerprint are no-ops to prevent duplicates.
//
// Always returns false (the epic remains open until QG passes); the bool
// return preserves the symmetry with checkEpicQG's other branches so the
// caller can keep its `return d.handleEpicQGFailure(...)` form.
func (d *Dispatcher) handleEpicQGFailure(ctx context.Context, epicID, workerID, epicBranch, qgOutput string) bool { //nolint:unparam // bool mirrors checkEpicQG's success/failure return so the call site stays a single-line return
	fp, summary := FingerprintQGFailure(qgOutput, QGFingerprintOptions{})
	rec := QGFailureRecord{
		BeadID:      epicID,
		WorkerID:    workerID,
		Component:   "epic",
		Fingerprint: fp,
		Summary:     summary,
		Output:      qgOutput,
	}
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{})

	_ = d.logEvent(ctx, "epic_qg_failed", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"output":%q,"fingerprint":%q,"class":%q,"decision":%q}`, qgOutput, fp, cls.Class, cls.Decision))

	// Systemic or flaky: record/reuse an infra incident; no epic-specific fix bead.
	if cls.Decision == QGFailureDecisionCreateOrReuseInfra || cls.Decision == QGFailureDecisionBackoffRetry {
		_, _ = d.createOrReuseQGInfraIncident(ctx, rec, cls)
		return false
	}

	// Impossible acceptance/state failures need the existing epic state fixed;
	// creating another epic child repeats the missing-AC loop.
	if cls.Decision == QGFailureDecisionBumpOriginal {
		return false
	}

	// Deterministic or unknown: one fix bead per (epic, fingerprint); skip if already created.
	if !d.epicFixBeadExists(ctx, epicID, fp) {
		beads, err := CreateBeadGraph(ctx, d.beads, epicID, []beadstore.CreateParams{{
			Title:              fmt.Sprintf("P0: Fix QG failures on %s", epicBranch),
			Type:               "bug",
			Priority:           0,
			Description:        fmt.Sprintf("Epic %s QG failed on branch %s.\n\nQG output:\n%s", epicID, epicBranch, qgOutput),
			AcceptanceCriteria: epicQGFixAcceptance(epicID, epicBranch),
		}})
		if err == nil && len(beads) > 0 {
			d.recordEpicFixBead(ctx, epicID, fp, beads[0].ID)
		}
	}
	return false
}

func (d *Dispatcher) epicFixBeadExists(ctx context.Context, epicID, fingerprint string) bool {
	if d.db == nil {
		return false
	}
	var count int
	_ = d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM qg_epic_fix_beads WHERE epic_id=? AND fingerprint=?`,
		epicID, fingerprint).Scan(&count)
	return count > 0
}

func (d *Dispatcher) recordEpicFixBead(ctx context.Context, epicID, fingerprint, beadID string) {
	if d.db == nil {
		return
	}
	_, _ = d.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO qg_epic_fix_beads (epic_id, fingerprint, bead_id) VALUES (?, ?, ?)`,
		epicID, fingerprint, beadID)
}

// handleEpicQGInfraFailure classifies an infrastructure error (worktree create
// failure or QG runner error) as systemic/transient/unknown, records an infra
// incident via the standard QG failure store, and returns false. It never
// creates a direct epic child fix task.
func (d *Dispatcher) handleEpicQGInfraFailure(ctx context.Context, epicID, workerID, epicBranch string, err error) bool { //nolint:unparam // always false: infra errors never allow epic close to proceed
	errText := err.Error()
	fingerprint, summary := FingerprintQGFailure(errText, QGFingerprintOptions{})
	rec := QGFailureRecord{
		BeadID:      epicID,
		WorkerID:    workerID,
		Component:   "dispatcher",
		Output:      errText,
		Fingerprint: fingerprint,
		Summary:     summary,
	}
	cls := d.classifyQGFailure(ctx, rec, QGFailureHistory{})
	cls.Decision = QGFailureDecisionCreateOrReuseInfra

	incident, incErr := d.createOrReuseQGInfraIncident(ctx, rec, cls)
	if incErr != nil {
		_ = d.logEvent(ctx, "epic_qg_infra_record_failed", "dispatcher", epicID, workerID,
			fmt.Sprintf(`{"branch":%q,"error":%q}`, epicBranch, incErr.Error()))
		return false
	}
	_ = d.logEvent(ctx, "qg_infra_incident_reused", "dispatcher", epicID, workerID,
		fmt.Sprintf(`{"incident_id":%d,"class":%q,"fingerprint":%q}`, incident.ID, cls.Class, fingerprint))
	return false
}

func epicQGFixAcceptance(epicID, epicBranch string) string {
	return fmt.Sprintf("Test: epic QG failure for %s | Cmd: git branch --list %s | grep -q '^..%s$' && ORO_QG_CONTEXT=local ./scripts/quality_gate.sh | Assert: quality gate passes on %s without creating another missing-AC child task.\nRead: scripts/quality_gate.sh, docs/runbooks/beadstore-recovery.md\nEdges: reproduce the failing QG on %s before changing code; do not close %s directly; fix the underlying QG failure, then let the dispatcher retry epic auto-close.",
		epicID, epicBranch, epicBranch, epicBranch, epicBranch, epicID)
}

func (d *Dispatcher) mergeAndComplete(ctx context.Context, beadID, workerID, worktree, branch, epicID, targetBranch string, assignmentID int64) { //nolint:funlen // orchestrates merge pipeline; splitting would obscure the sequential flow
	defer d.guardMerge(beadID)()

	// Closed-bead guard (oro-jev9): if the bead was closed externally between
	// assignment and review (e.g. manager dedup-closed it as a duplicate),
	// abort before merging. Otherwise the worker's commit lands on the target
	// branch even though the bead is already resolved.
	detail, showErr := d.beads.Show(ctx, beadID)
	if showErr == nil && detail != nil && detail.Status == "closed" {
		_ = d.logEvent(ctx, "merge_aborted_closed_bead", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target":%q}`, branch, targetBranch))
		d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
			BeadID:       beadID,
			AssignmentID: assignmentID,
			WorkerID:     workerID,
			Worktree:     worktree,
			Branch:       branch,
			Reason:       "external_close_without_merge_proof",
			Details:      "merge aborted because bead was already closed before merge proof",
		})
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}

	if !d.checkPreMergeLeaks(ctx, beadID, workerID, worktree, branch, targetBranch, assignmentID) {
		return
	}
	if showErr == nil && d.completeEpicRebaseChild(ctx, detail, beadID, workerID, worktree, branch, epicID, targetBranch, assignmentID) {
		return
	}

	result, err := d.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       beadID,
		TargetBranch: targetBranch,
		PreFFCheck: func(checkCtx context.Context, finalWorktree string) error {
			return d.runPreMergeQG(checkCtx, beadID, workerID, finalWorktree, assignmentID, targetBranch)
		},
	})
	if err != nil {
		if d.handlePreFFCheckError(ctx, beadID, workerID, worktree, assignmentID, err) {
			return
		}
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
			d.safeGo(func() {
				d.handleMergeConflictResult(ctx, beadID, workerID, worktree, epicID, targetBranch, assignmentID, resultCh)
			})
			_ = d.logEvent(ctx, "merge_conflict", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"files":%q}`, conflictErr.Files))
			return
		}
		// Non-conflict merge failure after rebase/ff retry is still recoverable.
		// Keep the worktree and agent branch available for the escalation agent
		// and for a future reassignment; otherwise the only recovery context can
		// be deleted before ops gets a chance to inspect it.
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		if updateErr := d.updateBeadStatus(ctx, beadID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "merge_failed_reopen_failed", "dispatcher", beadID, workerID, updateErr.Error())
		}
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID, "merge failed", err.Error()), beadID, workerID)
		_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, err.Error())
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}
	if result.Noop {
		d.handleNoopMerge(ctx, beadID, workerID, worktree, branch, epicID, targetBranch, assignmentID, result.CommitSHA)
		return
	}

	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, epicID, targetBranch, assignmentID, result.CommitSHA)
}

func (d *Dispatcher) handleNoopMerge(ctx context.Context, beadID, workerID, worktree, branch, epicID, targetBranch string, assignmentID int64, sha string) {
	target := targetBranch
	if target == "" {
		target = d.cfg.DefaultBranch
	}
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"merged but assignment cleanup failed", err.Error()), beadID, workerID)
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}
	if err := d.CloseBead(ctx, beadID, fmt.Sprintf("Merged: %s", sha)); err != nil {
		_ = d.logEvent(ctx, "close_bead_after_noop_merge_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"no-op merge proven but bead close failed", err.Error()), beadID, workerID)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
	d.cancelOpsAgents(ctx, beadID, workerID, "bead_merged_noop")
	_ = d.logEvent(ctx, "merge_noop", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"target":%q,"sha":%q}`, branch, target, sha))
	if epicID != "" {
		d.mu.Lock()
		delete(d.epicMergeFailed, epicID)
		d.mu.Unlock()
	}
	d.autoCloseEpicIfComplete(ctx, workerID, epicID)
	d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree, target)
	d.maybeConsolidateMemory(ctx)
	d.maybeTriggerDream(ctx)
	d.maybeTriggerJanitor(ctx)
}

func (d *Dispatcher) completeEpicRebaseChild(ctx context.Context, detail *protocol.BeadDetail, beadID, workerID, worktree, branch, epicID, targetBranch string, assignmentID int64) bool {
	if !IsEpicRebaseChild(detail, epicID, targetBranch) {
		return false
	}
	recoveryTarget := epicRebaseChildRecoveryTarget(detail, targetBranch)
	if recoveryTarget == "" {
		d.failEpicRebaseChild(ctx, beadID, workerID, assignmentID, "epic rebase child target resolution failed", fmt.Errorf("cannot resolve recovery target for %s", beadID))
		return true
	}
	if err := d.validateEpicRebaseChildAncestry(ctx, branch, recoveryTarget, targetBranch); err != nil {
		d.failEpicRebaseChild(ctx, beadID, workerID, assignmentID, "epic rebase child ancestry check failed", err)
		return true
	}
	if err := d.worktrees.UpdateBranchRef(ctx, targetBranch, branch); err != nil {
		d.failEpicRebaseChild(ctx, beadID, workerID, assignmentID, "epic rebase child update failed", err)
		return true
	}
	sha, err := d.worktrees.BranchHead(ctx, branch)
	if err != nil || strings.TrimSpace(sha) == "" {
		sha = branch
	}
	_ = d.logEvent(ctx, "epic_rebase_child_ref_updated", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"epic":%q,"branch":%q,"source":%q}`, epicID, targetBranch, branch))
	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, epicID, targetBranch, assignmentID, sha)
	return true
}

func epicRebaseChildRecoveryTarget(detail *protocol.BeadDetail, epicBranch string) string {
	if detail == nil {
		return ""
	}
	if target, _ := detail.Metadata["epic_rebase_target"].(string); strings.TrimSpace(target) != "" {
		return strings.TrimSpace(target)
	}
	return strings.TrimSpace(strings.TrimPrefix(detail.Title, "Rebase "+epicBranch+" onto "))
}

func (d *Dispatcher) validateEpicRebaseChildAncestry(ctx context.Context, branch, targetBranch, epicBranch string) error {
	checker, ok := d.worktrees.(assignmentBaseBranchSafetyChecker)
	if !ok {
		return fmt.Errorf("cannot verify required ancestry for recovery branch %s", branch)
	}
	for _, requiredAncestor := range []string{targetBranch, epicBranch} {
		hasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, requiredAncestor, branch)
		if err != nil {
			return fmt.Errorf("check whether recovery branch %s contains %s: %w", branch, requiredAncestor, err)
		}
		if hasUniqueCommits {
			return fmt.Errorf("recovery branch %s does not contain required ancestry from %s", branch, requiredAncestor)
		}
	}
	return nil
}

func (d *Dispatcher) failEpicRebaseChild(ctx context.Context, beadID, workerID string, assignmentID int64, summary string, cause error) {
	if updateErr := d.updateBeadStatus(ctx, beadID, "open"); updateErr != nil {
		_ = d.logEvent(ctx, "merge_failed_reopen_failed", "dispatcher", beadID, workerID, updateErr.Error())
	}
	if requeueErr := d.requeueAssignment(ctx, assignmentID); requeueErr != nil {
		_ = d.logEvent(ctx, "merge_failed_requeue_failed", "dispatcher", beadID, workerID, requeueErr.Error())
	}
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID, summary, cause.Error()), beadID, workerID)
	_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, cause.Error())
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
}

// IsEpicRebaseChild reports whether detail is the canonical recovery task for
// rebasing an epic branch onto its target. Recovery tasks must be allowed to
// run against the divergence they were created to repair.
func IsEpicRebaseChild(detail *protocol.BeadDetail, epicID, targetBranch string) bool {
	if detail == nil || epicID == "" || targetBranch == "" {
		return false
	}
	epicBranch := protocol.EpicBranchPrefix + epicID
	if targetBranch != epicBranch {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(detail.Title), "Rebase "+epicBranch+" onto ")
}

func (d *Dispatcher) finalizeSuccessfulMerge(ctx context.Context, beadID, workerID, worktree, epicID, targetBranch string, assignmentID int64, sha string) {
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"merged but assignment cleanup failed", err.Error()), beadID, workerID)
		d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)
		return
	}
	if err := d.CloseBead(ctx, beadID, fmt.Sprintf("Merged: %s", sha)); err != nil {
		_ = d.logEvent(ctx, "close_bead_after_merge_failed", "dispatcher", beadID, workerID, err.Error())
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"merged but bead close failed", err.Error()), beadID, workerID)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)

	// A successful merge proves the system is producing, not crash-looping —
	// reset the unexpected-exit counter so reconcileScale's runaway cap
	// (managed+exits >= 2*target) can't strand a long-running dispatcher with
	// fewer workers than target after natural worker turnover (oro-1dbr).
	d.mu.Lock()
	d.unexpectedManagedExits = 0
	d.mu.Unlock()

	d.cancelOpsAgents(ctx, beadID, workerID, "bead_merged")

	_ = d.logEvent(ctx, "merged", "dispatcher", beadID, workerID, fmt.Sprintf(`{"sha":%q}`, sha))
	mergedTo := targetBranch
	if mergedTo == "" {
		mergedTo = d.cfg.DefaultBranch
	}
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeComplete, beadID, "merged to "+mergedTo, sha), beadID, workerID)

	if epicID != "" {
		d.mu.Lock()
		delete(d.epicMergeFailed, epicID)
		d.mu.Unlock()
	}
	d.autoCloseEpicIfComplete(ctx, workerID, epicID)
	d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree, targetBranch)

	d.maybeConsolidateMemory(ctx)
	d.maybeTriggerDream(ctx)
	d.maybeTriggerJanitor(ctx)
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
			if d.memoryServices.Consolidate == nil {
				return
			}
			merged, pruned, err := d.memoryServices.Consolidate(ctx)
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

// maybeTriggerDream increments beadsSinceDream and, when it reaches
// DreamInterval, resets the counter and spawns an async dream agent.
// DreamInterval=0 disables dreaming entirely.
func (d *Dispatcher) maybeTriggerDream(ctx context.Context) {
	d.mu.Lock()
	if d.cfg.DreamInterval <= 0 {
		d.mu.Unlock()
		return
	}
	d.beadsSinceDream++
	fire := d.beadsSinceDream >= d.cfg.DreamInterval
	if fire {
		d.beadsSinceDream = 0
	}
	d.mu.Unlock()

	if fire {
		d.triggerDream(ctx)
	}
}

// maybeTriggerJanitor counts completed merges and starts a janitor scan only
// once the configured interval is reached. Normal runs wait for a sufficiently
// idle queue; three intervals force a run so sustained load cannot starve
// cleanliness work. Every configured audit cadence replaces that janitor run.
func (d *Dispatcher) maybeTriggerJanitor(ctx context.Context) {
	d.mu.Lock()
	if !d.cfg.JanitorEnabled || d.cfg.JanitorInterval <= 0 {
		d.mu.Unlock()
		return
	}

	interval := uint64(d.cfg.JanitorInterval)
	d.mergesSinceJanitor++
	forceRun := d.mergesSinceJanitor/interval >= 3
	ready := d.mergesSinceJanitor >= interval && d.cachedQueueDepth <= d.cfg.JanitorIdleThreshold
	if !ready && !forceRun {
		d.mu.Unlock()
		return
	}

	d.mergesSinceJanitor = 0
	spawn := d.janitorSpawnFn
	if d.cfg.AuditEnabled && d.cfg.AuditEveryNJanitors > 0 {
		d.janitorRunsSinceAudit++
		if d.janitorRunsSinceAudit >= uint64(d.cfg.AuditEveryNJanitors) {
			d.janitorRunsSinceAudit = 0
			spawn = d.auditSpawnFn
			d.mu.Unlock()
			d.safeGo(func() {
				if spawn != nil {
					spawn(ctx)
					return
				}
				d.spawnAudit(ctx)
			})
			return
		}
	}
	d.mu.Unlock()

	d.safeGo(func() { d.spawnJanitor(ctx, spawn) })
}

// spawnJanitor is the serialized asynchronous boundary for a selected janitor
// cycle. Failed scans restore one interval of merge budget for a prompt retry.
func (d *Dispatcher) spawnJanitor(ctx context.Context, spawn func(context.Context)) {
	d.cleanlinessCycleMu.Lock()
	defer d.cleanlinessCycleMu.Unlock()

	if spawn != nil {
		spawn(ctx)
		return
	}
	if err := d.runJanitor(ctx); err != nil {
		d.restoreJanitorCadenceAfterFailure()
		_ = d.logEvent(ctx, "janitor_scan_failed", "dispatcher", "", "", err.Error())
	}
}

func (d *Dispatcher) restoreJanitorCadenceAfterFailure() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cfg.JanitorInterval > 0 {
		interval := uint64(d.cfg.JanitorInterval)
		const maxUint64 = ^uint64(0)
		if maxUint64-d.mergesSinceJanitor < interval {
			d.mergesSinceJanitor = maxUint64
		} else {
			d.mergesSinceJanitor += interval
		}
	}
	if d.cfg.AuditEnabled && d.cfg.AuditEveryNJanitors > 0 && d.janitorRunsSinceAudit > 0 {
		d.janitorRunsSinceAudit--
	}
}

// spawnAudit is the asynchronous boundary for an audit that replaces a
// janitor cycle. Cleanliness cycles serialize across both roles.
func (d *Dispatcher) spawnAudit(ctx context.Context) {
	d.cleanlinessCycleMu.Lock()
	defer d.cleanlinessCycleMu.Unlock()

	roleBeadID, err := d.ensureRoleBead(ctx, "audit")
	if err != nil {
		d.restoreAuditCadenceAfterFailure()
		_ = d.logEvent(ctx, "audit_role_failed", "dispatcher", "", "", err.Error())
		return
	}
	if err := d.runAudit(ctx, roleBeadID); err != nil {
		d.restoreAuditCadenceAfterFailure()
		_ = d.logEvent(ctx, "audit_scan_failed", "dispatcher", "", "", err.Error())
	}
}

func (d *Dispatcher) restoreAuditCadenceAfterFailure() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cfg.JanitorInterval > 0 {
		interval := uint64(d.cfg.JanitorInterval)
		const maxUint64 = ^uint64(0)
		if maxUint64-d.mergesSinceJanitor < interval {
			d.mergesSinceJanitor = maxUint64
		} else {
			d.mergesSinceJanitor += interval
		}
	}
	if d.cfg.AuditEnabled && d.cfg.AuditEveryNJanitors > 0 {
		d.janitorRunsSinceAudit = uint64(d.cfg.AuditEveryNJanitors - 1)
	}
}

// triggerDream spawns a dream memory-consolidation agent and handles the result
// asynchronously. DreamInterval<=0 disables dreaming entirely (no-op).
// Errors from the agent are logged but do not propagate.
func (d *Dispatcher) triggerDream(ctx context.Context) {
	if d.cfg.DreamInterval <= 0 {
		return
	}
	if d.memoryServices.TrimSearchEvents != nil {
		n, err := d.memoryServices.TrimSearchEvents(ctx, 30*24*time.Hour)
		if err != nil {
			_ = d.logEvent(ctx, "retention_trim_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		} else if n > 0 {
			_ = d.logEvent(ctx, "retention_trim", "dispatcher", "", "",
				fmt.Sprintf(`{"deleted":%d}`, n))
		}
	}
	resultCh := d.ops.Dream(ctx, d.dreamOpts(ctx))
	d.safeGo(func() { d.handleDreamResult(ctx, resultCh) })
}

func (d *Dispatcher) dreamOpts(ctx context.Context) ops.DreamOpts {
	return ops.DreamOpts{
		Memories:       d.dumpMemoriesForDream(ctx),
		ActiveBiasTags: d.activeBiasTags(ctx),
	}
}

type calibratingCardStore interface {
	Calibration(context.Context) (cards.Scorecard, error)
}

func (d *Dispatcher) activeBiasTags(ctx context.Context) []string {
	store, ok := d.cardStore.(calibratingCardStore)
	if !ok {
		return nil
	}
	scorecard, err := store.Calibration(ctx)
	if err != nil || scorecard.Skipped {
		return nil
	}
	return scorecard.ActiveBiasTags
}

// dumpMemoriesForDream serializes all memories as a text block for the dream agent.
// Returns empty string on error or when no memory store is configured.
func (d *Dispatcher) dumpMemoriesForDream(ctx context.Context) string {
	if d.memories == nil {
		return ""
	}
	mems, err := d.memories.DumpAll(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "dream_dump_failed", "dispatcher", "", "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return ""
	}
	if len(mems) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range mems {
		fmt.Fprintf(&b, "- [%d] (%s) %s\n", m.ID, m.Type, m.Content)
	}
	return b.String()
}

// handleDreamResult waits for the dream agent result, parses any dream actions
// from the feedback, and applies them via dreamExecuteFn (defaults to
// memoryServices.ExecuteDream). Ops errors are logged; the function never panics.
func (d *Dispatcher) handleDreamResult(ctx context.Context, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		if result.Err != nil {
			_ = d.logEvent(ctx, "dream_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, result.Err.Error()))
			return
		}
		actions := ParseDreamActions(result.Feedback)
		if d.cfg.GradeGateEnabled {
			d.writeDreamProposals(ctx, actions)
			_ = d.logEvent(ctx, "dream_complete", "dispatcher", "", "",
				fmt.Sprintf(`{"actions":%d}`, len(actions)))
			return
		}
		execFn := d.dreamExecuteFn
		if execFn == nil {
			execFn = func(ctx context.Context, actions []DreamAction, _ MemoryStore, logFn func(string)) error {
				if d.memoryServices.ExecuteDream == nil {
					return nil
				}
				return d.memoryServices.ExecuteDream(ctx, actions, logFn)
			}
		}
		logFn := func(msg string) {
			_ = d.logEvent(ctx, "dream_execute_error", "dispatcher", "", "", msg)
		}
		if err := execFn(ctx, actions, d.memories, logFn); err != nil {
			_ = d.logEvent(ctx, "dream_execute_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		_ = d.logEvent(ctx, "dream_complete", "dispatcher", "", "",
			fmt.Sprintf(`{"actions":%d}`, len(actions)))
	}
}

func (d *Dispatcher) writeDreamProposals(ctx context.Context, actions []DreamAction) {
	if d.cardStore == nil {
		return
	}
	for _, action := range actions {
		if _, err := d.cardStore.Create(ctx, dreamProposalParams(action)); err != nil {
			_ = d.logEvent(ctx, "dream_proposal_failed", "dispatcher", "", "",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
}

func dreamProposalParams(action DreamAction) cards.CardCreateParams {
	content := dreamActionContent(action)
	return cards.CardCreateParams{
		Type:         dreamActionCardType(action),
		Title:        dreamProposalTitle(content),
		BodySummary:  content,
		BodyFull:     content,
		Tags:         action.Params.Tags,
		GradeState:   "proposed",
		ProposalHash: dreamProposalHash(action),
	}
}

func dreamActionCardType(action DreamAction) cards.CardType {
	if action.Params.Type == "decision" {
		return cards.CardTypeDecision
	}
	return cards.CardTypePattern
}

func dreamActionContent(action DreamAction) string {
	if action.Params.Content != "" {
		return action.Params.Content
	}
	switch action.Kind {
	case "DELETE":
		return fmt.Sprintf("Dream proposed deleting memory %d", action.ID)
	case "MERGE":
		return fmt.Sprintf("Dream proposed merging memories %s", joinInt64s(action.IDs))
	default:
		return fmt.Sprintf("Dream proposed %s action", strings.ToLower(action.Kind))
	}
}

func dreamProposalTitle(content string) string {
	const maxTitleRunes = 80
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= maxTitleRunes {
		return string(runes)
	}
	return string(runes[:maxTitleRunes])
}

func dreamProposalHash(action DreamAction) string {
	h := sha256.Sum256([]byte(fmt.Sprintf(
		"dream-proposal-v1|%s|%d|%v|%s|%s|%s",
		action.Kind,
		action.ID,
		action.IDs,
		action.Params.Type,
		strings.Join(action.Params.Tags, ","),
		action.Params.Content,
	)))
	return hex.EncodeToString(h[:])
}

func joinInt64s(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ",")
}

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
		execution:    w.execution,
		worktree:     w.worktree,
		runtime:      w.runtime,
		model:        w.model,
		epicID:       w.epicID,
		baseBranch:   w.baseBranch,
		targetBranch: w.targetBranch,
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
		assignmentID: assignmentID,
		execution:    snap.execution,
		beadID:       beadID,
		epicID:       snap.epicID,
		worktree:     snap.worktree,
		baseBranch:   snap.baseBranch,
		targetBranch: snap.targetBranch,
		runtime:      snap.runtime,
		model:        snap.model,
		title:        title,
		labels:       labels,
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

func (d *Dispatcher) handleReadyForReview(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ReadyForReview == nil {
		return
	}
	beadID := msg.ReadyForReview.BeadID

	d.touchProgress(workerID)
	d.recordWorkerProgress(ctx, workerID, beadID, "ready_for_review")
	_ = d.logEvent(ctx, "ready_for_review", workerID, beadID, workerID, "")

	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, targetBranch string
	var assignmentID int64
	if ok {
		w.state = protocol.WorkerReviewing
		worktree = w.worktree
		targetBranch = w.targetBranch
		assignmentID = w.assignmentID
	}
	d.mu.Unlock()

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
	Dirty bool
	Files []string
}

// Feedback returns actionable worker feedback for a dirty pre-review worktree.
func (r PreReviewGitHygieneResult) Feedback() string {
	if len(r.Files) == 0 {
		return "Pre-review git hygiene failed. Remove unrelated edits or stage/commit task files before requesting review again."
	}
	return "Pre-review git hygiene failed. stage/commit task files or remove unrelated edits before requesting review again. Dirty files: " +
		strings.Join(r.Files, ", ")
}

func (d *Dispatcher) checkPreReviewGitHygiene(ctx context.Context, _, worktree string) (PreReviewGitHygieneResult, error) {
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
	for _, entry := range entries {
		if d.isIgnorableManagedQualityGateStatus(worktree, entry) {
			continue
		}
		files = append(files, entry.Path)
	}
	if len(files) == 0 {
		return PreReviewGitHygieneResult{}, nil
	}
	sort.Strings(files)
	return PreReviewGitHygieneResult{Dirty: true, Files: files}, nil
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

func (d *Dispatcher) isIgnorableManagedQualityGateStatus(worktree string, entry gitStatusPorcelainEntry) bool {
	if entry.Code == "??" && entry.Path == filepath.ToSlash(filepath.Join(protocol.OroDir, "assignment-capability.json")) {
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
	_ = d.logEvent(ctx, "review_approved", "ops", beadID, workerID, result.Feedback)
	d.clearRejectionCount(beadID)
	d.appendExtractedReviewPatterns(ctx, beadID, workerID, result.Feedback)
	d.sendReviewApproved(workerID, result.Feedback)
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

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, wOK := d.workers[workerID]; wOK {
		w.state = protocol.WorkerReserved
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
	if w.state == protocol.WorkerReserved {
		w.lastSeen = d.nowFunc()
		for _, pending := range w.pendingMsgs {
			_ = d.sendToWorker(w, pending)
		}
		w.pendingMsgs = nil
		return
	}

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

func (d *Dispatcher) reactivateRequeuedAssignment(ctx context.Context, beadID, workerID string) int64 {
	if d.db == nil || beadID == "" {
		return 0
	}
	var assignmentID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='requeued' ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&assignmentID); err != nil {
		return d.activeAssignmentIDForBead(ctx, beadID)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='active', completed_at=NULL, worker_id=? WHERE id=?`,
		workerID, assignmentID,
	); err != nil {
		_ = d.logEvent(ctx, "assignment_reactivate_failed", "dispatcher", beadID, workerID, err.Error())
		return 0
	}
	_ = d.logEvent(ctx, "assignment_reactivated", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
	return assignmentID
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
	if d.shutdownReconnectIfSpawnForStopping(workerID) {
		return
	}

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

	assignmentID := d.reactivateRequeuedAssignment(ctx, beadID, workerID)

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return
	}

	d.processReconnectUnderLock(ctx, w, workerID, beadID, msg.Reconnect.State)
	if assignmentID > 0 && w.beadID == beadID {
		w.assignmentID = assignmentID
	}
	d.mu.Unlock()

	// Process any buffered events
	for _, buffered := range msg.Reconnect.BufferedEvents {
		d.handleMessage(ctx, workerID, buffered)
	}
}

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

func (d *Dispatcher) shutdownReconnectIfSpawnForStopping(workerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || !w.spawnFor || w.state != protocol.WorkerShuttingDown {
		return false
	}
	w.markShuttingDownWithoutAssignment()
	sendShutdownWithoutBuffering(w)
	w.lastSeen = d.nowFunc()
	return true
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
	var assignmentID int64
	if ok {
		w.shutdownApproved = true
		beadID = w.beadID // capture before clearing
		assignmentID = w.assignmentID
		if w.shutdownReason == shutdownReasonScaleDown || w.spawnFor {
			sendShutdownWithoutBuffering(w)
			w.markShuttingDownWithoutAssignment()
		} else {
			_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
			w.state = protocol.WorkerIdle
			w.shutdownReason = ""
			w.assignmentID = 0
			w.beadID = ""
			w.epicID = ""
			w.isEpicDecomp = false
		}
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

// handleDirectiveWithACK handles a DIRECTIVE message from the manager and sends an ACK response.
// This is used for short-lived manager connections that send a directive and expect an ACK.
func (d *Dispatcher) handleDirectiveWithACK(ctx context.Context, conn net.Conn, msg protocol.Message) {
	if msg.Directive == nil {
		return
	}

	dir := protocol.Directive(msg.Directive.Op)
	args := msg.Directive.Args
	source, reason := directiveProvenance(msg.Directive)
	ack := protocol.ACKPayload{OK: true}

	if !dir.Valid() && dir != directiveLaunchWorkers && dir != directiveCancelWorkerLaunch {
		ack.OK = false
		ack.Detail = "invalid directive"
	} else {
		detail, err := d.applyDirectiveWithProvenance(dir, args, source, reason)
		if err != nil {
			ack.OK = false
			ack.Detail = err.Error()
		} else {
			_ = d.logEvent(ctx, "directive", source, "", "",
				fmt.Sprintf(`{"directive":%q,"args":%q,"source":%q,"reason":%q}`, msg.Directive.Op, args, source, reason))
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

// assignLoop watches the filesystem task-data directory and assigns work when
// files change. Native sqlite mode skips that watch.
func (d *Dispatcher) assignLoop(ctx context.Context) {
	if d.shouldSkipTaskDataWatch() {
		d.assignLoopPoll(ctx)
		return
	}

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

func (d *Dispatcher) shouldSkipTaskDataWatch() bool {
	return strings.EqualFold(strings.TrimSpace(d.beadSourceMode), "sqlite")
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
		// File changed in task-data directory.
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

type schedulingUnitKind int

const (
	unitSpawnFor schedulingUnitKind = iota
	unitFocused
	unitIndependent
	unitEpic
)

type schedulingUnit struct {
	kind          schedulingUnitKind
	epicID        string
	epicPriority  int
	epicCreatedAt string
	beads         []protocol.Bead
}

type schedulingPlan struct {
	units []schedulingUnit
}

type schedulingEpicRoot struct {
	id        string
	priority  int
	createdAt string
	ok        bool
}

// buildSchedulingPlan groups ready beads into assignment units. Independent
// work is scheduled before epic units, while epic units are ordered by their
// root epic priority so one epic's frontier stays contiguous.
func (d *Dispatcher) buildSchedulingPlan(ctx context.Context, beads []protocol.Bead) (plan schedulingPlan, prioritySnapshot map[string]bool, focusVersion uint64) {
	d.mu.Lock()
	epic := d.focusedEpic
	focusVersion = d.focusVersion
	prioritySnapshot = make(map[string]bool, len(d.priorityBeads))
	for id := range d.priorityBeads {
		prioritySnapshot[id] = true
	}
	d.mu.Unlock()

	focused := d.focusedDescendants(ctx, beads, epic)
	parentCache := make(map[string]*protocol.BeadDetail)
	epicUnitIndexes := make(map[string]int)

	for _, bead := range beads {
		switch {
		case prioritySnapshot[bead.ID]:
			plan.appendUnit(unitSpawnFor, bead)
		case focused[bead.ID]:
			plan.appendUnit(unitFocused, bead)
		case bead.Epic == "":
			plan.appendUnit(unitIndependent, bead)
		default:
			plan.appendEpicUnit(d.schedulingEpicRoot(ctx, bead.Epic, parentCache), bead, epicUnitIndexes)
		}
	}
	plan.sort()

	return plan, prioritySnapshot, focusVersion
}

func (p *schedulingPlan) appendUnit(kind schedulingUnitKind, bead protocol.Bead) {
	p.units = append(p.units, schedulingUnit{
		kind:  kind,
		beads: []protocol.Bead{bead},
	})
}

func (p *schedulingPlan) appendEpicUnit(root schedulingEpicRoot, bead protocol.Bead, unitIndexes map[string]int) {
	if !root.ok {
		p.appendUnit(unitIndependent, bead)
		return
	}
	unitIdx, ok := unitIndexes[root.id]
	if !ok {
		p.units = append(p.units, schedulingUnit{
			kind:          unitEpic,
			epicID:        root.id,
			epicPriority:  root.priority,
			epicCreatedAt: root.createdAt,
		})
		unitIdx = len(p.units) - 1
		unitIndexes[root.id] = unitIdx
	}
	p.units[unitIdx].beads = append(p.units[unitIdx].beads, bead)
}

func (p *schedulingPlan) sort() {
	for i := range p.units {
		sort.SliceStable(p.units[i].beads, func(a, b int) bool {
			return p.units[i].beads[a].Priority < p.units[i].beads[b].Priority
		})
	}
	sort.SliceStable(p.units, func(i, j int) bool {
		return schedulingUnitLess(p.units[i], p.units[j])
	})
}

func (p schedulingPlan) beads() []protocol.Bead {
	total := 0
	for _, unit := range p.units {
		total += len(unit.beads)
	}
	beads := make([]protocol.Bead, 0, total)
	for _, unit := range p.units {
		beads = append(beads, unit.beads...)
	}
	return beads
}

func schedulingUnitLess(left, right schedulingUnit) bool {
	if left.kind != right.kind {
		return left.kind < right.kind
	}
	if left.kind != unitEpic {
		return left.beads[0].Priority < right.beads[0].Priority
	}
	if left.epicPriority != right.epicPriority {
		return left.epicPriority < right.epicPriority
	}
	if left.epicCreatedAt != right.epicCreatedAt {
		return left.epicCreatedAt < right.epicCreatedAt
	}
	return left.epicID < right.epicID
}

func (d *Dispatcher) schedulingEpicRoot(ctx context.Context, parentID string, parentCache map[string]*protocol.BeadDetail) schedulingEpicRoot {
	visited := make(map[string]bool)
	var root schedulingEpicRoot
	current := parentID
	for current != "" {
		if visited[current] {
			return schedulingEpicRoot{}
		}
		visited[current] = true

		parent, ok := parentCache[current]
		if !ok {
			detail, err := d.beads.Show(ctx, current)
			if err != nil || detail == nil {
				return schedulingEpicRoot{}
			}
			parent = detail
			parentCache[current] = detail
		}
		if strings.EqualFold(parent.Type, "epic") {
			root = schedulingEpicRoot{
				id:        current,
				priority:  parent.Priority,
				createdAt: parent.CreatedAt,
				ok:        true,
			}
		}
		current = parent.Epic
	}
	return root
}

func (d *Dispatcher) focusedDescendants(ctx context.Context, beads []protocol.Bead, focusedEpic string) map[string]bool {
	focused := make(map[string]bool)
	if focusedEpic == "" {
		return focused
	}
	parentCache := make(map[string]string)
	for _, bead := range beads {
		if d.isFocusedDescendant(ctx, bead.Epic, focusedEpic, parentCache) {
			focused[bead.ID] = true
		}
	}
	return focused
}

func (d *Dispatcher) isFocusedDescendant(ctx context.Context, parentID, focusedEpic string, parentCache map[string]string) bool {
	seen := make(map[string]bool)
	for parentID != "" {
		if parentID == focusedEpic {
			return true
		}
		if seen[parentID] {
			return false
		}
		seen[parentID] = true
		if cached, ok := parentCache[parentID]; ok {
			parentID = cached
			continue
		}
		parent, err := d.beads.Show(ctx, parentID)
		if err != nil || parent == nil {
			parentCache[parentID] = ""
			return false
		}
		parentCache[parentID] = parent.Epic
		parentID = parent.Epic
	}
	return false
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
	d.assignPendingHandoffsToIdleWorkers()

	// Find idle workers and count total workers.
	d.mu.Lock()
	var idle []idleWorker
	totalWorkers := 0
	for _, w := range d.workers {
		totalWorkers++
		if w.state == protocol.WorkerIdle {
			idle = append(idle, idleWorker{worker: w, targetBeadID: w.targetBeadID, spawnFor: w.spawnFor})
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
	d.cachedIdleWorkers = len(idle)
	d.mu.Unlock()

	if d.shouldScanForCycles() {
		d.scanDependencyCycles(ctx)
	}

	beads := d.filterAssignable(ctx, allBeads)
	if redeployable, blocked := d.recoveryQuarantineAssignmentScope(ctx); blocked {
		return
	} else if len(redeployable) > 0 {
		beads = filterBeadsByID(beads, redeployable)
	}

	plan, pbSnapshot, focusVersion := d.buildSchedulingPlan(ctx, beads)
	beads = plan.beads()
	reservedTargets, hasPendingSpawnFor := d.reservedSpawnForTargets()

	// Auto-scale: if we have assignable beads but no idle workers, scale up to MaxWorkers.
	if !hasPendingSpawnFor {
		queueDepth, idleCount := autoscaleInputsForIdleWorkers(idle, beads, reservedTargets)
		d.maybeAutoScale(ctx, queueDepth, idleCount)
	}

	// Priority contention is now handled by the preemption system (oro-wofg).
	// Escalating to the manager is noisy and unhelpful.
	// if len(idle) == 0 && totalWorkers > 0 {
	// 	d.checkPriorityContention(ctx, beads, totalWorkers)
	// 	return
	// }
	if len(idle) == 0 {
		return
	}

	assignedBeads := d.assignTargetedIdleWorkers(ctx, idle, beads, focusVersion)
	d.assignGeneralIdleWorkers(ctx, idle, plan, pbSnapshot, assignedBeads, reservedTargets, focusVersion)
}

func filterBeadsByID(beads []protocol.Bead, ids map[string]bool) []protocol.Bead {
	filtered := make([]protocol.Bead, 0, len(beads))
	for _, bead := range beads {
		if ids[bead.ID] {
			filtered = append(filtered, bead)
		}
	}
	return filtered
}

// recoveryQuarantineAssignmentScope preserves the recovery safety interlock:
// open quarantines block ordinary work, but a clean preserved worktree may be
// handed to one fresh worker to continue its own bead.
func (d *Dispatcher) recoveryQuarantineAssignmentScope(ctx context.Context) (map[string]bool, bool) {
	if d.db == nil {
		d.setRecoveryAssignmentFreeze(false, 0, "")
		return nil, false
	}
	openQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, d.db)
	if err != nil {
		reason := "recovery_quarantine_metric_load_failed: " + err.Error()
		d.setRecoveryAssignmentFreeze(true, 0, reason)
		d.logRecoveryAssignmentBlocked(ctx, 0, reason)
		return nil, true
	}
	if openQuarantines == 0 {
		d.setRecoveryAssignmentFreeze(false, 0, "")
		return nil, false
	}
	preservableQuarantines, err := d.countPreservableRecoveryQuarantines(ctx)
	if err != nil {
		reason := "recovery_quarantine_classification_failed: " + err.Error()
		d.setRecoveryAssignmentFreeze(true, openQuarantines, reason)
		d.logRecoveryAssignmentBlocked(ctx, openQuarantines, reason)
		return nil, true
	}
	if preservableQuarantines == 0 {
		d.setRecoveryAssignmentFreeze(false, 0, "")
		return nil, false
	}
	redeployable, err := d.autoRedeployablePreservedWorktrees(ctx)
	if err != nil {
		reason := "recovery_quarantine_inspection_failed: " + err.Error()
		d.setRecoveryAssignmentFreeze(true, preservableQuarantines, reason)
		d.logRecoveryAssignmentBlocked(ctx, preservableQuarantines, reason)
		return nil, true
	}
	if len(redeployable) == 0 {
		const reason = "open_recovery_quarantine"
		d.setRecoveryAssignmentFreeze(true, preservableQuarantines, reason)
		d.logRecoveryAssignmentBlocked(ctx, preservableQuarantines, reason)
		return nil, true
	}
	d.setRecoveryAssignmentFreeze(false, 0, "")
	return redeployable, false
}

func (d *Dispatcher) setRecoveryAssignmentFreeze(frozen bool, blockingQuarantines int, reason string) {
	d.mu.Lock()
	d.assignmentFrozenByQuarantine = frozen
	d.blockingRecoveryQuarantines = blockingQuarantines
	d.assignmentFreezeReason = reason
	d.mu.Unlock()
}

func (d *Dispatcher) logRecoveryAssignmentBlocked(ctx context.Context, openQuarantines int, reason string) {
	now := d.nowFunc()
	d.mu.Lock()
	if !d.lastRecoveryAssignmentBlockLog.IsZero() && now.Sub(d.lastRecoveryAssignmentBlockLog) < time.Minute {
		d.mu.Unlock()
		return
	}
	d.lastRecoveryAssignmentBlockLog = now
	d.mu.Unlock()

	_ = d.logEvent(ctx, "assignment_blocked_by_recovery_quarantine", "dispatcher", "", "",
		fmt.Sprintf(`{"open_recovery_quarantines":%d,"reason":%q}`, openQuarantines, reason))
}

func (d *Dispatcher) shouldScanForCycles() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cachedQueueDepth == 0 || d.cachedIdleWorkers == 0 {
		return false
	}
	return d.lastCycleScanAt.IsZero() || d.nowFunc().Sub(d.lastCycleScanAt) >= d.cfg.CycleScanInterval
}

func (d *Dispatcher) scanDependencyCycles(ctx context.Context) {
	d.mu.Lock()
	d.lastCycleScanAt = d.nowFunc()
	d.mu.Unlock()

	cycles, err := d.beads.DependencyCycles(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "dependency_cycle_scan_failed", "dispatcher", "", "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	for _, cycle := range cycles {
		d.escalateDependencyCycle(ctx, cycle)
	}
}

func (d *Dispatcher) escalateDependencyCycle(ctx context.Context, cycle beadstore.Cycle) {
	path := canonicalDependencyCyclePath(cycle)
	if len(path) < 2 {
		return
	}
	key := strings.Join(path, "\x00")
	d.mu.Lock()
	if d.escalatedCycles[key] {
		d.mu.Unlock()
		return
	}
	d.escalatedCycles[key] = true
	d.mu.Unlock()

	anchor := path[0]
	pathText := strings.Join(path, " -> ")
	msg := protocol.FormatEscalation(
		protocol.EscDependencyCycle,
		anchor,
		"blocking dependency cycle detected",
		fmt.Sprintf("Path: %s", pathText),
	)
	_ = d.beads.AppendJourney(ctx, anchor, beadstore.JourneyEvent{
		Ts:      d.nowFunc().UTC().Format(time.RFC3339Nano),
		Actor:   "dispatcher",
		Event:   "dependency_cycle_detected",
		Payload: fmt.Sprintf(`{"cycle_key":%q,"path":%q}`, key, pathText),
	})
	d.escalate(ctx, msg, anchor, "")
}

func canonicalDependencyCyclePath(cycle beadstore.Cycle) []string {
	if len(cycle) == 0 {
		return nil
	}
	nodes := append([]string(nil), cycle...)
	if len(nodes) > 1 && nodes[0] == nodes[len(nodes)-1] {
		nodes = nodes[:len(nodes)-1]
	}
	if len(nodes) == 0 {
		return nil
	}
	start := 0
	for i := 1; i < len(nodes); i++ {
		if nodes[i] < nodes[start] {
			start = i
		}
	}
	out := make([]string, 0, len(nodes)+1)
	for i := range nodes {
		out = append(out, nodes[(start+i)%len(nodes)])
	}
	out = append(out, out[0])
	return out
}

func (d *Dispatcher) reservedSpawnForTargets() (map[string]bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	targets := make(map[string]bool, len(d.pendingWorkerTargets))
	for _, target := range d.pendingWorkerTargets {
		if target != "" {
			targets[target] = true
		}
	}
	for _, worker := range d.workers {
		if worker.state == protocol.WorkerIdle && worker.targetBeadID != "" {
			targets[worker.targetBeadID] = true
		}
	}
	return targets, d.hasPendingSpawnForLocked()
}

func (d *Dispatcher) hasPendingSpawnForLocked() bool {
	for _, target := range d.pendingWorkerTargets {
		if target != "" {
			return true
		}
	}
	return false
}

// autoscaleInputsForIdleWorkers computes (queueDepth, idleCount) for autoscaling.
// reservedTargets contains bead IDs that are exclusively reserved for spawn-for workers
// (both pending and connected-idle). These beads must not inflate the autoscale queue
// depth because no general worker can claim them.
func autoscaleInputsForIdleWorkers(idle []idleWorker, beads []protocol.Bead, reservedTargets map[string]bool) (queueDepth, idleCount int) {
	if len(idle) == 0 {
		// No connected workers: count only beads that general workers can actually claim.
		// Excluding reserved spawn-for targets prevents autoscale from spawning general
		// workers for beads they can never take, which wastes worker slots.
		return countGeneralQueueDepth(beads, reservedTargets), 0
	}

	autoscaleIdle := 0
	targetedIdle := 0
	generalIdle := 0
	targets := make(map[string]bool)
	for _, candidate := range idle {
		if candidate.targetBeadID != "" {
			targets[candidate.targetBeadID] = true
		}
		if candidate.spawnFor {
			continue
		}
		autoscaleIdle++
		if candidate.targetBeadID == "" {
			generalIdle++
			continue
		}
		targetedIdle++
	}

	if autoscaleIdle == 0 {
		// All idle workers are spawn-for workers. Compute general queue depth excluding
		// both connected and pending spawn-for targets.
		return countGeneralQueueDepth(beads, targets, reservedTargets), 0
	}
	if targetedIdle == 0 || generalIdle > 0 {
		return len(beads), autoscaleIdle
	}

	generalQueueDepth := countGeneralQueueDepth(beads, targets, reservedTargets)
	if generalQueueDepth == 0 {
		return len(beads), autoscaleIdle
	}
	return targetedIdle + generalQueueDepth, 0
}

func countGeneralQueueDepth(beads []protocol.Bead, reservedSets ...map[string]bool) int {
	depth := 0
	for _, bead := range beads {
		if isReservedBead(bead.ID, reservedSets...) {
			continue
		}
		depth++
	}
	return depth
}

func isReservedBead(beadID string, reservedSets ...map[string]bool) bool {
	for _, reserved := range reservedSets {
		if reserved[beadID] {
			return true
		}
	}
	return false
}

func (d *Dispatcher) assignTargetedIdleWorkers(ctx context.Context, idle []idleWorker, beads []protocol.Bead, focusVersion uint64) map[string]bool {
	assignedBeads := make(map[string]bool)
	beadsByID := make(map[string]protocol.Bead, len(beads))
	for _, bead := range beads {
		beadsByID[bead.ID] = bead
	}

	for _, candidate := range idle {
		if candidate.targetBeadID == "" {
			continue
		}
		bead, ok := beadsByID[candidate.targetBeadID]
		if !ok {
			continue
		}
		_ = d.assignBead(ctx, candidate.worker, bead, focusVersion)
		d.mu.Lock()
		if candidate.worker.state != protocol.WorkerIdle {
			assignedBeads[bead.ID] = true
			candidate.worker.targetBeadID = ""
			delete(d.priorityBeads, bead.ID)
		}
		d.mu.Unlock()
	}
	return assignedBeads
}

func (d *Dispatcher) assignGeneralIdleWorkers(ctx context.Context, idle []idleWorker, plan schedulingPlan, pbSnapshot, assignedBeads, reservedTargets map[string]bool, focusVersion uint64) {
	// Assign beads to idle workers. Advance the idle cursor only when a worker is
	// actually claimed — epics skipped in assignBead leave the worker idle so the
	// next bead in the list can still be paired with it.
	idleIdx := 0
	for _, unit := range plan.units {
		idleIdx = d.assignGeneralSchedulingUnit(ctx, idle, idleIdx, unit, pbSnapshot, assignedBeads, reservedTargets, focusVersion)
	}
}

func (d *Dispatcher) assignGeneralSchedulingUnit(ctx context.Context, idle []idleWorker, idleIdx int, unit schedulingUnit, pbSnapshot, assignedBeads, reservedTargets map[string]bool, focusVersion uint64) int {
	nextIdleIdx := idleIdx
	for _, bead := range unit.beads {
		if assignedBeads[bead.ID] {
			continue
		}
		if reservedTargets[bead.ID] {
			continue
		}
		nextIdleIdx = d.nextGeneralIdleIndex(idle, nextIdleIdx)
		if nextIdleIdx >= len(idle) {
			break
		}
		_ = d.assignBead(ctx, idle[nextIdleIdx].worker, bead, focusVersion)
		_, nextIdleIdx = d.advanceAssignedGeneralIdle(idle, nextIdleIdx, bead.ID, pbSnapshot)
	}
	return nextIdleIdx
}

func (d *Dispatcher) nextGeneralIdleIndex(idle []idleWorker, idleIdx int) int {
	for idleIdx < len(idle) {
		d.mu.Lock()
		isAssignableIdle := idle[idleIdx].worker.state == protocol.WorkerIdle &&
			idle[idleIdx].worker.targetBeadID == "" &&
			!idle[idleIdx].worker.spawnFor
		d.mu.Unlock()
		if isAssignableIdle {
			return idleIdx
		}
		idleIdx++
	}
	return idleIdx
}

func (d *Dispatcher) advanceAssignedGeneralIdle(idle []idleWorker, idleIdx int, beadID string, pbSnapshot map[string]bool) (claimed bool, nextIdleIdx int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	nextIdleIdx = idleIdx
	claimed = idle[idleIdx].worker.state != protocol.WorkerIdle
	if claimed {
		nextIdleIdx++
	}
	if pbSnapshot[beadID] {
		delete(d.priorityBeads, beadID)
	}
	return claimed, nextIdleIdx
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
	// Guard against re-entry: if we already processed this external close, skip (FM2).
	d.mu.Lock()
	alreadyProcessed := d.processedExternalClose[beadID]
	d.mu.Unlock()
	if alreadyProcessed {
		return
	}

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
	case detail.Status == "open":
		if err := d.updateBeadStatus(ctx, beadID, "in_progress"); err != nil {
			_ = d.logEvent(ctx, "assigned_bead_status_reconcile_failed", "dispatcher", beadID, workerID, err.Error())
			return
		}
		_ = d.logEvent(ctx, "assigned_bead_status_reconciled", "dispatcher", beadID, workerID,
			`{"from":"open","to":"in_progress"}`)
		return
	default:
		// Bead exists and is not explicitly closed — keep worker assigned.
		return
	}

	worktree, epicID, targetBranch, assignmentID := d.shutdownWorkerForClose(workerID, beadID)
	d.finalizeExternalClose(ctx, workerID, beadID, worktree, epicID, targetBranch, assignmentID)
}

// shutdownWorkerForClose sends SHUTDOWN, captures the worker's worktree/epic/
// targetBranch/assignmentID, clears worker state under lock, and marks the
// close as processed (FM2). Removes the worker entirely when sendToWorker
// fails so tryAssign doesn't cycle beads through a zombie (oro-e2jk).
func (d *Dispatcher) shutdownWorkerForClose(workerID, beadID string) (worktree, epicID, targetBranch string, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.processedExternalClose[beadID] = true
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID {
		return "", "", "", 0
	}
	assignmentID = w.assignmentID
	worktree = w.worktree
	epicID = w.epicID
	targetBranch = w.targetBranch
	if err := d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown}); err != nil {
		_ = w.conn.Close()
		delete(d.workers, workerID)
		return worktree, epicID, targetBranch, assignmentID
	}
	w.state = protocol.WorkerShuttingDown
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.model = ""
	return worktree, epicID, targetBranch, assignmentID
}

// finalizeExternalClose cleans up the assignment record, worktree, and ops
// agents after an external close. If the worker has a worktree (and therefore
// possibly committed work on agent/<beadID>), the dispatcher first attempts
// to ff-merge that branch to its target so a worker that called
// `oro task close` itself doesn't silently drop committed work
// (oro-0xqv: oro-ohlro lost commit 099cc7a6 this way). Merger handles the
// no-commits / branch-missing cases by returning an error which we treat as
// the legacy cancellation path.
//
// Recovery outcomes:
//   - Merge succeeds: log external_close_recovered with the SHA. The merger
//     also removes the worktree, so we only complete the assignment and clear
//     tracking afterward.
//   - Merge fails (conflict, missing branch, transient error): log
//     external_close_recovery_failed, escalate with the worktree path and
//     error so the manager can recover manually, then proceed with the
//     legacy cleanup (worktree remove, tracking clear, cancellation event).
func (d *Dispatcher) finalizeExternalClose(ctx context.Context, workerID, beadID, worktree, epicID, targetBranch string, assignmentID int64) {
	logCancelled := func() {
		_ = d.logEvent(ctx, "external_close_cancelled", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"assignment_id":%d,"epic_id":%q,"target_branch":%q}`, assignmentID, epicID, targetBranch))
	}
	if worktree != "" {
		d.safeGo(func() {
			recovered := d.tryRecoverExternalCloseWork(ctx, workerID, beadID, worktree, targetBranch)
			d.cancelOpsAgents(ctx, beadID, workerID, "external_close")
			if recovered {
				_ = d.completeAssignment(ctx, assignmentID, beadID)
				// removeWorktreeAndClearTracking is a no-op if the merger already
				// took the worktree on a successful recovery merge.
				d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree, targetBranch)
				d.clearBeadTracking(beadID)
			} else {
				d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
					BeadID:       beadID,
					AssignmentID: assignmentID,
					WorkerID:     workerID,
					Worktree:     worktree,
					Branch:       protocol.BranchPrefix + beadID,
					Reason:       "external_close_recovery_failed",
					Details:      "external close recovery merge failed",
				})
			}
			logCancelled()
		})
		return
	}
	_ = d.completeAssignment(ctx, assignmentID, beadID)
	d.clearBeadTracking(beadID)
	logCancelled()
}

// tryRecoverExternalCloseWork attempts to ff-merge the agent branch for a
// bead that was closed externally so committed work isn't silently dropped.
// Logs external_close_recovered on success, external_close_recovery_failed
// + escalates on failure. Returns true only when merge proof exists.
func (d *Dispatcher) tryRecoverExternalCloseWork(ctx context.Context, workerID, beadID, worktree, targetBranch string) bool {
	branch := protocol.BranchPrefix + beadID
	result, err := d.merger.Merge(ctx, merge.Opts{
		Branch:       branch,
		Worktree:     worktree,
		BeadID:       beadID,
		TargetBranch: targetBranch,
	})
	if err == nil && result != nil {
		_ = d.logEvent(ctx, "external_close_recovered", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"sha":%q,"branch":%q,"target":%q}`, result.CommitSHA, branch, targetBranch))
		return true
	}
	errMsg := "no recoverable result"
	if err != nil {
		errMsg = err.Error()
	}
	_ = d.logEvent(ctx, "external_close_recovery_failed", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"worktree":%q,"target":%q,"error":%q}`, branch, worktree, targetBranch, errMsg))
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID,
		"external close: failed to recover worker branch "+branch,
		"worktree="+worktree+"; error="+errMsg), beadID, workerID)
	return false
}

// filterAssignable returns beads eligible for assignment: excludes closed beads,
// beads with status in_progress or blocked, beads with recent worktree creation
// failures (within cooldown window), beads currently in-flight (assigningBeads),
// beads with unresolved blocking dependencies, and beads whose agent branch is
// already merged to main.
// Epics are allowed through; assignBead performs the HasChildren check.
func (d *Dispatcher) filterAssignable(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	now := d.nowFunc()

	allBeads = d.filterExecutableBeads(ctx, allBeads)
	allBeads = d.filterRecoveryQuarantinedBeads(ctx, allBeads)

	d.mu.Lock()
	candidates := d.assignmentCandidatesLocked(allBeads, now)
	d.mu.Unlock()

	return d.filterAlreadyMergedBranches(ctx, candidates)
}

func (d *Dispatcher) filterExecutableBeads(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	// Already-decomposed epics are not executable worker tasks. Childless epics
	// remain assignable so a decomposition worker can create child beads.
	executable := make([]protocol.Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if strings.EqualFold(b.Type, "epic") {
			hasChildren, err := d.beads.HasChildren(ctx, b.ID)
			if err != nil {
				_ = d.logEvent(ctx, "epic_has_children_error", "dispatcher", b.ID, "", err.Error())
				continue
			}
			if hasChildren {
				d.processEpicSkip(ctx, b)
				continue
			}
		}
		executable = append(executable, b)
	}
	return executable
}

func (d *Dispatcher) filterRecoveryQuarantinedBeads(ctx context.Context, allBeads []protocol.Bead) []protocol.Bead {
	if len(allBeads) == 0 || d.db == nil {
		return allBeads
	}
	quarantined, err := d.openRecoveryQuarantineBeads(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "recovery_quarantine_filter_failed", "dispatcher", "", "", err.Error())
		return nil
	}
	if len(quarantined) == 0 {
		return allBeads
	}
	redeployable, err := d.autoRedeployablePreservedWorktrees(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "recovery_quarantine_redeploy_inspection_failed", "dispatcher", "", "", err.Error())
		return nil
	}
	filtered := make([]protocol.Bead, 0, len(allBeads))
	for _, bead := range allBeads {
		if quarantined[bead.ID] {
			if redeployable[bead.ID] {
				filtered = append(filtered, bead)
				continue
			}
			_ = d.logEvent(ctx, "recovery_quarantined_bead_skipped", "dispatcher", bead.ID, "",
				`{"reason":"open_recovery_quarantine"}`)
			continue
		}
		filtered = append(filtered, bead)
	}
	return filtered
}

func (d *Dispatcher) openRecoveryQuarantineBeads(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT DISTINCT q.bead_id
FROM recovery_quarantines q
LEFT JOIN assignments a ON a.id=q.assignment_id
WHERE q.status IN ('open', 'human_owned')
   OR (q.status='resolved' AND a.status='requeued' AND q.reason != 'branch_worktree_mismatch')`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query open recovery quarantines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var beadID string
		if err := rows.Scan(&beadID); err != nil {
			return nil, fmt.Errorf("scan open recovery quarantine: %w", err)
		}
		out[beadID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open recovery quarantines: %w", err)
	}
	return out, nil
}

func (d *Dispatcher) assignmentCandidatesLocked(allBeads []protocol.Bead, now time.Time) []protocol.Bead {
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
	return candidates
}

func (d *Dispatcher) filterAlreadyMergedBranches(ctx context.Context, candidates []protocol.Bead) []protocol.Bead {
	// Second pass: check whether the agent branch is already merged to the
	// branch this bead would target if assigned.
	// This requires a git subprocess, so it runs outside the lock.
	out := make([]protocol.Bead, 0, len(candidates))
	for _, b := range candidates {
		targetBranch, err := d.assignmentTargetBranch(ctx, b)
		if err != nil {
			_ = d.logEvent(ctx, "assignment_target_resolve_error", "dispatcher", b.ID, "", err.Error())
			out = append(out, b)
			continue
		}
		if d.isBranchMergedInto(ctx, b.ID, targetBranch) {
			_ = d.CloseBead(ctx, b.ID, fmt.Sprintf("branch already merged to %s", targetBranch))
			_ = d.logEvent(ctx, "bead_branch_already_merged", "dispatcher", b.ID, "", "")
			continue
		}
		out = append(out, b)
	}
	return out
}

func (d *Dispatcher) assignmentTargetBranch(ctx context.Context, bead protocol.Bead) (string, error) {
	defaultBranch := d.cfg.DefaultBranch
	if bead.Metadata != nil {
		if v, ok := bead.Metadata[MetaBranch]; ok {
			if s, ok := v.(string); ok && s != "" {
				defaultBranch = s
			}
		}
	}
	targetBranch, _, err := resolveEpicBranch(ctx, d.beads, bead.Epic, defaultBranch)
	if err != nil {
		return "", err
	}
	return targetBranch, nil
}

// processEpicSkip handles an epic found in the ready queue that must not be
// assigned to a worker. It logs non_executable_issue_type and checks whether
// all children are done so the epic can be auto-closed (fallback path for epics
// whose last child completed before the epic status was updated).
func (d *Dispatcher) processEpicSkip(ctx context.Context, bead protocol.Bead) {
	d.mu.Lock()
	alreadyLogged := d.epicSkipLogged[bead.ID]
	if !alreadyLogged {
		d.epicSkipLogged[bead.ID] = true
	}
	d.mu.Unlock()
	if !alreadyLogged {
		_ = d.logEvent(ctx, "non_executable_issue_type", "dispatcher", bead.ID, "",
			`{"reason":"non_executable_issue_type","issue_type":"epic"}`)
	}
	hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
	if err != nil || !hasChildren {
		return
	}
	allClosed, err := d.beads.AllChildrenClosed(ctx, bead.ID)
	if err != nil || !allClosed {
		return
	}
	targetBranch := resolveEpicTargetBranch(bead.Metadata, d.cfg.DefaultBranch)
	d.completeEpicClose(ctx, bead.ID, "", "All children completed", targetBranch)
}

// isBranchMergedInto reports whether agent/<beadID> represents work that has
// been merged into targetBranch. A branch is considered merged only when it
// (1) has at least one commit beyond its merge-base with targetBranch AND
// (2) is an ancestor of targetBranch.
//
// The empty-branch guard (1) prevents a destructive false positive: a stale
// agent branch sitting at a commit already in targetBranch's history (e.g., the
// worker never committed implementation) would otherwise satisfy --is-ancestor
// trivially, causing the dispatcher to close the bead as "branch already
// merged" and orphan any earlier worker's implementation commits. Returns false
// when the branch does not exist or any git command fails.
func (d *Dispatcher) isBranchMergedInto(ctx context.Context, beadID, targetBranch string) bool {
	branch := protocol.BranchPrefix + beadID // "agent/<beadID>"
	tipOut, err := d.commandRunner().Run(ctx, "git", "rev-parse", branch)
	if err != nil {
		return false
	}
	baseOut, err := d.commandRunner().Run(ctx, "git", "merge-base", branch, targetBranch)
	if err != nil {
		return false
	}
	if strings.TrimSpace(string(tipOut)) == strings.TrimSpace(string(baseOut)) {
		return false
	}
	_, err = d.commandRunner().Run(ctx, "git", "merge-base", "--is-ancestor", branch, targetBranch)
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
	// between mergeAndComplete setting mergingBeads and oro task close propagating
	// the status change — without this check the task appears "ready" to
	// oro task ready --json and gets re-assigned, causing bead_closed_externally spam.
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
		_ = d.logEvent(ctx, "bead_skipped_missing_ac", "dispatcher", bead.ID, workerID,
			`{"reason":"missing_acceptance"}`)
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMissingAC, bead.ID, "no acceptance criteria — spawning AC writer", ""), bead.ID, workerID)
		d.recordAssignmentFailure(bead.ID) // 60-second cooldown prevents re-triggering
		return title, "", false            // skip assignment this cycle
	}
	if !strings.EqualFold(bead.Type, "epic") {
		if executable, reason := isWorkerExecutableBead(bead, protocol.BeadDetail{AcceptanceCriteria: acceptance}); !executable {
			_ = d.logEvent(ctx, "bead_skipped_non_tdd_acceptance", "dispatcher", bead.ID, workerID,
				fmt.Sprintf(`{"reason":%q}`, reason))
			if reason == "non_tdd_acceptance" {
				d.escalate(ctx, protocol.FormatEscalation(protocol.EscNonTDDAC, bead.ID,
					fmt.Sprintf("priority %d bead has Cmd/Assert without 'Test:' prefix — rewrite acceptance or move out of worker queue (oro-5833)", bead.Priority), ""), bead.ID, workerID)
			}
			d.recordAssignmentFailure(bead.ID)
			return title, "", false
		}
	}
	if modules := protocol.CountDistinctModules(acceptance); modules > 2 {
		// Epics are expected to span multiple modules; skip the oversized check.
		// Also skip if the bead already has children — it was decomposed externally.
		isEpic := strings.EqualFold(bead.Type, "epic")
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

func isWorkerExecutableBead(bead protocol.Bead, detail protocol.BeadDetail) (executable bool, reason string) {
	if strings.EqualFold(bead.Type, "epic") {
		return false, "non_executable_type"
	}
	if strings.TrimSpace(detail.AcceptanceCriteria) == "" {
		return false, "missing_acceptance"
	}
	hasTest := strings.Contains(detail.AcceptanceCriteria, "Test:")
	hasOperationalMarker := strings.Contains(detail.AcceptanceCriteria, "Cmd:") ||
		strings.Contains(detail.AcceptanceCriteria, "Assert:")
	if !hasTest && hasOperationalMarker {
		return false, "non_tdd_acceptance"
	}
	return true, ""
}

// handleEpicBranchMissing checks if an epic branch is missing and decides whether to
// escalate or retry based on epic status. Handles all cases and returns early from assignBead.
func (d *Dispatcher) handleEpicBranchMissing(ctx context.Context, bead protocol.Bead, w *trackedWorker,
	baseBranch string, resolvedEpicID string, branchCheckErr error,
) {
	// Before escalating, check the epic's status to decide if this is
	// a genuine problem or a transient state.
	epicDetail, showErr := d.beads.Show(ctx, resolvedEpicID)

	// If Show returns an error, this is transient (e.g., DB issue).
	// Log and return without escalating — will retry next cycle.
	if showErr != nil {
		_ = d.logEvent(ctx, "epic_show_error", "dispatcher", bead.ID, w.id,
			fmt.Sprintf("error fetching epic %s: %v", resolvedEpicID, showErr))
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return
	}

	// If Show returns nil detail with no error, treat as error (retry).
	if epicDetail == nil {
		_ = d.logEvent(ctx, "epic_show_error", "dispatcher", bead.ID, w.id,
			fmt.Sprintf("epic %s returned nil detail", resolvedEpicID))
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return
	}

	// Check epic status to decide whether to escalate or retry.
	// open: epic not yet assigned → don't escalate, retry next cycle
	// blocked: epic is blocked → don't escalate, skip for now
	// in_progress: epic being worked on, branch missing → escalate (genuine problem)
	// closed: epic finished, branch missing → escalate (genuine problem)
	switch epicDetail.Status {
	case "open", "blocked":
		// Epic not yet assigned or is blocked; branch will be created when epic is worked.
		// Return without escalating — will retry.
		_ = d.logEvent(ctx, "epic_branch_pending", "dispatcher", bead.ID, w.id,
			fmt.Sprintf("epic %s in %s status, branch not yet created", resolvedEpicID, epicDetail.Status))
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		return
	}

	// For in_progress and closed statuses, escalate — branch should exist.
	reason := fmt.Sprintf("epic branch %q not found for bead %s", baseBranch, bead.ID)
	if branchCheckErr != nil {
		reason = fmt.Sprintf("checking epic branch %q: %v", baseBranch, branchCheckErr)
	}
	_ = d.logEvent(ctx, "epic_branch_missing", "dispatcher", bead.ID, w.id, reason)
	d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuckWorker, bead.ID, "epic branch missing", reason), bead.ID, w.id)
	_ = d.updateBeadStatus(ctx, bead.ID, "open")
	d.mu.Lock()
	delete(d.assigningBeads, bead.ID)
	d.mu.Unlock()
}

// ensureEpicBranchReady checks whether baseBranch exists and, if not, creates it
// lazily. Returns true if assignment should proceed, false if it should abort.
// When BranchExists itself fails, the existing handleEpicBranchMissing path is
// preserved. When the branch is simply absent and resolvedEpicID is non-empty,
// lazyCreateEpicBranch is attempted.
func (d *Dispatcher) ensureEpicBranchReady(ctx context.Context, bead protocol.Bead, w *trackedWorker, baseBranch, resolvedEpicID string) bool {
	exists, beErr := d.worktrees.BranchExists(ctx, baseBranch)
	if beErr != nil {
		// BranchExists itself failed (git broken) — preserve existing retry/escalate behavior.
		d.handleEpicBranchMissing(ctx, bead, w, baseBranch, resolvedEpicID, beErr)
		return false
	}
	// resolvedEpicID != "" guards against MetaBranch custom targets (e.g. "develop")
	// that resolve with an empty epic ID — those skip lazy creation.
	if !exists && resolvedEpicID != "" {
		return d.lazyCreateEpicBranch(ctx, bead.ID, baseBranch)
	}
	if exists && resolvedEpicID != "" {
		if !d.prepareEpicBranchForAssignment(ctx, bead.ID, w.id, baseBranch) {
			return false
		}
	}
	return true
}

func (d *Dispatcher) prepareEpicBranchForAssignment(ctx context.Context, beadID, workerID, baseBranch string) bool {
	preparer, ok := d.worktrees.(assignmentBaseBranchPreparer)
	if !ok {
		return true
	}
	fastForwarded, err := preparer.PrepareBaseBranchForAssignment(ctx, baseBranch, d.cfg.DefaultBranch)
	if err != nil {
		return d.rejectEpicBranchPreparation(ctx, beadID, workerID, baseBranch, err)
	}
	if fastForwarded {
		_ = d.logEvent(ctx, "epic_branch_fast_forwarded", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"base_branch":%q}`, baseBranch, d.cfg.DefaultBranch))
	}
	checker, ok := d.worktrees.(assignmentBaseBranchSafetyChecker)
	if !ok {
		return true
	}
	diverged, err := assignmentBaseBranchDiverged(ctx, checker, baseBranch, d.cfg.DefaultBranch)
	if err != nil {
		return d.rejectEpicBranchPreparation(ctx, beadID, workerID, baseBranch, err)
	}
	if !diverged {
		return true
	}
	if d.isEpicRebaseChildForBase(ctx, beadID, baseBranch) {
		_ = d.logEvent(ctx, "epic_rebase_child_prepare_diverged", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"base_branch":%q}`, baseBranch, d.cfg.DefaultBranch))
		return true
	}
	epicID := strings.TrimPrefix(baseBranch, protocol.EpicBranchPrefix)
	if d.tryDeterministicEpicRebase(ctx, epicID, workerID, baseBranch, d.cfg.DefaultBranch) {
		_ = d.logEvent(ctx, "epic_deterministic_rebase_prepare_diverged", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"base_branch":%q}`, baseBranch, d.cfg.DefaultBranch))
		return true
	}
	divergenceErr := fmt.Errorf("epic branch %s diverged from %s", baseBranch, d.cfg.DefaultBranch)
	if _, ensureErr := d.ensureEpicRebaseChild(ctx, epicID, baseBranch, d.cfg.DefaultBranch, divergenceErr.Error()); ensureErr != nil {
		_ = d.logEvent(ctx, "epic_rebase_child_ensure_failed", "dispatcher", beadID, workerID, ensureErr.Error())
	}
	return d.rejectEpicBranchPreparation(ctx, beadID, workerID, baseBranch, divergenceErr)
}

func assignmentBaseBranchDiverged(ctx context.Context, checker assignmentBaseBranchSafetyChecker, branch, baseBranch string) (bool, error) {
	branchHasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, branch, baseBranch)
	if err != nil {
		return false, fmt.Errorf("check unique commits on %s relative to %s: %w", branch, baseBranch, err)
	}
	if !branchHasUniqueCommits {
		return false, nil
	}
	baseHasUniqueCommits, err := checker.BaseBranchHasUniqueCommits(ctx, baseBranch, branch)
	if err != nil {
		return false, fmt.Errorf("check unique commits on %s relative to %s: %w", baseBranch, branch, err)
	}
	return baseHasUniqueCommits, nil
}

func (d *Dispatcher) rejectEpicBranchPreparation(ctx context.Context, beadID, workerID, baseBranch string, err error) bool {
	_ = d.logEvent(ctx, "epic_branch_prepare_failed", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"base_branch":%q,"error":%q}`, baseBranch, d.cfg.DefaultBranch, err.Error()))
	_ = d.updateBeadStatus(ctx, beadID, "open")
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
	d.recordAssignmentFailure(beadID)
	return false
}

// lazyCreateEpicBranch creates baseBranch from d.cfg.DefaultBranch when it is
// absent. Returns true if the caller should continue with assignment, false if
// the creation failed genuinely (bead reverted, failure recorded, escalation sent).
func (d *Dispatcher) lazyCreateEpicBranch(ctx context.Context, beadID, baseBranch string) bool {
	if err := d.worktrees.CreateBranch(ctx, baseBranch, d.cfg.DefaultBranch); err != nil {
		// Branch may already exist due to a concurrent child assignment (race) — re-check.
		exists2, _ := d.worktrees.BranchExists(ctx, baseBranch)
		if !exists2 {
			// Genuine failure (permissions, disk) — revert bead and escalate.
			_ = d.logEvent(ctx, "epic_branch_create_failed", "dispatcher", beadID, "",
				fmt.Sprintf(`{"branch":%q,"error":%q}`, baseBranch, err.Error()))
			_ = d.updateBeadStatus(ctx, beadID, "open")
			d.mu.Lock()
			delete(d.assigningBeads, beadID)
			d.mu.Unlock()
			d.recordAssignmentFailure(beadID)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuckWorker, beadID,
				"epic branch creation failed", err.Error()), beadID, "")
			return false
		}
		// Race resolved — another goroutine created the branch first.
		_ = d.logEvent(ctx, "epic_branch_race_resolved", "dispatcher", beadID, "",
			fmt.Sprintf(`{"branch":%q}`, baseBranch))
		return true
	}
	_ = d.logEvent(ctx, "epic_branch_created", "dispatcher", beadID, "",
		fmt.Sprintf(`{"branch":%q,"from":%q}`, baseBranch, d.cfg.DefaultBranch))
	return true
}

func (d *Dispatcher) assignBead(ctx context.Context, w *trackedWorker, bead protocol.Bead, focusVersionOpt ...uint64) error { //nolint:funlen,gocognit,gocyclo // orchestration logic, splitting would obscure flow
	if strings.TrimSpace(bead.ID) == "" {
		return fmt.Errorf("assignBead: empty bead ID")
	}
	focusVersion := d.currentFocusVersion()
	if len(focusVersionOpt) > 0 {
		focusVersion = focusVersionOpt[0]
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
	if d.focusChangedSince(focusVersion) {
		d.notifyAssignLoop()
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
		if w2.beadID == bead.ID && (w2.state == protocol.WorkerBusy || w2.state == protocol.WorkerReserved) {
			d.mu.Unlock()
			_ = d.logEvent(ctx, "assignment_race_detected", "dispatcher", bead.ID, w.id,
				fmt.Sprintf("bead already assigned to worker %s", w2.id))
			return nil
		}
	}
	if live, ok := d.workers[w.id]; !ok || live != w || w.state != protocol.WorkerIdle {
		d.mu.Unlock()
		return nil
	}
	d.assigningBeads[bead.ID] = true
	delete(d.escalatedBeads, bead.ID)
	w.state = protocol.WorkerReserved
	w.assignmentID = 0
	w.beadID = bead.ID
	w.epicID = ""
	w.isEpicDecomp = isEpicDecomp
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.runtime = ""
	w.model = ""
	w.reasoning = ""
	w.lastProgress = d.nowFunc()
	d.mu.Unlock()

	// Mark bead as in_progress BEFORE worktree creation.
	// This updates external state so other dispatchers see the bead is claimed.
	if err := d.updateBeadStatus(ctx, bead.ID, "in_progress"); err != nil {
		_ = d.logEvent(ctx, "update_status_failed", "dispatcher", bead.ID, w.id, err.Error())
		d.recordAssignmentFailure(bead.ID)
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID)
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, "", false, 0)
		return nil
	}

	// Check if a worktree already exists for this bead (from previous worker timeout/kill).
	// If it exists, reuse it to preserve uncommitted changes (oro-1eo8).
	d.mu.Lock()
	existingWorktree := d.worktreeByBead[bead.ID]
	d.mu.Unlock()

	var worktree, branch string
	var createdWorktree bool
	var err error
	// Resolve the base/target branch for this bead.
	// resolveEpicBranch walks the parent chain to find the actual epic ancestor —
	// bead.Epic maps to the JSON "parent" field and may point to a non-epic bead.
	// If the bead carries Metadata[MetaBranch], use that as the fallback default
	// branch instead of d.cfg.DefaultBranch (e.g. a standalone bead targeting a
	// custom integration branch).
	defaultBranch := d.cfg.DefaultBranch
	if bead.Metadata != nil {
		if v, ok := bead.Metadata[MetaBranch]; ok {
			if s, ok := v.(string); ok && s != "" {
				defaultBranch = s
			}
		}
	}
	baseBranch, resolvedEpicID, resolveErr := resolveEpicBranch(ctx, d.beads, bead.Epic, defaultBranch)
	if resolveErr != nil {
		_ = d.logEvent(ctx, "epic_branch_resolve_error", "dispatcher", bead.ID, w.id, resolveErr.Error())
		d.recordAssignmentFailure(bead.ID)
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID)
		return nil
	}
	if baseBranch != d.cfg.DefaultBranch {
		if !d.ensureEpicBranchReady(ctx, bead, w, baseBranch, resolvedEpicID) {
			d.releaseAssignmentReservation(w.id, bead.ID)
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

	worktree, branch, createdWorktree = d.prepareAssignmentWorktree(ctx, bead.ID, w.id, existingWorktree, baseBranch, targetBranch)
	if worktree == "" {
		d.releaseAssignmentReservation(w.id, bead.ID)
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, worktree, createdWorktree, 0)
		return nil
	}
	if !d.assignmentReservationHeld(w.id, bead.ID) {
		d.abortAssignmentReservationLost(ctx, bead.ID, w.id, worktree, createdWorktree, 0)
		return nil
	}

	assignmentID, assignErr := d.createAssignment(ctx, bead.ID, w.id, worktree)
	if assignErr != nil {
		_ = d.logEvent(ctx, "assignment_persist_failed", "dispatcher", bead.ID, w.id, assignErr.Error())
		d.recordAssignmentFailure(bead.ID)
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		if createdWorktree {
			_ = d.worktrees.Remove(ctx, worktree)
			d.mu.Lock()
			delete(d.worktreeByBead, bead.ID)
			d.mu.Unlock()
		}
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID)
		return nil
	}
	if d.focusChangedSince(focusVersion) {
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, worktree, createdWorktree, assignmentID)
		return nil
	}
	if !d.attachAssignmentToReservation(w.id, bead.ID, assignmentID, worktree, baseBranch, targetBranch, resolvedEpicID, isEpicDecomp) {
		d.abortAssignmentReservationLost(ctx, bead.ID, w.id, worktree, createdWorktree, assignmentID)
		return nil
	}
	_ = d.logEvent(ctx, "assign", "dispatcher", bead.ID, w.id,
		fmt.Sprintf(`{"worktree":%q,"branch":%q}`, worktree, branch))
	d.recordWorkerProgress(ctx, w.id, bead.ID, "assign")

	var codeCtx string
	if d.codeIndex != nil {
		ctx5s, cancel5s := context.WithTimeout(ctx, 5*time.Second)
		defer cancel5s()
		results, _ := d.searchCodeInWorkdir(ctx5s, bead.Title, 5, worktree)
		if len(results) > 0 {
			codeCtx = formatSearchResults(results)
		}
	}

	// Call estimator if bead needs estimation (no explicit model and no estimate yet)
	if bead.Model == "" && bead.EstimatedMinutes == 0 && d.estimator != nil {
		bead.EstimatedMinutes = d.estimator.Estimate(ctx, bead.Title, acceptance)
	}

	// Runtime launch selection is intentionally deferred to oro-zdqd/oro-snx1.
	// This step propagates runtime/model while preserving the existing
	// Claude-only worker launch path.
	resolvedRuntime, resolvedModel, resolvedReasoning := agentmodel.ResolveForBead("worker", bead)
	if isEpicDecomp {
		resolvedRuntime, resolvedModel, resolvedReasoning = agentmodel.ResolveForRole("ops_decompose")
	}
	execution := workerExecutionContext(assignmentID, isEpicDecomp, filepath.Base(d.cfg.RepoRoot))
	capability, capabilityErr := d.issueAssignmentCapability(
		ctx,
		execution.AssignmentID,
		execution.Generation,
		ActorRole(execution.ActorRole),
	)
	if capabilityErr != nil {
		_ = d.logEvent(ctx, "assignment_capability_issue_failed", "dispatcher", bead.ID, w.id, capabilityErr.Error())
		if completeErr := d.completeAssignment(ctx, assignmentID, bead.ID); completeErr != nil {
			_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", bead.ID, w.id, completeErr.Error())
		}
		_ = d.updateBeadStatus(ctx, bead.ID, "open")
		if createdWorktree {
			_ = d.worktrees.Remove(ctx, worktree)
			d.mu.Lock()
			delete(d.worktreeByBead, bead.ID)
			d.mu.Unlock()
		}
		d.mu.Lock()
		delete(d.assigningBeads, bead.ID)
		d.mu.Unlock()
		d.releaseAssignmentReservation(w.id, bead.ID)
		return nil
	}
	execution.Capability = capability.Token
	payload := d.buildAssignPayload(ctx, &trackedWorker{
		id:           w.id,
		beadID:       bead.ID,
		worktree:     worktree,
		runtime:      resolvedRuntime,
		model:        resolvedModel,
		reasoning:    resolvedReasoning,
		isEpicDecomp: isEpicDecomp,
		targetBranch: targetBranch,
	}, 0, "", "", execution)
	if payload.Title == "" {
		payload.Title = title
	}
	if payload.AcceptanceCriteria == "" {
		payload.AcceptanceCriteria = acceptance
	}
	payload.CodeSearchContext = codeCtx
	// Release any prior bead this worker was carrying — the new assignment is
	// committed, so any leftover in_progress state on the old bead must be
	// cleared (oro-xqrh).
	d.releasePriorAssignment(ctx, w, bead.ID)
	d.mu.Lock()
	if d.focusVersion != focusVersion {
		d.mu.Unlock()
		d.abortAssignmentForFocusChange(ctx, bead.ID, w.id, worktree, createdWorktree, assignmentID)
		return nil
	}
	if !d.assignmentReservationHeldLocked(w.id, bead.ID) {
		d.mu.Unlock()
		d.abortAssignmentReservationLost(ctx, bead.ID, w.id, worktree, createdWorktree, assignmentID)
		return nil
	}
	w.state = protocol.WorkerBusy
	w.assignmentID = assignmentID
	w.execution = execution
	w.beadID = bead.ID
	w.epicID = resolvedEpicID // actual epic ancestor ID for auto-close on merge
	w.isEpicDecomp = isEpicDecomp
	w.worktree = worktree
	w.baseBranch = baseBranch
	w.targetBranch = targetBranch
	w.runtime = resolvedRuntime
	w.model = resolvedModel
	w.reasoning = resolvedReasoning
	w.lastProgress = d.nowFunc()
	err = d.sendToWorker(w, protocol.Message{
		Type:   protocol.MsgAssign,
		Assign: payload,
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
		if completeErr := d.completeAssignment(ctx, assignmentID, bead.ID); completeErr != nil {
			_ = d.logEvent(ctx, "assignment_cleanup_failed", "dispatcher", bead.ID, w.id, completeErr.Error())
		}
		_ = d.worktrees.Remove(ctx, worktree)
		_ = d.logEvent(ctx, "worktree_cleanup", "dispatcher", bead.ID, w.id, err.Error())
	}
	return nil
}

func (d *Dispatcher) prepareAssignmentWorktree(
	ctx context.Context,
	beadID, workerID, existingWorktree, baseBranch, targetBranch string,
) (worktree, branch string, created bool) {
	if existingWorktree != "" {
		expectedBranch := protocol.BranchPrefix + beadID
		if !d.validateExistingWorktreeForReuse(ctx, beadID, workerID, existingWorktree, expectedBranch, baseBranch) {
			return "", "", false
		}
		_ = d.logEvent(ctx, "worktree_reused", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"worktree":%q}`, existingWorktree))
		return existingWorktree, expectedBranch, false
	}
	if !d.createFreshAssignmentWorktreeAllowed(ctx, beadID, workerID, targetBranch) {
		return "", "", false
	}
	worktree, branch, err := d.worktrees.Create(ctx, beadID, baseBranch)
	if err != nil {
		_ = d.logEvent(ctx, "worktree_error", "dispatcher", beadID, workerID, err.Error())
		d.recordAssignmentFailure(beadID)
		_ = d.updateBeadStatus(ctx, beadID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, beadID)
		d.mu.Unlock()
		return "", "", false
	}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()
	return worktree, branch, true
}

func (d *Dispatcher) createFreshAssignmentWorktreeAllowed(ctx context.Context, beadID, workerID, targetBranch string) bool {
	if cleanErr := d.deleteStaleAgentBranch(ctx, beadID, workerID, targetBranch); cleanErr != nil {
		return d.rejectFreshAssignmentWorktree(ctx, beadID)
	}
	return true
}

func (d *Dispatcher) rejectFreshAssignmentWorktree(ctx context.Context, beadID string) bool {
	d.recordAssignmentFailure(beadID)
	_ = d.updateBeadStatus(ctx, beadID, "open")
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
	return false
}

func (d *Dispatcher) validateExistingWorktreeForReuse(ctx context.Context, beadID, workerID, worktree, expectedBranch, baseBranch string) bool {
	currentBranch, currentErr := d.worktrees.CurrentBranch(ctx, worktree)
	if currentErr != nil || currentBranch != expectedBranch {
		d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
			BeadID:   beadID,
			WorkerID: workerID,
			Worktree: worktree,
			Branch:   expectedBranch,
			Reason:   "branch_worktree_mismatch",
			Details:  "tracked worktree is not checked out on expected agent branch during assignment",
		})
		d.recordAssignmentFailure(beadID)
		_ = d.updateBeadStatus(ctx, beadID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, beadID)
		d.mu.Unlock()
		return false
	}
	preparer, ok := d.worktrees.(existingWorktreeReusePreparer)
	if !ok {
		return true
	}
	fastForwarded, err := preparer.PrepareExistingForReuse(ctx, worktree, expectedBranch, baseBranch)
	if err != nil {
		recovered, recoveryErr := d.recoverExistingWorktreeReuseDivergence(ctx,
			beadID, workerID, worktree, expectedBranch, baseBranch, err)
		if recovered {
			return true
		}
		if recoveryErr != nil {
			err = recoveryErr
		}
		d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
			BeadID:   beadID,
			WorkerID: workerID,
			Worktree: worktree,
			Branch:   expectedBranch,
			Reason:   "unsafe_stale_branch",
			Details:  err.Error(),
		})
		d.recordAssignmentFailure(beadID)
		_ = d.updateBeadStatus(ctx, beadID, "open")
		d.mu.Lock()
		delete(d.assigningBeads, beadID)
		d.mu.Unlock()
		return false
	}
	if fastForwarded {
		_ = d.logEvent(ctx, "worktree_fast_forwarded", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"worktree":%q,"branch":%q,"base_branch":%q}`, worktree, expectedBranch, baseBranch))
	}
	return true
}

func (d *Dispatcher) recoverExistingWorktreeReuseDivergence(ctx context.Context, beadID, workerID, worktree, expectedBranch, baseBranch string, prepareErr error) (bool, error) {
	if !isBranchDivergedFromBase(prepareErr) {
		return false, prepareErr
	}
	if d.isEpicRebaseChildForBase(ctx, beadID, baseBranch) {
		_ = d.logEvent(ctx, "epic_rebase_child_reuse_diverged", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"worktree":%q,"branch":%q,"base_branch":%q,"error":%q}`,
				worktree, expectedBranch, baseBranch, prepareErr.Error()))
		return true, nil
	}
	rebaser, ok := d.worktrees.(existingWorktreeDivergedRebaser)
	if !ok {
		return false, prepareErr
	}
	if err := rebaser.RebaseDivergedExistingForReuse(ctx, worktree, expectedBranch, baseBranch); err != nil {
		return false, fmt.Errorf("%w; rebase diverged existing worktree: %w", prepareErr, err)
	}
	_ = d.logEvent(ctx, "worktree_rebased_for_reuse", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"worktree":%q,"branch":%q,"base_branch":%q}`,
			worktree, expectedBranch, baseBranch))
	return true, nil
}

func (d *Dispatcher) isEpicRebaseChildForBase(ctx context.Context, beadID, baseBranch string) bool {
	if !strings.HasPrefix(baseBranch, protocol.EpicBranchPrefix) {
		return false
	}
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		return false
	}
	epicID := strings.TrimPrefix(baseBranch, protocol.EpicBranchPrefix)
	return IsEpicRebaseChild(detail, epicID, baseBranch)
}

func isBranchDivergedFromBase(err error) bool {
	return err != nil && strings.Contains(err.Error(), "diverged from base")
}

func (d *Dispatcher) focusChangedSince(version uint64) bool {
	d.mu.Lock()
	changed := d.focusVersion != version
	d.mu.Unlock()
	return changed
}

func (d *Dispatcher) currentFocusVersion() uint64 {
	d.mu.Lock()
	version := d.focusVersion
	d.mu.Unlock()
	return version
}

func (d *Dispatcher) assignmentReservationHeld(workerID, beadID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.assignmentReservationHeldLocked(workerID, beadID)
}

func (d *Dispatcher) assignmentReservationHeldLocked(workerID, beadID string) bool {
	w, ok := d.workers[workerID]
	return ok && w != nil && w.state == protocol.WorkerReserved && w.beadID == beadID
}

func (d *Dispatcher) attachAssignmentToReservation(workerID, beadID string, assignmentID int64, worktree, baseBranch, targetBranch, epicID string, isEpicDecomp bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.assignmentReservationHeldLocked(workerID, beadID) {
		return false
	}
	w := d.workers[workerID]
	if w == nil {
		return false
	}
	w.assignmentID = assignmentID
	w.worktree = worktree
	w.baseBranch = baseBranch
	w.targetBranch = targetBranch
	w.epicID = epicID
	w.isEpicDecomp = isEpicDecomp
	return true
}

func (d *Dispatcher) releaseAssignmentReservation(workerID, beadID string) {
	d.mu.Lock()
	released := d.releaseAssignmentReservationLocked(workerID, beadID)
	d.mu.Unlock()
	if released {
		d.notifyAssignLoop()
	}
}

func (d *Dispatcher) releaseAssignmentReservationLocked(workerID, beadID string) bool {
	w, ok := d.workers[workerID]
	if !ok || w.state != protocol.WorkerReserved || w.beadID != beadID {
		return false
	}
	w.state = protocol.WorkerIdle
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.runtime = ""
	w.model = ""
	w.reasoning = ""
	w.lastProgress = d.nowFunc()
	return true
}

func (d *Dispatcher) abortAssignmentReservationLost(ctx context.Context, beadID, workerID, worktree string, removeWorktree bool, assignmentID int64) {
	if assignmentID != 0 {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
	}
	if !d.isBeadClosed(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
	}
	if removeWorktree && worktree != "" {
		_ = d.worktrees.Remove(ctx, worktree)
		d.mu.Lock()
		delete(d.worktreeByBead, beadID)
		d.mu.Unlock()
	}
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.releaseAssignmentReservationLocked(workerID, beadID)
	d.mu.Unlock()
	_ = d.logEvent(ctx, "assignment_aborted_reservation_lost", "dispatcher", beadID, workerID, "")
	d.notifyAssignLoop()
}

func (d *Dispatcher) abortAssignmentForFocusChange(ctx context.Context, beadID, workerID, worktree string, removeWorktree bool, assignmentID int64) {
	if assignmentID != 0 {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
	}
	if !d.isBeadClosed(ctx, beadID) {
		_ = d.updateBeadStatus(ctx, beadID, "open")
	}
	if removeWorktree && worktree != "" {
		_ = d.worktrees.Remove(ctx, worktree)
		d.mu.Lock()
		delete(d.worktreeByBead, beadID)
		d.mu.Unlock()
	}
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.releaseAssignmentReservationLocked(workerID, beadID)
	d.mu.Unlock()
	_ = d.logEvent(ctx, "assignment_aborted_focus_changed", "dispatcher", beadID, workerID, "")
	d.notifyAssignLoop()
}

func (d *Dispatcher) isBeadClosed(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	return err == nil && detail != nil && detail.Status == "closed"
}

func (d *Dispatcher) searchCodeInWorkdir(ctx context.Context, query string, topK int, _ string) ([]SearchResult, error) {
	chunks, err := d.codeIndex.FTS5Search(ctx, query, topK)
	if err != nil {
		return nil, fmt.Errorf("search code fts5: %w", err)
	}
	results := make([]SearchResult, 0, len(chunks))
	for i, chunk := range chunks {
		results = append(results, SearchResult{
			CodeChunk: chunk,
			Score:     1.0 / float64(i+1),
		})
	}
	return results, nil
}

// checkEpicAssignable determines whether an epic bead should proceed to assignment.
// Returns (isEpicDecomp=true, skip=false) when the epic has no children and should
// be assigned for decomposition. Returns (false, true) to skip in all other cases:
// epic with open children (not ready), epic with all children closed (auto-closed here),
// or any HasChildren/AllChildrenClosed error. For non-epic beads both values are false.
func (d *Dispatcher) checkEpicAssignable(ctx context.Context, bead protocol.Bead, workerID string) (isEpicDecomp, skip bool) {
	if !strings.EqualFold(bead.Type, "epic") {
		return false, false
	}
	hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_has_children_error", "dispatcher", bead.ID, workerID, err.Error())
		return false, true
	}
	if !hasChildren {
		return d.checkChildlessEpicAssignable(ctx, bead, workerID)
	}
	// Epic has children: auto-close if all done, otherwise skip.
	allClosed, err := d.beads.AllChildrenClosed(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_all_children_closed_error", "dispatcher", bead.ID, workerID, err.Error())
		return false, true
	}
	if allClosed {
		targetBranch := resolveEpicTargetBranch(bead.Metadata, d.cfg.DefaultBranch)
		d.completeEpicClose(ctx, bead.ID, workerID, "All children completed", targetBranch)
	}
	return false, true
}

func (d *Dispatcher) checkChildlessEpicAssignable(ctx context.Context, bead protocol.Bead, workerID string) (isEpicDecomp, skip bool) {
	detail, err := d.beads.Show(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_ac_fetch_failed", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return true, false
	}
	if detail == nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_ac_fetch_failed", "dispatcher", bead.ID, workerID,
			`{"error":"show returned nil epic"}`)
		return true, false
	}

	cmd, ok := d.parseEpicAcceptanceCmd(ctx, "epic_pre_decompose_acceptance_parse_error", bead.ID, workerID, detail.AcceptanceCriteria)
	if !ok {
		return true, false
	}
	if cmd == "" {
		return true, false
	}
	if d.acceptance == nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_unavailable", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"cmd":%q}`, cmd))
		return true, false
	}

	output, passed, runErr := d.acceptance.Run(ctx, cmd)
	if runErr != nil {
		_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_error", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"cmd":%q,"error":%q}`, cmd, runErr.Error()))
		return true, false
	}
	if !passed {
		_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_failed", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"cmd":%q,"output":%q}`, cmd, output))
		return true, false
	}

	_ = d.logEvent(ctx, "epic_pre_decompose_acceptance_passed", "dispatcher", bead.ID, workerID,
		fmt.Sprintf(`{"cmd":%q}`, cmd))
	targetBranch := resolveEpicTargetBranch(detail.Metadata, d.cfg.DefaultBranch)
	d.completeEpicClose(ctx, bead.ID, workerID, "Acceptance test passed before decomposition", targetBranch)
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

const (
	directiveLaunchWorkers      protocol.Directive = "launch-workers"
	directiveCancelWorkerLaunch protocol.Directive = "cancel-worker-launch"
)

type workerLaunchReservation struct {
	WorkerIDs []string `json:"worker_ids"`
}

// applyDirective transitions the dispatcher state machine and returns a detail
// string for the ACK response. Returns an error for invalid args (e.g. scale).
func (d *Dispatcher) applyDirective(dir protocol.Directive, args string) (string, error) {
	return d.applyDirectiveWithProvenance(dir, args, "operator", "operator_request")
}

//nolint:gocyclo // dispatcher routing function - complexity is inherent to the pattern
func (d *Dispatcher) applyDirectiveWithProvenance(dir protocol.Directive, args, source, reason string) (string, error) {
	if detail, handled, err := d.applyCapacityDirective(dir, args); handled {
		return detail, err
	}
	if detail, handled, err := d.applyOpsDirective(dir, args); handled {
		return detail, err
	}
	if detail, handled, err := d.applyEscalationDirective(dir, args); handled {
		return detail, err
	}
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
	case protocol.DirectiveHealth:
		return d.applyHealth()
	case protocol.DirectiveWorkerLogs:
		return d.applyWorkerLogs(args)
	case protocol.DirectiveStart:
		return d.applyStart()
	case protocol.DirectiveStop:
		return "", fmt.Errorf("stop directive disabled; use 'oro stop' for graceful shutdown")
	case protocol.DirectivePause:
		return d.applyPause(source, reason)
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

func (d *Dispatcher) applyEscalationDirective(dir protocol.Directive, args string) (detail string, handled bool, err error) {
	switch dir {
	case protocol.DirectivePendingEscalations:
		detail, err := d.applyPendingEscalations()
		return detail, true, err
	case protocol.DirectiveAckEscalation:
		detail, err := d.applyAckEscalation(args)
		return detail, true, err
	default:
		return "", false, nil
	}
}

func (d *Dispatcher) applyCapacityDirective(dir protocol.Directive, args string) (detail string, handled bool, err error) {
	switch dir {
	case protocol.DirectiveMaxWorkers:
		detail, err := d.applyMaxWorkersDirective(args)
		return detail, true, err
	case directiveLaunchWorkers:
		detail, err := d.applyLaunchWorkers(args)
		return detail, true, err
	case directiveCancelWorkerLaunch:
		detail, err := d.applyCancelWorkerLaunch(args)
		return detail, true, err
	default:
		return "", false, nil
	}
}

// applyStart transitions the dispatcher to running state.
func (d *Dispatcher) applyStart() (string, error) {
	d.setState(StateRunning)
	return "started", nil
}

// directiveProvenance normalizes legacy directives as explicit operator actions.
func directiveProvenance(payload *protocol.DirectivePayload) (source, reason string) {
	source = payload.Source
	if source == "" {
		source = "operator"
	}
	reason = payload.Reason
	if reason == "" {
		reason = "operator_request"
	}
	return source, reason
}

// applyPause transitions the dispatcher to paused state with its provenance.
func (d *Dispatcher) applyPause(source, reason string) (string, error) {
	d.mu.Lock()
	d.pauseSource = source
	d.pauseReason = reason
	d.mu.Unlock()
	d.setState(StatePaused)
	return "paused", nil
}

// applyResume transitions the dispatcher from paused to running.
func (d *Dispatcher) applyResume() (string, error) {
	if d.GetState() == StateRunning {
		return "already running", nil
	}
	d.setState(StateRunning)
	d.mu.Lock()
	d.pauseSource = ""
	d.pauseReason = ""
	d.mu.Unlock()
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
	epic, immediate, err := parseFocusArgs(args)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.focusedEpic = epic
	d.focusVersion++
	d.mu.Unlock()
	if d.GetState() != StateRunning {
		d.setState(StateRunning)
	}
	if epic == "" {
		return "focus cleared", nil
	}
	if !immediate {
		return fmt.Sprintf("focused on %s", epic), nil
	}
	preempted := d.preemptWorkersOutsideFocus(context.Background(), epic)
	return fmt.Sprintf("focused on %s; preempted %d non-focused %s", epic, preempted, pluralize(preempted, "worker", "workers")), nil
}

func parseFocusArgs(args string) (epic string, immediate bool, err error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return "", false, nil
	}
	for _, field := range fields {
		switch field {
		case "--immediate", "-i":
			immediate = true
		default:
			if strings.HasPrefix(field, "-") {
				return "", false, fmt.Errorf("unknown focus option %q", field)
			}
			if epic != "" {
				return "", false, fmt.Errorf("focus accepts one epic ID")
			}
			epic = field
		}
	}
	if immediate && epic == "" {
		return "", false, fmt.Errorf("epic ID required with --immediate")
	}
	return epic, immediate, nil
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func (d *Dispatcher) preemptWorkersOutsideFocus(ctx context.Context, focusedEpic string) int {
	type candidate struct {
		workerID string
		beadID   string
	}
	d.mu.Lock()
	candidates := make([]candidate, 0, len(d.workers))
	for workerID, worker := range d.workers {
		if worker.beadID == "" || !preemptableWorkerState(worker.state) {
			continue
		}
		candidates = append(candidates, candidate{workerID: workerID, beadID: worker.beadID})
	}
	d.mu.Unlock()

	parentCache := make(map[string]string)
	preempted := 0
	for _, candidate := range candidates {
		if d.beadIsFocusedDescendant(ctx, candidate.beadID, focusedEpic, parentCache) {
			continue
		}
		if d.restartWorkerIfStillOnBead(ctx, candidate.workerID, candidate.beadID, "focus --immediate") {
			preempted++
		}
	}
	if preempted > 0 {
		d.notifyAssignLoop()
	}
	return preempted
}

func preemptableWorkerState(state protocol.WorkerState) bool {
	return state == protocol.WorkerBusy || state == protocol.WorkerReviewing
}

func (d *Dispatcher) beadIsFocusedDescendant(ctx context.Context, beadID, focusedEpic string, parentCache map[string]string) bool {
	if beadID == focusedEpic {
		return true
	}
	if cached, ok := parentCache[beadID]; ok {
		return d.isFocusedDescendant(ctx, cached, focusedEpic, parentCache)
	}
	bead, err := d.beads.Show(ctx, beadID)
	if err != nil || bead == nil {
		parentCache[beadID] = ""
		return false
	}
	parentCache[beadID] = bead.Epic
	return d.isFocusedDescendant(ctx, bead.Epic, focusedEpic, parentCache)
}

func (d *Dispatcher) restartWorkerIfStillOnBead(ctx context.Context, workerID, beadID, reason string) bool {
	d.mu.Lock()
	worker, ok := d.workers[workerID]
	if !ok || worker.beadID != beadID || !preemptableWorkerState(worker.state) {
		d.mu.Unlock()
		return false
	}
	assignmentID := worker.assignmentID
	wasManaged := worker.managed
	_ = worker.conn.Close()
	delete(d.workers, workerID)
	if wasManaged {
		d.pendingManagedIDs[workerID] = true
		d.pendingManagedSince[workerID] = d.nowFunc()
	}
	procMgr := d.procMgr
	d.mu.Unlock()

	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "focus_immediate_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	d.clearBeadTracking(beadID)
	_ = d.completeAssignment(ctx, assignmentID, beadID)
	_ = d.logEvent(ctx, "worker_restarted", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"reason":%q}`, reason))

	if procMgr != nil {
		if _, err := procMgr.Spawn(workerID); err != nil {
			_ = d.logEvent(ctx, "worker_spawn_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	return true
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
			Managed:           w.managed,
			SpawnFor:          w.spawnFor,
			TargetBeadID:      w.targetBeadID,
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

	// Count ready beads that are not assigned. Childless epics are executable
	// decomposition work, so status must not blanket-filter epic types here.
	queueDepth := 0
	for _, bead := range readyBeads {
		if !assignedBeadIDs[bead.ID] {
			queueDepth++
		}
	}
	return queueDepth
}

func (d *Dispatcher) statusQueueBeads(ctx context.Context, readyBeads []protocol.Bead) []protocol.Bead {
	if len(readyBeads) == 0 {
		return readyBeads
	}
	queueBeads := make([]protocol.Bead, 0, len(readyBeads))
	for _, bead := range readyBeads {
		if strings.EqualFold(bead.Type, "epic") {
			hasChildren, err := d.beads.HasChildren(ctx, bead.ID)
			if err != nil || hasChildren {
				continue
			}
		}
		queueBeads = append(queueBeads, bead)
	}
	return queueBeads
}

// qgFailureStatus queries the state DB and returns a snapshot of open QG
// failure incidents. On DB error it logs to stderr and returns a zero value.
func (d *Dispatcher) qgFailureStatus(ctx context.Context) QGFailureStatus {
	if d.db == nil {
		return QGFailureStatus{}
	}

	openRows, err := d.openQGIncidentRows(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: open incidents: %v\n", err)
		return QGFailureStatus{}
	}
	openRows = d.filterClosedQGIncidentBeads(ctx, openRows)

	var occ30m int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM qg_failure_occurrences
 WHERE created_at >= datetime('now', '-30 minutes')`,
	).Scan(&occ30m); err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: count occurrences 30m: %v\n", err)
		return QGFailureStatus{}
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT id, fingerprint FROM qg_failure_incidents
 WHERE status = 'open'
 ORDER BY occurrence_count DESC
 LIMIT 5`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: top fingerprints: %v\n", err)
		return QGFailureStatus{}
	}
	defer func() { _ = rows.Close() }()

	var fps []string
	for rows.Next() {
		var row qgIncidentStatusRow
		if err := rows.Scan(&row.ID, &row.Fingerprint); err != nil {
			fmt.Fprintf(os.Stderr, "qgFailureStatus: scan fingerprint: %v\n", err)
			return QGFailureStatus{}
		}
		if d.qgIncidentBeadClosed(ctx, row.ID) {
			_ = d.closeQGIncidentRow(ctx, row.ID)
			continue
		}
		fps = append(fps, row.Fingerprint)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: rows error: %v\n", err)
		return QGFailureStatus{}
	}
	recentFingerprints, err := factoryhealth.LoadRecentQGFingerprints(ctx, d.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qgFailureStatus: recent fingerprints: %v\n", err)
	}

	return QGFailureStatus{
		OpenIncidents:      len(openRows),
		Occurrences30m:     occ30m,
		TopFingerprints:    fps,
		RecentFingerprints: recentFingerprints,
	}
}

func (d *Dispatcher) openQGIncidentRows(ctx context.Context) ([]qgIncidentStatusRow, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, fingerprint FROM qg_failure_incidents
 WHERE status = 'open'
 ORDER BY occurrence_count DESC`)
	if err != nil {
		return nil, fmt.Errorf("query open qg incidents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []qgIncidentStatusRow
	for rows.Next() {
		var row qgIncidentStatusRow
		if err := rows.Scan(&row.ID, &row.Fingerprint); err != nil {
			return nil, fmt.Errorf("scan open qg incident: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open qg incidents: %w", err)
	}
	return out, nil
}

func (d *Dispatcher) filterClosedQGIncidentBeads(ctx context.Context, rows []qgIncidentStatusRow) []qgIncidentStatusRow {
	if len(rows) == 0 {
		return rows
	}
	open := rows[:0]
	for _, row := range rows {
		if d.qgIncidentBeadClosed(ctx, row.ID) {
			_ = d.closeQGIncidentRow(ctx, row.ID)
			continue
		}
		open = append(open, row)
	}
	return open
}

func (d *Dispatcher) qgIncidentBeadClosed(ctx context.Context, incidentID int64) bool {
	if d.beads == nil || incidentID <= 0 {
		return false
	}
	detail, err := d.beads.Show(ctx, fmt.Sprintf("oro-qg-incident-%d", incidentID))
	if err != nil {
		var notFound *protocol.BeadNotFoundError
		return errors.As(err, &notFound)
	}
	if detail == nil {
		return true
	}
	if detail.Status == "closed" {
		return true
	}
	return false
}

func (d *Dispatcher) closeQGIncidentRow(ctx context.Context, incidentID int64) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE qg_failure_incidents
   SET status = 'closed'
 WHERE id = ? AND status = 'open'`, incidentID)
	if err != nil {
		return fmt.Errorf("close qg incident row: %w", err)
	}
	return nil
}

//nolint:funlen // Status JSON intentionally assembles one wire contract in field order.
func (d *Dispatcher) buildStatusJSON() string {
	now := d.nowFunc()

	// Fetch ready beads to determine which attempt counts are valid.
	ctx := context.Background()
	readyBeads, err := d.beads.Ready(ctx)
	if err != nil {
		readyBeads = nil // Continue with empty ready list on error.
	}
	readyBeads = d.statusQueueBeads(ctx, readyBeads)

	qgStatus := d.qgFailureStatus(ctx)
	openRecoveryQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, d.db)
	if err != nil {
		_ = d.logEvent(ctx, "status_recovery_quarantine_load_failed", "dispatcher", "", "", err.Error())
	}

	d.mu.Lock()
	workers, assignments, activeCount, idleCount := d.snapshotWorkers(now)

	// Calculate live queue depth (ready beads minus assigned beads).
	queueDepth := calculateLiveQueueDepth(readyBeads, d.workers)
	managedCount, unmanagedCount := workerRoleCounts(d.workers)

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
	attemptCounts := filterAttemptCounts(d.attemptCounts, activeBeadIDs)

	resp := statusResponse{
		State:                        string(d.state),
		PID:                          os.Getpid(),
		WorkerCount:                  len(d.workers),
		QueueDepth:                   queueDepth,
		Assignments:                  assignments,
		FocusedEpic:                  d.focusedEpic,
		Workers:                      workers,
		ActiveCount:                  activeCount,
		IdleCount:                    idleCount,
		TargetCount:                  d.targetWorkers,
		MaxWorkers:                   d.cfg.MaxWorkers,
		ManagedCount:                 managedCount,
		UnmanagedCount:               unmanagedCount,
		PendingWorkerCount:           len(d.pendingManagedIDs) + len(d.pendingExternalIDs),
		UptimeSeconds:                now.Sub(d.startTime).Seconds(),
		PendingHandoffCount:          len(d.pendingHandoffs),
		AttemptCounts:                attemptCounts,
		ProgressTimeoutSecs:          d.cfg.ProgressTimeout.Seconds(),
		QGFailureIncidentsOpen:       qgStatus.OpenIncidents,
		QGFailureOccurrences30m:      qgStatus.Occurrences30m,
		QGFailureTopFingerprints:     qgStatus.TopFingerprints,
		AssignmentFrozenByQuarantine: d.assignmentFrozenByQuarantine,
		BlockingRecoveryQuarantines:  d.blockingRecoveryQuarantines,
		AssignmentFreezeReason:       d.assignmentFreezeReason,
	}
	state := string(d.state)
	targetWorkers := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	pendingWorkerCount := len(d.pendingManagedIDs) + len(d.pendingExternalIDs)
	pendingHandoffCount := len(d.pendingHandoffs)
	progressTimeoutSecs := d.cfg.ProgressTimeout.Seconds()
	heartbeatTimeoutSecs := d.cfg.HeartbeatTimeout.Seconds()
	assignmentFrozenByQuarantine := d.assignmentFrozenByQuarantine
	blockingRecoveryQuarantines := d.blockingRecoveryQuarantines
	assignmentFreezeReason := d.assignmentFreezeReason
	d.mu.Unlock()

	health := d.evaluateFactoryHealth(ctx, now, factoryHealthInput{
		daemonRunning:                true,
		daemonPID:                    os.Getpid(),
		dispatcherState:              state,
		workers:                      workers,
		queueDepth:                   queueDepth,
		targetWorkers:                targetWorkers,
		maxWorkers:                   maxWorkers,
		pendingWorkerCount:           pendingWorkerCount,
		pendingHandoffCount:          pendingHandoffCount,
		qgStatus:                     qgStatus,
		openRecoveryQuarantines:      openRecoveryQuarantines,
		assignmentFrozenByQuarantine: assignmentFrozenByQuarantine,
		blockingRecoveryQuarantines:  blockingRecoveryQuarantines,
		assignmentFreezeReason:       assignmentFreezeReason,
		progressTimeoutSecs:          progressTimeoutSecs,
		heartbeatTimeoutSecs:         heartbeatTimeoutSecs,
	})
	resp.Health = &health

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}

func workerRoleCounts(workers map[string]*trackedWorker) (managedCount, unmanagedCount int) {
	for _, w := range workers {
		if w.managed {
			managedCount++
		} else {
			unmanagedCount++
		}
	}
	return managedCount, unmanagedCount
}

func filterAttemptCounts(attemptCounts map[string]int, activeBeadIDs map[string]bool) map[string]int {
	if len(attemptCounts) == 0 {
		return nil
	}
	filtered := make(map[string]int)
	for beadID, count := range attemptCounts {
		if activeBeadIDs[beadID] {
			filtered[beadID] = count
		}
	}
	return filtered
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
	d.explicitScaleTarget = true
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
	var killPending []string
	procMgr := d.procMgr
	if n > 0 {
		live := d.liveWorkerCountLocked()
		for id := range d.pendingManagedIDs {
			if live <= n {
				break
			}
			killPending = append(killPending, id)
			delete(d.pendingManagedIDs, id)
			delete(d.pendingManagedSince, id)
			delete(d.pendingWorkerTargets, id)
			delete(d.pendingSpawnForWorkers, id)
			live--
		}
	}
	d.mu.Unlock()

	if procMgr != nil {
		for _, id := range killPending {
			_ = procMgr.Kill(id)
		}
	}
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
	assignmentID := w.assignmentID
	managed := w.managed
	spawnFor := w.spawnFor

	if spawnFor {
		sendShutdownWithoutBuffering(w)
		if current := d.workers[workerID]; current == w {
			w.markShuttingDownWithoutAssignment()
		}
	} else {
		// Tell the worker process to exit before removing dispatcher bookkeeping.
		// Closing the connection alone makes `oro worker` treat it as a transient
		// connection drop and reconnect while the dispatcher is still alive.
		sendShutdownWithoutBuffering(w)
		delete(d.workers, workerID)
	}

	// Decrement target count only for managed workers; external workers are
	// not counted against targetWorkers. Spawn-for workers are one-shot
	// managed processes and are also outside targetWorkers.
	if managed && !spawnFor && d.targetWorkers > 0 {
		d.targetWorkers--
	}
	d.mu.Unlock()

	// DO NOT remove the worktree here - preserve it for respawn reuse (oro-1eo8).
	// The worktree will be reused if the same bead is reassigned, or cleaned up
	// on successful completion or explicit shutdown.

	// Reset bead to open so it can be reassigned.
	if beadID != "" {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "kill_worker_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		d.clearBeadTracking(beadID)
		_ = d.completeAssignment(ctx, assignmentID, beadID)
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

	newID := fmt.Sprintf("worker-spawnfor-%d", d.nowFunc().UnixNano())
	d.mu.Lock()
	for _, w := range d.workers {
		if w.beadID == beadID {
			workerID := w.id
			d.mu.Unlock()
			return "", fmt.Errorf("bead %s already assigned to %s", beadID, workerID)
		}
	}
	if d.procMgr == nil {
		d.mu.Unlock()
		return "", fmt.Errorf("no process manager configured")
	}
	totalWorkers := d.liveWorkerCountLocked()
	if d.cfg.MaxWorkers > 0 && totalWorkers >= d.cfg.MaxWorkers {
		maxWorkers := d.cfg.MaxWorkers
		d.mu.Unlock()
		return "", fmt.Errorf("max workers reached: total=%d MaxWorkers=%d", totalWorkers, maxWorkers)
	}
	procMgr := d.procMgr
	d.priorityBeads[beadID] = true
	d.pendingManagedIDs[newID] = true
	d.pendingManagedSince[newID] = d.nowFunc()
	d.pendingWorkerTargets[newID] = beadID
	d.pendingSpawnForWorkers[newID] = true
	d.mu.Unlock()

	if _, err := procMgr.Spawn(newID); err != nil {
		d.mu.Lock()
		delete(d.priorityBeads, beadID)
		delete(d.pendingManagedIDs, newID)
		delete(d.pendingManagedSince, newID)
		delete(d.pendingWorkerTargets, newID)
		delete(d.pendingSpawnForWorkers, newID)
		d.mu.Unlock()
		return "", fmt.Errorf("spawn failed: %w", err)
	}

	_ = d.logEvent(context.Background(), "spawn_for", "dispatcher", beadID, newID, "")
	return fmt.Sprintf("spawned worker %s for bead %s", newID, beadID), nil
}

func (d *Dispatcher) parseWorkerLaunchReservation(args string) (workerLaunchReservation, error) {
	var req workerLaunchReservation
	if err := json.Unmarshal([]byte(args), &req); err != nil {
		return req, fmt.Errorf("invalid worker launch args: %w", err)
	}
	if len(req.WorkerIDs) == 0 {
		return req, fmt.Errorf("worker IDs required")
	}
	seen := make(map[string]bool, len(req.WorkerIDs))
	for _, id := range req.WorkerIDs {
		if strings.TrimSpace(id) == "" {
			return req, fmt.Errorf("worker ID required")
		}
		if seen[id] {
			return req, fmt.Errorf("duplicate worker ID %q", id)
		}
		seen[id] = true
	}
	return req, nil
}

func (d *Dispatcher) applyLaunchWorkers(args string) (string, error) {
	req, err := d.parseWorkerLaunchReservation(args)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	d.cleanupStalePendingManagedLocked(d.nowFunc())
	for _, id := range req.WorkerIDs {
		if _, exists := d.workers[id]; exists {
			d.mu.Unlock()
			return "", fmt.Errorf("worker %s already connected", id)
		}
		if d.pendingManagedIDs[id] || d.pendingExternalIDs[id] {
			d.mu.Unlock()
			return "", fmt.Errorf("worker %s already pending", id)
		}
	}
	totalWorkers := d.liveWorkerCountLocked()
	if d.cfg.MaxWorkers > 0 {
		available := d.cfg.MaxWorkers - totalWorkers
		if len(req.WorkerIDs) > available {
			maxWorkers := d.cfg.MaxWorkers
			d.mu.Unlock()
			return "", fmt.Errorf("max workers reached: requested=%d available=%d total=%d MaxWorkers=%d",
				len(req.WorkerIDs), available, totalWorkers, maxWorkers)
		}
	}
	now := d.nowFunc()
	for _, id := range req.WorkerIDs {
		d.pendingExternalIDs[id] = true
		d.pendingExternalSince[id] = now
	}
	d.mu.Unlock()

	return fmt.Sprintf("reserved %d workers", len(req.WorkerIDs)), nil
}

func (d *Dispatcher) applyCancelWorkerLaunch(args string) (string, error) {
	req, err := d.parseWorkerLaunchReservation(args)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	cancelled := 0
	for _, id := range req.WorkerIDs {
		if d.pendingExternalIDs[id] {
			delete(d.pendingExternalIDs, id)
			delete(d.pendingExternalSince, id)
			cancelled++
		}
	}
	d.mu.Unlock()

	return fmt.Sprintf("cancelled %d worker reservations", cancelled), nil
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

	// Capture bead ID, assignment ID, and managed flag before removing worker.
	beadID := w.beadID
	assignmentID := w.assignmentID
	wasManaged := w.managed

	// Close connection and remove worker from pool
	_ = w.conn.Close()
	delete(d.workers, workerID)

	// If the original worker was managed, record the ID so registerWorker
	// sets managed=true when the respawned process connects.
	if wasManaged {
		d.pendingManagedIDs[workerID] = true
		d.pendingManagedSince[workerID] = d.nowFunc()
	}

	// Target count remains unchanged (unlike kill-worker)
	procMgr := d.procMgr
	d.mu.Unlock()

	killErr := d.killManagedWorkerForRestart(ctx, procMgr, workerID, beadID, wasManaged)
	completeErr := d.completeRestartAssignment(ctx, beadID, assignmentID, workerID)
	if completeErr != nil || killErr != nil {
		if wasManaged {
			d.mu.Lock()
			delete(d.pendingManagedIDs, workerID)
			delete(d.pendingManagedSince, workerID)
			d.mu.Unlock()
		}
		if completeErr != nil {
			_ = d.logEvent(ctx, "restart_worker_assignment_completion_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, completeErr.Error()))
			return "", completeErr
		}
		return "", killErr
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
	if beadID != "" {
		_ = d.logEvent(ctx, "worker_restarted", "dispatcher", beadID, workerID,
			`{"reason":"restart-worker directive"}`)
	}

	return fmt.Sprintf("worker %s restarted", workerID), nil
}

func (d *Dispatcher) killManagedWorkerForRestart(ctx context.Context, procMgr ProcessManager, workerID, beadID string, wasManaged bool) error {
	if !wasManaged || procMgr == nil {
		return nil
	}
	if err := procMgr.Kill(workerID); err != nil {
		_ = d.logEvent(ctx, "restart_worker_kill_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return fmt.Errorf("kill managed worker for restart: %w", err)
	}
	return nil
}

// completeRestartAssignment makes a restarted worker's assignment available
// again. Tracking is cleared only after completion succeeds so failed cleanup
// remains visible for recovery instead of stranding an active assignment.
func (d *Dispatcher) completeRestartAssignment(ctx context.Context, beadID string, assignmentID int64, workerID string) error {
	if beadID == "" {
		return nil
	}
	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "restart_worker_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
		return fmt.Errorf("complete restart assignment: %w", err)
	}
	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "restart_worker_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
	}
	d.clearBeadTracking(beadID)
	_ = d.logEvent(ctx, "restart_worker_assignment_recovered", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
	d.notifyAssignLoop()
	return nil
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

	// Mark worker as preempting; save previous state for rollback on send failure.
	prevState := w.state
	w.state = protocol.WorkerPreempting

	// Send PREEMPT message through sendToWorker (handles disconnected workers).
	msg := protocol.Message{
		Type: protocol.MsgPreempt,
	}
	if err := d.sendToWorker(w, msg); err != nil {
		// Reset state: preempt message was not delivered.
		w.state = prevState
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
	if d.hasPendingSpawnForLocked() {
		d.mu.Unlock()
		return
	}
	currentTarget := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	explicitScaleTarget := d.explicitScaleTarget
	liveManagedCount := d.liveManagedWorkerCountLocked()
	if explicitScaleTarget && currentTarget > 0 && liveManagedCount <= currentTarget {
		d.explicitScaleTarget = false
		explicitScaleTarget = false
	}
	d.mu.Unlock()

	if explicitScaleTarget {
		return
	}

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
	d.cleanupStalePendingManagedLocked(d.nowFunc())
	target := d.targetWorkers
	// Count both connected managed workers AND pending spawns (oro-ovpc).
	// Without counting pending, concurrent reconcileScale calls both see
	// managedCount=0 and spawn duplicates before workers connect.
	managedCount := d.managedWorkerCountLocked()
	// Guard: cap at 2*target using only managed workers (connected + pending +
	// exits) to prevent runaway crash-respawn loops (oro-135n, oro-kdne).
	// Unmanaged (orphaned) workers are excluded so they cannot block managed
	// worker spawning.
	managedExits := d.unexpectedManagedExits
	totalWorkers := d.activeWorkerCountLocked()
	totalLiveWorkers := d.liveWorkerCountLocked()
	maxWorkers := d.cfg.MaxWorkers
	hasPendingSpawnFor := d.hasPendingSpawnForLocked()
	d.mu.Unlock()

	desiredManaged := target
	if maxWorkers > 0 && totalWorkers > maxWorkers {
		capDesired := managedCount - (totalWorkers - maxWorkers)
		if capDesired < desiredManaged {
			desiredManaged = capDesired
		}
	}
	if desiredManaged < 0 {
		desiredManaged = 0
	}

	switch {
	case managedCount > desiredManaged:
		return d.scaleDown(desiredManaged, managedCount)
	case managedCount < target:
		if hasPendingSpawnFor {
			return fmt.Sprintf("target=%d, managed=%d, pending spawn-for active, skipping scaleUp", target, managedCount)
		}
		if managedCount+managedExits >= 2*target {
			return fmt.Sprintf("target=%d, managed=%d, exits=%d, managed+exits %d >= 2*target %d — cap reached, skipping scaleUp",
				target, managedCount, managedExits, managedCount+managedExits, 2*target)
		}
		capacity := target - managedCount
		if maxWorkers > 0 {
			capacity = maxWorkers - totalLiveWorkers
		}
		if capacity <= 0 {
			return fmt.Sprintf("target=%d, managed=%d, total=%d, MaxWorkers=%d — total cap reached, skipping scaleUp",
				target, managedCount, totalLiveWorkers, maxWorkers)
		}
		return d.scaleUp(target, managedCount, capacity)
	default:
		return ""
	}
}

func (d *Dispatcher) cleanupStalePendingManagedLocked(now time.Time) {
	if d.cfg.HeartbeatTimeout <= 0 {
		return
	}
	for id := range d.pendingManagedSince {
		if !d.pendingManagedIDs[id] {
			delete(d.pendingManagedSince, id)
			continue
		}
		if now.Sub(d.pendingManagedSince[id]) <= d.cfg.HeartbeatTimeout {
			continue
		}
		spawnFor := d.pendingSpawnForWorkers[id]
		delete(d.pendingManagedIDs, id)
		delete(d.pendingManagedSince, id)
		delete(d.pendingWorkerTargets, id)
		delete(d.pendingSpawnForWorkers, id)
		if !spawnFor {
			d.unexpectedManagedExits++
		}
	}
	for id, since := range d.pendingExternalSince {
		if !d.pendingExternalIDs[id] {
			delete(d.pendingExternalSince, id)
			continue
		}
		if now.Sub(since) <= d.cfg.HeartbeatTimeout {
			continue
		}
		delete(d.pendingExternalIDs, id)
		delete(d.pendingExternalSince, id)
	}
}

func (d *Dispatcher) managedWorkerCountLocked() int {
	count := 0
	for id := range d.pendingManagedIDs {
		if !d.pendingSpawnForWorkers[id] {
			count++
		}
	}
	for _, w := range d.workers {
		if w.managed && !w.spawnFor && w.state != protocol.WorkerShuttingDown {
			count++
		}
	}
	return count
}

func (d *Dispatcher) liveManagedWorkerCountLocked() int {
	count := 0
	for id := range d.pendingManagedIDs {
		if !d.pendingSpawnForWorkers[id] {
			count++
		}
	}
	for _, w := range d.workers {
		if w.managed && !w.spawnFor {
			count++
		}
	}
	return count
}

func (d *Dispatcher) activeWorkerCountLocked() int {
	count := 0
	for _, w := range d.workers {
		if w.state != protocol.WorkerShuttingDown {
			count++
		}
	}
	for id := range d.pendingManagedIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	for id := range d.pendingExternalIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	return count
}

func (d *Dispatcher) liveWorkerCountLocked() int {
	count := len(d.workers)
	for id := range d.pendingManagedIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	for id := range d.pendingExternalIDs {
		if _, connected := d.workers[id]; !connected {
			count++
		}
	}
	return count
}

// scaleUp spawns (target - connected) new worker processes.
func (d *Dispatcher) scaleUp(target, connected, capacity int) string {
	toSpawn := target - connected
	if toSpawn > capacity {
		toSpawn = capacity
	}
	if d.procMgr == nil {
		return fmt.Sprintf("target=%d, need %d workers but no ProcessManager configured", target, toSpawn)
	}

	spawned := 0
	for i := 0; i < toSpawn; i++ {
		id := fmt.Sprintf("worker-%d-%d", d.nowFunc().UnixNano(), i)
		d.mu.Lock()
		if d.cfg.MaxWorkers > 0 && d.liveWorkerCountLocked() >= d.cfg.MaxWorkers {
			d.mu.Unlock()
			break
		}
		d.pendingManagedIDs[id] = true
		d.pendingManagedSince[id] = d.nowFunc()
		d.mu.Unlock()
		if _, err := d.procMgr.Spawn(id); err != nil {
			d.mu.Lock()
			delete(d.pendingManagedIDs, id)
			delete(d.pendingManagedSince, id)
			d.mu.Unlock()
			continue
		}
		spawned++
	}
	return fmt.Sprintf("target=%d, spawning %d", target, spawned)
}

// scaleDown initiates graceful shutdown for excess managed workers, preferring
// idle workers first, then newest busy workers. Unmanaged workers are skipped.
func (d *Dispatcher) scaleDown(target, connected int) string {
	toRemove := connected - target

	d.mu.Lock()
	killPending := d.removePendingManagedForScaleDownLocked(&toRemove)
	idle, busy := d.managedScaleDownCandidatesLocked(toRemove)
	procMgr := d.procMgr
	d.mu.Unlock()

	// Build removal list: idle first, then busy (newest = end of slice).
	var victims []string
	victims = append(victims, idle...)
	victims = append(victims, busy...)

	// Trim to the number we need to remove.
	if len(victims) > toRemove {
		victims = victims[:toRemove]
	}

	if procMgr != nil {
		for _, id := range killPending {
			_ = procMgr.Kill(id)
		}
	}
	for _, id := range victims {
		d.gracefulShutdownWorker(id, d.cfg.ShutdownTimeout, shutdownReasonScaleDown)
	}

	return fmt.Sprintf("target=%d, shutting down %d", target, len(killPending)+len(victims))
}

func (d *Dispatcher) removePendingManagedForScaleDownLocked(toRemove *int) []string {
	var killPending []string
	for id := range d.pendingManagedIDs {
		if *toRemove == 0 {
			break
		}
		if d.pendingSpawnForWorkers[id] {
			continue
		}
		killPending = append(killPending, id)
		delete(d.pendingManagedIDs, id)
		delete(d.pendingManagedSince, id)
		delete(d.pendingWorkerTargets, id)
		delete(d.pendingSpawnForWorkers, id)
		(*toRemove)--
	}
	return killPending
}

func (d *Dispatcher) managedScaleDownCandidatesLocked(toRemove int) (idle, busy []string) {
	if toRemove <= 0 {
		return nil, nil
	}
	for id, w := range d.workers {
		if !isManagedScaleDownCandidate(w) {
			continue
		}
		if w.state == protocol.WorkerIdle {
			idle = append(idle, id)
		} else {
			busy = append(busy, id)
		}
	}
	return idle, busy
}

func isManagedScaleDownCandidate(w *trackedWorker) bool {
	return w.managed && !w.spawnFor && w.state != protocol.WorkerShuttingDown
}

// heartbeatLoop, checkHeartbeats → worker_pool.go

// --- SQLite helpers ---

// recordWorkerProgress persists a worker event that is useful for auditing
// assignment activity. It deliberately does not update lastProgress: timeout
// state is driven only by real worker protocol transitions.
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
func (d *Dispatcher) escalate(ctx context.Context, msg, beadID, workerID string) {
	d.escalateWithOneShot(ctx, msg, beadID, workerID, true)
}

// escalateWithoutOneShot records and delivers an escalation without starting a
// corrective ops process. It is used when a review timeout has just cancelled
// the bead's active review, so cleanup has a stable no-active-ops boundary.
func (d *Dispatcher) escalateWithoutOneShot(ctx context.Context, msg, beadID, workerID string) {
	d.escalateWithOneShot(ctx, msg, beadID, workerID, false)
}

func (d *Dispatcher) escalateWithOneShot(ctx context.Context, msg, beadID, workerID string, allowOneShot bool) {
	// Extract escalation type for database storage (separate from one-shot determination).
	dbEscType := extractEscalationType(msg)

	oneShot := ""
	if allowOneShot && d.ops != nil {
		oneShot = parseEscalationType(msg)
	}
	if protocol.EscalationType(oneShot) == protocol.EscOversizedBead {
		if d.routeNewRoutableEscalation(ctx, protocol.EscalationType(oneShot), beadID, workerID, msg) {
			return
		}
	}

	// Persist escalation to SQLite before attempting tmux delivery.
	escalationID := d.insertEscalation(ctx, dbEscType, beadID, workerID, msg)

	if protocol.EscalationType(oneShot) == protocol.EscOversizedBead {
		d.spawnEscalationOneShot(ctx, escalationID, oneShot, beadID, workerID, msg)
		return
	}

	if err := d.escalator.Escalate(ctx, msg); err != nil {
		if isInformationalEscalation(protocol.EscalationType(dbEscType)) {
			_ = d.logEvent(ctx, "notification_skipped", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q,"message":%q,"type":%q}`, err.Error(), msg, dbEscType))
			return
		}
		_ = d.logEvent(ctx, "escalation_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q,"message":%q}`, err.Error(), msg))
	}

	// Spawn one-shot manager agent for actionable escalation types.
	// Only spawn for types with a one-shot playbook (use parseEscalationType, not extractEscalationType).
	if oneShot != "" {
		d.spawnEscalationOneShot(ctx, escalationID, oneShot, beadID, workerID, msg)
	}
}

func (d *Dispatcher) insertEscalation(ctx context.Context, escType, beadID, workerID, msg string) int64 {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO escalations (type, bead_id, worker_id, message) VALUES (?, ?, ?, ?)`,
		escType, beadID, workerID, msg)
	if err != nil {
		return 0
	}
	escalationID, _ := res.LastInsertId()
	return escalationID
}

func (d *Dispatcher) routeNewRoutableEscalation(ctx context.Context, escType protocol.EscalationType, beadID, workerID, msg string) bool {
	if d.ops == nil || !isRoutableEscalationType(escType) || beadID == "" {
		return false
	}

	rec, wasCreated, err := d.createRoutableOpsRun(ctx, 0, escType, beadID, workerID, msg)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_route_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"error":%q}`, escType, err.Error()))
		return false
	}
	if !wasCreated {
		d.logOpsRunBlockedAssignment(ctx, rec, 0, escType, beadID, workerID)
		return true
	}

	escalationID := d.insertEscalation(ctx, string(escType), beadID, workerID, msg)
	if escalationID > 0 {
		if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET escalation_id=? WHERE id=?`, escalationID, rec.ID); err != nil {
			_ = d.logEvent(ctx, "ops_run_route_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"error":%q}`, rec.ID, escType, err.Error()))
		}
	}

	d.spawnEscalationOneShot(ctx, escalationID, string(escType), beadID, workerID, msg)
	return true
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
			case <-d.shutdownCh:
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
	if err := d.routePendingRoutableEscalations(ctx); err != nil {
		_ = d.logEvent(ctx, "pending_escalation_route_failed", "dispatcher", "", "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
	}

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
		if d.shouldSkipPendingEscalationRetry(esc.escType) {
			continue
		}

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

func (d *Dispatcher) shouldSkipPendingEscalationRetry(escType string) bool {
	return !factoryhealth.IsKnownEscalationType(escType) ||
		(protocol.EscalationType(escType) == protocol.EscOversizedBead && d.ops != nil)
}

func (d *Dispatcher) routePendingRoutableEscalations(ctx context.Context) error {
	if d.ops == nil {
		return nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, type, bead_id, worker_id, message
		 FROM escalations
		 WHERE status = 'pending' AND retry_count < 5
		 ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query pending routable escalations: %w", err)
	}
	defer rows.Close()

	type pendingRoutableEscalation struct {
		id       int64
		escType  protocol.EscalationType
		beadID   string
		workerID string
		msg      string
	}
	var pending []pendingRoutableEscalation
	for rows.Next() {
		var (
			id       int64
			escType  string
			beadID   sql.NullString
			workerID sql.NullString
			msg      string
		)
		if err := rows.Scan(&id, &escType, &beadID, &workerID, &msg); err != nil {
			return fmt.Errorf("scan pending routable escalation: %w", err)
		}
		if !isRoutableEscalationType(protocol.EscalationType(escType)) || !beadID.Valid || beadID.String == "" {
			continue
		}
		pending = append(pending, pendingRoutableEscalation{
			id:       id,
			escType:  protocol.EscalationType(escType),
			beadID:   beadID.String,
			workerID: workerID.String,
			msg:      msg,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pending routable escalations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close pending routable escalations: %w", err)
	}

	for _, esc := range pending {
		if !d.shouldRetryEscalation(ctx, string(esc.escType), esc.beadID) {
			d.ackEscalation(ctx, esc.id, esc.beadID, esc.workerID)
			continue
		}
		if err := d.routeExistingRoutableEscalation(ctx, esc.id, esc.escType, esc.beadID, esc.workerID, esc.msg); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) routeExistingRoutableEscalation(ctx context.Context, escalationID int64, escType protocol.EscalationType, beadID, workerID, msg string) error {
	rec, wasCreated, err := d.createRoutableOpsRun(ctx, escalationID, escType, beadID, workerID, msg)
	if err != nil {
		return err
	}
	if !wasCreated {
		d.logOpsRunBlockedAssignment(ctx, rec, escalationID, escType, beadID, workerID)
		d.ackEscalation(ctx, escalationID, beadID, workerID)
		return nil
	}
	d.spawnEscalationOneShot(ctx, escalationID, string(escType), beadID, workerID, msg)
	return nil
}

func (d *Dispatcher) createRoutableOpsRun(ctx context.Context, escalationID int64, escType protocol.EscalationType, beadID, workerID, msg string) (OpsRunRecord, bool, error) {
	runType, ok := routedOpsRunType(escType)
	if !ok {
		return OpsRunRecord{}, false, fmt.Errorf("unsupported routable escalation type %q", escType)
	}
	rec, wasCreated, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		EscalationID:  escalationID,
		Type:          string(runType),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: os.Getpid(),
		Status:        opsRunStatusRunning,
		Error:         msg,
	})
	if err != nil {
		return OpsRunRecord{}, false, err
	}
	return rec, wasCreated, nil
}

func isRoutableEscalationType(escType protocol.EscalationType) bool {
	_, ok := routedOpsRunType(escType)
	return ok
}

func routedOpsRunType(escType protocol.EscalationType) (ops.Type, bool) {
	switch escType {
	case protocol.EscOversizedBead:
		return ops.OpsDecompose, true
	default:
		return "", false
	}
}

func (d *Dispatcher) logOpsRunBlockedAssignment(ctx context.Context, rec OpsRunRecord, escalationID int64, escType protocol.EscalationType, beadID, workerID string) {
	_ = d.logEvent(ctx, "ops_run_blocked_assignment", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"ops_run_id":%d,"escalation_id":%d,"type":%q,"escalation_type":%q}`, rec.ID, escalationID, rec.Type, escType))
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
	case protocol.EscNonTDDAC:
		return d.retryNonTDDAC(ctx, beadID)
	case protocol.EscMergeComplete, protocol.EscManualIntegration, protocol.EscDependencyCycle:
		return false
	default:
		return true
	}
}

func isInformationalEscalation(escType protocol.EscalationType) bool {
	switch escType {
	case protocol.EscMergeComplete, protocol.EscManualIntegration:
		return true
	default:
		return false
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
		protocol.EscPriorityContention, protocol.EscMissingAC, protocol.EscOversizedBead:
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
	switch protocol.EscalationType(escType) {
	case protocol.EscMissingAC:
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
	case protocol.EscOversizedBead:
		if d.ops.HasActiveForBead(beadID) {
			return
		}
		resultCh = d.ops.Decompose(ctx, ops.DecomposeOpts{
			BeadID:  beadID,
			Workdir: d.workdirForOpsRun(beadID),
			Reason:  msg,
		})
	default:
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
// If the one-shot fails (timeout, error, or non-zero exit), it records a
// failed ops run so health reporting can surface the failure.
var errDecomposeValidationUnavailable = errors.New("decompose validation unavailable")

func (d *Dispatcher) handleEscalationResult(ctx context.Context, escalationID int64, escType, beadID, workerID string, resultCh <-chan ops.Result) {
	result := <-resultCh
	if result.Err != nil {
		_ = d.logEvent(ctx, "oneshot_escalation_failed", "ops", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"error":%q}`, escType, result.Err.Error()))
		if protocol.EscalationType(escType) == protocol.EscMissingAC || protocol.EscalationType(escType) == protocol.EscOversizedBead {
			d.recordAssignmentFailure(beadID)
		}
		d.completeOneShotOpsRunFailureBestEffort(ctx, escalationID, escType, beadID, workerID, result)
		d.ackEscalation(ctx, escalationID, beadID, workerID)
		return
	}
	_ = d.logEvent(ctx, "oneshot_escalation_complete", "ops", beadID, workerID,
		fmt.Sprintf(`{"type":%q,"verdict":%q,"feedback":%q}`, escType, result.Verdict, result.Feedback))

	if protocol.EscalationType(escType) == protocol.EscOversizedBead && result.Verdict == ops.VerdictFailed {
		d.recordAssignmentFailure(beadID)
		d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusFailed, string(result.Verdict), result.Feedback, result.Feedback)
		d.ackEscalation(ctx, escalationID, beadID, workerID)
		return
	}
	if protocol.EscalationType(escType) == protocol.EscOversizedBead {
		if err := d.validateDecomposeResult(ctx, beadID); err != nil {
			if errors.Is(err, errDecomposeValidationUnavailable) {
				_ = d.logEvent(ctx, "oneshot_escalation_validation_skipped", "ops", beadID, workerID,
					fmt.Sprintf(`{"type":%q,"error":%q}`, escType, err.Error()))
				d.clearAssignmentFailure(beadID)
				d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusResolved, string(result.Verdict), result.Feedback, "")
				d.ackEscalation(ctx, escalationID, beadID, workerID)
				return
			}
			d.recordAssignmentFailure(beadID)
			d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusFailed, string(result.Verdict), result.Feedback, err.Error())
			_ = d.logEvent(ctx, "oneshot_escalation_validation_failed", "ops", beadID, workerID,
				fmt.Sprintf(`{"type":%q,"error":%q}`, escType, err.Error()))
			return
		}
		d.clearAssignmentFailure(beadID)
		d.completeDecomposeOpsRunBestEffort(ctx, beadID, opsRunStatusResolved, string(result.Verdict), result.Feedback, "")
	}

	// Ack the escalation in the persistent queue so the retry loop doesn't re-deliver it.
	d.ackEscalation(ctx, escalationID, beadID, workerID)
}

func (d *Dispatcher) validateDecomposeResult(ctx context.Context, beadID string) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("%w for %s: dispatcher db is nil", errDecomposeValidationUnavailable, beadID)
	}

	var parent struct {
		ID     string
		Type   string
		Status string
	}
	err := d.db.QueryRowContext(ctx, `
SELECT id, COALESCE(type, ''), COALESCE(status, '')
FROM beads
WHERE id=? AND deleted=0`, beadID).Scan(&parent.ID, &parent.Type, &parent.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("decompose validation failed for %s: parent bead missing", beadID)
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("%w for %s: %w", errDecomposeValidationUnavailable, beadID, err)
		}
		return fmt.Errorf("decompose validation failed for %s: load parent: %w", beadID, err)
	}

	children, err := d.loadDecomposeChildren(ctx, beadID)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		if strings.EqualFold(parent.Type, "epic") || parent.Status == "closed" {
			return nil
		}
		return fmt.Errorf("decompose validation failed for %s: non-epic parent has no child tasks", beadID)
	}

	for _, child := range children {
		if !hasTDDAcceptance(child.AcceptanceCriteria) {
			return fmt.Errorf("decompose validation failed for %s: child %s acceptance criteria must include Test:, Cmd:, and Assert: markers", beadID, child.ID)
		}
		hasDep, err := d.parentDependsOnChild(ctx, beadID, child.ID)
		if err != nil {
			return err
		}
		if !hasDep {
			return fmt.Errorf("decompose validation failed for %s: parent does not depend on child %s", beadID, child.ID)
		}
	}
	return nil
}

func (d *Dispatcher) loadDecomposeChildren(ctx context.Context, beadID string) ([]protocol.Bead, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, COALESCE(type, ''), COALESCE(acceptance_criteria, '')
FROM beads
WHERE parent_id=? AND deleted=0
ORDER BY id`, beadID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, fmt.Errorf("%w for %s: %w", errDecomposeValidationUnavailable, beadID, err)
		}
		return nil, fmt.Errorf("decompose validation failed for %s: load children: %w", beadID, err)
	}
	defer rows.Close()

	var children []protocol.Bead
	for rows.Next() {
		var child protocol.Bead
		if err := rows.Scan(&child.ID, &child.Type, &child.AcceptanceCriteria); err != nil {
			return nil, fmt.Errorf("decompose validation failed for %s: scan child: %w", beadID, err)
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decompose validation failed for %s: iterate children: %w", beadID, err)
	}
	return children, nil
}

func hasTDDAcceptance(acceptance string) bool {
	return strings.Contains(acceptance, "Test:") &&
		strings.Contains(acceptance, "Cmd:") &&
		strings.Contains(acceptance, "Assert:")
}

func (d *Dispatcher) parentDependsOnChild(ctx context.Context, parentID, childID string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM bead_deps
WHERE bead_id=? AND depends_on_id=? AND type IN ('blocks', 'conditional-blocks')`, parentID, childID).Scan(&n)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return false, fmt.Errorf("%w for %s: %w", errDecomposeValidationUnavailable, parentID, err)
		}
		return false, fmt.Errorf("decompose validation failed for %s: check dependency on %s: %w", parentID, childID, err)
	}
	return n > 0, nil
}

func (d *Dispatcher) clearAssignmentFailure(beadID string) {
	d.mu.Lock()
	delete(d.worktreeFailures, beadID)
	d.mu.Unlock()
}

func (d *Dispatcher) completeDecomposeOpsRunBestEffort(ctx context.Context, beadID, status, verdict, feedback, errorText string) {
	rec, err := FindBlockingOpsRun(ctx, d.db, string(ops.OpsDecompose), beadID)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"type":%q,"status":%q,"error":%q}`, ops.OpsDecompose, status, err.Error()))
		return
	}
	if rec == nil {
		return
	}
	if err := CompleteOpsRun(ctx, d.db, rec.ID, status, verdict, feedback, errorText); err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"status":%q,"error":%q}`, rec.ID, ops.OpsDecompose, status, err.Error()))
	}
}

func (d *Dispatcher) completeOneShotOpsRunFailureBestEffort(ctx context.Context, escalationID int64, escType, beadID, workerID string, result ops.Result) {
	runType, ok := opsRunTypeForEscalationResult(escType, result)
	if !ok {
		return
	}
	errorText := ""
	if result.Err != nil {
		errorText = result.Err.Error()
	}
	verdict := string(result.Verdict)
	if verdict == "" {
		verdict = string(ops.VerdictFailed)
	}
	rec, err := FindBlockingOpsRun(ctx, d.db, string(runType), beadID)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"status":%q,"error":%q}`, runType, opsRunStatusFailed, err.Error()))
		return
	}
	if rec != nil {
		d.populateOpsRunEscalationIDBestEffort(ctx, rec.ID, escalationID, runType, beadID, workerID)
		if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusFailed, verdict, result.Feedback, errorText); err != nil {
			_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"ops_run_id":%d,"type":%q,"status":%q,"error":%q}`, rec.ID, runType, opsRunStatusFailed, err.Error()))
		}
		return
	}
	if _, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		EscalationID:  escalationID,
		Type:          string(runType),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: os.Getpid(),
		Status:        opsRunStatusFailed,
		Verdict:       verdict,
		Feedback:      result.Feedback,
		Error:         errorText,
	}); err != nil {
		_ = d.logEvent(ctx, "ops_run_complete_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"status":%q,"error":%q}`, runType, opsRunStatusFailed, err.Error()))
	}
}

func (d *Dispatcher) populateOpsRunEscalationIDBestEffort(ctx context.Context, opsRunID, escalationID int64, runType ops.Type, beadID, workerID string) {
	if escalationID <= 0 {
		return
	}
	result, err := d.db.ExecContext(ctx, `
UPDATE ops_runs
SET escalation_id = ?
WHERE id = ?
  AND escalation_id IS NULL`, escalationID, opsRunID)
	if err != nil {
		_ = d.logEvent(ctx, "ops_run_update_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"ops_run_id":%d,"escalation_id":%d,"type":%q,"error":%q}`, opsRunID, escalationID, runType, err.Error()))
		return
	}
	if _, err := result.RowsAffected(); err != nil {
		_ = d.logEvent(ctx, "ops_run_update_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"ops_run_id":%d,"escalation_id":%d,"type":%q,"error":%q}`, opsRunID, escalationID, runType, err.Error()))
	}
}

func opsRunTypeForEscalationResult(escType string, result ops.Result) (ops.Type, bool) {
	if result.Type != "" {
		return result.Type, true
	}
	switch protocol.EscalationType(escType) {
	case protocol.EscMissingAC:
		return ops.OpsWriteAC, true
	case protocol.EscOversizedBead:
		return ops.OpsDecompose, true
	case protocol.EscStuckWorker, protocol.EscMergeConflict, protocol.EscPriorityContention:
		return ops.OpsEscalation, true
	default:
		return "", false
	}
}

func (d *Dispatcher) ackEscalation(ctx context.Context, escalationID int64, beadID, workerID string) {
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

func (d *Dispatcher) createAssignment(ctx context.Context, beadID, workerID, worktree string) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		return 0, fmt.Errorf("create assignment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create assignment last insert id: %w", err)
	}
	return id, nil
}

// persistBeadCount updates a counter column on the active assignment row for a bead.
// column must be one of "attempt_count" or "handoff_count". This is a best-effort
// operation: errors are logged but do not propagate.
func (d *Dispatcher) persistBeadCount(ctx context.Context, assignmentID int64, beadID, column string, value int) {
	if d.db == nil {
		return
	}
	// Allowlist columns to prevent SQL injection.
	switch column {
	case "attempt_count", "handoff_count":
	default:
		return
	}
	var (
		err error
		res sql.Result
	)
	if assignmentID > 0 {
		res, err = d.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE assignments SET %s=? WHERE id=?`, column),
			value, assignmentID)
	} else {
		res, err = d.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE assignments SET %s=? WHERE bead_id=? AND status='active'`, column),
			value, beadID)
	}
	if err != nil {
		_ = d.logEvent(ctx, "persist_count_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"column":%q,"value":%d,"error":%q}`, column, value, err.Error()))
		return
	}
	if assignmentID > 0 {
		rows, rowsErr := res.RowsAffected()
		if rowsErr == nil && rows != 1 {
			_ = d.logEvent(ctx, "persist_count_target_mismatch", "dispatcher", beadID, "",
				fmt.Sprintf(`{"assignment_id":%d,"column":%q,"value":%d,"rows_affected":%d}`, assignmentID, column, value, rows))
		}
	}
}

// pruneStaleAgentBranches safe-deletes merged agent/* branches at startup.
// Unmerged or checked-out branches are preserved by git branch -d. Non-fatal:
// errors are logged and startup continues.
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

	dirty, dirtyStatus, dirtyErr := d.worktreeDirty(ctx, worktree)
	if dirty || dirtyErr != nil {
		return dirty, dirtyStatus, dirtyErr
	}
	return d.branchHasUnmergedWork(ctx, beadID, worktree, baseBranch)
}

func (d *Dispatcher) worktreeDirty(ctx context.Context, worktree string) (dirty bool, status string, err error) {
	out, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "status", "--porcelain")
	if err != nil {
		return false, "", fmt.Errorf("git status in %s: %w", worktree, err)
	}
	status = strings.TrimSpace(string(out))
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
func (d *Dispatcher) releasePriorAssignment(ctx context.Context, w *trackedWorker, newBeadID string) {
	if w == nil {
		return
	}
	d.mu.Lock()
	priorBeadID := w.beadID
	priorAssignmentID := w.assignmentID
	workerID := w.id
	priorWorktree := d.worktreeByBead[priorBeadID]
	d.mu.Unlock()

	if priorBeadID == "" {
		persistedBeadID, persistedWorktree := d.activeAssignmentBead(ctx, priorAssignmentID, workerID)
		priorBeadID = persistedBeadID
		if priorWorktree == "" {
			priorWorktree = persistedWorktree
		}
	}

	if priorBeadID == "" || priorBeadID == newBeadID {
		return
	}

	// Preserve external close (oro-wp74): if the prior bead has been closed by
	// another party (e.g. manager dedup), do not reopen it. Reopening masks the
	// dedup and lets the bead be re-picked, feeding the oro-jev9 race.
	externallyClosed := false
	if detail, showErr := d.beads.Show(ctx, priorBeadID); showErr == nil && detail != nil && detail.Status == "closed" {
		externallyClosed = true
	}
	if priorAssignmentID > 0 {
		var err error
		if externallyClosed {
			err = d.completeAssignment(ctx, priorAssignmentID, priorBeadID)
		} else {
			err = d.requeueAssignment(ctx, priorAssignmentID)
		}
		if err != nil {
			_ = d.logEvent(ctx, "release_prior_assignment_failed", "dispatcher", priorBeadID, workerID, err.Error())
		}
	}
	if !externallyClosed {
		if err := d.updateBeadStatus(ctx, priorBeadID, "open"); err != nil {
			_ = d.logEvent(ctx, "release_prior_status_failed", "dispatcher", priorBeadID, workerID, err.Error())
		}
	}
	if priorWorktree != "" {
		d.mu.Lock()
		d.worktreeByBead[priorBeadID] = priorWorktree
		d.mu.Unlock()
		_ = d.logEvent(ctx, "worker_abandon_work_preserved", "dispatcher", priorBeadID, workerID,
			fmt.Sprintf(`{"branch":%q,"worktree":%q}`, protocol.BranchPrefix+priorBeadID, priorWorktree))
	}
	_ = d.logEvent(ctx, "worker_abandon_release", "dispatcher", priorBeadID, workerID,
		fmt.Sprintf(`{"reason":"reassign_to_%s","prior_assignment_id":%d,"externally_closed":%t}`, newBeadID, priorAssignmentID, externallyClosed))
}

func (d *Dispatcher) activeAssignmentBead(ctx context.Context, assignmentID int64, workerID string) (beadID, worktree string) {
	if assignmentID <= 0 || d.db == nil {
		return "", ""
	}

	if err := d.db.QueryRowContext(ctx,
		`SELECT bead_id, worktree FROM assignments WHERE id=? AND status='active'`,
		assignmentID).Scan(&beadID, &worktree); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			_ = d.logEvent(ctx, "release_prior_assignment_lookup_failed", "dispatcher", "", workerID, err.Error())
		}
		return "", ""
	}
	return beadID, worktree
}

func (d *Dispatcher) completeAssignment(ctx context.Context, assignmentID int64, beadID string) error {
	const maxSQLiteBusyRetries = 20
	for attempt := 0; ; attempt++ {
		err := d.completeAssignmentOnce(ctx, assignmentID, beadID)
		if err == nil || !isSQLiteBusyError(err) {
			return err
		}
		if attempt >= maxSQLiteBusyRetries {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("complete assignment retry canceled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) completeAssignmentOnce(ctx context.Context, assignmentID int64, beadID string) error {
	var (
		err error
		res sql.Result
	)
	if assignmentID > 0 {
		res, err = d.db.ExecContext(ctx,
			`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE id=? AND status!='quarantined'`,
			assignmentID)
	} else {
		res, err = d.db.ExecContext(ctx,
			`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE bead_id=? AND status='active'`,
			beadID)
	}
	if err != nil {
		return fmt.Errorf("complete assignment: %w", err)
	}
	if assignmentID > 0 {
		rows, rowsErr := res.RowsAffected()
		if rowsErr == nil && rows == 0 && d.assignmentIsQuarantined(ctx, assignmentID) {
			_ = d.logEvent(ctx, "assignment_completion_skipped_quarantined", "dispatcher", beadID, "",
				fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
			return nil
		}
		if rowsErr == nil && rows != 1 {
			return fmt.Errorf("complete assignment: assignment_id %d affected %d rows", assignmentID, rows)
		}
	}
	return nil
}

func isSQLiteBusyError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "sqlite_busy") ||
		strings.Contains(text, "database is locked") ||
		strings.Contains(text, "database table is locked")
}

func (d *Dispatcher) assignmentIsQuarantined(ctx context.Context, assignmentID int64) bool {
	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		return false
	}
	return status == "quarantined"
}

func (d *Dispatcher) assignmentIDLocked(workerID, beadID string) int64 {
	if w, ok := d.workers[workerID]; ok && (beadID == "" || w.beadID == beadID) {
		return w.assignmentID
	}
	return 0
}

func (d *Dispatcher) activeAssignmentIDForBead(ctx context.Context, beadID string) int64 {
	if d.db == nil || beadID == "" {
		return 0
	}
	var assignmentID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='active' ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&assignmentID); err != nil {
		return 0
	}
	return assignmentID
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
		if b.Len() >= maxCodeSearchContextSize {
			return truncateCodeSearchContext(b.String())
		}
	}
	return strings.TrimSpace(b.String())
}

func truncateCodeSearchContext(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxCodeSearchContextSize {
		return s
	}

	truncated := trimValidUTF8(s[:maxCodeSearchContextSize])
	truncated = strings.TrimSpace(truncated)
	if strings.Count(truncated, "```")%2 != 0 {
		truncated += "\n```"
	}
	return truncated + "\n\n[code search context truncated]"
}

func trimValidUTF8(s string) string {
	for s != "" {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
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

// advanceRemoteGate advances dispatcher-owned candidate state without relying
// on the worker that originally produced the candidate.
func (d *Dispatcher) advanceRemoteGate(ctx context.Context, gateID int64, from, to RemoteGateState) (RemoteGate, error) {
	if d == nil || d.remoteGates == nil {
		return RemoteGate{}, errors.New("advance remote gate: store is unavailable")
	}
	return d.remoteGates.AdvanceRemoteGate(ctx, gateID, from, to)
}

// ConnectedWorkers, TargetWorkers, WorkerInfo, WorkerModel → worker_pool.go
