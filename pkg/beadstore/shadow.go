package beadstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"time"

	"oro/pkg/protocol"
)

const shadowStartedAtKey = "beadstore_shadow_started_at"

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
//
//oro:testonly
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

type dependencyWriter interface {
	AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error
}

type dependencyRemover interface {
	RemoveDependency(ctx context.Context, beadID, dependsOnID string) error
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

// LoadOrInitShadowStartedAt returns the persisted shadow validation window,
// creating it once when shadow mode is first enabled for a dispatcher DB.
func LoadOrInitShadowStartedAt(ctx context.Context, db *sql.DB) (time.Time, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM kv_store WHERE key = ?`, shadowStartedAtKey).Scan(&raw)
	if err == nil {
		startedAt, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			return time.Time{}, fmt.Errorf("parse %s: %w", shadowStartedAtKey, parseErr)
		}
		return startedAt, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("load %s: %w", shadowStartedAtKey, err)
	}

	now := time.Now().UTC()
	formatted := now.Format(time.RFC3339Nano)
	result, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO kv_store (key, value, updated_at) VALUES (?, ?, ?)`,
		shadowStartedAtKey, formatted, formatted,
	)
	if err != nil {
		return time.Time{}, fmt.Errorf("initialize %s: %w", shadowStartedAtKey, err)
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 1 {
		return now, nil
	}

	err = db.QueryRowContext(ctx, `SELECT value FROM kv_store WHERE key = ?`, shadowStartedAtKey).Scan(&raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("reload %s: %w", shadowStartedAtKey, err)
	}
	startedAt, parseErr := time.Parse(time.RFC3339Nano, raw)
	if parseErr != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", shadowStartedAtKey, parseErr)
	}
	return startedAt, nil
}

