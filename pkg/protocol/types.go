package protocol

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// parenAnnotation matches parenthetical annotations in Read: tokens, e.g. "(from bead 1.1)".
var parenAnnotation = regexp.MustCompile(`\s*\([^)]*\)`)

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
	Status             string         `json:"status,omitempty"` // open, in_progress, blocked, closed
	Priority           int            `json:"priority"`
	Epic               string         `json:"parent,omitempty"`              // parent epic ID for focus filtering
	Type               string         `json:"issue_type,omitempty"`          // task, bug, feature, epic
	Model              string         `json:"model,omitempty"`               // claude model override; empty = auto-route by estimate
	EstimatedMinutes   int            `json:"estimated_minutes,omitempty"`   // estimated work duration in minutes
	AcceptanceCriteria string         `json:"acceptance_criteria,omitempty"` // acceptance criteria text
	Dependencies       []Dependency   `json:"dependencies,omitempty"`        // dependency relationships
	UpdatedAt          string         `json:"updated_at,omitempty"`          // RFC3339 timestamp of last update
	ClosedAt           string         `json:"closed_at,omitempty"`           // RFC3339 timestamp of when closed
	CreatedAt          string         `json:"created_at,omitempty"`          // RFC3339 timestamp of creation
	Description        string         `json:"description,omitempty"`         // detailed description
	CloseReason        string         `json:"close_reason,omitempty"`        // reason for closing
	Owner              string         `json:"owner,omitempty"`               // owner/assignee identifier
	Notes              string         `json:"notes,omitempty"`               // freeform notes
	Tags               []string       `json:"tags,omitempty"`                // tags for categorization
	Metadata           map[string]any `json:"metadata,omitempty"`            // arbitrary metadata; mixed-type values
	Labels             []string       `json:"labels,omitempty"`              // structured labels
}

// BeadDetail holds extended information about a single bead.
type BeadDetail struct {
	ID                 string         `json:"id"`
	Title              string         `json:"title"`
	Description        string         `json:"description,omitempty"`
	AcceptanceCriteria string         `json:"acceptance_criteria"`
	Status             string         `json:"status,omitempty"`
	Epic               string         `json:"parent,omitempty"`     // parent ID; empty for standalone beads
	Type               string         `json:"issue_type,omitempty"` // task, bug, feature, epic
	Model              string         `json:"model,omitempty"`
	WorkerID           string         `json:"worker_id,omitempty"`
	ContextPercent     int            `json:"context_percent,omitempty"`
	LastHeartbeat      string         `json:"last_heartbeat,omitempty"`
	GitDiff            string         `json:"git_diff,omitempty"`
	Memory             string         `json:"memory,omitempty"`
	Dependencies       []Dependency   `json:"dependencies,omitempty"`
	Owner              string         `json:"owner,omitempty"` // owner/assignee identifier
	Notes              string         `json:"notes,omitempty"` // freeform notes
	Metadata           map[string]any `json:"metadata,omitempty"`
	Labels             []string       `json:"labels,omitempty"`
}

// MetaBranch is the metadata key used to store branch information.
const MetaBranch = "branch"

// Model constants for routing.
const (
	ModelOpus   = "opus"
	ModelSonnet = "sonnet"
	ModelHaiku  = "haiku"
)

// DefaultModel is used when a bead has no explicit model set and estimate-based
// routing does not apply.
const DefaultModel = ModelSonnet

// ResolveModel returns the model to use for this bead. Priority:
//  1. Explicit Model field (bead-level override)
//  2. Estimate-based routing: <=5 min -> Haiku, >5 min -> Sonnet
//  3. DefaultModel (Sonnet) as fallback
func (b Bead) ResolveModel() string {
	if b.Model != "" {
		return b.Model
	}
	if b.EstimatedMinutes > 0 && b.EstimatedMinutes <= 5 {
		return ModelHaiku
	}
	return ModelSonnet
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

// CountDistinctModules counts distinct Go package directories referenced in
// "Read:" lines of the acceptance criteria text. Each Read: line may contain
// comma-separated file paths (e.g. "pkg/foo/bar.go:42, pkg/baz/qux.go:10").
// The package directory is computed via filepath.Dir; duplicate directories are
// collapsed. Returns 0 when no Read: lines are present.
//
//oro:testonly
func CountDistinctModules(acceptance string) int {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(acceptance, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Read:") {
			continue
		}
		content := strings.TrimPrefix(line, "Read:")
		// Treat semicolons as additional separators like commas.
		content = strings.ReplaceAll(content, ";", ",")
		for _, part := range strings.Split(content, ",") {
			// Strip parenthetical annotations (e.g. "(from bead 1.1)") before processing.
			part = parenAnnotation.ReplaceAllString(part, "")
			part = strings.TrimSpace(part)
			if part == "" || isAllDigits(part) {
				continue
			}
			// Skip bare symbol names: entries with no slash and no dot (e.g., "Embed", "ExportVocab").
			// Keep filenames like "main.go" (has dot) and paths like "pkg/foo/bar.go" (has slash).
			if !strings.Contains(part, "/") && !strings.Contains(part, ".") {
				continue
			}
			seen[filepath.Dir(part)] = struct{}{}
		}
	}
	return len(seen)
}

// isAllDigits reports whether s is non-empty and contains only ASCII digits.
// Used to detect bare line-number tokens (e.g. "26", "51") in Read: fields.
func isAllDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
