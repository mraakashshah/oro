package beadstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"oro/pkg/protocol"
)

// FakeStore is an in-memory Store implementation for tests.
//
//oro:testonly
type FakeStore struct {
	mu     sync.RWMutex
	beads  map[string]protocol.Bead
	nextID int
}

// NewFakeStore returns a map-backed Store seeded with optional beads.
//
//oro:testonly
func NewFakeStore(initial ...protocol.Bead) *FakeStore {
	store := &FakeStore{
		beads:  make(map[string]protocol.Bead, len(initial)),
		nextID: 1,
	}
	for _, bead := range initial {
		store.beads[bead.ID] = cloneBead(bead)
	}
	return store
}

// Ready returns open beads with no active blockers.
func (s *FakeStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check ready context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var ready []protocol.Bead
	for _, bead := range s.beads {
		if bead.Status == "open" && bead.WorkerID == "" && !isFutureDeferred(bead.DeferUntil) && !s.hasActiveBlockerLocked(bead) {
			ready = append(ready, cloneBead(bead))
		}
	}
	sortBeads(ready)
	return ready, nil
}

// InProgress returns beads currently assigned or being worked.
func (s *FakeStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check in-progress context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var inProgress []protocol.Bead
	for _, bead := range s.beads {
		if bead.Status == "in_progress" || (bead.WorkerID != "" && bead.Status != "closed") {
			inProgress = append(inProgress, cloneBead(bead))
		}
	}
	sortBeads(inProgress)
	return inProgress, nil
}

// Blocked returns beads that cannot currently be claimed.
func (s *FakeStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check blocked context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var blocked []protocol.Bead
	for _, bead := range s.beads {
		if bead.WorkerID == "" && (bead.Status == "blocked" || (bead.Status == "open" && s.hasActiveBlockerLocked(bead))) {
			blocked = append(blocked, cloneBead(bead))
		}
	}
	sortBeads(blocked)
	return blocked, nil
}

// Closed returns recently closed beads, capped by limit.
// Nonpositive limits return an empty result, matching SQL LIMIT 0 semantics.
func (s *FakeStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check closed context: %w", err)
	}
	if limit <= 0 {
		return []protocol.Bead{}, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var closed []protocol.Bead
	for _, bead := range s.beads {
		if bead.Status == "closed" {
			closed = append(closed, cloneBead(bead))
		}
	}
	sort.Slice(closed, func(i, j int) bool {
		if closed[i].ClosedAt != closed[j].ClosedAt {
			return closed[i].ClosedAt > closed[j].ClosedAt
		}
		return closed[i].ID < closed[j].ID
	})
	if len(closed) > limit {
		closed = closed[:limit]
	}
	return closed, nil
}

// Show returns nil when the bead is not found.
func (s *FakeStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("show bead context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bead, ok := s.beads[id]
	if !ok {
		return nil, nil
	}
	clone := cloneBead(bead)
	return &clone, nil
}

// Create persists a bead and returns its complete stored representation.
func (s *FakeStore) Create(ctx context.Context, params CreateParams) (*protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create bead context: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := params.ID
	if id == "" {
		id = s.nextIDLocked()
	}
	if _, ok := s.beads[id]; ok {
		return nil, fmt.Errorf("bead %s already exists", id)
	}

	now := nowString()
	bead := protocol.Bead{
		ID:                 id,
		Title:              params.Title,
		Status:             "open",
		Priority:           params.Priority,
		Epic:               params.ParentID,
		Type:               params.Type,
		EstimatedMinutes:   params.EstimatedMinutes,
		AcceptanceCriteria: params.AcceptanceCriteria,
		UpdatedAt:          now,
		CreatedAt:          now,
		Description:        params.Description,
		Tags:               cloneStrings(params.Tags),
		Labels:             cloneStrings(params.Labels),
		Metadata:           stringMapToAny(params.Metadata),
	}
	s.beads[id] = cloneBead(bead)
	clone := cloneBead(bead)
	return &clone, nil
}

// Update applies non-nil fields from params.
func (s *FakeStore) Update(ctx context.Context, id string, params UpdateParams) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("update bead context: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[id]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if params.Status != nil && !validStatus(*params.Status) {
		return fmt.Errorf("beadstore: invalid status %q", *params.Status)
	}
	changed := false
	if params.Status != nil {
		bead.Status = *params.Status
		switch *params.Status {
		case "open":
			bead.DeferUntil = ""
			bead.ClosedAt = ""
			bead.CloseReason = ""
		case "closed":
			if bead.ClosedAt == "" {
				bead.ClosedAt = nowString()
			}
		}
		changed = true
	}
	if params.Priority != nil {
		bead.Priority = *params.Priority
		changed = true
	}
	if params.Type != nil {
		bead.Type = *params.Type
		changed = true
	}
	if params.AcceptanceCriteria != nil {
		bead.AcceptanceCriteria = *params.AcceptanceCriteria
		changed = true
	}
	if params.Notes != nil && strings.TrimSpace(*params.Notes) != "" {
		if bead.Notes == "" {
			bead.Notes = *params.Notes
		} else {
			bead.Notes += "\n\n" + *params.Notes
		}
		changed = true
	}
	if params.ParentID != nil {
		bead.Epic = *params.ParentID
		changed = true
	}
	if params.Owner != nil {
		bead.Owner = *params.Owner
		changed = true
	}
	if !changed {
		return nil
	}
	bead.UpdatedAt = nowString()
	s.beads[id] = bead
	return nil
}

