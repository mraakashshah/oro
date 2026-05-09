package beadstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"oro/pkg/cards"
	"oro/pkg/protocol"
)

// readTxImpl implements ReadTx over an active *sql.Tx.
type readTxImpl struct {
	tx      *sql.Tx
	cardsTx cards.ReadTx
}

// Cards implements ReadTx.
func (r *readTxImpl) Cards() cards.ReadTx { return r.cardsTx }

// Ready implements ReadTx with the same assignment-filtering behavior as Store.Ready.
func (r *readTxImpl) Ready(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := queryBeadsInTx(ctx, r.tx, `SELECT `+beadColumns+` FROM beads_ready ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	return filterUnassignedInTx(ctx, r.tx, beads)
}

// InProgress implements ReadTx with the same active-assignment merge as Store.InProgress:
// populates WorkerID for matching beads and surfaces assignment-only beads.
func (r *readTxImpl) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := queryBeadsInTx(ctx, r.tx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status='in_progress' ORDER BY updated_at DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	active, err := activeAssignmentsInTx(ctx, r.tx)
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
		assigned, err := queryBeadsInTx(ctx, r.tx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status!='closed' AND id=?`, beadID)
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

// Blocked implements ReadTx with the same assignment-filtering behavior as Store.Blocked.
func (r *readTxImpl) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := queryBeadsInTx(ctx, r.tx, `SELECT `+beadColumns+` FROM beads_blocked ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	return filterUnassignedInTx(ctx, r.tx, beads)
}

// Closed implements ReadTx.
func (r *readTxImpl) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	if limit <= 0 {
		return []protocol.Bead{}, nil
	}
	return queryBeadsInTx(ctx, r.tx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status='closed' ORDER BY closed_at DESC, updated_at DESC LIMIT ?`, limit)
}

// Show implements ReadTx and populates runtime fields (WorkerID) like Store.Show.
// Memory enrichment is intentionally skipped: it requires a Store-level fetcher and
// is not part of the §4.7 render-snapshot contract.
func (r *readTxImpl) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	beads, err := queryBeadsInTx(ctx, r.tx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(beads) == 0 {
		return nil, nil
	}
	bead := &beads[0]
	if err := enrichRuntimeInTx(ctx, r.tx, bead); err != nil {
		return nil, err
	}
	return bead, nil
}

// HasChildren implements ReadTx.
func (r *readTxImpl) HasChildren(ctx context.Context, epicID string) (bool, error) {
	var n int
	if err := r.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads WHERE parent_id=? AND deleted=0`, epicID).Scan(&n); err != nil {
		return false, fmt.Errorf("beadstore: tx count children for %s: %w", epicID, err)
	}
	return n > 0, nil
}

// AllChildrenClosed implements ReadTx.
func (r *readTxImpl) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	var total int
	var open sql.NullInt64
	err := r.tx.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(CASE WHEN status!='closed' THEN 1 ELSE 0 END) FROM beads WHERE parent_id=? AND deleted=0`,
		epicID).Scan(&total, &open)
	if err != nil {
		return false, fmt.Errorf("beadstore: tx count open children for %s: %w", epicID, err)
	}
	return total == 0 || !open.Valid || open.Int64 == 0, nil
}

// FindByParentAndTag implements ReadTx.
func (r *readTxImpl) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	return queryBeadsInTx(ctx, r.tx, `SELECT `+prefixedBeadColumns("b")+`
FROM beads b
JOIN bead_tags t ON t.bead_id=b.id AND t.tag=?
WHERE b.deleted=0 AND b.parent_id=?
ORDER BY b.priority ASC, b.created_at ASC`, tag, parentID)
}

// Journey implements ReadTx.
func (r *readTxImpl) Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error) {
	rows, err := r.tx.QueryContext(ctx, `
		SELECT id, bead_id, ts, actor, event, COALESCE(payload, '')
		FROM bead_journey
		WHERE bead_id = ? AND ts >= ?
		ORDER BY ts ASC, id ASC`,
		beadID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("beadstore: tx journey for %s: %w", beadID, err)
	}
	defer rows.Close()
	return scanJourneyEvents(rows)
}

// LatestJourney implements ReadTx.
func (r *readTxImpl) LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error) {
	rows, err := r.tx.QueryContext(ctx, `
		SELECT id, bead_id, ts, actor, event, COALESCE(payload, '')
		FROM bead_journey
		WHERE bead_id = ?
		ORDER BY ts DESC, id DESC
		LIMIT ?`,
		beadID, limit)
	if err != nil {
		return nil, fmt.Errorf("beadstore: tx latest journey for %s: %w", beadID, err)
	}
	defer rows.Close()
	events, err := scanJourneyEvents(rows)
	if err != nil {
		return nil, err
	}
	reverseEvents(events)
	return events, nil
}

// WithReadTx executes fn inside a single read-only SQL transaction.
// The ReadTx.Cards() accessor returns a cards.ReadTx bound to the same
// transaction, so cards reads participate in the same consistent snapshot.
func (s *SQLiteStore) WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("beadstore: begin read tx: %w", err)
	}
	rtx := &readTxImpl{
		tx:      tx,
		cardsTx: cards.NewReadTx(tx),
	}
	if err := fn(rtx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit read tx: %w", err)
	}
	return nil
}

