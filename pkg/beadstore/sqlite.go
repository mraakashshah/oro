package beadstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

var _ Store = (*SQLiteStore)(nil)

// MemoryFetcher matches pkg/memory.ForPrompt's inputs without making beadstore
// import the memory package.
type MemoryFetcher func(ctx context.Context, tags []string, description string, maxTokens int) (string, error)

// Option configures a SQLiteStore.
type Option func(*SQLiteStore)

// WithMemoryFetcher configures runtime memory enrichment for shown beads.
//
//oro:testonly
func WithMemoryFetcher(fetch MemoryFetcher) Option {
	return func(s *SQLiteStore) {
		s.memory = fetch
	}
}

// SQLiteStore persists beads in a SQLite database.
type SQLiteStore struct {
	db           *sql.DB
	memory       MemoryFetcher
	memMaxTokens int
	writeMu      sync.Mutex
}

// OpenSQLiteStore opens a SQLite database, migrates the schema, and returns a store.
//
// Production code uses cmd/oro.openStateDB instead, which additionally applies
// native beadstore migrations. This helper stays for tests that only need the
// bead schema.
//
//oro:testonly
func OpenSQLiteStore(ctx context.Context, path string, opts ...Option) (*SQLiteStore, error) {
	db, err := dbutil.OpenDB(path)
	if err != nil {
		return nil, fmt.Errorf("beadstore: open sqlite db %q: %w", path, err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("beadstore: migrate sqlite schema %q: %w", path, err)
	}
	return NewSQLiteStore(db, opts...), nil
}

// NewSQLiteStore returns a store backed by db.
func NewSQLiteStore(db *sql.DB, opts ...Option) *SQLiteStore {
	store := &SQLiteStore{db: db, memMaxTokens: 2000}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

// Ready returns open beads with no active blockers or active assignment.
func (s *SQLiteStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads_ready ORDER BY priority ASC, created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	return s.filterUnassigned(ctx, beads)
}

// InProgress returns beads currently assigned or explicitly in progress.
func (s *SQLiteStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status='in_progress' ORDER BY updated_at DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	active, err := s.activeAssignments(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(beads))
	for i := range beads {
		seen[beads[i].ID] = struct{}{}
		if workerID := active[beads[i].ID]; workerID != "" {
			beads[i].WorkerID = workerID
		}
	}
	activeIDs := make([]string, 0, len(active))
	for beadID := range active {
		if _, ok := seen[beadID]; !ok {
			activeIDs = append(activeIDs, beadID)
		}
	}
	sort.Strings(activeIDs)
	for _, beadID := range activeIDs {
		assigned, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status!='closed' AND id=?`, beadID)
		if err != nil {
			return nil, err
		}
		if len(assigned) == 0 {
			continue
		}
		assigned[0].WorkerID = active[beadID]
		beads = append(beads, assigned[0])
	}
	return beads, nil
}

// Blocked returns open beads with active blockers and no active assignment.
func (s *SQLiteStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads_blocked ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	return s.filterUnassigned(ctx, beads)
}

// Closed returns recently closed beads, capped by limit.
func (s *SQLiteStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	if limit <= 0 {
		return []protocol.Bead{}, nil
	}
	return s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status='closed' ORDER BY closed_at DESC, updated_at DESC LIMIT ?`, limit)
}

// Show returns the bead for id, or nil when it does not exist.
func (s *SQLiteStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	return s.show(ctx, id, true)
}

func (s *SQLiteStore) show(ctx context.Context, id string, includeMemory bool) (*protocol.Bead, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(beads) == 0 {
		return nil, nil
	}
	bead := &beads[0]
	if err := s.enrichRuntime(ctx, bead); err != nil {
		return nil, err
	}
	if includeMemory && s.memory != nil {
		if memory, err := s.memory(ctx, bead.Tags, bead.Description, s.memMaxTokens); err == nil {
			bead.Memory = memory
		}
	}
	return bead, nil
}

// Create persists a new bead and returns its complete stored representation.
func (s *SQLiteStore) Create(ctx context.Context, params CreateParams) (*protocol.Bead, error) {
	params, err := normalizeCreateParams(params)
	if err != nil {
		return nil, err
	}
	now := nowString()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beadstore: begin create transaction: %w", err)
	}
	defer rollback(tx)

	var parent sql.NullString
	if params.ParentID != "" {
		if err := ensureBeadExists(ctx, tx, params.ParentID); err != nil {
			return nil, err
		}
		parent = sql.NullString{String: params.ParentID, Valid: true}
	}
	var estimate sql.NullInt64
	if params.EstimatedMinutes > 0 {
		estimate = sql.NullInt64{Int64: int64(params.EstimatedMinutes), Valid: true}
	}
	tier := sql.NullString{String: params.Tier, Valid: params.Tier != ""}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO beads (id, title, contract_version, draft, description, acceptance_criteria, status, priority, type, parent_id, estimated_minutes, tier, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		params.ID, params.Title, params.ContractVersion, params.Draft, params.Description, params.AcceptanceCriteria, params.Status, params.Priority, params.Type, parent, estimate, tier, now, now); err != nil {
		return nil, fmt.Errorf("beadstore: create bead %s: %w", params.ID, err)
	}
	if err := replaceStrings(ctx, tx, "bead_tags", "tag", params.ID, params.Tags); err != nil {
		return nil, err
	}
	if err := replaceStrings(ctx, tx, "bead_labels", "label", params.ID, params.Labels); err != nil {
		return nil, err
	}
	if err := replaceMetadata(ctx, tx, params.ID, params.Metadata); err != nil {
		return nil, err
	}
	if err := insertEvent(ctx, tx, "bead_created", params.ID, map[string]any{
		"type":     params.Type,
		"priority": params.Priority,
		"status":   params.Status,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("beadstore: commit create %s: %w", params.ID, err)
	}
	return s.show(ctx, params.ID, false)
}

// Update applies non-nil fields from params to id.
func (s *SQLiteStore) Update(ctx context.Context, id string, params UpdateParams) error {
	if params.Status != nil && !validStatus(*params.Status) {
		return fmt.Errorf("beadstore: invalid status %q", *params.Status)
	}
	if params.Draft != nil && !*params.Draft {
		return fmt.Errorf("beadstore: clearing draft requires validated publish")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin update transaction: %w", err)
	}
	defer rollback(tx)

	if params.ParentID != nil && *params.ParentID != "" {
		if err := ensureBeadExists(ctx, tx, *params.ParentID); err != nil {
			return err
		}
	}

	stmt := newUpdateStatement(params)
	stmt.args = append(stmt.args, id)
	// Fields in stmt.assignments come only from fixed literals in newUpdateStatement.
	res, err := tx.ExecContext(ctx, stmt.query(), stmt.args...) //nolint:gosec // fixed assignment fragments are selected by code, not caller input.
	if err != nil {
		return fmt.Errorf("beadstore: update %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("beadstore: update %s rows affected: %w", id, err)
	}
	if affected == 0 {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if err := applyUpdateSideEffects(ctx, tx, id, params); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, "bead_updated", id, updatePayload(params)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit update %s: %w", id, err)
	}
	return nil
}

// UpdateStatusIf atomically changes id from expected to next status.
func (s *SQLiteStore) UpdateStatusIf(ctx context.Context, id, expected, next string) (bool, error) {
	if !validStatus(next) {
		return false, fmt.Errorf("beadstore: invalid status %q", next)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE beads SET status = ? WHERE id = ? AND status = ?`, next, id, expected)
	if err != nil {
		return false, fmt.Errorf("beadstore: conditionally update status for %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("beadstore: conditionally update status for %s rows affected: %w", id, err)
	}
	return affected == 1, nil
}

// applyUpdateSideEffects applies tag and note side-effects inside an open
// transaction. Extracted to keep (*SQLiteStore).Update below the cyclomatic
// complexity limit.
func applyUpdateSideEffects(ctx context.Context, tx *sql.Tx, id string, params UpdateParams) error {
	if params.Tags != nil {
		if err := replaceStrings(ctx, tx, "bead_tags", "tag", id, *params.Tags); err != nil {
			return err
		}
	}
	if params.Notes != nil && strings.TrimSpace(*params.Notes) != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_notes (bead_id, author, content, created_at) VALUES (?, 'oro', ?, ?)`, id, *params.Notes, nowString()); err != nil {
			return fmt.Errorf("beadstore: add note %s: %w", id, err)
		}
	}
	return nil
}

// Close marks id closed with reason.
func (s *SQLiteStore) Close(ctx context.Context, id, reason string) error {
	now := nowString()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin close transaction: %w", err)
	}
	defer rollback(tx)

	res, err := tx.ExecContext(ctx, `
UPDATE beads
SET status='closed',
    close_reason=COALESCE(close_reason, ?),
    closed_at=COALESCE(closed_at, ?),
    updated_at=CASE WHEN status='closed' THEN updated_at ELSE ? END
WHERE id=? AND deleted=0`, reason, now, now, id)
	if err != nil {
		return fmt.Errorf("beadstore: close %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("beadstore: close %s rows affected: %w", id, err)
	}
	if affected == 0 {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if err := insertEvent(ctx, tx, "bead_closed", id, map[string]any{"reason": reason}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit close %s: %w", id, err)
	}
	return nil
}

// Delete soft-deletes a bead with the supplied reason.
func (s *SQLiteStore) Delete(ctx context.Context, id, reason string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("delete bead context: %w", err)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "deleted by user"
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin delete transaction: %w", err)
	}
	defer rollback(tx)

	if err := ensureDeletable(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bead_deps WHERE bead_id=? OR depends_on_id=?`, id, id); err != nil {
		return fmt.Errorf("delete bead %s dependency edges: %w", id, err)
	}

	now := nowString()
	res, err := tx.ExecContext(ctx, `
UPDATE beads
SET deleted=1, close_reason=?, updated_at=?
WHERE id=? AND deleted=0`, reason, now, id)
	if err != nil {
		return fmt.Errorf("delete bead %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete bead %s rows affected: %w", id, err)
	}
	if n == 0 {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if err := insertEvent(ctx, tx, "bead_deleted", id, map[string]any{"reason": reason}); err != nil {
		return err
	}
	if err := insertJourneyEvent(ctx, tx, id, JourneyEvent{
		Ts:      now,
		Actor:   "human",
		Event:   "deleted",
		Payload: mustJSON(map[string]any{"reason": reason}),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit delete %s: %w", id, err)
	}
	return nil
}

func ensureDeletable(ctx context.Context, tx *sql.Tx, id string) error {
	var activeAssignments int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, id).Scan(&activeAssignments); err != nil {
		return fmt.Errorf("delete bead %s active assignment check: %w", id, err)
	}
	if activeAssignments > 0 {
		return fmt.Errorf("delete bead %s: active assignment exists", id)
	}

	var children int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads WHERE parent_id=? AND deleted=0`, id).Scan(&children); err != nil {
		return fmt.Errorf("delete bead %s child check: %w", id, err)
	}
	if children > 0 {
		return fmt.Errorf("delete bead %s: recursive delete unsupported for bead with non-deleted children", id)
	}
	return nil
}

// AddDependency records a dependency edge from beadID to dependsOnID.
func (s *SQLiteStore) AddDependency(ctx context.Context, beadID, dependsOnID, depType string) error {
	depType = strings.TrimSpace(depType)
	if depType == "" {
		depType = "blocks"
	}
	if beadID == dependsOnID {
		return fmt.Errorf("beadstore: dependency cannot point to itself")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin add dependency transaction: %w", err)
	}
	defer rollback(tx)

	if err := ensureBeadExists(ctx, tx, beadID); err != nil {
		return err
	}
	if err := ensureBeadExists(ctx, tx, dependsOnID); err != nil {
		return err
	}
	if isBlockingDepType(depType) {
		graph, err := loadBlockingGraph(ctx, tx)
		if err != nil {
			return err
		}
		if path := reachablePath(graph, dependsOnID, beadID); len(path) > 0 {
			return &protocol.DependencyCycleError{
				BeadID:      beadID,
				DependsOnID: dependsOnID,
				Path:        append([]string{beadID}, path...),
			}
		}
	}
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO bead_deps (bead_id, depends_on_id, type, created_by)
VALUES (?, ?, ?, 'oro')`, beadID, dependsOnID, depType)
	if err != nil {
		return fmt.Errorf("beadstore: add dependency %s -> %s: %w", beadID, dependsOnID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("beadstore: count dependency insert %s -> %s: %w", beadID, dependsOnID, err)
	}
	if changed == 1 {
		if err := insertEvent(ctx, tx, "bead_dependency_added", beadID, map[string]any{
			"depends_on_id": dependsOnID,
			"type":          depType,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit add dependency %s -> %s: %w", beadID, dependsOnID, err)
	}
	return nil
}

// RemoveDependency deletes the dependency edge from beadID to dependsOnID.
func (s *SQLiteStore) RemoveDependency(ctx context.Context, beadID, dependsOnID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin remove dependency transaction: %w", err)
	}
	defer rollback(tx)

	if err := ensureBeadExists(ctx, tx, beadID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bead_deps WHERE bead_id=? AND depends_on_id=?`, beadID, dependsOnID); err != nil {
		return fmt.Errorf("beadstore: remove dependency %s -> %s: %w", beadID, dependsOnID, err)
	}
	if err := insertEvent(ctx, tx, "bead_dependency_removed", beadID, map[string]any{
		"depends_on_id": dependsOnID,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit remove dependency %s -> %s: %w", beadID, dependsOnID, err)
	}
	return nil
}

// ListDependencies returns dependencies for beadID.
func (s *SQLiteStore) ListDependencies(ctx context.Context, beadID string) ([]protocol.Dependency, error) {
	bead, err := s.Show(ctx, beadID)
	if err != nil {
		return nil, err
	}
	if bead == nil {
		return nil, &protocol.BeadNotFoundError{BeadID: beadID}
	}
	return bead.Dependencies, nil
}

// DependencyCycles returns cycles in active blocking dependencies.
func (s *SQLiteStore) DependencyCycles(ctx context.Context) ([]Cycle, error) {
	graph, err := loadBlockingGraph(ctx, s.db)
	if err != nil {
		return nil, err
	}
	return findCycles(graph), nil
}

// CountByStatus returns open, in-progress, and closed bead counts.
func (s *SQLiteStore) CountByStatus(ctx context.Context) (StatusCounts, error) {
	var counts StatusCounts
	err := s.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE
    WHEN b.status IN ('open','blocked')
      AND NOT EXISTS (
        SELECT 1 FROM assignments a
        WHERE a.bead_id = b.id
          AND a.status = 'active'
      )
    THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE
    WHEN b.status = 'in_progress'
      OR (
        b.status != 'closed'
        AND EXISTS (
          SELECT 1 FROM assignments a
          WHERE a.bead_id = b.id
            AND a.status = 'active'
        )
      )
    THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN b.status = 'closed' THEN 1 ELSE 0 END), 0)
FROM beads b
WHERE b.deleted = 0`).Scan(&counts.Open, &counts.InProgress, &counts.Closed)
	if err != nil {
		return StatusCounts{}, fmt.Errorf("beadstore: count statuses: %w", err)
	}
	return counts, nil
}

// Defer sets id's defer-until timestamp.
func (s *SQLiteStore) Defer(ctx context.Context, id, until string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin defer transaction: %w", err)
	}
	defer rollback(tx)

	res, err := tx.ExecContext(ctx, `
UPDATE beads
SET deferred_until=?, updated_at=?
WHERE id=? AND deleted=0`, until, nowString(), id)
	if err != nil {
		return fmt.Errorf("beadstore: defer %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("beadstore: defer %s rows affected: %w", id, err)
	}
	if affected == 0 {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if err := insertEvent(ctx, tx, "bead_deferred", id, map[string]any{"until": until}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit defer %s: %w", id, err)
	}
	return nil
}

// Undefer clears id's defer-until timestamp.
func (s *SQLiteStore) Undefer(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin undefer transaction: %w", err)
	}
	defer rollback(tx)

	res, err := tx.ExecContext(ctx, `
UPDATE beads
SET deferred_until=NULL, updated_at=?
WHERE id=? AND deleted=0`, nowString(), id)
	if err != nil {
		return fmt.Errorf("beadstore: undefer %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("beadstore: undefer %s rows affected: %w", id, err)
	}
	if affected == 0 {
		return &protocol.BeadNotFoundError{BeadID: id}
	}
	if err := insertEvent(ctx, tx, "bead_undeferred", id, map[string]any{}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit undefer %s: %w", id, err)
	}
	return nil
}

// HasChildren reports whether epicID has active child beads.
func (s *SQLiteStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads WHERE parent_id=? AND deleted=0`, epicID).Scan(&n); err != nil {
		return false, fmt.Errorf("beadstore: count children for %s: %w", epicID, err)
	}
	return n > 0, nil
}

