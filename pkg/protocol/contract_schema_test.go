package protocol_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestContractFieldsMigrateAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("exec runtime schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, legacyBeadsTableDDL); err != nil {
		t.Fatalf("create legacy beads table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id, title) VALUES ('legacy', 'Legacy bead')`); err != nil {
		t.Fatalf("seed legacy bead: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate legacy bead schema: %v", err)
	}

	store := beadstore.NewSQLiteStore(db)
	legacy, err := store.Show(ctx, "legacy")
	if err != nil {
		t.Fatalf("show legacy bead: %v", err)
	}
	if legacy == nil {
		t.Fatal("show legacy bead returned nil")
	}
	if legacy.ContractVersion != 0 || legacy.Draft {
		t.Fatalf("legacy contract fields = version %d, draft %t; want version 0, draft false", legacy.ContractVersion, legacy.Draft)
	}

	created, err := store.Create(ctx, beadstore.CreateParams{
		ID:              "draft-v2",
		Title:           "Draft contract bead",
		ContractVersion: 2,
		Draft:           true,
	})
	if err != nil {
		t.Fatalf("create draft bead: %v", err)
	}
	if created.ContractVersion != 2 || !created.Draft {
		t.Fatalf("created contract fields = version %d, draft %t; want version 2, draft true", created.ContractVersion, created.Draft)
	}
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("migrate legacy database to v3: %v", err)
	}
	if err := migrations.MigrateToV4(ctx, db); err != nil {
		t.Fatalf("migrate legacy database to v4: %v", err)
	}

	reloaded, err := store.Show(ctx, "draft-v2")
	if err != nil {
		t.Fatalf("reload draft bead: %v", err)
	}
	if reloaded == nil {
		t.Fatal("reload draft bead returned nil")
	}
	if reloaded.ContractVersion != 2 || !reloaded.Draft {
		t.Fatalf("reloaded contract fields = version %d, draft %t; want version 2, draft true", reloaded.ContractVersion, reloaded.Draft)
	}

	exported, err := store.Export(ctx)
	if err != nil {
		t.Fatalf("export beads: %v", err)
	}
	exportedByID := decodeExport(t, exported)
	if exportedByID["legacy"].ContractVersion != 0 || exportedByID["legacy"].Draft {
		t.Fatalf("exported legacy contract fields = %#v", exportedByID["legacy"])
	}
	if exportedByID["draft-v2"].ContractVersion != 2 || !exportedByID["draft-v2"].Draft {
		t.Fatalf("exported draft contract fields = %#v", exportedByID["draft-v2"])
	}
}

func decodeExport(t *testing.T, exported []byte) map[string]protocol.Bead {
	t.Helper()
	beads := make(map[string]protocol.Bead)
	for _, line := range strings.Split(strings.TrimSpace(string(exported)), "\n") {
		var bead protocol.Bead
		if err := json.Unmarshal([]byte(line), &bead); err != nil {
			t.Fatalf("decode export line %q: %v", line, err)
		}
		beads[bead.ID] = bead
	}
	return beads
}

const legacyBeadsTableDDL = `
CREATE TABLE beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','blocked','closed')),
    priority              INTEGER NOT NULL DEFAULT 2,
    type                  TEXT NOT NULL DEFAULT 'task' CHECK (type IN ('task','bug','epic','research','chore','review','premortem')),
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
);`
