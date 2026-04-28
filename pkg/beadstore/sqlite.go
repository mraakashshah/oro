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

type Option func(*SQLiteStore)

func WithMemoryFetcher(fetch MemoryFetcher) Option {
	return func(s *SQLiteStore) {
		s.memory = fetch
	}
}

type SQLiteStore struct {
	db           *sql.DB
	memory       MemoryFetcher
	memMaxTokens int
	writeMu      sync.Mutex
}

func OpenSQLiteStore(ctx context.Context, path string, opts ...Option) (*SQLiteStore, error) {
	db, err := dbutil.OpenDB(path)
	if err != nil {
		return nil, err
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return NewSQLiteStore(db, opts...), nil
}

func NewSQLiteStore(db *sql.DB, opts ...Option) *SQLiteStore {
	store := &SQLiteStore{db: db, memMaxTokens: 2000}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

func (s *SQLiteStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads_ready ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	return s.filterUnassigned(ctx, beads)
}

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

func (s *SQLiteStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads_blocked ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	return s.filterUnassigned(ctx, beads)
}

func (s *SQLiteStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	if limit <= 0 {
		return []protocol.Bead{}, nil
	}
	return s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 AND status='closed' ORDER BY closed_at DESC, updated_at DESC LIMIT ?`, limit)
}

func (s *SQLiteStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
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
	if s.memory != nil {
		if memory, err := s.memory(ctx, bead.Tags, bead.Description, s.memMaxTokens); err == nil {
			bead.Memory = memory
		}
	}
	return bead, nil
}

func (s *SQLiteStore) Create(ctx context.Context, params CreateParams) (*protocol.Bead, error) {
	if strings.TrimSpace(params.Title) == "" {
		return nil, fmt.Errorf("beadstore: title is required")
	}
	if params.ID == "" {
		params.ID = generateBeadID()
	}
	if params.Type == "" {
		params.Type = "task"
	}
	now := nowString()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	var parent sql.NullString
	if params.ParentID != "" {
		parent = sql.NullString{String: params.ParentID, Valid: true}
	}
	var estimate sql.NullInt64
	if params.EstimatedMinutes > 0 {
		estimate = sql.NullInt64{Int64: int64(params.EstimatedMinutes), Valid: true}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO beads (id, title, description, acceptance_criteria, status, priority, type, parent_id, estimated_minutes, created_at, updated_at)
VALUES (?, ?, ?, ?, 'open', ?, ?, ?, ?, ?, ?)`,
		params.ID, params.Title, params.Description, params.AcceptanceCriteria, params.Priority, params.Type, parent, estimate, now, now); err != nil {
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
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Show(ctx, params.ID)
}

func (s *SQLiteStore) Update(ctx context.Context, id string, params UpdateParams) error {
	if params.Status != nil && !validStatus(*params.Status) {
		return fmt.Errorf("beadstore: invalid status %q", *params.Status)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	assignments := []string{"updated_at=?"}
	args := []any{nowString()}
	if params.Status != nil {
		assignments = append(assignments, "status=?")
		args = append(args, *params.Status)
		switch *params.Status {
		case "open":
			assignments = append(assignments, "deferred_until=NULL", "closed_at=NULL", "close_reason=NULL")
		case "closed":
			assignments = append(assignments, "closed_at=COALESCE(closed_at, ?)")
			args = append(args, nowString())
		}
	}
	if params.Priority != nil {
		assignments = append(assignments, "priority=?")
		args = append(args, *params.Priority)
	}
	if params.Type != nil {
		assignments = append(assignments, "type=?")
		args = append(args, *params.Type)
	}
	if params.AcceptanceCriteria != nil {
		assignments = append(assignments, "acceptance_criteria=?")
		args = append(args, *params.AcceptanceCriteria)
	}
	if params.ParentID != nil {
		if *params.ParentID == "" {
			assignments = append(assignments, "parent_id=NULL")
		} else {
			assignments = append(assignments, "parent_id=?")
			args = append(args, *params.ParentID)
		}
	}
	if params.Owner != nil {
		if *params.Owner == "" {
			assignments = append(assignments, "owner=NULL")
		} else {
			assignments = append(assignments, "owner=?")
			args = append(args, *params.Owner)
		}
	}

	args = append(args, id)
	res, err := tx.ExecContext(ctx, `UPDATE beads SET `+strings.Join(assignments, ", ")+` WHERE id=? AND deleted=0`, args...)
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
	if params.Notes != nil && strings.TrimSpace(*params.Notes) != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_notes (bead_id, author, content, created_at) VALUES (?, 'oro', ?, ?)`, id, *params.Notes, nowString()); err != nil {
			return fmt.Errorf("beadstore: add note %s: %w", id, err)
		}
	}
	if err := insertEvent(ctx, tx, "bead_updated", id, updatePayload(params)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Close(ctx context.Context, id, reason string) error {
	now := nowString()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
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
	return tx.Commit()
}

func (s *SQLiteStore) Defer(ctx context.Context, id, until string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
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
	return tx.Commit()
}

func (s *SQLiteStore) Undefer(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
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
	return tx.Commit()
}

func (s *SQLiteStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads WHERE parent_id=? AND deleted=0`, epicID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *SQLiteStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	var total int
	var open sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN status!='closed' THEN 1 ELSE 0 END) FROM beads WHERE parent_id=? AND deleted=0`, epicID).Scan(&total, &open); err != nil {
		return false, err
	}
	return total == 0 || !open.Valid || open.Int64 == 0, nil
}

func (s *SQLiteStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	return s.queryBeads(ctx, `SELECT `+prefixedBeadColumns("b")+`
FROM beads b
JOIN bead_tags t ON t.bead_id=b.id AND t.tag=?
WHERE b.deleted=0 AND b.parent_id=?
ORDER BY b.priority ASC, b.created_at ASC`, tag, parentID)
}

func (s *SQLiteStore) Export(ctx context.Context) ([]byte, error) {
	beads, err := s.queryBeads(ctx, `SELECT `+beadColumns+` FROM beads WHERE deleted=0 ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	enc := json.NewEncoder(&out)
	for _, bead := range beads {
		if err := enc.Encode(bead); err != nil {
			return nil, err
		}
	}
	return []byte(out.String()), nil
}

const beadColumns = `id, title, description, acceptance_criteria, status, priority, type, parent_id, owner, estimated_minutes, tier, model, deferred_until, close_reason, created_at, updated_at, closed_at`

func prefixedBeadColumns(prefix string) string {
	parts := strings.Split(beadColumns, ", ")
	for i, part := range parts {
		parts[i] = prefix + "." + part
	}
	return strings.Join(parts, ", ")
}

func (s *SQLiteStore) queryBeads(ctx context.Context, query string, args ...any) ([]protocol.Bead, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
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
		return nil, err
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
		return nil, err
	}
	defer rows.Close()

	active := map[string]string{}
	for rows.Next() {
		var beadID, workerID string
		if err := rows.Scan(&beadID, &workerID); err != nil {
			return nil, err
		}
		if _, ok := active[beadID]; !ok {
			active[beadID] = workerID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
		return protocol.Bead{}, err
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
		if beads[i].Model == "" {
			if model, ok := metadata["model"].(string); ok && isAllowedModel(model) {
				beads[i].Model = model
			}
		}
		deps, err := s.loadDependencies(ctx, id)
		if err != nil {
			return err
		}
		beads[i].Dependencies = deps
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
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) loadMetadata(ctx context.Context, id string) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM bead_metadata WHERE bead_id=? ORDER BY key`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	metadata := map[string]any{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		metadata[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}

func (s *SQLiteStore) loadDependencies(ctx context.Context, id string) ([]protocol.Dependency, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bead_id, depends_on_id, type FROM bead_deps WHERE bead_id=? ORDER BY depends_on_id, type`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []protocol.Dependency
	for rows.Next() {
		var dep protocol.Dependency
		if err := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func (s *SQLiteStore) enrichRuntime(ctx context.Context, bead *protocol.Bead) error {
	var workerID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT worker_id FROM assignments WHERE bead_id=? AND status='active' ORDER BY assigned_at DESC, id DESC LIMIT 1`, bead.ID).Scan(&workerID); err != nil {
		if isNoSuchTable(err) || err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if workerID.Valid {
		bead.WorkerID = workerID.String
	}
	return nil
}

func replaceStrings(ctx context.Context, tx *sql.Tx, table, column, beadID string, values []string) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE bead_id=?", table), beadID); err != nil {
		return err
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (bead_id, %s) VALUES (?, ?)", table, column), beadID, value); err != nil {
			return err
		}
	}
	return nil
}

func replaceMetadata(ctx context.Context, tx *sql.Tx, beadID string, metadata map[string]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bead_metadata WHERE bead_id=?`, beadID); err != nil {
		return err
	}
	for key, value := range metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_metadata (bead_id, key, value) VALUES (?, ?, ?)`, beadID, key, value); err != nil {
			return err
		}
	}
	return nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, eventType, beadID string, payload map[string]any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events (type, source, bead_id, payload, created_at) VALUES (?, 'beadstore', ?, ?, ?)`, eventType, beadID, string(payloadJSON), nowString())
	if err != nil && isNoSuchTable(err) {
		return nil
	}
	return err
}

func updatePayload(params UpdateParams) map[string]any {
	payload := map[string]any{}
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
	if params.Notes != nil {
		payload["notes"] = *params.Notes
	}
	if params.ParentID != nil {
		payload["parent_id"] = *params.ParentID
	}
	if params.Owner != nil {
		payload["owner"] = *params.Owner
	}
	return payload
}

func validStatus(status string) bool {
	return status == "open" || status == "in_progress" || status == "closed"
}

func isAllowedModel(model string) bool {
	return model == protocol.ModelHaiku || model == protocol.ModelSonnet || model == protocol.ModelOpus
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