// AllChildrenClosed reports whether epicID has no open child beads.
func (s *SQLiteStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	var total int
	var open sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN status!='closed' THEN 1 ELSE 0 END) FROM beads WHERE parent_id=? AND deleted=0`, epicID).Scan(&total, &open); err != nil {
		return false, fmt.Errorf("beadstore: count open children for %s: %w", epicID, err)
	}
	return total == 0 || !open.Valid || open.Int64 == 0, nil
}

// FindByParentAndTag returns children under parentID that have tag.
func (s *SQLiteStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	return s.queryBeads(ctx, `SELECT `+prefixedBeadColumns()+`
FROM beads b
JOIN bead_tags t ON t.bead_id=b.id AND t.tag=?
WHERE b.deleted=0 AND b.parent_id=?
ORDER BY b.priority ASC, b.created_at ASC`, tag, parentID)
}

// FindByMetadataKey returns every non-deleted bead that has key, regardless of status.
func (s *SQLiteStore) FindByMetadataKey(ctx context.Context, key string) ([]*protocol.Bead, error) {
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("beadstore: metadata key is required")
	}
	beads, err := s.queryBeads(ctx, `SELECT `+prefixedBeadColumns()+`
FROM beads b
JOIN bead_metadata m ON m.bead_id=b.id AND m.key=?
WHERE b.deleted=0
ORDER BY b.created_at ASC, b.id ASC`, key)
	if err != nil {
		return nil, err
	}
	return beadPointers(beads), nil
}

// Export returns all active beads as newline-delimited JSON.
func (s *SQLiteStore) Export(ctx context.Context) ([]byte, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	enc := json.NewEncoder(&out)
	for _, bead := range beads {
		if err := enc.Encode(bead); err != nil {
			return nil, fmt.Errorf("beadstore: encode export bead %s: %w", bead.ID, err)
		}
	}
	return []byte(out.String()), nil
}

const beadColumns = `id, title, contract_version, draft, description, acceptance_criteria, status, priority, type, parent_id, owner, estimated_minutes, tier, model, deferred_until, close_reason, created_at, updated_at, closed_at`

func prefixedBeadColumns() string {
	parts := strings.Split(beadColumns, ", ")
	for i, part := range parts {
		parts[i] = "b." + part
	}
	return strings.Join(parts, ", ")
}

func beadPointers(beads []protocol.Bead) []*protocol.Bead {
	pointers := make([]*protocol.Bead, len(beads))
	for i := range beads {
		pointers[i] = &beads[i]
	}
	return pointers
}

func (s *SQLiteStore) queryBeads(ctx context.Context, query string, args ...any) ([]protocol.Bead, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("beadstore: query beads: %w", err)
	}
	defer rows.Close()

	var beads []protocol.Bead
	for rows.Next() {
		bead, err := scanBead(rows)
		if err != nil {
			return nil, err
		}
		beads = append(beads, bead)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate beads: %w", err)
	}
	if err := s.loadChildren(ctx, beads); err != nil {
		return nil, err
	}
	return beads, nil
}

func (s *SQLiteStore) filterUnassigned(ctx context.Context, beads []protocol.Bead) ([]protocol.Bead, error) {
	active, err := s.activeAssignments(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return beads, nil
	}
	filtered := beads[:0]
	for _, bead := range beads {
		if _, ok := active[bead.ID]; ok {
			continue
		}
		filtered = append(filtered, bead)
	}
	return filtered, nil
}

func (s *SQLiteStore) activeAssignments(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bead_id, worker_id FROM assignments WHERE status='active' ORDER BY assigned_at DESC, id DESC`)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("beadstore: query active assignments: %w", err)
	}
	defer rows.Close()

	active := map[string]string{}
	for rows.Next() {
		var beadID, workerID string
		if err := rows.Scan(&beadID, &workerID); err != nil {
			return nil, fmt.Errorf("beadstore: scan active assignment: %w", err)
		}
		if _, ok := active[beadID]; !ok {
			active[beadID] = workerID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate active assignments: %w", err)
	}
	return active, nil
}

