package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// legacyBeadsTableDDL is the beads table DDL as it existed before §4.6.c,
// without a CHECK constraint on the type column.  Used to construct a
// pre-migration database state so the test exercises the actual rebuild path.
const legacyBeadsTableDDL = `
CREATE TABLE IF NOT EXISTS beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN
                          ('open','in_progress','blocked','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task',
    parent_id             TEXT REFERENCES beads(id),
    owner                 TEXT,
    estimated_minutes     INTEGER,
    tier                  TEXT,
    model                 TEXT,
    deferred_until        TEXT,
    close_reason          TEXT,
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    closed_at             TEXT,
    deleted               INTEGER NOT NULL DEFAULT 0
);
`

// openPreTypeConstraintDB creates an in-memory SQLite database whose beads
// table matches the pre-§4.6.c schema: the status CHECK is present but the
// type column has no CHECK constraint.  This lets TestBeadTypeCheckConstraint
// exercise the real table-rebuild migration path.
func openPreTypeConstraintDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), legacyBeadsTableDDL); err != nil {
		t.Fatalf("create legacy beads table: %v", err)
	}
	return db
}

// TestBeadTypeCheckConstraint verifies §4.6.c: the bead type CHECK constraint
// migration.
//
// Assertions (per acceptance criteria):
//   - post-migration, INSERT with type IN (task,bug,epic,research,chore,premortem,review) succeeds
//   - INSERT with a disallowed type fails with a CHECK constraint error
//   - rows that existed before the migration are preserved verbatim
//   - a second call is a no-op (idempotent)
func TestBeadTypeCheckConstraint(t *testing.T) {
	ctx := context.Background()
	db := openPreTypeConstraintDB(t)

	// Seed rows with canonical types that must be preserved after the migration.
	type row struct{ id, title, typ string }
	seeds := []row{
		{"seed-task", "A task bead", "task"},
		{"seed-epic", "An epic bead", "epic"},
		{"seed-bug", "A bug bead", "bug"},
	}
	for _, s := range seeds {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO beads (id, title, status, type) VALUES (?, ?, 'open', ?)`,
			s.id, s.title, s.typ,
		); err != nil {
			t.Fatalf("seed row %s: %v", s.id, err)
		}
	}

	// Apply the migration.
	if _, err := protocol.EnsureBeadTypeCheckConstraint(ctx, db); err != nil {
		t.Fatalf("EnsureBeadTypeCheckConstraint: %v", err)
	}

	// Existing rows are preserved verbatim (same title, same type).
	for _, s := range seeds {
		var gotTitle, gotType string
		if err := db.QueryRowContext(ctx,
			`SELECT title, type FROM beads WHERE id=?`, s.id,
		).Scan(&gotTitle, &gotType); err != nil {
			t.Errorf("row %s missing post-migration: %v", s.id, err)
			continue
		}
		if gotTitle != s.title || gotType != s.typ {
			t.Errorf("row %s: want (title=%q type=%q) got (title=%q type=%q)",
				s.id, s.title, s.typ, gotTitle, gotType)
		}
	}

	// Post-migration: all seven canonical types are accepted.
	for _, tp := range []string{"task", "bug", "epic", "research", "chore", "premortem", "review"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO beads (id, title, status, type) VALUES (?, ?, 'open', ?)`,
			"ok-"+tp, "t", tp,
		); err != nil {
			t.Errorf("type=%q: expected insert success, got %v", tp, err)
		}
	}

	// Post-migration: a non-canonical type must fail with a CHECK violation.
	_, insertErr := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status, type) VALUES ('bad-1', 'bad', 'open', 'invalid')`,
	)
	if insertErr == nil {
		t.Error("type='invalid': expected CHECK constraint failure, got nil")
	} else if !strings.Contains(insertErr.Error(), "CHECK") && !strings.Contains(insertErr.Error(), "constraint") {
		t.Errorf("type='invalid': expected CHECK constraint error, got %v", insertErr)
	}

	// Idempotency: a second call must be a no-op and leave the constraint intact.
	if _, err := protocol.EnsureBeadTypeCheckConstraint(ctx, db); err != nil {
		t.Fatalf("EnsureBeadTypeCheckConstraint (idempotent): %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status, type) VALUES ('bad-2', 'bad2', 'open', 'invalid')`,
	); err == nil {
		t.Error("post-idempotency type='invalid': expected CHECK constraint failure, got nil")
	}
}
