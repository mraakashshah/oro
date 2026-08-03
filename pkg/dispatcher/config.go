package dispatcher

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"oro/pkg/factoryhealth"
	"oro/pkg/storage"
)

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
	SweepConfig             SweepConfig   // Dispatcher sweep cadence; zero values use maintenance defaults.
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
	ReviewEvidenceDir       string        // Absolute directory for assignment-scoped QG evidence.
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
	// StorageController coordinates durable storage pause epochs. A nil
	// controller preserves the dispatcher's existing admission behavior.
	StorageController *storage.Controller
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
	if c.ReviewEvidenceDir != "" && !filepath.IsAbs(c.ReviewEvidenceDir) {
		return fmt.Errorf("ReviewEvidenceDir must be absolute, got %q", c.ReviewEvidenceDir)
	}
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