func scanBead(rows *sql.Rows) (protocol.Bead, error) {
	var bead protocol.Bead
	var parent, owner, tier, model, deferredUntil, reason, closedAt sql.NullString
	var estimate sql.NullInt64
	if err := rows.Scan(
		&bead.ID,
		&bead.Title,
		&bead.ContractVersion,
		&bead.Draft,
		&bead.Description,
		&bead.AcceptanceCriteria,
		&bead.Status,
		&bead.Priority,
		&bead.Type,
		&parent,
		&owner,
		&estimate,
		&tier,
		&model,
		&deferredUntil,
		&reason,
		&bead.CreatedAt,
		&bead.UpdatedAt,
		&closedAt,
	); err != nil {
		return protocol.Bead{}, fmt.Errorf("beadstore: scan bead row: %w", err)
	}
	if parent.Valid {
		bead.Epic = parent.String
	}
	if owner.Valid {
		bead.Owner = owner.String
	}
	if estimate.Valid {
		bead.EstimatedMinutes = int(estimate.Int64)
	}
	if tier.Valid {
		bead.Tier = protocol.Tier(tier.String)
	}
	if model.Valid {
		bead.Model = model.String
	}
	if deferredUntil.Valid {
		bead.DeferUntil = deferredUntil.String
	}
	if reason.Valid {
		bead.CloseReason = reason.String
	}
	if closedAt.Valid {
		bead.ClosedAt = closedAt.String
	}
	return bead, nil
}

