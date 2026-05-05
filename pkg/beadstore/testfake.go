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

	"oro/pkg/cards"
	"oro/pkg/protocol"
)

// FakeStore is an in-memory Store implementation for tests.
//
//oro:testonly
type FakeStore struct {
	mu                 sync.RWMutex
	beads              map[string]protocol.Bead
	closed             []string
	nextID             int
	journeys           map[string][]JourneyEvent
	gateStates         map[string]GateState
	pipelineStages     map[string]PipelineStage
	premortCycleCounts map[string]int
}

// NewFakeStore returns a map-backed Store seeded with optional beads.
//
//oro:testonly
func NewFakeStore(initial ...protocol.Bead) *FakeStore {
	store := &FakeStore{
		beads:              make(map[string]protocol.Bead, len(initial)),
		journeys:           make(map[string][]JourneyEvent),
		gateStates:         make(map[string]GateState),
		pipelineStages:     make(map[string]PipelineStage),
		premortCycleCounts: make(map[string]int),
		nextID:             1,
	}
	for _, bead := range initial {
		store.beads[bead.ID] = cloneBead(bead)
	}
	return store
}

// SetBeads replaces the store contents with the supplied beads.
//
//oro:testonly
func (s *FakeStore) SetBeads(beads []protocol.Bead) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.beads = make(map[string]protocol.Bead, len(beads))
	for _, bead := range beads {
		if bead.Status == "" {
			bead.Status = "open"
		}
		if strings.TrimSpace(bead.AcceptanceCriteria) == "" {
			bead.AcceptanceCriteria = "Test: auto | Assert: PASS"
		}
		s.beads[bead.ID] = cloneBead(bead)
	}
}

// ClosedBeads returns bead IDs closed through Close, in call order.
//
//oro:testonly
func (s *FakeStore) ClosedBeads() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, len(s.closed))
	copy(out, s.closed)
	return out
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
		if bead.WorkerID != "" {
			continue
		}
		if bead.Status == "blocked" ||
			(bead.Status == "open" && (!isFutureDeferred(bead.DeferUntil) || s.hasHardBlockerLocked(bead)) && s.hasActiveBlockerLocked(bead)) {
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
	if err := validateUpdateParams(params); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[id]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if !applyBeadFields(&bead, params) {
		return nil
	}
	bead.UpdatedAt = nowString()
	s.beads[id] = bead
	return nil
}

// validateUpdateParams returns an error for semantically invalid fields.
func validateUpdateParams(params UpdateParams) error {
	if params.Status != nil && !validStatus(*params.Status) {
		return fmt.Errorf("beadstore: invalid status %q", *params.Status)
	}
	return nil
}

// applyBeadFields applies non-nil fields from params to bead.
// Returns true when at least one field changed.
func applyBeadFields(bead *protocol.Bead, params UpdateParams) bool {
	changed := false
	if params.Status != nil {
		applyStatusUpdate(bead, *params.Status)
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
	if params.Tags != nil {
		bead.Tags = cloneStrings(*params.Tags)
		changed = true
	}
	return changed
}

func applyStatusUpdate(bead *protocol.Bead, status string) {
	bead.Status = status
	switch status {
	case "open":
		bead.DeferUntil = ""
		bead.ClosedAt = ""
		bead.CloseReason = ""
	case "blocked":
		bead.DeferUntil = ""
		bead.ClosedAt = ""
		bead.CloseReason = ""
	case "in_progress":
		bead.ClosedAt = ""
		bead.CloseReason = ""
	case "closed":
		if bead.ClosedAt == "" {
			bead.ClosedAt = nowString()
		}
	}
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
	s.closed = append(s.closed, id)
	return nil
}

// AddDependency records a dependency edge for an existing bead.
func (s *FakeStore) AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("add dependency context: %w", err)
	}
	if strings.TrimSpace(depType) == "" {
		depType = "blocks"
	}
	if beadID == dependsOnID {
		return fmt.Errorf("beadstore: dependency cannot point to itself")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[beadID]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: beadID}
	}
	if _, ok := s.beads[dependsOnID]; !ok {
		return &protocol.BeadNotFoundError{BeadID: dependsOnID}
	}
	for _, dep := range bead.Dependencies {
		if dep.DependsOnID == dependsOnID && dep.Type == depType {
			return nil
		}
	}
	bead.Dependencies = append(bead.Dependencies, protocol.Dependency{
		IssueID:     beadID,
		DependsOnID: dependsOnID,
		Type:        depType,
	})
	bead.UpdatedAt = nowString()
	s.beads[beadID] = bead
	return nil
}