// Close marks a bead closed with the supplied reason.
func (s *FakeStore) Close(ctx context.Context, id, reason string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("close bead context: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[id]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if bead.Status == "closed" {
		return nil
	}
	now := nowString()
	bead.Status = "closed"
	bead.CloseReason = reason
	bead.ClosedAt = now
	bead.UpdatedAt = now
	s.beads[id] = bead
	return nil
}

func (s *FakeStore) Defer(ctx context.Context, id, until string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("defer bead context: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[id]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	bead.DeferUntil = until
	bead.UpdatedAt = nowString()
	s.beads[id] = bead
	return nil
}

func (s *FakeStore) Undefer(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("undefer bead context: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[id]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	bead.DeferUntil = ""
	bead.UpdatedAt = nowString()
	s.beads[id] = bead
	return nil
}

// HasChildren reports whether epicID has any children.
func (s *FakeStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("check children context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, bead := range s.beads {
		if bead.Epic == epicID {
			return true, nil
		}
	}
	return false, nil
}

// AllChildrenClosed reports whether every child of epicID is closed.
func (s *FakeStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("check child closure context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, bead := range s.beads {
		if bead.Epic == epicID && bead.Status != "closed" {
			return false, nil
		}
	}
	return true, nil
}

// FindByParentAndTag returns children under parentID that have tag.
func (s *FakeStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("find children by tag context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var matches []protocol.Bead
	for _, bead := range s.beads {
		if bead.Epic == parentID && hasTag(bead, tag) {
			matches = append(matches, cloneBead(bead))
		}
	}
	sortBeads(matches)
	return matches, nil
}

// Export returns a JSONL backup snapshot.
func (s *FakeStore) Export(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("export beads context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	beads := make([]protocol.Bead, 0, len(s.beads))
	for _, bead := range s.beads {
		beads = append(beads, cloneBead(bead))
	}
	sort.Slice(beads, func(i, j int) bool {
		return beads[i].ID < beads[j].ID
	})

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, bead := range beads {
		if err := encoder.Encode(bead); err != nil {
			return nil, fmt.Errorf("encode bead %s: %w", bead.ID, err)
		}
	}
	return buf.Bytes(), nil
}

func (s *FakeStore) hasActiveBlockerLocked(bead protocol.Bead) bool {
	for _, dependency := range bead.Dependencies {
		if dependency.Type != "blocks" && dependency.Type != "conditional-blocks" {
			continue
		}
		if dependency.DependsOnID == "" {
			continue
		}
		blocker, ok := s.beads[dependency.DependsOnID]
		if !ok || blocker.Status != "closed" {
			return true
		}
	}
	return false
}

func (s *FakeStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("fake-%d", s.nextID)
		s.nextID++
		if _, ok := s.beads[id]; !ok {
			return id
		}
	}
}

func cloneBead(bead protocol.Bead) protocol.Bead {
	bead.Dependencies = cloneDependencies(bead.Dependencies)
	bead.Tags = cloneStrings(bead.Tags)
	bead.Metadata = cloneMetadata(bead.Metadata)
	bead.Labels = cloneStrings(bead.Labels)
	return bead
}

func cloneDependencies(in []protocol.Dependency) []protocol.Dependency {
	if in == nil {
		return nil
	}
	out := make([]protocol.Dependency, len(in))
	copy(out, in)
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneMetadata(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMetadata(typed)
	case map[string]string:
		return stringMapToAny(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	case []string:
		return cloneStrings(typed)
	default:
		return value
	}
}

func stringMapToAny(in map[string]string) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func hasTag(bead protocol.Bead, tag string) bool {
	for _, existing := range bead.Tags {
		if existing == tag {
			return true
		}
	}
	return false
}

func isFutureDeferred(until string) bool {
	if until == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return true
	}
	return parsed.After(time.Now().UTC())
}

func sortBeads(beads []protocol.Bead) {
	sort.Slice(beads, func(i, j int) bool {
		if beads[i].Priority != beads[j].Priority {
			return beads[i].Priority < beads[j].Priority
		}
		return beads[i].ID < beads[j].ID
	})
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