// queryBeadsInTx runs a bead SELECT inside a transaction and loads child rows.
func queryBeadsInTx(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]protocol.Bead, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("beadstore: tx query beads: %w", err)
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
		return nil, fmt.Errorf("beadstore: tx iterate beads: %w", err)
	}
	if err := loadBeadChildrenInTx(ctx, tx, beads); err != nil {
		return nil, err
	}
	return beads, nil
}

// loadBeadChildrenInTx loads tags, labels, metadata, dependencies, and notes
// for each bead using the supplied transaction.
func loadBeadChildrenInTx(ctx context.Context, tx *sql.Tx, beads []protocol.Bead) error {
	for i := range beads {
		id := beads[i].ID

		tags, err := txStringRows(ctx, tx, `SELECT tag FROM bead_tags WHERE bead_id=? ORDER BY tag`, id)
		if err != nil {
			return err
		}
		beads[i].Tags = tags

		labels, err := txStringRows(ctx, tx, `SELECT label FROM bead_labels WHERE bead_id=? ORDER BY label`, id)
		if err != nil {
			return err
		}
		beads[i].Labels = labels

		metadata, err := txMetadata(ctx, tx, id)
		if err != nil {
			return err
		}
		beads[i].Metadata = metadata
		applyLegacyMetadataTier(&beads[i], metadata)

		deps, err := txDependencies(ctx, tx, id)
		if err != nil {
			return err
		}
		beads[i].Dependencies = deps
		hydrateParentFromDependencies(&beads[i])

		notes, err := txStringRows(ctx, tx, `SELECT content FROM bead_notes WHERE bead_id=? ORDER BY created_at, id`, id)
		if err != nil {
			return err
		}
		beads[i].Notes = strings.Join(notes, "\n\n")
	}
	return nil
}

func txStringRows(ctx context.Context, tx *sql.Tx, query, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("beadstore: tx string rows for %s: %w", id, err)
	}
	defer rows.Close()
	var vals []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("beadstore: tx scan string row for %s: %w", id, err)
		}
		vals = append(vals, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: tx iterate string rows for %s: %w", id, err)
	}
	return vals, nil
}

func txMetadata(ctx context.Context, tx *sql.Tx, id string) (map[string]any, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM bead_metadata WHERE bead_id=? ORDER BY key`, id)
	if err != nil {
		return nil, fmt.Errorf("beadstore: tx metadata for %s: %w", id, err)
	}
	defer rows.Close()
	meta := map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("beadstore: tx scan metadata for %s: %w", id, err)
		}
		meta[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: tx iterate metadata for %s: %w", id, err)
	}
	if len(meta) == 0 {
		return nil, nil
	}
	return meta, nil
}

func txDependencies(ctx context.Context, tx *sql.Tx, id string) ([]protocol.Dependency, error) {
	rows, err := tx.QueryContext(ctx, `SELECT bead_id, depends_on_id, type FROM bead_deps WHERE bead_id=? ORDER BY depends_on_id, type`, id)
	if err != nil {
		return nil, fmt.Errorf("beadstore: tx dependencies for %s: %w", id, err)
	}
	defer rows.Close()
	var deps []protocol.Dependency
	for rows.Next() {
		var dep protocol.Dependency
		if err := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type); err != nil {
			return nil, fmt.Errorf("beadstore: tx scan dependency for %s: %w", id, err)
		}
		deps = append(deps, dep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: tx iterate dependencies for %s: %w", id, err)
	}
	return deps, nil
}

// activeAssignmentsInTx mirrors (*SQLiteStore).activeAssignments but reads inside
// the supplied transaction so the result participates in the same snapshot.
func activeAssignmentsInTx(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT bead_id, worker_id FROM assignments WHERE status='active' ORDER BY assigned_at DESC, id DESC`)
	if err != nil {
		if isNoSuchTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("beadstore: tx query active assignments: %w", err)
	}
	defer rows.Close()
	active := map[string]string{}
	for rows.Next() {
		var beadID, workerID string
		if err := rows.Scan(&beadID, &workerID); err != nil {
			return nil, fmt.Errorf("beadstore: tx scan active assignment: %w", err)
		}
		if _, ok := active[beadID]; !ok {
			active[beadID] = workerID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: tx iterate active assignments: %w", err)
	}
	return active, nil
}

// filterUnassignedInTx mirrors (*SQLiteStore).filterUnassigned but reads inside
// the supplied transaction.
func filterUnassignedInTx(ctx context.Context, tx *sql.Tx, beads []protocol.Bead) ([]protocol.Bead, error) {
	active, err := activeAssignmentsInTx(ctx, tx)
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

// enrichRuntimeInTx mirrors (*SQLiteStore).enrichRuntime but reads inside the
// supplied transaction so the runtime field stays consistent with other reads.
func enrichRuntimeInTx(ctx context.Context, tx *sql.Tx, bead *protocol.Bead) error {
	var workerID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT worker_id FROM assignments WHERE bead_id=? AND status='active' ORDER BY assigned_at DESC, id DESC LIMIT 1`,
		bead.ID).Scan(&workerID)
	if err != nil {
		if isNoSuchTable(err) || err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("beadstore: tx runtime assignment for %s: %w", bead.ID, err)
	}
	if workerID.Valid {
		bead.WorkerID = workerID.String
	}
	return nil
}
