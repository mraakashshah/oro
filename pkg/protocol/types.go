package protocol

import "fmt"

// Bead represents a ready work item from the bead source.
type Bead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Epic     string `json:"epic,omitempty"`       // parent epic ID for focus filtering
	Type     string `json:"issue_type,omitempty"` // task, bug, feature, epic
	Model    string `json:"model,omitempty"`      // claude model override; empty = auto-route by Type
}

// BeadDetail holds extended information about a single bead.
type BeadDetail struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	Model              string `json:"model,omitempty"`
}

// Model constants for routing.
const (
	ModelOpus   = "claude-opus-4-6"
	ModelSonnet = "claude-sonnet-4-5-20250929"
)

// DefaultModel is used when a bead has no explicit model set and no type-based
// routing applies. Kept as ModelOpus for backward compatibility.
const DefaultModel = ModelOpus

// ResolveModel returns the model to use for this bead. Priority:
//  1. Explicit Model field (bead-level override)
//  2. Type-based routing: epic/feature -> Opus, task/bug -> Sonnet
//  3. DefaultModel (Opus) as fallback
func (b Bead) ResolveModel() string {
	if b.Model != "" {
		return b.Model
	}
	switch b.Type {
	case "epic", "feature":
		return ModelOpus
	case "task", "bug":
		return ModelSonnet
	default:
		return DefaultModel
	}
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
	EscPriorityContention EscalationType = "PRIORITY_CONTENTION"
	EscWorkerCrash        EscalationType = "WORKER_CRASH"
	EscStatus             EscalationType = "STATUS"
	EscDrainComplete      EscalationType = "DRAIN_COMPLETE"
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