// RemoveDependency removes dependency edges from beadID to dependsOnID.
func (s *FakeStore) RemoveDependency(ctx context.Context, beadID, dependsOnID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("remove dependency context: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bead, ok := s.beads[beadID]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: beadID}
	}
	filtered := bead.Dependencies[:0]
	for _, dep := range bead.Dependencies {
		if dep.DependsOnID != dependsOnID {
			filtered = append(filtered, dep)
		}
	}
	bead.Dependencies = cloneDependencies(filtered)
	bead.UpdatedAt = nowString()
	s.beads[beadID] = bead
	return nil
}

// ListDependencies returns dependency edges recorded for beadID.
func (s *FakeStore) ListDependencies(ctx context.Context, beadID string) ([]protocol.Dependency, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list dependency context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bead, ok := s.beads[beadID]
	if !ok {
		return nil, &protocol.BeadNotFoundError{BeadID: beadID}
	}
	return cloneDependencies(bead.Dependencies), nil
}

// CountByStatus returns counts for open, in-progress, and closed beads.
func (s *FakeStore) CountByStatus(ctx context.Context) (StatusCounts, error) {
	if err := ctx.Err(); err != nil {
		return StatusCounts{}, fmt.Errorf("count status context: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var counts StatusCounts
	for _, bead := range s.beads {
		switch {
		case bead.Status == "closed":
			counts.Closed++
		case bead.Status == "in_progress" || bead.WorkerID != "":
			counts.InProgress++
		case bead.Status == "open" || bead.Status == "blocked":
			counts.Open++
		}
	}
	return counts, nil
}

// Defer marks a bead deferred until the supplied timestamp.
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

// Undefer clears a bead's defer timestamp.
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

// CountChildren returns the number of non-deleted child beads for parentID.
func (s *FakeStore) CountChildren(ctx context.Context, epicID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("count children context: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, bead := range s.beads {
		if bead.Epic == epicID {
			n++
		}
	}
	return n, nil
}

// GateState returns the current gate_state for beadID. Returns GateNone when
// no gate state has been set, matching the SQL schema's DEFAULT 'none'.
func (s *FakeStore) GateState(_ context.Context, beadID string) (GateState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeGate(s.gateStates[beadID]), nil
}

// HasClosedPremortemChild reports whether parentID has a closed child of type="premortem".
func (s *FakeStore) HasClosedPremortemChild(ctx context.Context, parentID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("has closed premortem child context: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, bead := range s.beads {
		if bead.Epic == parentID && bead.Type == "premortem" && bead.Status == "closed" {
			return true, nil
		}
	}
	return false, nil
}

// IncrPremortCycleCount increments the premortem_cycle_count for beadID by 1.
func (s *FakeStore) IncrPremortCycleCount(_ context.Context, beadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.premortCycleCounts[beadID]++
	return nil
}

// PremortCycleCount returns the current premortem cycle count for beadID (test helper).
//
//oro:testonly
func (s *FakeStore) PremortCycleCount(beadID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.premortCycleCounts[beadID]
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

func (s *FakeStore) hasHardBlockerLocked(bead protocol.Bead) bool {
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
	until = strings.TrimSpace(until)
	if until == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return true
	}
	return time.Now().UTC().Before(t)
}

func sortBeads(beads []protocol.Bead) {
	sort.Slice(beads, func(i, j int) bool {
		if beads[i].Priority != beads[j].Priority {
			return beads[i].Priority < beads[j].Priority
		}
		return beads[i].ID < beads[j].ID
	})
}

// AppendJourney appends a single event to beadID's in-memory journey log.
func (s *FakeStore) AppendJourney(_ context.Context, beadID string, evt JourneyEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	evt.BeadID = beadID
	s.journeys[beadID] = append(s.journeys[beadID], evt)
	return nil
}

// Journey returns events for beadID with ts >= since in ascending order.
func (s *FakeStore) Journey(_ context.Context, beadID string, since time.Time) ([]JourneyEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sinceStr := since.UTC().Format(time.RFC3339Nano)
	var out []JourneyEvent
	for _, e := range s.journeys[beadID] {
		if e.Ts >= sinceStr {
			out = append(out, e)
		}
	}
	return out, nil
}

// LatestJourney returns the most recent limit events for beadID in ascending order.
func (s *FakeStore) LatestJourney(_ context.Context, beadID string, limit int) ([]JourneyEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.journeys[beadID]
	if limit <= 0 || len(all) == 0 {
		return nil, nil
	}
	start := len(all) - limit
	if start < 0 {
		start = 0
	}
	out := make([]JourneyEvent, len(all)-start)
	copy(out, all[start:])
	return out, nil
}

// normalizeGate maps GateState("") to GateNone so FakeStore's zero-value
// map entries behave consistently with SQLite's DEFAULT 'none'.
func normalizeGate(gs GateState) GateState {
	if gs == GateState("") {
		return GateNone
	}
	return gs
}

// SetGateState atomically transitions beadID's gate state and records the
// change. Returns ErrStaleGate if the current state does not equal from.
// GateState("") and GateNone are treated as equivalent (both mean "not yet set").
func (s *FakeStore) SetGateState(_ context.Context, beadID string, from, to GateState, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := normalizeGate(s.gateStates[beadID])
	if cur != normalizeGate(from) {
		return ErrStaleGate
	}
	s.gateStates[beadID] = to
	payload := fmt.Sprintf(`{"from":%q,"to":%q,"reason":%q}`, from, to, reason)
	s.journeys[beadID] = append(s.journeys[beadID], JourneyEvent{
		BeadID:  beadID,
		Ts:      nowString(),
		Actor:   "dispatcher",
		Event:   "gate_state_changed",
		Payload: payload,
	})
	return nil
}

// SetPremortemVerdict persists a premortem agent's verdict (§11.4) on
// beadID by writing the bead's Metadata map. Existing values for the
// premortem_verdict and premortem_reason keys are overwritten; other keys
// are preserved.
func (s *FakeStore) SetPremortemVerdict(_ context.Context, beadID, verdict, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bead, ok := s.beads[beadID]
	if !ok {
		return &protocol.BeadNotFoundError{BeadID: beadID}
	}
	if bead.Metadata == nil {
		bead.Metadata = map[string]any{}
	}
	bead.Metadata["premortem_verdict"] = verdict
	bead.Metadata["premortem_reason"] = reason
	bead.UpdatedAt = nowString()
	s.beads[beadID] = bead
	return nil
}

// TransitionPipelineStage atomically transitions beadID's pipeline stage.
// Returns ErrStaleStage if the current stage does not equal from.
func (s *FakeStore) TransitionPipelineStage(_ context.Context, beadID string, from, to PipelineStage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.pipelineStages[beadID]
	if cur != from {
		return ErrStaleStage
	}
	s.pipelineStages[beadID] = to
	payload := fmt.Sprintf(`{"from":%q,"to":%q}`, from, to)
	s.journeys[beadID] = append(s.journeys[beadID], JourneyEvent{
		BeadID:  beadID,
		Ts:      nowString(),
		Actor:   "dispatcher",
		Event:   "pipeline_stage_changed",
		Payload: payload,
	})
	return nil
}

// WithReadTx executes fn with a ReadTx that delegates to the FakeStore.
// Cards() returns nil because FakeStore has no card store.
//
//oro:testonly
func (s *FakeStore) WithReadTx(_ context.Context, fn func(tx ReadTx) error) error {
	return fn(&fakeReadTx{s: s})
}

// fakeReadTx is a thin adapter so FakeStore can satisfy the ReadTx interface.
//
//oro:testonly
type fakeReadTx struct{ s *FakeStore }

// Ready implements ReadTx.
func (r *fakeReadTx) Ready(ctx context.Context) ([]protocol.Bead, error) {
	return r.s.Ready(ctx)
}

// InProgress implements ReadTx.
func (r *fakeReadTx) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	return r.s.InProgress(ctx)
}

// Blocked implements ReadTx.
func (r *fakeReadTx) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	return r.s.Blocked(ctx)
}

