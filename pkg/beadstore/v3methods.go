package beadstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AppendJourney inserts a single event into beadID's append-only journey table.
// It is a direct INSERT — no read-modify-write cycle, no lock.
func (s *SQLiteStore) AppendJourney(ctx context.Context, beadID string, evt JourneyEvent) error {
	payload := sql.NullString{String: evt.Payload, Valid: evt.Payload != ""}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
		VALUES (?, ?, ?, ?, ?)`,
		beadID, evt.Ts, evt.Actor, evt.Event, payload)
	if err != nil {
		return fmt.Errorf("beadstore: append journey for %s: %w", beadID, err)
	}
	return nil
}

// Journey returns all events for beadID with ts >= since, in ascending order.
func (s *SQLiteStore) Journey(ctx context.Context, beadID string, since time.Time) ([]JourneyEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, bead_id, ts, actor, event, COALESCE(payload, '')
		FROM bead_journey
		WHERE bead_id = ? AND ts >= ?
		ORDER BY ts ASC, id ASC`,
		beadID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("beadstore: journey for %s: %w", beadID, err)
	}
	defer rows.Close()
	return scanJourneyEvents(rows)
}

// LatestJourney returns the most recent limit events for beadID in ascending
// (chronological) order.
func (s *SQLiteStore) LatestJourney(ctx context.Context, beadID string, limit int) ([]JourneyEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, bead_id, ts, actor, event, COALESCE(payload, '')
		FROM bead_journey
		WHERE bead_id = ?
		ORDER BY ts DESC, id DESC
		LIMIT ?`,
		beadID, limit)
	if err != nil {
		return nil, fmt.Errorf("beadstore: latest journey for %s: %w", beadID, err)
	}
	defer rows.Close()
	events, err := scanJourneyEvents(rows)
	if err != nil {
		return nil, err
	}
	reverseEvents(events)
	return events, nil
}

// TransitionPipelineStage atomically transitions beadID's pipeline_stage from →
// to and appends a pipeline_stage_changed journey event. Returns ErrStaleStage
// if the current pipeline_stage does not match from.
func (s *SQLiteStore) TransitionPipelineStage(ctx context.Context, beadID string, from, to PipelineStage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beadstore: begin transition pipeline stage transaction: %w", err)
	}
	defer rollback(tx)

	// Match both explicit 'none' value and NULL (beads that predate the v3 migration).
	res, err := tx.ExecContext(ctx, `
		UPDATE beads
		   SET pipeline_stage = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id = ? AND deleted = 0
		   AND (pipeline_stage = ? OR (pipeline_stage IS NULL AND ? = 'none'))`,
		to, beadID, from, from)
	if err != nil {
		return fmt.Errorf("beadstore: transition pipeline stage for %s: %w", beadID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("beadstore: transition pipeline stage rows affected for %s: %w", beadID, err)
	}
	if n == 0 {
		return ErrStaleStage
	}

	payloadJSON, err := json.Marshal(map[string]any{
		"from": from,
		"to":   to,
	})
	if err != nil {
		return fmt.Errorf("beadstore: marshal pipeline_stage_changed payload: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
		VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'dispatcher', 'pipeline_stage_changed', ?)`,
		beadID, string(payloadJSON)); err != nil {
		return fmt.Errorf("beadstore: append pipeline_stage_changed event for %s: %w", beadID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("beadstore: commit transition pipeline stage for %s: %w", beadID, err)
	}
	return nil
}

// CountChildren returns the number of non-deleted child beads for parentID.
func (s *SQLiteStore) CountChildren(ctx context.Context, parentID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads WHERE parent_id=? AND deleted=0`, parentID).Scan(&n); err != nil {
		return 0, fmt.Errorf("beadstore: count children for %s: %w", parentID, err)
	}
	return n, nil
}

func scanJourneyEvents(rows *sql.Rows) ([]JourneyEvent, error) {
	events := make([]JourneyEvent, 0)
	for rows.Next() {
		var e JourneyEvent
		if err := rows.Scan(&e.ID, &e.BeadID, &e.Ts, &e.Actor, &e.Event, &e.Payload); err != nil {
			return nil, fmt.Errorf("beadstore: scan journey event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate journey events: %w", err)
	}
	return events, nil
}

func reverseEvents(events []JourneyEvent) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}