func (s *SQLiteStore) loadChildren(ctx context.Context, beads []protocol.Bead) error {
	for i := range beads {
		id := beads[i].ID
		tags, err := s.loadStringRows(ctx, `SELECT tag FROM bead_tags WHERE bead_id=? ORDER BY tag`, id)
		if err != nil {
			return err
		}
		beads[i].Tags = tags
		labels, err := s.loadStringRows(ctx, `SELECT label FROM bead_labels WHERE bead_id=? ORDER BY label`, id)
		if err != nil {
			return err
		}
		beads[i].Labels = labels
		metadata, err := s.loadMetadata(ctx, id)
		if err != nil {
			return err
		}
		beads[i].Metadata = metadata
		applyLegacyMetadataTier(&beads[i], metadata)
		deps, err := s.loadDependencies(ctx, id)
		if err != nil {
			return err
		}
		beads[i].Dependencies = deps
		hydrateParentFromDependencies(&beads[i])
		notes, err := s.loadStringRows(ctx, `SELECT content FROM bead_notes WHERE bead_id=? ORDER BY created_at, id`, id)
		if err != nil {
			return err
		}
		beads[i].Notes = strings.Join(notes, "\n\n")
	}
	return nil
}

func (s *SQLiteStore) loadStringRows(ctx context.Context, query, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("beadstore: query string rows for %s: %w", id, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("beadstore: scan string row for %s: %w", id, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate string rows for %s: %w", id, err)
	}
	return values, nil
}

func (s *SQLiteStore) loadMetadata(ctx context.Context, id string) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM bead_metadata WHERE bead_id=? ORDER BY key`, id)
	if err != nil {
		return nil, fmt.Errorf("beadstore: query metadata for %s: %w", id, err)
	}
	defer rows.Close()
	metadata := map[string]any{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("beadstore: scan metadata for %s: %w", id, err)
		}
		metadata[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate metadata for %s: %w", id, err)
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}

func (s *SQLiteStore) loadDependencies(ctx context.Context, id string) ([]protocol.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bead_id, depends_on_id, type FROM bead_deps WHERE bead_id=? ORDER BY depends_on_id, type`, id)
	if err != nil {
		return nil, fmt.Errorf("beadstore: query dependencies for %s: %w", id, err)
	}
	defer rows.Close()
	var deps []protocol.Dependency
	for rows.Next() {
		var dep protocol.Dependency
		if err := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type); err != nil {
			return nil, fmt.Errorf("beadstore: scan dependency for %s: %w", id, err)
		}
		deps = append(deps, dep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate dependencies for %s: %w", id, err)
	}
	return deps, nil
}