// Closed implements ReadTx.
func (r *fakeReadTx) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	return r.s.Closed(ctx, limit)
}

// Show implements ReadTx.
func (r *fakeReadTx) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	return r.s.Show(ctx, id)
}

// HasChildren implements ReadTx.
func (r *fakeReadTx) HasChildren(ctx context.Context, epicID string) (bool, error) {
	return r.s.HasChildren(ctx, epicID)
}

// AllChildrenClosed implements ReadTx.
func (r *fakeReadTx) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	return r.s.AllChildrenClosed(ctx, epicID)
}

// FindByParentAndTag implements ReadTx.
func (r *fakeReadTx) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	return r.s.FindByParentAndTag(ctx, parentID, tag)
}

// Journey implements ReadTx.
func (r *fakeReadTx) Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error) {
	return r.s.Journey(ctx, beadID, since)
}

// LatestJourney implements ReadTx.
func (r *fakeReadTx) LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error) {
	return r.s.LatestJourney(ctx, beadID, limit)
}

// Cards implements ReadTx. Returns nil because FakeStore has no card store.
// TODO(oro-6v7p): plumb a fake cards.ReadTx so render-path tests using
// tx.Cards() do not nil-panic.
func (r *fakeReadTx) Cards() cards.ReadTx { return nil }

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
