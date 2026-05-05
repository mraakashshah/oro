// Package beadstore defines Oro's native bead-state storage boundary.
package beadstore

import (
	"context"
	"time"

	"oro/pkg/protocol"
)

// Store is Oro's native bead-state interface.
type Store interface {
	// Ready returns open beads with no active blockers.
	Ready(ctx context.Context) ([]protocol.Bead, error)
	// InProgress returns beads currently assigned or being worked.
	InProgress(ctx context.Context) ([]protocol.Bead, error)
	// Blocked returns beads that cannot currently be claimed.
	Blocked(ctx context.Context) ([]protocol.Bead, error)
	// Closed returns recently closed beads, capped by limit.
	Closed(ctx context.Context, limit int) ([]protocol.Bead, error)
	// Show returns nil when the bead is not found.
	Show(ctx context.Context, id string) (*protocol.Bead, error)

	// Create persists a bead and returns its complete stored representation.
	Create(ctx context.Context, params CreateParams) (*protocol.Bead, error)
	// Update applies non-nil fields from params.
	Update(ctx context.Context, id string, params UpdateParams) error
	// Close marks a bead closed with the supplied reason.
	Close(ctx context.Context, id, reason string) error

	// HasChildren reports whether epicID has any children.
	HasChildren(ctx context.Context, epicID string) (bool, error)
	// AllChildrenClosed reports whether every child of epicID is closed.
	AllChildrenClosed(ctx context.Context, epicID string) (bool, error)
	// FindByParentAndTag returns children under parentID that have tag.
	FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)

	// Export returns a JSONL backup snapshot.
	Export(ctx context.Context) ([]byte, error)

	// AppendJourney appends a single event to beadID's append-only audit trail.
	// It is a single INSERT with no read-modify-write; callers must supply Ts.
	AppendJourney(ctx context.Context, beadID string, evt JourneyEvent) error
	// Journey returns all events for beadID with ts >= since, in ascending order.
	Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error)
	// LatestJourney returns the most recent limit events for beadID in
	// ascending (chronological) order.
	LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error)

	// SetGateState atomically transitions beadID's gate_state from → to and
	// appends a gate_state_changed journey event. Returns ErrStaleGate if the
	// current gate_state does not equal from (concurrent write raced ahead).
	SetGateState(ctx context.Context, beadID string, from, to GateState, reason string) error

	// TransitionPipelineStage atomically transitions beadID's pipeline_stage
	// from → to and appends a pipeline_stage_changed journey event. Returns
	// ErrStaleStage if the current pipeline_stage does not equal from.
	TransitionPipelineStage(ctx context.Context, beadID string, from, to PipelineStage) error
}

// StatusCounts contains quick bead counts by lifecycle status.
type StatusCounts struct {
	Open       int `json:"open"`
	InProgress int `json:"in_progress"`
	Closed     int `json:"closed"`
}

// CreateParams contains the fields needed to create a bead.
type CreateParams struct {
	Title              string
	Type               string
	Priority           int
	Description        string
	AcceptanceCriteria string
	ParentID           string
	Tags               []string
	Labels             []string
	Metadata           map[string]string
	EstimatedMinutes   int
	ID                 string
}

// UpdateParams contains optional bead fields to update.
type UpdateParams struct {
	Status             *string
	Priority           *int
	Type               *string
	AcceptanceCriteria *string
	Notes              *string
	// ParentID uses nil for no change and "" for clearing the parent.
	ParentID *string
	Owner    *string
}
