// Package beadstore defines Oro's native bead-state storage boundary.
package beadstore

import (
	"context"

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
	Status   *string
	Priority *int
	Type     *string
	// ParentID uses nil for no change and "" for clearing the parent.
	ParentID *string
	Owner    *string
}