func hydrateParentFromDependencies(bead *protocol.Bead) {
	if bead.Epic != "" {
		return
	}
	for _, dep := range bead.Dependencies {
		if dep.Type == "parent-child" && dep.DependsOnID != "" {
			bead.Epic = dep.DependsOnID
			return
		}
	}
}

func (s *SQLiteStore) enrichRuntime(ctx context.Context, bead *protocol.Bead) error {
	var workerID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT worker_id FROM assignments WHERE bead_id=? AND status='active' ORDER BY assigned_at DESC, id DESC LIMIT 1`, bead.ID).Scan(&workerID); err != nil {
		if isNoSuchTable(err) || err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("beadstore: query runtime assignment for %s: %w", bead.ID, err)
	}
	if workerID.Valid {
		bead.WorkerID = workerID.String
	}
	return nil
}

func replaceStrings(ctx context.Context, tx *sql.Tx, table, column, beadID string, values []string) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE bead_id=?", table), beadID); err != nil {
		return fmt.Errorf("beadstore: clear %s for %s: %w", table, beadID, err)
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (bead_id, %s) VALUES (?, ?)", table, column), beadID, value); err != nil {
			return fmt.Errorf("beadstore: add %s value for %s: %w", table, beadID, err)
		}
	}
	return nil
}

func replaceMetadata(ctx context.Context, tx *sql.Tx, beadID string, metadata map[string]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bead_metadata WHERE bead_id=?`, beadID); err != nil {
		return fmt.Errorf("beadstore: clear metadata for %s: %w", beadID, err)
	}
	for key, value := range metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_metadata (bead_id, key, value) VALUES (?, ?, ?)`, beadID, key, value); err != nil {
			return fmt.Errorf("beadstore: add metadata %q for %s: %w", key, beadID, err)
		}
	}
	return nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, eventType, beadID string, payload map[string]any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("beadstore: marshal %s event for %s: %w", eventType, beadID, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events (type, source, bead_id, payload, created_at) VALUES (?, 'beadstore', ?, ?, ?)`, eventType, beadID, string(payloadJSON), nowString())
	if err != nil && isNoSuchTable(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("beadstore: insert %s event for %s: %w", eventType, beadID, err)
	}
	return nil
}