// Ready returns primary's ready beads after comparing the secondary read.
func (s *ShadowStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.Ready(ctx)
	secondary, secondaryErr := s.secondary.Ready(ctx)
	s.compareBeads(ctx, "Ready", primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("read ready beads", primaryErr)
}

// InProgress returns primary's active beads after comparing the secondary read.
func (s *ShadowStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.InProgress(ctx)
	secondary, secondaryErr := s.secondary.InProgress(ctx)
	s.compareBeads(ctx, "InProgress", primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("read in-progress beads", primaryErr)
}

// Blocked returns primary's blocked beads after comparing the secondary read.
func (s *ShadowStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.Blocked(ctx)
	secondary, secondaryErr := s.secondary.Blocked(ctx)
	s.compareBeads(ctx, "Blocked", primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("read blocked beads", primaryErr)
}

// Closed returns primary's recently closed beads after comparing the secondary read.
func (s *ShadowStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.Closed(ctx, limit)
	secondary, secondaryErr := s.secondary.Closed(ctx, limit)
	s.compareBeads(ctx, "Closed", primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("read closed beads", primaryErr)
}

// Show returns primary's bead after comparing the secondary read.
func (s *ShadowStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	primary, primaryErr := s.primary.Show(ctx, id)
	secondary, secondaryErr := s.secondary.Show(ctx, id)
	s.compareShown(primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("show bead", primaryErr)
}

// Create writes to primary only.
func (s *ShadowStore) Create(ctx context.Context, params CreateParams) (*protocol.Bead, error) {
	bead, err := s.primary.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("shadow primary create bead: %w", err)
	}
	return bead, nil
}

// Update writes to primary only.
func (s *ShadowStore) Update(ctx context.Context, id string, params UpdateParams) error {
	if err := s.primary.Update(ctx, id, params); err != nil {
		return fmt.Errorf("shadow primary update bead: %w", err)
	}
	return nil
}

// UpdateStatusIf conditionally writes to the primary store only.
func (s *ShadowStore) UpdateStatusIf(ctx context.Context, id, expected, next string) (bool, error) {
	updated, err := s.primary.UpdateStatusIf(ctx, id, expected, next)
	if err != nil {
		return false, fmt.Errorf("shadow primary conditionally update bead status: %w", err)
	}
	return updated, nil
}

// UpdateStatusIfConn conditionally writes to a connection-aware primary.
func (s *ShadowStore) UpdateStatusIfConn(ctx context.Context, conn *sql.Conn, id, expected, next string) (bool, error) {
	primary, ok := s.primary.(interface {
		UpdateStatusIfConn(context.Context, *sql.Conn, string, string, string) (bool, error)
	})
	if !ok {
		return false, errors.New("shadow primary does not support connection-aware status updates")
	}
	updated, err := primary.UpdateStatusIfConn(ctx, conn, id, expected, next)
	if err != nil {
		return false, fmt.Errorf("shadow primary conditionally update bead status: %w", err)
	}
	return updated, nil
}

// Close writes to primary only.
func (s *ShadowStore) Close(ctx context.Context, id, reason string) error {
	if err := s.primary.Close(ctx, id, reason); err != nil {
		return fmt.Errorf("shadow primary close bead: %w", err)
	}
	return nil
}

// Delete writes to primary only.
func (s *ShadowStore) Delete(ctx context.Context, id, reason string) error {
	if err := s.primary.Delete(ctx, id, reason); err != nil {
		return fmt.Errorf("shadow primary delete bead: %w", err)
	}
	return nil
}

// Defer writes to primary only when the primary store supports deferred beads.
func (s *ShadowStore) Defer(ctx context.Context, id, until string) error {
	primary, ok := s.primary.(deferredStore)
	if !ok {
		return fmt.Errorf("primary store does not support defer")
	}
	if err := primary.Defer(ctx, id, until); err != nil {
		return fmt.Errorf("shadow primary defer bead: %w", err)
	}
	return nil
}

// Undefer writes to primary only when the primary store supports deferred beads.
func (s *ShadowStore) Undefer(ctx context.Context, id string) error {
	primary, ok := s.primary.(deferredStore)
	if !ok {
		return fmt.Errorf("primary store does not support undefer")
	}
	if err := primary.Undefer(ctx, id); err != nil {
		return fmt.Errorf("shadow primary undefer bead: %w", err)
	}
	return nil
}

// AddDependency writes to primary only when the primary store supports
// dependency edges.
func (s *ShadowStore) AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error {
	primary, ok := s.primary.(dependencyWriter)
	if !ok {
		return fmt.Errorf("primary store does not support dependencies")
	}
	if err := primary.AddDependency(ctx, beadID, dependsOnID, depType); err != nil {
		return fmt.Errorf("shadow primary add dependency: %w", err)
	}
	return nil
}

// RemoveDependency writes to primary only when the primary store supports
// dependency edges.
func (s *ShadowStore) RemoveDependency(ctx context.Context, beadID, dependsOnID string) error {
	primary, ok := s.primary.(dependencyRemover)
	if !ok {
		return fmt.Errorf("primary store does not support dependencies")
	}
	if err := primary.RemoveDependency(ctx, beadID, dependsOnID); err != nil {
		return fmt.Errorf("shadow primary remove dependency: %w", err)
	}
	return nil
}

// HasChildren returns primary's answer after comparing the secondary read.
func (s *ShadowStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	primary, primaryErr := s.primary.HasChildren(ctx, epicID)
	secondary, secondaryErr := s.secondary.HasChildren(ctx, epicID)
	s.compareAggregateValue(ctx, "HasChildren", epicID, primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("check children", primaryErr)
}

// AllChildrenClosed returns primary's answer after comparing the secondary read.
func (s *ShadowStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	primary, primaryErr := s.primary.AllChildrenClosed(ctx, epicID)
	secondary, secondaryErr := s.secondary.AllChildrenClosed(ctx, epicID)
	s.compareAggregateValue(ctx, "AllChildrenClosed", epicID, primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("check child closure", primaryErr)
}

// FindByParentAndTag returns primary's children after comparing the secondary read.
func (s *ShadowStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	primary, primaryErr := s.primary.FindByParentAndTag(ctx, parentID, tag)
	secondary, secondaryErr := s.secondary.FindByParentAndTag(ctx, parentID, tag)
	s.compareBeads(ctx, "FindByParentAndTag", primary, primaryErr, secondary, secondaryErr)
	return primary, wrapPrimaryStoreError("find children by tag", primaryErr)
}

// FindByMetadataKey returns primary's matching beads after comparing the secondary read.
func (s *ShadowStore) FindByMetadataKey(ctx context.Context, key string) ([]*protocol.Bead, error) {
	primary, primaryErr := s.primary.FindByMetadataKey(ctx, key)
	secondary, secondaryErr := s.secondary.FindByMetadataKey(ctx, key)
	s.compareBeads(ctx, "FindByMetadataKey", beadValues(primary), primaryErr, beadValues(secondary), secondaryErr)
	return primary, wrapPrimaryStoreError("find beads by metadata key", primaryErr)
}

// Export returns primary's JSONL snapshot after comparing the secondary read.
func (s *ShadowStore) Export(ctx context.Context) ([]byte, error) {
	primary, primaryErr := s.primary.Export(ctx)
	secondary, secondaryErr := s.secondary.Export(ctx)
	if primaryErr != nil || secondaryErr != nil {
		s.report("Export", ShadowDivergenceReal, "read error")
		return primary, wrapPrimaryStoreError("export beads", primaryErr)
	}
	if !bytes.Equal(primary, secondary) {
		primaryBeads, decodedPrimary := decodeExportBeadsForCompare(primary)
		secondaryBeads, decodedSecondary := decodeExportBeadsForCompare(secondary)
		if !decodedPrimary || !decodedSecondary {
			s.report("Export", ShadowDivergenceReal, "export decode error")
			return primary, nil
		}
		kind := ClassifyShadowDivergence(primaryBeads, secondaryBeads, s.shadowStartedAt)
		if kind != ShadowDivergenceNone {
			s.report("Export", kind, "export mismatch")
		}
	}
	return primary, nil
}

// AppendJourney delegates to primary only; journey data is not shadow-compared.
func (s *ShadowStore) AppendJourney(ctx context.Context, beadID string, evt JourneyEvent) error {
	return wrapPrimaryStoreError("append journey", s.primary.AppendJourney(ctx, beadID, evt))
}

// Journey delegates to primary only.
func (s *ShadowStore) Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error) {
	events, err := s.primary.Journey(ctx, beadID, since)
	return events, wrapPrimaryStoreError("journey", err)
}

// LatestJourney delegates to primary only.
func (s *ShadowStore) LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error) {
	events, err := s.primary.LatestJourney(ctx, beadID, limit)
	return events, wrapPrimaryStoreError("latest journey", err)
}

// TransitionPipelineStage delegates to primary only.
func (s *ShadowStore) TransitionPipelineStage(ctx context.Context, beadID string, from, to PipelineStage) error {
	return wrapPrimaryStoreError("transition pipeline stage", s.primary.TransitionPipelineStage(ctx, beadID, from, to))
}

// CountChildren delegates to primary only.
func (s *ShadowStore) CountChildren(ctx context.Context, parentID string) (int, error) {
	n, err := s.primary.CountChildren(ctx, parentID)
	return n, wrapPrimaryStoreError("count children", err)
}

// DependencyCycles delegates to primary only.
func (s *ShadowStore) DependencyCycles(ctx context.Context) ([]Cycle, error) {
	cycles, err := s.primary.DependencyCycles(ctx)
	return cycles, wrapPrimaryStoreError("dependency cycles", err)
}

// WithReadTx delegates to primary so reads see a consistent snapshot from the
// authoritative store. Errors returned from fn are passed through unwrapped —
// only BeginTx/Commit failures are framed as primary-store malfunctions.
func (s *ShadowStore) WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error {
	var closureErr error
	err := s.primary.WithReadTx(ctx, func(tx ReadTx) error {
		closureErr = fn(tx)
		return closureErr
	})
	if closureErr != nil {
		return closureErr
	}
	return wrapPrimaryStoreError("with read tx", err)
}

func wrapPrimaryStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("shadow primary %s: %w", operation, err)
}

func (s *ShadowStore) compareBeads(ctx context.Context, op string, primary []protocol.Bead, primaryErr error, secondary []protocol.Bead, secondaryErr error) {
	if primaryErr != nil || secondaryErr != nil {
		s.report(op, ShadowDivergenceReal, "read error")
		return
	}
	kind := ClassifyShadowDivergenceWithResolver(primary, secondary, s.shadowStartedAt, func(id string) (*protocol.Bead, error) {
		bead, err := s.primary.Show(ctx, id)
		if err != nil {
			return nil, wrapPrimaryStoreError("resolve primary bead", err)
		}
		return bead, nil
	})
	if kind != ShadowDivergenceNone {
		s.report(op, kind, "bead result mismatch")
	}
}

func beadValues(beads []*protocol.Bead) []protocol.Bead {
	values := make([]protocol.Bead, len(beads))
	for i, bead := range beads {
		if bead != nil {
			values[i] = *bead
		}
	}
	return values
}

func (s *ShadowStore) compareShown(primary *protocol.Bead, primaryErr error, secondary *protocol.Bead, secondaryErr error) {
	if primaryErr != nil || secondaryErr != nil {
		s.report("Show", ShadowDivergenceReal, "read error")
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
	s.report("Show", ClassifyShadowDivergence(primarySlice, secondarySlice, s.shadowStartedAt), "show result mismatch")
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

func (s *ShadowStore) compareAggregateValue(ctx context.Context, op, parentID string, primary bool, primaryErr error, secondary bool, secondaryErr error) {
	if primaryErr != nil || secondaryErr != nil {
		s.report(op, ShadowDivergenceReal, "read error")
		return
	}
	if primary == secondary {
		return
	}

	primaryChildren, primaryChildrenErr := s.childrenForAggregate(ctx, s.primary, parentID)
	secondaryChildren, secondaryChildrenErr := s.childrenForAggregate(ctx, s.secondary, parentID)
	if primaryChildrenErr != nil || secondaryChildrenErr != nil {
		s.report(op, ShadowDivergenceReal, "aggregate child read error")
		return
	}
	kind := ClassifyShadowDivergenceWithResolver(primaryChildren, secondaryChildren, s.shadowStartedAt, func(id string) (*protocol.Bead, error) {
		bead, err := s.primary.Show(ctx, id)
		if err != nil {
			return nil, wrapPrimaryStoreError("resolve primary aggregate child", err)
		}
		return bead, nil
	})
	if kind == ShadowDivergenceNone {
		kind = ShadowDivergenceReal
	}
	s.report(op, kind, "aggregate result mismatch")
}

func (s *ShadowStore) childrenForAggregate(ctx context.Context, store Store, parentID string) ([]protocol.Bead, error) {
	data, err := store.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("export aggregate children: %w", err)
	}
	beads, err := decodeExportBeads(data)
	if err != nil {
		return nil, fmt.Errorf("decode aggregate children: %w", err)
	}
	children := make([]protocol.Bead, 0)
	for _, bead := range beads {
		if bead.Epic == parentID {
			children = append(children, bead)
		}
	}
	return children, nil
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
	var rawArray []shadowBeadJSON
	if err := json.Unmarshal(data, &rawArray); err == nil {
		beads := make([]protocol.Bead, len(rawArray))
		for i, bead := range rawArray {
			beads[i] = bead.toProtocol()
		}
		return beads, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	var beads []protocol.Bead
	for {
		var raw shadowBeadJSON
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode exported bead: %w", err)
		}
		beads = append(beads, raw.toProtocol())
	}
	return beads, nil
}

func decodeExportBeadsForCompare(data []byte) ([]protocol.Bead, bool) {
	beads, err := decodeExportBeads(data)
	if err != nil {
		return nil, false
	}
	return beads, true
}

type shadowBeadJSON struct {
	protocol.Bead
	ParentID string `json:"parent_id"`
	TypeName string `json:"type"`
}

func (b shadowBeadJSON) toProtocol() protocol.Bead {
	bead := b.Bead
	if bead.Epic == "" {
		bead.Epic = b.ParentID
	}
	if bead.Type == "" {
		bead.Type = b.TypeName
	}
	return bead
}
