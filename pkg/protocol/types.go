package protocol

import (
	"fmt"
	"strings"
)

// Dependency represents a dependency relationship between beads.
type Dependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"` // "blocks", "parent-child", etc.
}

// EmbedRequest is sent by a dispatcher to request text embedding.
type EmbedRequest struct {
	Text string `json:"text"`
}

// EmbedResponse is sent by a worker with the computed embedding vector or error.
type EmbedResponse struct {
	Vec []float32 `json:"vec,omitempty"`
	Err string    `json:"err,omitempty"`
}

// RerankByIDsRequest is sent to request re-ranking of memories by relevance.
type RerankByIDsRequest struct {
	Query     string  `json:"query"`
	MemoryIDs []int64 `json:"memory_ids"`
}

// RerankByIDsResponse contains re-ranking scores or an error.
type RerankByIDsResponse struct {
	Scores []float64 `json:"scores,omitempty"`
	Err    string    `json:"err,omitempty"`
}

// Bead represents a ready work item from the bead source.
type Bead struct {
	ID                 string         `json:"id"`
	Title              string         `json:"title"`
	ContractVersion    int            `json:"contract_version,omitempty"`
	Draft              bool           `json:"draft,omitempty"`
	Status             string         `json:"status,omitempty"` // open, in_progress, blocked, closed
	Priority           int            `json:"priority"`
	Epic               string         `json:"parent,omitempty"`              // parent epic ID for focus filtering
	Type               string         `json:"issue_type,omitempty"`          // task, bug, feature, epic
	Model              string         `json:"model,omitempty"`               // provider-native model override; empty = route by tier/estimate
	Tier               Tier           `json:"tier,omitempty"`                // provider-neutral routing tier
	WorkerID           string         `json:"worker_id,omitempty"`           // currently assigned worker, when known
	ContextPercent     int            `json:"context_percent,omitempty"`     // current worker context usage, when known
	LastHeartbeat      string         `json:"last_heartbeat,omitempty"`      // latest worker heartbeat timestamp, when known
	GitDiff            string         `json:"git_diff,omitempty"`            // live worktree diff, when requested
	Memory             string         `json:"memory,omitempty"`              // retrieved memory context, when requested
	EstimatedMinutes   int            `json:"estimated_minutes,omitempty"`   // estimated work duration in minutes
	AcceptanceCriteria string         `json:"acceptance_criteria,omitempty"` // acceptance criteria text
	Dependencies       []Dependency   `json:"dependencies,omitempty"`        // dependency relationships
	UpdatedAt          string         `json:"updated_at,omitempty"`          // RFC3339 timestamp of last update
	ClosedAt           string         `json:"closed_at,omitempty"`           // RFC3339 timestamp of when closed
	CreatedAt          string         `json:"created_at,omitempty"`          // RFC3339 timestamp of creation
	DeferUntil         string         `json:"defer_until,omitempty"`         // RFC3339 timestamp until the bead is hidden from ready
	Description        string         `json:"description,omitempty"`         // detailed description
	CloseReason        string         `json:"close_reason,omitempty"`        // reason for closing
	Owner              string         `json:"owner,omitempty"`               // owner/assignee identifier
	Notes              string         `json:"notes,omitempty"`               // freeform notes
	Tags               []string       `json:"tags,omitempty"`                // tags for categorization
	Metadata           map[string]any `json:"metadata,omitempty"`            // arbitrary metadata; mixed-type values
	Labels             []string       `json:"labels,omitempty"`              // structured labels
	ContextThresholds  string         `json:"context_thresholds,omitempty"`  // JSON {warning, checkpoint} per-bead overrides (§9.4)
}

// BeadDetail is a migration-window alias for the unified Bead shape.
type BeadDetail = Bead

// MetaBranch is the metadata key used to store branch information.
const MetaBranch = "branch"

// Model constants for routing.
const (
	ModelOpus   = "opus"
	ModelSonnet = "sonnet"
	ModelHaiku  = "haiku"
)

// Tier identifies a provider-neutral routing tier.
type Tier string

// Provider-neutral tier constants.
const (
	TierFast       Tier = "fast"
	TierBalanced   Tier = "balanced"
	TierDeep       Tier = "deep"
	TierBackground Tier = "background"
)

// DefaultModel is used when a bead has no explicit model set and estimate-based
// routing does not apply.
const DefaultModel = ModelSonnet

