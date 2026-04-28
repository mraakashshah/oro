package beadstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"oro/pkg/protocol"
)

// ShadowDivergenceKind classifies a mismatch between primary and secondary reads.
type ShadowDivergenceKind string

const (
	// ShadowDivergenceNone means both read paths returned equivalent data.
	ShadowDivergenceNone ShadowDivergenceKind = "none"
	// ShadowDivergenceReal means the secondary disagreed on data it should match.
	ShadowDivergenceReal ShadowDivergenceKind = "real"
	// ShadowDivergenceDrift means the mismatch is expected because writes go only to primary.
	ShadowDivergenceDrift ShadowDivergenceKind = "drift"
)

// ShadowDivergence records one observed mismatch from a shadow read.
type ShadowDivergence struct {
	Operation string
	Kind      ShadowDivergenceKind
	Reason    string
}

// ShadowStoreOption configures a ShadowStore.
type ShadowStoreOption func(*ShadowStore)

// WithShadowStartedAt sets the boundary used to separate structural mismatches
// from drift caused by primary-only writes during shadow validation.
func WithShadowStartedAt(startedAt time.Time) ShadowStoreOption {
	return func(s *ShadowStore) {
		s.shadowStartedAt = startedAt
	}
}

// WithShadowDivergenceReporter observes classified read divergences.
func WithShadowDivergenceReporter(reporter func(ShadowDivergence)) ShadowStoreOption {
	return func(s *ShadowStore) {
		s.reporter = reporter
	}
}

// WithShadowLogger logs classified read divergences.
func WithShadowLogger(logger *slog.Logger) ShadowStoreOption {
	return func(s *ShadowStore) {
		s.logger = logger
	}
}

// ShadowStore validates reads against a secondary Store while keeping primary
// authoritative for returned values and all writes.
type ShadowStore struct {
	primary         Store
	secondary       Store
	shadowStartedAt time.Time
	reporter        func(ShadowDivergence)
	logger          *slog.Logger
}

type deferredStore interface {
	Defer(ctx context.Context, id, until string) error
	Undefer(ctx context.Context, id string) error
}