func insertJourneyEvent(ctx context.Context, tx *sql.Tx, beadID string, evt JourneyEvent) error {
	payload := sql.NullString{String: evt.Payload, Valid: evt.Payload != ""}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
		VALUES (?, ?, ?, ?, ?)`,
		beadID, evt.Ts, evt.Actor, evt.Event, payload)
	if err != nil && isNoSuchTable(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("beadstore: insert journey %s for %s: %w", evt.Event, beadID, err)
	}
	return nil
}

func mustJSON(payload map[string]any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func ensureBeadExists(ctx context.Context, tx *sql.Tx, id string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM beads WHERE id=? AND deleted=0`, id).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return &protocol.BeadNotFoundError{BeadID: id}
		}
		return fmt.Errorf("beadstore: ensure bead %s exists: %w", id, err)
	}
	return nil
}

type updateStatement struct {
	assignments []string
	args        []any
}

func newUpdateStatement(params UpdateParams) updateStatement {
	stmt := updateStatement{
		assignments: []string{"updated_at=?"},
		args:        []any{nowString()},
	}
	stmt.addStatus(params.Status)
	stmt.addPtr("title=?", params.Title)
	stmt.addPtr("description=?", params.Description)
	stmt.addPtr("priority=?", params.Priority)
	stmt.addPtr("type=?", params.Type)
	stmt.addPtr("acceptance_criteria=?", params.AcceptanceCriteria)
	stmt.addPtr("estimated_minutes=?", params.EstimatedMinutes)
	stmt.addPtr("contract_version=?", params.ContractVersion)
	stmt.addPtr("draft=?", params.Draft)
	stmt.addNullableString("parent_id", params.ParentID)
	stmt.addNullableString("owner", params.Owner)
	return stmt
}