// DefaultTier is used when a bead has no explicit tier set and estimate-based
// routing does not apply.
const DefaultTier = TierBalanced

// IsKnown reports whether the tier is one of Oro's defined routing tiers.
func (t Tier) IsKnown() bool {
	switch t {
	case TierFast, TierBalanced, TierDeep, TierBackground:
		return true
	default:
		return false
	}
}

// ParseTier normalizes a serialized tier value.
//
//oro:testonly
func ParseTier(raw string) (Tier, bool) {
	tier := Tier(strings.TrimSpace(strings.ToLower(raw)))
	return tier, tier.IsKnown()
}

// LegacyModelToTier maps legacy Claude-family model names onto neutral tiers.
func LegacyModelToTier(model string) (Tier, bool) {
	switch strings.TrimSpace(strings.ToLower(model)) {
	case ModelHaiku:
		return TierFast, true
	case ModelSonnet:
		return TierBalanced, true
	case ModelOpus:
		return TierDeep, true
	default:
		return "", false
	}
}

// ResolveModel returns the explicit model override, when present.
// Runtime/tier/estimate fallback resolution lives in pkg/agentmodel.
func (b Bead) ResolveModel() string {
	return b.Model
}

// ResolveTier returns the neutral routing tier for this bead. Priority:
//  1. Explicit Tier field
//  2. Legacy model mapping from explicit Model field
//  3. Estimate-based routing: <=5 min -> fast, >5 min -> balanced
//  4. DefaultTier (balanced) as fallback
func (b Bead) ResolveTier() Tier {
	if b.Tier.IsKnown() {
		return b.Tier
	}
	if tier, ok := LegacyModelToTier(b.Model); ok {
		return tier
	}
	if b.EstimatedMinutes > 0 && b.EstimatedMinutes <= 5 {
		return TierFast
	}
	return DefaultTier
}

// WorkerState represents the state of a connected worker.
type WorkerState string

// Worker state constants.
const (
	WorkerIdle         WorkerState = "idle"
	WorkerBusy         WorkerState = "busy"
	WorkerReserved     WorkerState = "reserved" // transient: I/O in progress, heartbeat checker must skip
	WorkerReviewing    WorkerState = "reviewing"
	WorkerPreempting   WorkerState = "preempting"    // transient: PREEMPT sent, waiting for worker to gracefully stop
	WorkerShuttingDown WorkerState = "shutting_down" // transient: handoff SHUTDOWN sent, not yet disconnected
)

// EscalationType classifies a structured escalation message.
type EscalationType string

// Escalation type constants for [ORO-DISPATCH] messages.
const (
	EscMergeConflict      EscalationType = "MERGE_CONFLICT"
	EscStuck              EscalationType = "STUCK"
	EscStuckWorker        EscalationType = "STUCK_WORKER"
	EscPriorityContention EscalationType = "PRIORITY_CONTENTION"
	EscWorkerCrash        EscalationType = "WORKER_CRASH"
	EscStatus             EscalationType = "STATUS"
	EscDrainComplete      EscalationType = "DRAIN_COMPLETE"
	EscMissingAC          EscalationType = "MISSING_AC"
	EscEpicComplete       EscalationType = "EPIC_COMPLETE"
	EscMergeComplete      EscalationType = "MERGE_COMPLETE"
	EscOversizedBead      EscalationType = "OVERSIZED_BEAD"
	EscNonTDDAC           EscalationType = "NON_TDD_AC"
	EscManualIntegration  EscalationType = "MANUAL_INTEGRATION"
	EscDependencyCycle    EscalationType = "DEPENDENCY_CYCLE"
)

// FormatEscalation produces a structured escalation message in the form:
//
//	[ORO-DISPATCH] <TYPE>: <bead-id> — <summary>. <details>.
//
// If details is empty the trailing details clause is omitted.
func FormatEscalation(typ EscalationType, beadID, summary, details string) string {
	if details != "" {
		return fmt.Sprintf("[ORO-DISPATCH] %s: %s — %s. %s.", typ, beadID, summary, details)
	}
	return fmt.Sprintf("[ORO-DISPATCH] %s: %s — %s.", typ, beadID, summary)
}

// CountReadFiles counts lines starting with "Read:" in the acceptance criteria string.
//
//oro:testonly
func CountReadFiles(acceptance string) int {
	count := 0
	for _, line := range strings.Split(acceptance, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Read:") {
			count++
		}
	}
	return count
}
