package protocol

import (
	"fmt"
	"strings"
)

// Bead represents a ready work item from the bead source.
type Bead struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Priority           int    `json:"priority"`
	Epic               string `json:"epic,omitempty"`                // parent epic ID for focus filtering
	Type               string `json:"issue_type,omitempty"`          // task, bug, feature, epic
	Model              string `json:"model,omitempty"`               // claude model override; empty = auto-route by estimate
	EstimatedMinutes   int    `json:"estimated_minutes,omitempty"`   // estimated work duration in minutes
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"` // acceptance criteria text
}

// BeadDetail holds extended information about a single bead.
type BeadDetail struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	Model              string `json:"model,omitempty"`
	WorkerID           string `json:"worker_id,omitempty"`
	ContextPercent     int    `json:"context_percent,omitempty"`
	LastHeartbeat      string `json:"last_heartbeat,omitempty"`
	GitDiff            string `json:"git_diff,omitempty"`
	Memory             string `json:"memory,omitempty"`
}

// Model constants for routing.
const (
	ModelOpus   = "claude-opus-4-6"
	ModelSonnet = "claude-sonnet-4-5-20250929"
	ModelHaiku  = "claude-haiku-4-5-20251001"
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