// NewShadowStore returns a Store that dual-reads primary and secondary stores.
func NewShadowStore(primary, secondary Store, opts ...ShadowStoreOption) *ShadowStore {
	store := &ShadowStore{
		primary:         primary,
		secondary:       secondary,
		shadowStartedAt: time.Now(),
	}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

// Ready returns primary's ready beads after comparing the secondary read.
func (s *ShadowStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.Ready(ctx)
	secondary, secondaryErr := s.secondary.Ready(ctx)
	s.compareBeads(ctx, "Ready", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// InProgress returns primary's active beads after comparing the secondary read.
func (s *ShadowStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.InProgress(ctx)
	secondary, secondaryErr := s.secondary.InProgress(ctx)
	s.compareBeads(ctx, "InProgress", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// Blocked returns primary's blocked beads after comparing the secondary read.
func (s *ShadowStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.Blocked(ctx)
	secondary, secondaryErr := s.secondary.Blocked(ctx)
	s.compareBeads(ctx, "Blocked", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// Closed returns primary's recently closed beads after comparing the secondary read.
func (s *ShadowStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.Closed(ctx, limit)
	secondary, secondaryErr := s.secondary.Closed(ctx, limit)
	s.compareBeads(ctx, "Closed", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// Show returns primary's bead after comparing the secondary read.
func (s *ShadowStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	primary, primaryErr := s.primary.Show(ctx, id)
	secondary, secondaryErr := s.secondary.Show(ctx, id)
	s.compareShown("Show", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// Create writes to primary only.
func (s *ShadowStore) Create(ctx context.Context, params CreateParams) (*protocol.Bead, error) {
	return s.primary.Create(ctx, params)
}

// Update writes to primary only.
func (s *ShadowStore) Update(ctx context.Context, id string, params UpdateParams) error {
	return s.primary.Update(ctx, id, params)
}

// Close writes to primary only.
func (s *ShadowStore) Close(ctx context.Context, id, reason string) error {
	return s.primary.Close(ctx, id, reason)
}

// Defer writes to primary only when the primary store supports deferred beads.
func (s *ShadowStore) Defer(ctx context.Context, id, until string) error {
	primary, ok := s.primary.(deferredStore)
	if !ok {
		return fmt.Errorf("primary store does not support defer")
	}
	return primary.Defer(ctx, id, until)
}

// Undefer writes to primary only when the primary store supports deferred beads.
func (s *ShadowStore) Undefer(ctx context.Context, id string) error {
	primary, ok := s.primary.(deferredStore)
	if !ok {
		return fmt.Errorf("primary store does not support undefer")
	}
	return primary.Undefer(ctx, id)
}

// HasChildren returns primary's answer after comparing the secondary read.
func (s *ShadowStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	primary, primaryErr := s.primary.HasChildren(ctx, epicID)
	secondary, secondaryErr := s.secondary.HasChildren(ctx, epicID)
	s.compareValue("HasChildren", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// AllChildrenClosed returns primary's answer after comparing the secondary read.
func (s *ShadowStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	primary, primaryErr := s.primary.AllChildrenClosed(ctx, epicID)
	secondary, secondaryErr := s.secondary.AllChildrenClosed(ctx, epicID)
	s.compareValue("AllChildrenClosed", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// FindByParentAndTag returns primary's children after comparing the secondary read.
func (s *ShadowStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.FindByParentAndTag(ctx, parentID, tag)
	secondary, secondaryErr := s.secondary.FindByParentAndTag(ctx, parentID, tag)
	s.compareBeads(ctx, "FindByParentAndTag", primary, primaryErr, secondary, secondaryErr)
	return primary, primaryErr
}

// Export returns primary's JSONL snapshot after comparing the secondary read.
func (s *ShadowStore) Export(ctx context.Context) ([]byte, error) {
	primary, primaryErr := s.primary.Export(ctx)
	secondary, secondaryErr := s.secondary.Export(ctx)
	if primaryErr != nil || secondaryErr != nil {
		s.report("Export", ShadowDivergenceReal, "read error")
		return primary, primaryErr
	}
	if !bytes.Equal(primary, secondary) {
		primaryBeads, decodePrimaryErr := decodeExportBeads(primary)
		secondaryBeads, decodeSecondaryErr := decodeExportBeads(secondary)
		if decodePrimaryErr != nil || decodeSecondaryErr != nil {
			s.report("Export", ShadowDivergenceReal, "export decode error")
			return primary, primaryErr
		}
		kind := ClassifyShadowDivergence(primaryBeads, secondaryBeads, s.shadowStartedAt)
		if kind != ShadowDivergenceNone {
			s.report("Export", kind, "export mismatch")
		}
	}
	return primary, primaryErr
}

func (s *ShadowStore) compareBeads(ctx context.Context, op string, primary []protocol.Bead, primaryErr error, secondary []protocol.Bead, secondaryErr error) {
	if primaryErr != nil || secondaryErr != nil {
		s.report(op, ShadowDivergenceReal, "read error")
		return
	}
	kind := ClassifyShadowDivergenceWithResolver(primary, secondary, s.shadowStartedAt, func(id string) (*protocol.Bead, error) {
		return s.primary.Show(ctx, id)
	})
	if kind != ShadowDivergenceNone {
		s.report(op, kind, "bead result mismatch")
	}
}

func (s *ShadowStore) compareShown(op string, primary *protocol.Bead, primaryErr error, secondary *protocol.Bead, secondaryErr error) {
	if primaryErr != nil || secondaryErr != nil {
		s.report(op, ShadowDivergenceReal, "read error")
		return
	}
	if reflect.DeepEqual(primary, secondary) {
		return
	}

	var primarySlice []protocol.Bead
	if primary != nil {
		primarySlice = append(primarySlice, *primary)
	}
	var secondarySlice []protocol.Bead
	if secondary != nil {
		secondarySlice = append(secondarySlice, *secondary)
	}
	s.report(op, ClassifyShadowDivergence(primarySlice, secondarySlice, s.shadowStartedAt), "show result mismatch")
}

func (s *ShadowStore) compareValue(op string, primary any, primaryErr error, secondary any, secondaryErr error) {
	if primaryErr != nil || secondaryErr != nil {
		s.report(op, ShadowDivergenceReal, "read error")
		return
	}
	if !reflect.DeepEqual(primary, secondary) {
		s.report(op, ShadowDivergenceReal, "read result mismatch")
	}
}

func (s *ShadowStore) report(op string, kind ShadowDivergenceKind, reason string) {
	if kind == ShadowDivergenceNone {
		return
	}
	event := ShadowDivergence{
		Operation: op,
		Kind:      kind,
		Reason:    reason,
	}
	if s.reporter != nil {
		s.reporter(event)
	}
	if s.logger != nil {
		s.logger.Warn("beadstore shadow divergence", "operation", event.Operation, "kind", event.Kind, "reason", event.Reason)
	}
}

// ClassifyShadowDivergence partitions read mismatches into structural
// divergences and tolerated drift from primary-only writes during shadow mode.
func ClassifyShadowDivergence(primary, secondary []protocol.Bead, shadowStartedAt time.Time) ShadowDivergenceKind {
	return ClassifyShadowDivergenceWithResolver(primary, secondary, shadowStartedAt, nil)
}

// ClassifyShadowDivergenceWithResolver classifies result-set mismatches. The
// resolver is optional; when provided it lets filtered reads distinguish stale
// secondary rows from real secondary-only data by reading the current primary row.
func ClassifyShadowDivergenceWithResolver(primary, secondary []protocol.Bead, shadowStartedAt time.Time, resolvePrimary func(string) (*protocol.Bead, error)) ShadowDivergenceKind {
	if reflect.DeepEqual(primary, secondary) {
		return ShadowDivergenceNone
	}

	primaryByID := beadsByID(primary)
	secondaryByID := beadsByID(secondary)
	kind := ShadowDivergenceNone

	for id, primaryBead := range primaryByID {
		secondaryBead, ok := secondaryByID[id]
		if !ok {
			kind = mergeDivergence(kind, classifyPrimaryOnly(primaryBead, shadowStartedAt))
			continue
		}
		if reflect.DeepEqual(primaryBead, secondaryBead) {
			continue
		}
		if primaryNewerDuringShadow(primaryBead, secondaryBead, shadowStartedAt) {
			kind = mergeDivergence(kind, ShadowDivergenceDrift)
			continue
		}
		kind = mergeDivergence(kind, ShadowDivergenceReal)
	}

	for id := range secondaryByID {
		if _, ok := primaryByID[id]; !ok {
			kind = mergeDivergence(kind, classifySecondaryOnly(id, secondaryByID[id], shadowStartedAt, resolvePrimary))
		}
	}

	return kind
}

func beadsByID(beads []protocol.Bead) map[string]protocol.Bead {
	byID := make(map[string]protocol.Bead, len(beads))
	for _, bead := range beads {
		byID[bead.ID] = bead
	}
	return byID
}

func classifyPrimaryOnly(bead protocol.Bead, shadowStartedAt time.Time) ShadowDivergenceKind {
	if updatedInShadow(bead.UpdatedAt, shadowStartedAt) {
		return ShadowDivergenceDrift
	}
	return ShadowDivergenceReal
}

func classifySecondaryOnly(id string, bead protocol.Bead, shadowStartedAt time.Time, resolvePrimary func(string) (*protocol.Bead, error)) ShadowDivergenceKind {
	if resolvePrimary != nil {
		current, err := resolvePrimary(id)
		if err == nil && current != nil && updatedInShadow(current.UpdatedAt, shadowStartedAt) {
			return ShadowDivergenceDrift
		}
	}
	if updatedInShadow(bead.UpdatedAt, shadowStartedAt) {
		return ShadowDivergenceDrift
	}
	return ShadowDivergenceReal
}

func mergeDivergence(current, next ShadowDivergenceKind) ShadowDivergenceKind {
	if current == ShadowDivergenceReal || next == ShadowDivergenceReal {
		return ShadowDivergenceReal
	}
	if current == ShadowDivergenceDrift || next == ShadowDivergenceDrift {
		return ShadowDivergenceDrift
	}
	return ShadowDivergenceNone
}

func primaryNewerDuringShadow(primary, secondary protocol.Bead, shadowStartedAt time.Time) bool {
	primaryUpdatedAt, ok := parseShadowTime(primary.UpdatedAt)
	if !ok || primaryUpdatedAt.Before(shadowStartedAt) {
		return false
	}
	secondaryUpdatedAt, ok := parseShadowTime(secondary.UpdatedAt)
	if !ok {
		return false
	}
	return primaryUpdatedAt.After(secondaryUpdatedAt)
}

func updatedInShadow(raw string, shadowStartedAt time.Time) bool {
	updatedAt, ok := parseShadowTime(raw)
	if !ok {
		return false
	}
	return !updatedAt.Before(shadowStartedAt)
}

func parseShadowTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func decodeExportBeads(data []byte) ([]protocol.Bead, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var beads []protocol.Bead
	for scanner.Scan() {
		var bead protocol.Bead
		if err := json.Unmarshal(scanner.Bytes(), &bead); err != nil {
			return nil, fmt.Errorf("decode exported bead: %w", err)
		}
		beads = append(beads, bead)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan exported beads: %w", err)
	}
	return beads, nil
}