func (s *updateStatement) addStatus(status *string) {
	if status == nil {
		return
	}
	s.assignments = append(s.assignments, "status=?")
	s.args = append(s.args, *status)
	switch *status {
	case "open":
		s.assignments = append(s.assignments, "deferred_until=NULL", "closed_at=NULL", "close_reason=NULL")
	case "blocked":
		s.assignments = append(s.assignments, "deferred_until=NULL", "closed_at=NULL", "close_reason=NULL")
	case "in_progress":
		s.assignments = append(s.assignments, "closed_at=NULL", "close_reason=NULL")
	case "closed":
		s.assignments = append(s.assignments, "closed_at=COALESCE(closed_at, ?)")
		s.args = append(s.args, nowString())
	}
}

func (s *updateStatement) addPtr(assignment string, value any) {
	switch v := value.(type) {
	case *int:
		if v != nil {
			s.assignments = append(s.assignments, assignment)
			s.args = append(s.args, *v)
		}
	case *string:
		if v != nil {
			s.assignments = append(s.assignments, assignment)
			s.args = append(s.args, *v)
		}
	case *bool:
		if v != nil {
			s.assignments = append(s.assignments, assignment)
			s.args = append(s.args, *v)
		}
	}
}

func (s *updateStatement) addNullableString(column string, value *string) {
	if value == nil {
		return
	}
	if *value == "" {
		s.assignments = append(s.assignments, column+"=NULL")
		return
	}
	s.assignments = append(s.assignments, column+"=?")
	s.args = append(s.args, *value)
}

