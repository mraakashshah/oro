package beadstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BackfillJourneyEvents populates bead_journey for all existing beads that have
// no journey events yet (§4.6.f). COALESCE chains ensure the ts NOT NULL
// constraint is always satisfied even when bead timestamps are NULL (legacy data).
//
// For each bead: emits an 'imported' event with
// ts = COALESCE(created_at, <now>).
//
// For closed beads: additionally emits a 'closed' event with
// ts = COALESCE(closed_at, updated_at, created_at, <now>).
//
// The function is idempotent: beads that already have journey events are skipped.
func BackfillJourneyEvents(ctx context.Context, db *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("backfill journey events: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
SELECT
    b.id,
    COALESCE(b.created_at, ?),
    'migration',
    'imported',
    '{"source":"v20-migration"}'
FROM beads b
WHERE b.deleted = 0
  AND NOT EXISTS (
    SELECT 1 FROM bead_journey j WHERE j.bead_id = b.id
  )`, now); err != nil {
		return fmt.Errorf("backfill journey events: insert imported: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
SELECT
    b.id,
    COALESCE(b.closed_at, b.updated_at, b.created_at, ?),
    'migration',
    'closed',
    '{"source":"v20-migration"}'
FROM beads b
WHERE b.deleted = 0
  AND b.status = 'closed'
  AND NOT EXISTS (
    SELECT 1 FROM bead_journey j WHERE j.bead_id = b.id AND j.event = 'closed'
  )`, now); err != nil {
		return fmt.Errorf("backfill journey events: insert closed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("backfill journey events: commit: %w", err)
	}
	return nil
}