func (s updateStatement) query() string {
	return `UPDATE beads SET ` + strings.Join(s.assignments, ", ") + ` WHERE id=? AND deleted=0`
}

func updatePayload(params UpdateParams) map[string]any {
	payload := map[string]any{}
	if params.Title != nil {
		payload["title"] = *params.Title
	}
	if params.Description != nil {
		payload["description"] = *params.Description
	}
	if params.Status != nil {
		payload["status"] = *params.Status
	}
	if params.Priority != nil {
		payload["priority"] = *params.Priority
	}
	if params.Type != nil {
		payload["type"] = *params.Type
	}
	if params.AcceptanceCriteria != nil {
		payload["acceptance_criteria"] = *params.AcceptanceCriteria
	}
	if params.EstimatedMinutes != nil {
		payload["estimated_minutes"] = *params.EstimatedMinutes
	}
	if params.ContractVersion != nil {
		payload["contract_version"] = *params.ContractVersion
	}
	if params.Draft != nil {
		payload["draft"] = *params.Draft
	}
	if params.Notes != nil {
		payload["notes"] = *params.Notes
	}
	if params.ParentID != nil {
		payload["parent_id"] = *params.ParentID
	}
	if params.Owner != nil {
		payload["owner"] = *params.Owner
	}
	if params.Tags != nil {
		payload["tags"] = *params.Tags
	}
	return payload
}

func validStatus(status string) bool {
	return status == "open" || status == "in_progress" || status == "blocked" || status == "closed"
}

func normalizeCreateParams(params CreateParams) (CreateParams, error) {
	if strings.TrimSpace(params.Title) == "" {
		return CreateParams{}, fmt.Errorf("beadstore: title is required")
	}
	if params.Status == "" {
		params.Status = "open"
	}
	if !validStatus(params.Status) {
		return CreateParams{}, fmt.Errorf("beadstore: invalid status %q", params.Status)
	}
	if params.ID == "" {
		params.ID = generateBeadID()
	}
	if params.Type == "" {
		params.Type = "task"
	}
	return params, nil
}

// applyLegacyMetadataTier sets bead.Tier from metadata["model"] when both the
// model column and tier column are empty. This converts legacy Claude-family
// model names (opus/sonnet/haiku) stored in metadata to provider-neutral tiers.
func applyLegacyMetadataTier(bead *protocol.Bead, metadata map[string]any) {
	if bead.Model != "" || bead.Tier != "" {
		return
	}
	model, ok := metadata["model"].(string)
	if !ok {
		return
	}
	if t, ok := protocol.LegacyModelToTier(model); ok {
		bead.Tier = t
	}
}

func generateBeadID() string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("oro-%d", time.Now().UnixNano())
	}
	var suffix strings.Builder
	for _, v := range b {
		suffix.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return "oro-" + suffix.String()
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func isNoSuchTable(err error) bool {
	return strings.Contains(err.Error(), "no such table")
}
