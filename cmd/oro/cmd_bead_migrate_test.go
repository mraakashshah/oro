package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var beadMigrationDefaultDoltCountArgs = []string{"sql", "--result-format", "json", "-q", beadMigrationDoltCountQuery}

func TestBeadMigrateFromDoltDryRunFixturePrintsPlanWithoutMutatingDB(t *testing.T) {
	repoRoot := findRepoRoot(t)
	fixture := filepath.Join(repoRoot, "testdata", "dolt-100")
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	cmd := newBeadCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate-from-dolt", "--dry-run", "--from-fixture", fixture})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead migrate-from-dolt dry-run error: %v\n%s", err, out.String())
	}

	got := out.String()
	for _, want := range []string{
		"Migration plan",
		"source: fixture",
		"beads: 100",
		"dependencies: 3",
		"tags: 5",
		"labels: 2",
		"metadata entries: 4",
		"notes: 3",
		"DRY RUN -- no writes performed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}

	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated DB path %s: stat err=%v", dbPath, err)
	}
}

func TestMigrate100BeadFixture(t *testing.T) {
	repoRoot := findRepoRoot(t)
	fixture := filepath.Join(repoRoot, "testdata", "dolt-100")
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-fixture", fixture)

	data, err := os.ReadFile(filepath.Join(fixture, "export.jsonl"))
	if err != nil {
		t.Fatalf("read fixture export: %v", err)
	}
	rawBeads, err := decodeBDExport(data)
	if err != nil {
		t.Fatalf("decode fixture export: %v", err)
	}
	if len(rawBeads) != 100 {
		t.Fatalf("fixture rows = %d, want 100", len(rawBeads))
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()
	migrated, err := loadSQLiteMigrationBeads(t.Context(), db)
	if err != nil {
		t.Fatalf("load migrated beads: %v", err)
	}
	if len(migrated) != len(rawBeads) {
		t.Fatalf("migrated bead count = %d, want %d", len(migrated), len(rawBeads))
	}

	var divergences []string
	for _, raw := range rawBeads {
		var source bdExportBead
		if err := json.Unmarshal(raw, &source); err != nil {
			t.Fatalf("decode fixture bead: %v", err)
		}
		current, ok := migrated[source.ID]
		if !ok {
			divergences = append(divergences, fmt.Sprintf("%s: missing from migrated store", source.ID))
			continue
		}
		want := comparableFixtureBead(source, true)
		got := comparableFixtureBead(current.BDExportBead, false)
		divergences = append(divergences, diffFixtureBeads(source.ID, got, want)...)
	}
	if len(divergences) > 0 {
		const maxReport = 40
		report := divergences
		if len(report) > maxReport {
			report = report[:maxReport]
		}
		t.Fatalf("fixture migration divergences: %d\n%s", len(divergences), strings.Join(report, "\n"))
	}
}

func TestMigrateFromDoltOldSchemaAllowsLaterAndDanglingDependencyTargets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	seedOldBeadSchemaForMigration(t, dbPath)
	jsonlPath := writeMigrationJSONL(t, strings.Join([]string{
		`{"id":"oro-child","title":"Child","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","dependencies":[{"depends_on_id":"oro-parent","type":"blocks"},{"depends_on_id":"oro-missing","type":"blocks"}]}`,
		`{"id":"oro-parent","title":"Parent","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		"",
	}, "\n"))

	out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", jsonlPath)
	for _, want := range []string{
		"Migration complete",
		"source rows: 2",
		"imported rows: 2",
		"verification: OK (sqlite rows: 2)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("old-schema migration output missing %q:\n%s", want, out)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()
	var beadCount, depCount, fkViolations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE deleted=0`).Scan(&beadCount); err != nil {
		t.Fatalf("count migrated beads: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM bead_deps`).Scan(&depCount); err != nil {
		t.Fatalf("count migrated dependencies: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&fkViolations); err != nil {
		t.Fatalf("count foreign key violations: %v", err)
	}
	if beadCount != 2 || depCount != 2 || fkViolations != 1 {
		t.Fatalf("migrated counts beads/deps/fk = %d/%d/%d, want 2/2/1", beadCount, depCount, fkViolations)
	}
}

func TestMigrateUpdatedAtVerbatim(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	const wantUpdatedAt = "2026-01-02T03:04:05.678Z"
	jsonl := strings.Join([]string{
		`{"id":"oro-preserve","title":"Preserve updated_at","description":"Imported from bd","acceptance_criteria":"Keep source timestamps","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00.000Z","updated_at":"` + wantUpdatedAt + `","tags":["migration"],"labels":["phase-3"],"metadata":{"source":"bd"},"notes":["source note"]}`,
		"",
	}, "\n")
	jsonlPath := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(jsonl), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	cmd := newBeadCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate-from-dolt", "--from-jsonl", jsonlPath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead migrate-from-dolt error: %v\n%s", err, out.String())
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()

	var gotUpdatedAt string
	if err := db.QueryRow(`SELECT updated_at FROM beads WHERE id='oro-preserve'`).Scan(&gotUpdatedAt); err != nil {
		t.Fatalf("query migrated updated_at: %v", err)
	}
	if gotUpdatedAt != wantUpdatedAt {
		t.Fatalf("updated_at = %q, want %q", gotUpdatedAt, wantUpdatedAt)
	}
}

func TestMigrateFromDoltBackupAndReport(t *testing.T) {
	validJSONL := strings.Join([]string{
		`{"id":"oro-backup-a","title":"Backup A","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-backup-b","title":"Backup B","status":"closed","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		"",
	}, "\n")

	t.Run("writes source backup before import and reports counts", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		oroHome := filepath.Join(t.TempDir(), "oro-home")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		jsonlPath := writeMigrationJSONL(t, validJSONL)

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", jsonlPath)
		for _, want := range []string{
			"Migration complete",
			"backup snapshot:",
			filepath.Join(oroHome, "migrations"),
			"source rows: 2",
			"imported rows: 2",
			"verification: OK",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("migration backup/report output missing %q:\n%s", want, out)
			}
		}

		backupPath := migrationOutputValue(t, out, "backup snapshot:")
		if !strings.HasPrefix(backupPath, filepath.Join(oroHome, "migrations")+string(os.PathSeparator)) {
			t.Fatalf("backup path = %q, want under %s", backupPath, filepath.Join(oroHome, "migrations"))
		}
		backupData, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("read migration backup: %v", err)
		}
		if string(backupData) != validJSONL {
			t.Fatalf("backup bytes = %q, want exact source bytes %q", string(backupData), validJSONL)
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id IN ('oro-backup-a', 'oro-backup-b') AND deleted=0`).Scan(&count); err != nil {
			t.Fatalf("query migrated backup beads: %v", err)
		}
		if count != 2 {
			t.Fatalf("migrated backup bead count = %d, want 2", count)
		}
	})

	t.Run("backup failure aborts before mutation with clear error", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		oroHome := filepath.Join(t.TempDir(), "oro-home")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		if err := os.MkdirAll(oroHome, 0o700); err != nil {
			t.Fatalf("create oro home: %v", err)
		}
		if err := os.WriteFile(filepath.Join(oroHome, "migrations"), []byte("not a dir"), 0o600); err != nil {
			t.Fatalf("create migrations path conflict: %v", err)
		}
		jsonlPath := writeMigrationJSONL(t, validJSONL)

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", jsonlPath)
		if err == nil {
			t.Fatalf("migration succeeded despite backup failure:\n%s", out)
		}
		for _, want := range []string{"write migration backup", filepath.Join(oroHome, "migrations")} {
			if !strings.Contains(out+err.Error(), want) {
				t.Fatalf("backup failure output missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("backup failure mutated DB path %s: stat err=%v", dbPath, statErr)
		}
	})
}

func TestMigrateFromDoltRejectsNonEmptyNativeTarget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	staleJSONL := writeMigrationJSONL(t, `{"id":"oro-stale-native","title":"Stale native row","status":"closed","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n")
	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", staleJSONL)

	freshJSONL := writeMigrationJSONL(t, `{"id":"oro-fresh-source","title":"Fresh source row","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`+"\n")
	dryRunOut, dryRunErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--dry-run", "--from-jsonl", freshJSONL)
	if dryRunErr == nil {
		t.Fatalf("dry-run succeeded despite non-empty native target:\n%s", dryRunOut)
	}
	for _, want := range []string{
		"Migration plan",
		"migration error: native bead table is not empty (1 existing rows)",
		"DRY RUN -- no writes performed",
	} {
		if !strings.Contains(dryRunOut, want) {
			t.Fatalf("dry-run non-empty target output missing %q:\n%s", want, dryRunOut)
		}
	}

	applyOut, applyErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", freshJSONL)
	if applyErr == nil {
		t.Fatalf("apply succeeded despite non-empty native target:\n%s", applyOut)
	}
	if !strings.Contains(applyOut, "native bead table is not empty (1 existing rows)") {
		t.Fatalf("apply non-empty target output missing target guard:\n%s", applyOut)
	}
	if strings.Contains(applyOut, "Migration complete") {
		t.Fatalf("apply reported completion despite target guard:\n%s", applyOut)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open guarded db: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE deleted=0`).Scan(&count); err != nil {
		t.Fatalf("query guarded db count: %v", err)
	}
	if count != 1 {
		t.Fatalf("guarded db count = %d, want original stale row only", count)
	}
	var freshCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-fresh-source' AND deleted=0`).Scan(&freshCount); err != nil {
		t.Fatalf("query fresh source count: %v", err)
	}
	if freshCount != 0 {
		t.Fatalf("fresh source rows = %d, want 0 after guarded apply", freshCount)
	}
}

func TestMigrateFromDoltRejectsSoftDeletedNativeTarget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	staleJSONL := writeMigrationJSONL(t, `{"id":"oro-soft-stale","title":"Soft-deleted stale row","status":"closed","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n")
	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", staleJSONL)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open stale db: %v", err)
	}
	if _, err := db.Exec(`UPDATE beads SET deleted=1 WHERE id='oro-soft-stale'`); err != nil {
		_ = db.Close()
		t.Fatalf("soft-delete stale row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close stale db: %v", err)
	}

	freshJSONL := writeMigrationJSONL(t, `{"id":"oro-fresh-source","title":"Fresh source row","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`+"\n")
	dryRunOut, dryRunErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--dry-run", "--from-jsonl", freshJSONL)
	if dryRunErr == nil {
		t.Fatalf("dry-run succeeded despite soft-deleted native target row:\n%s", dryRunOut)
	}
	if !strings.Contains(dryRunOut, "migration error: native bead table is not empty (1 existing rows)") {
		t.Fatalf("dry-run soft-deleted target output missing target guard:\n%s", dryRunOut)
	}

	applyOut, applyErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", freshJSONL)
	if applyErr == nil {
		t.Fatalf("apply succeeded despite soft-deleted native target row:\n%s", applyOut)
	}
	if !strings.Contains(applyOut, "native bead table is not empty (1 existing rows)") {
		t.Fatalf("apply soft-deleted target output missing target guard:\n%s", applyOut)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open guarded db: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads`).Scan(&count); err != nil {
		t.Fatalf("query guarded db count: %v", err)
	}
	if count != 1 {
		t.Fatalf("guarded db total count = %d, want original soft-deleted row only", count)
	}
	var freshCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-fresh-source'`).Scan(&freshCount); err != nil {
		t.Fatalf("query fresh source count: %v", err)
	}
	if freshCount != 0 {
		t.Fatalf("fresh source rows = %d, want 0 after guarded apply", freshCount)
	}
}

func TestMigrateFromDoltRejectsTargetRowInsertedAfterPrecheck(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	origHook := beadMigrationAfterInitialTargetPrecheckForTest
	t.Cleanup(func() { beadMigrationAfterInitialTargetPrecheckForTest = origHook })
	beadMigrationAfterInitialTargetPrecheckForTest = func() {
		db, err := openStateDB(dbPath)
		if err != nil {
			t.Fatalf("open state db in race hook: %v", err)
		}
		defer db.Close()
		_, err = db.Exec(`INSERT INTO beads (id, title, status, priority, type, created_at, updated_at) VALUES ('oro-race-stale', 'Race stale row', 'closed', 2, 'task', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		if err != nil {
			t.Fatalf("insert stale row in race hook: %v", err)
		}
	}

	freshJSONL := writeMigrationJSONL(t, `{"id":"oro-fresh-source","title":"Fresh source row","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`+"\n")
	applyOut, applyErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", freshJSONL)
	if applyErr == nil {
		t.Fatalf("apply succeeded despite target row inserted after precheck:\n%s", applyOut)
	}
	if !strings.Contains(applyOut, "native bead table is not empty (1 existing rows)") {
		t.Fatalf("apply race output missing target guard:\n%s", applyOut)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open guarded db: %v", err)
	}
	defer db.Close()
	var staleCount, freshCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-race-stale'`).Scan(&staleCount); err != nil {
		t.Fatalf("query race stale count: %v", err)
	}
	if staleCount != 1 {
		t.Fatalf("race stale rows = %d, want 1", staleCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-fresh-source'`).Scan(&freshCount); err != nil {
		t.Fatalf("query fresh source count: %v", err)
	}
	if freshCount != 0 {
		t.Fatalf("fresh source rows = %d, want 0 after guarded race apply", freshCount)
	}
}

func TestMigrateFromDoltValidationReport(t *testing.T) {
	jsonlPath := writeMigrationJSONL(t, strings.Join([]string{
		`{"id":"oro-default-priority","title":"Default priority","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-p0","title":"Explicit P0","status":"open","priority":0,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-blocked","title":"Blocked","status":"blocked","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-bad-notes","title":"Bad notes","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","notes":[123]}`,
		`{"id":"oro-unknown-status","title":"Unknown status","status":"triaged","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-malformed","title":"Broken",`,
		`{"id":"oro-after-error","title":"Valid after errors","status":"closed","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","unexpected_field":"reported"}`,
		"",
	}, "\n"))

	t.Run("dry run reports row validation errors", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
		t.Setenv("ORO_PROJECT", "")

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--dry-run", "--from-jsonl", jsonlPath)
		if err == nil {
			t.Fatalf("dry-run succeeded despite validation errors:\n%s", out)
		}
		for _, want := range []string{
			"Migration plan",
			"beads: 4",
			"unknown fields: 1",
			"migration errors: 3",
			"line 4: decode notes for oro-bad-notes",
			"line 5: unknown status \"triaged\"",
			"line 6: decode bd export JSONL",
			"DRY RUN -- no writes performed",
		} {
			if !strings.Contains(out+err.Error(), want) {
				t.Fatalf("dry-run validation report missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("dry-run validation mutated DB path %s: stat err=%v", dbPath, statErr)
		}
	})

	t.Run("real import reports validation errors and imports safe rows", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
		t.Setenv("ORO_PROJECT", "")

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", jsonlPath)
		if err == nil {
			t.Fatalf("migration succeeded despite validation errors:\n%s", out)
		}
		for _, want := range []string{
			"Migration complete",
			"unknown fields: 1",
			"migration errors: 3",
			"line 4: decode notes for oro-bad-notes",
			"line 5: unknown status \"triaged\"",
			"line 6: decode bd export JSONL",
		} {
			if !strings.Contains(out+err.Error(), want) {
				t.Fatalf("migration validation report missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT id, status, priority FROM beads ORDER BY id`)
		if err != nil {
			t.Fatalf("query migrated beads: %v", err)
		}
		defer rows.Close()
		got := map[string]struct {
			status   string
			priority int
		}{}
		for rows.Next() {
			var id string
			var row struct {
				status   string
				priority int
			}
			if err := rows.Scan(&id, &row.status, &row.priority); err != nil {
				t.Fatalf("scan migrated bead: %v", err)
			}
			got[id] = row
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate migrated beads: %v", err)
		}
		want := map[string]struct {
			status   string
			priority int
		}{
			"oro-after-error":      {status: "closed", priority: 3},
			"oro-blocked":          {status: "blocked", priority: 1},
			"oro-default-priority": {status: "open", priority: 2},
			"oro-p0":               {status: "open", priority: 0},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("migrated rows = %#v, want %#v", got, want)
		}
		if _, ok := got["oro-unknown-status"]; ok {
			t.Fatalf("unknown status row was silently imported: %#v", got["oro-unknown-status"])
		}
		if _, ok := got["oro-bad-notes"]; ok {
			t.Fatalf("bad notes row was imported: %#v", got["oro-bad-notes"])
		}
	})

	t.Run("real import rolls back unsafe partial row and continues", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
		t.Setenv("ORO_PROJECT", "")
		partialJSONLPath := writeMigrationJSONL(t, strings.Join([]string{
			`{"id":"oro-before-partial","title":"Before partial","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			`{"id":"oro-partial","title":"Partial","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","tags":["dup","dup"]}`,
			`{"id":"oro-after-partial","title":"After partial","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			"",
		}, "\n"))

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", partialJSONLPath)
		if err == nil {
			t.Fatalf("migration succeeded despite unsafe partial row:\n%s", out)
		}
		for _, want := range []string{
			"Migration complete",
			"migration errors: 1",
			"row oro-partial: insert migrated tag for oro-partial",
		} {
			if !strings.Contains(out+err.Error(), want) {
				t.Fatalf("partial-row report missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		var partialCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-partial'`).Scan(&partialCount); err != nil {
			t.Fatalf("query partial bead: %v", err)
		}
		if partialCount != 0 {
			t.Fatalf("partial failed row persisted %d bead(s), want 0", partialCount)
		}
		var safeCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id IN ('oro-before-partial', 'oro-after-partial')`).Scan(&safeCount); err != nil {
			t.Fatalf("query safe beads: %v", err)
		}
		if safeCount != 2 {
			t.Fatalf("safe row count = %d, want 2", safeCount)
		}
		var tagCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM bead_tags WHERE bead_id='oro-partial'`).Scan(&tagCount); err != nil {
			t.Fatalf("query partial tags: %v", err)
		}
		if tagCount != 0 {
			t.Fatalf("partial failed row persisted %d tag(s), want 0", tagCount)
		}
	})
}

func TestMigrateFromDoltLocks(t *testing.T) {
	validJSONL := writeMigrationJSONL(t, `{"id":"oro-lock","title":"Lock migration","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n")
	invalidJSONL := writeMigrationJSONL(t, `{"id":"oro-lock-fail","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n")

	t.Run("refuses while dispatcher state db lock is active", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		if err := os.WriteFile(dbPath+".lock", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write dispatcher lock: %v", err)
		}

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if err == nil {
			t.Fatalf("migration succeeded with active dispatcher lock:\n%s", out)
		}
		for _, want := range []string{"dispatcher is running", "state.db", "stop it first"} {
			if !strings.Contains(err.Error()+out, want) {
				t.Fatalf("dispatcher lock error missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}
	})

	t.Run("uses canonical dispatcher lock path for symlinked state db", func(t *testing.T) {
		realDBPath, linkDBPath := setupSymlinkedMigrationDBEnv(t)
		if err := os.WriteFile(realDBPath+".lock", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write canonical dispatcher lock: %v", err)
		}

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if err == nil {
			t.Fatalf("migration succeeded despite canonical dispatcher lock via %s:\n%s", linkDBPath, out)
		}
		if !strings.Contains(err.Error()+out, "dispatcher is running") {
			t.Fatalf("canonical dispatcher lock error missing actionable message:\nerr=%v\nout=%s", err, out)
		}
	})

	t.Run("reclaims old live dispatcher lock", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		lockPath := dbPath + ".lock"
		if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write dispatcher lock: %v", err)
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(lockPath, old, old); err != nil {
			t.Fatalf("age dispatcher lock: %v", err)
		}

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		for _, want := range []string{"reclaiming stale dispatcher lock", "Migration complete"} {
			if !strings.Contains(out, want) {
				t.Fatalf("old dispatcher lock output missing %q:\n%s", want, out)
			}
		}
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("dispatcher lock not released after old-live reclaim: %v", err)
		}
	})

	t.Run("does not remove fresh dispatcher lock after stale race", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		canonicalDBPath, err := canonicalBeadMigrationStateDBPath(dbPath)
		if err != nil {
			t.Fatalf("canonicalize db path: %v", err)
		}
		lockPath := canonicalDBPath + ".lock"
		if err := os.WriteFile(lockPath, []byte("999999"), 0o600); err != nil {
			t.Fatalf("write stale dispatcher lock: %v", err)
		}

		freshPID := strconv.Itoa(os.Getpid())
		previousHook := beadMigrationBeforeRemoveInspectedPIDLockForTest
		raceAttempted := false
		beadMigrationBeforeRemoveInspectedPIDLockForTest = func(lock inspectedPIDLock) {
			if lock.path != lockPath {
				return
			}
			raceAttempted = true
			beadMigrationBeforeRemoveInspectedPIDLockForTest = func(inspectedPIDLock) {}
			if err := os.WriteFile(lockPath, []byte(freshPID), 0o600); err != nil {
				t.Fatalf("replace stale dispatcher lock with fresh lock: %v", err)
			}
		}
		defer func() {
			beadMigrationBeforeRemoveInspectedPIDLockForTest = previousHook
		}()

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if err == nil {
			t.Fatalf("migration succeeded after fresh dispatcher lock race:\n%s", out)
		}
		if !raceAttempted {
			t.Fatalf("test did not reach dispatcher stale removal race window")
		}
		if !strings.Contains(err.Error()+out, "dispatcher is running") {
			t.Fatalf("dispatcher race error missing actionable message:\nerr=%v\nout=%s", err, out)
		}
		got, readErr := os.ReadFile(lockPath)
		if readErr != nil {
			t.Fatalf("fresh dispatcher lock missing after stale race: %v", readErr)
		}
		if strings.TrimSpace(string(got)) != freshPID {
			t.Fatalf("fresh dispatcher lock content = %q, want %q", strings.TrimSpace(string(got)), freshPID)
		}
	})

	t.Run("refuses concurrent migration lock", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		if err := os.WriteFile(dbPath+".migrate.lock", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write migrate lock: %v", err)
		}

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if err == nil {
			t.Fatalf("migration succeeded with active migrate lock:\n%s", out)
		}
		for _, want := range []string{"another migration is running", "state.db", "migrate.lock"} {
			if !strings.Contains(err.Error()+out, want) {
				t.Fatalf("migrate lock error missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}
	})

	t.Run("uses canonical migrate lock path for symlinked state db", func(t *testing.T) {
		realDBPath, linkDBPath := setupSymlinkedMigrationDBEnv(t)
		if err := os.WriteFile(realDBPath+".migrate.lock", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write canonical migrate lock: %v", err)
		}

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if err == nil {
			t.Fatalf("migration succeeded despite canonical migrate lock via %s:\n%s", linkDBPath, out)
		}
		if !strings.Contains(err.Error()+out, "another migration is running") {
			t.Fatalf("canonical migrate lock error missing actionable message:\nerr=%v\nout=%s", err, out)
		}
	})

	t.Run("reclaims old live migrate lock", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		lockPath := dbPath + ".migrate.lock"
		if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write migrate lock: %v", err)
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(lockPath, old, old); err != nil {
			t.Fatalf("age migrate lock: %v", err)
		}

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		for _, want := range []string{"reclaiming stale migration lock", "Migration complete"} {
			if !strings.Contains(out, want) {
				t.Fatalf("old migrate lock output missing %q:\n%s", want, out)
			}
		}
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("migrate lock not released after old-live reclaim: %v", err)
		}
	})

	t.Run("does not remove fresh migrate lock after stale race", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		lockPath := dbPath + ".migrate.lock"
		if err := os.WriteFile(lockPath, []byte("999999"), 0o600); err != nil {
			t.Fatalf("write stale migrate lock: %v", err)
		}
		staleLock, err := inspectPIDLock(lockPath)
		if err != nil {
			t.Fatalf("inspect stale migrate lock: %v", err)
		}
		if !staleLock.stale {
			t.Fatalf("test lock is not stale: %+v", staleLock)
		}

		freshPID := strconv.Itoa(os.Getpid())
		if err := os.WriteFile(lockPath, []byte(freshPID), 0o600); err != nil {
			t.Fatalf("replace stale lock with fresh lock: %v", err)
		}
		removed, err := removeInspectedPIDLock(staleLock)
		if err != nil {
			t.Fatalf("remove stale migrate lock after replacement: %v", err)
		}
		if removed {
			t.Fatalf("removed fresh migrate lock after stale inspection race")
		}
		got, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatalf("fresh migrate lock missing after race: %v", err)
		}
		if strings.TrimSpace(string(got)) != freshPID {
			t.Fatalf("fresh migrate lock content = %q, want %q", strings.TrimSpace(string(got)), freshPID)
		}

		unlock, err := acquireBeadMigrationSelfLock(lockPath, nil)
		if err == nil {
			_ = unlock()
			t.Fatalf("acquired migration lock despite fresh replacement")
		}
		if !strings.Contains(err.Error(), "another migration is running") {
			t.Fatalf("fresh replacement error missing actionable message: %v", err)
		}
		got, err = os.ReadFile(lockPath)
		if err != nil {
			t.Fatalf("fresh migrate lock removed after failed acquire: %v", err)
		}
		if strings.TrimSpace(string(got)) != freshPID {
			t.Fatalf("fresh migrate lock content after failed acquire = %q, want %q", strings.TrimSpace(string(got)), freshPID)
		}
	})

	t.Run("guard blocks fresh migrate lock during stale removal window", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		canonicalDBPath, err := canonicalBeadMigrationStateDBPath(dbPath)
		if err != nil {
			t.Fatalf("canonicalize db path: %v", err)
		}
		lockPath := canonicalDBPath + ".migrate.lock"
		if err := os.WriteFile(lockPath, []byte("999999"), 0o600); err != nil {
			t.Fatalf("write stale migrate lock: %v", err)
		}

		previousHook := beadMigrationBeforeRemoveInspectedPIDLockForTest
		raceAttempted := false
		beadMigrationBeforeRemoveInspectedPIDLockForTest = func(lock inspectedPIDLock) {
			if lock.path != lockPath {
				return
			}
			raceAttempted = true
			beadMigrationBeforeRemoveInspectedPIDLockForTest = func(inspectedPIDLock) {}
			unlock, err := acquireBeadMigrationSelfLock(lockPath, nil)
			if err == nil {
				_ = unlock()
				t.Fatalf("competing migration acquired lock during stale removal window")
			}
			if !strings.Contains(err.Error(), "migration is acquiring") {
				t.Fatalf("competing migration error missing guard message: %v", err)
			}
		}
		defer func() {
			beadMigrationBeforeRemoveInspectedPIDLockForTest = previousHook
		}()

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if !raceAttempted {
			t.Fatalf("test did not reach stale removal race window")
		}
		if !strings.Contains(out, "Migration complete") {
			t.Fatalf("migration output missing completion:\n%s", out)
		}
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("migrate lock not released after guarded stale reclaim: %v", err)
		}
		if _, err := os.Stat(lockPath + ".guard"); !os.IsNotExist(err) {
			t.Fatalf("migrate guard lock not released after guarded stale reclaim: %v", err)
		}
	})

	t.Run("removes stale migrate lock and releases on success", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		if err := os.WriteFile(dbPath+".migrate.lock", []byte("999999"), 0o600); err != nil {
			t.Fatalf("write stale migrate lock: %v", err)
		}

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", validJSONL)
		if !strings.Contains(out, "Migration complete") {
			t.Fatalf("migration output missing completion:\n%s", out)
		}
		if _, err := os.Stat(dbPath + ".migrate.lock"); !os.IsNotExist(err) {
			t.Fatalf("migrate lock not released after success: %v", err)
		}
	})

	t.Run("releases migrate lock after failure", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--from-jsonl", invalidJSONL)
		if err == nil {
			t.Fatalf("migration unexpectedly succeeded:\n%s", out)
		}
		if !strings.Contains(err.Error()+out, "missing title") {
			t.Fatalf("migration failed for unexpected reason:\nerr=%v\nout=%s", err, out)
		}
		if _, statErr := os.Stat(dbPath + ".migrate.lock"); !os.IsNotExist(statErr) {
			t.Fatalf("migrate lock not released after failure: %v", statErr)
		}
	})
}

func TestMigrateFromDoltCorruptionPreflight(t *testing.T) {
	validJSONL := `{"id":"oro-preflight","title":"Preflight migration","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}` + "\n"

	t.Run("default source aborts on export and dolt count mismatch without mutating", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		fake := &fakeBeadMigrationRunner{
			outputs: map[string][]byte{
				key("bd", "export"): []byte(validJSONL),
				key("dolt", beadMigrationDefaultDoltCountArgs...): []byte(`{"rows":[{"count":2}]}`),
			},
		}
		restoreBeadMigrationRunner(t, fake)

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--dry-run")
		if err == nil {
			t.Fatalf("migration succeeded despite preflight mismatch:\n%s", out)
		}
		for _, want := range []string{"pre-flight", "bd export count: 1", "dolt internal count: 2", "partial dolt corruption", "--force-recover", "Aborting"} {
			if !strings.Contains(err.Error()+out, want) {
				t.Fatalf("preflight mismatch output missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("dry-run mismatch mutated DB path %s: stat err=%v", dbPath, statErr)
		}
		if fake.count("bd", "export") != 1 || fake.count("dolt", beadMigrationDefaultDoltCountArgs...) != 1 {
			t.Fatalf("unexpected command calls: %#v", fake.calls)
		}
	})

	t.Run("default source reports matched preflight counts on dry-run", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		fake := &fakeBeadMigrationRunner{
			outputs: map[string][]byte{
				key("bd", "export"): []byte(validJSONL),
				key("dolt", beadMigrationDefaultDoltCountArgs...): []byte(`{"rows":[{"count":1}]}`),
			},
		}
		restoreBeadMigrationRunner(t, fake)

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--dry-run")
		for _, want := range []string{
			"Pre-flight verified Dolt and bd export counts",
			"bd export count: 1",
			"dolt internal count: 1",
			"Migration plan",
			"DRY RUN -- no writes performed",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("matched preflight output missing %q:\n%s", want, out)
			}
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("dry-run matched preflight mutated DB path %s: stat err=%v", dbPath, statErr)
		}
		if fake.count("bd", "export") != 1 || fake.count("dolt", beadMigrationDefaultDoltCountArgs...) != 1 {
			t.Fatalf("unexpected command calls: %#v", fake.calls)
		}
	})

	t.Run("force recover reports acknowledgement and migrates readable export", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		fake := &fakeBeadMigrationRunner{
			outputs: map[string][]byte{
				key("bd", "export"): []byte(validJSONL),
				key("dolt", beadMigrationDefaultDoltCountArgs...): []byte(`{"rows":[{"count":2}]}`),
			},
		}
		restoreBeadMigrationRunner(t, fake)

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--force-recover")
		for _, want := range []string{"WARNING", "--force-recover", "bd export count: 1", "dolt internal count: 2", "data loss acknowledged", "source: bd export", "Migration complete"} {
			if !strings.Contains(out, want) {
				t.Fatalf("force-recover output missing %q:\n%s", want, out)
			}
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-preflight' AND deleted=0`).Scan(&count); err != nil {
			t.Fatalf("query migrated bead: %v", err)
		}
		if count != 1 {
			t.Fatalf("migrated bead count = %d, want 1", count)
		}
	})

	t.Run("default source reports malformed JSONL rows after preflight", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		export := strings.Join([]string{
			`{"id":"oro-default-valid","title":"Default valid","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			`{"id":"oro-default-bad","title":"Broken",`,
			`{"id":"oro-default-after","title":"Default after","status":"closed","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
			"",
		}, "\n")
		fake := &fakeBeadMigrationRunner{
			outputs: map[string][]byte{
				key("bd", "export"): []byte(export),
				key("dolt", beadMigrationDefaultDoltCountArgs...): []byte(`{"rows":[{"count":3}]}`),
			},
		}
		restoreBeadMigrationRunner(t, fake)

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt")
		if err == nil {
			t.Fatalf("default migration succeeded despite malformed row:\n%s", out)
		}
		for _, want := range []string{
			"Migration complete",
			"source: bd export",
			"migration errors: 1",
			"line 2: decode bd export JSONL",
		} {
			if !strings.Contains(out+err.Error(), want) {
				t.Fatalf("default malformed-row output missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id IN ('oro-default-valid', 'oro-default-after') AND deleted=0`).Scan(&count); err != nil {
			t.Fatalf("query migrated default beads: %v", err)
		}
		if count != 2 {
			t.Fatalf("default migrated safe row count = %d, want 2", count)
		}
	})

	t.Run("default source aborts on dolt count error unless forced", func(t *testing.T) {
		dbPath := setupMigrationLockTestEnv(t)
		fake := &fakeBeadMigrationRunner{
			outputs: map[string][]byte{
				key("bd", "export"): []byte(validJSONL),
			},
			errs: map[string]error{
				key("dolt", beadMigrationDefaultDoltCountArgs...): fmt.Errorf("dolt corruption"),
			},
		}
		restoreBeadMigrationRunner(t, fake)

		out, err := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--dry-run")
		if err == nil {
			t.Fatalf("migration succeeded despite dolt count error:\n%s", out)
		}
		for _, want := range []string{"pre-flight", "bd export count: 1", "dolt internal count error", "dolt corruption", "--force-recover", "Aborting"} {
			if !strings.Contains(err.Error()+out, want) {
				t.Fatalf("preflight error output missing %q:\nerr=%v\nout=%s", want, err, out)
			}
		}
		if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
			t.Fatalf("dry-run count error mutated DB path %s: stat err=%v", dbPath, statErr)
		}
	})

	t.Run("force recover continues after dolt count error", func(t *testing.T) {
		fake := &fakeBeadMigrationRunner{
			outputs: map[string][]byte{
				key("bd", "export"): []byte(validJSONL),
			},
			errs: map[string]error{
				key("dolt", beadMigrationDefaultDoltCountArgs...): fmt.Errorf("dolt corruption"),
			},
		}
		restoreBeadMigrationRunner(t, fake)
		setupMigrationLockTestEnv(t)

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--force-recover")
		for _, want := range []string{"WARNING", "dolt internal count error", "dolt corruption", "data loss acknowledged", "Migration complete"} {
			if !strings.Contains(out, want) {
				t.Fatalf("force-recover count-error output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("explicit jsonl and fixture sources skip dolt preflight", func(t *testing.T) {
		jsonlPath := writeMigrationJSONL(t, validJSONL)
		fixtureDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(fixtureDir, "export.jsonl"), []byte(validJSONL), 0o600); err != nil {
			t.Fatalf("write fixture export: %v", err)
		}
		for _, tc := range []struct {
			name string
			args []string
		}{
			{name: "jsonl", args: []string{"migrate-from-dolt", "--dry-run", "--from-jsonl", jsonlPath}},
			{name: "fixture", args: []string{"migrate-from-dolt", "--dry-run", "--from-fixture", fixtureDir}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeBeadMigrationRunner{}
				restoreBeadMigrationRunner(t, fake)
				setupMigrationLockTestEnv(t)

				out := runBeadMigrateCommand(t, tc.args...)
				if !strings.Contains(out, "Migration plan") {
					t.Fatalf("expected dry-run plan:\n%s", out)
				}
				if fake.count("dolt", beadMigrationDefaultDoltCountArgs...) != 0 {
					t.Fatalf("explicit source unexpectedly ran dolt preflight: %#v", fake.calls)
				}
			})
		}
	})

	t.Run("deferred status imports as open with defer_until preserved", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
		t.Setenv("ORO_PROJECT", "")
		deferredUntil := "2099-01-01T00:00:00Z"
		jsonlPath := writeMigrationJSONL(t, `{"id":"oro-deferred","title":"Deferred","status":"deferred","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","defer_until":"`+deferredUntil+`"}`+"\n")

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", jsonlPath)
		for _, want := range []string{
			"Migration complete",
			"migration warnings: 1",
			"status \"deferred\" will be stored as open with defer_until preserved",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("deferred migration output missing %q:\n%s", want, out)
			}
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		var status, gotDeferredUntil string
		if err := db.QueryRow(`SELECT status, deferred_until FROM beads WHERE id='oro-deferred'`).Scan(&status, &gotDeferredUntil); err != nil {
			t.Fatalf("query deferred bead: %v", err)
		}
		if status != "open" || gotDeferredUntil != deferredUntil {
			t.Fatalf("deferred row status/deferred_until = %q/%q, want open/%q", status, gotDeferredUntil, deferredUntil)
		}
	})

	t.Run("deferred status without defer_until imports with sentinel defer_until", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "state.db")
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
		t.Setenv("ORO_PROJECT", "")
		jsonlPath := writeMigrationJSONL(t, `{"id":"oro-bad-deferred","title":"Bad deferred","status":"deferred","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n")

		out := runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", jsonlPath)
		for _, want := range []string{
			"Migration complete",
			"migration warnings: 1",
			beadMigrationDeferredWithoutUntil,
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing-until deferred output missing %q:\n%s", want, out)
			}
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open migrated db: %v", err)
		}
		defer db.Close()
		var status, gotDeferredUntil string
		if err := db.QueryRow(`SELECT status, deferred_until FROM beads WHERE id='oro-bad-deferred'`).Scan(&status, &gotDeferredUntil); err != nil {
			t.Fatalf("query missing-until deferred bead: %v", err)
		}
		if status != "open" || gotDeferredUntil != beadMigrationDeferredWithoutUntil {
			t.Fatalf("missing-until deferred row status/deferred_until = %q/%q, want open/%q", status, gotDeferredUntil, beadMigrationDeferredWithoutUntil)
		}
	})
}

func TestBeadMigrationDoltCountArgs(t *testing.T) {
	t.Run("missing metadata uses default local dolt sql", func(t *testing.T) {
		args, err := beadMigrationDoltCountArgsForBeadsDir(t.TempDir())
		if err != nil {
			t.Fatalf("beadMigrationDoltCountArgsForBeadsDir error: %v", err)
		}
		if !reflect.DeepEqual(args, beadMigrationDefaultDoltCountArgs) {
			t.Fatalf("args = %#v, want %#v", args, beadMigrationDefaultDoltCountArgs)
		}
	})

	t.Run("server metadata selects configured host port and database", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeDoltCountMetadata(t, beadsDir, `{
			"backend":"dolt",
			"dolt_mode":"server",
			"dolt_server_port":13310,
			"dolt_database":"beads_oro"
		}`)

		args, err := beadMigrationDoltCountArgsForBeadsDir(beadsDir)
		if err != nil {
			t.Fatalf("beadMigrationDoltCountArgsForBeadsDir error: %v", err)
		}
		want := []string{
			"--host", "127.0.0.1",
			"--port", "13310",
			"--no-tls",
			"--use-db", "beads_oro",
			"sql",
			"--result-format", "json",
			"-q", beadMigrationDoltCountQuery,
		}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
	})

	t.Run("local dolt metadata selects dolt data dir and database", func(t *testing.T) {
		beadsDir := t.TempDir()
		writeDoltCountMetadata(t, beadsDir, `{
			"backend":"dolt",
			"dolt_database":"beads_local"
		}`)

		args, err := beadMigrationDoltCountArgsForBeadsDir(beadsDir)
		if err != nil {
			t.Fatalf("beadMigrationDoltCountArgsForBeadsDir error: %v", err)
		}
		want := []string{
			"--data-dir", filepath.Join(beadsDir, "dolt"),
			"--use-db", "beads_local",
			"sql",
			"--result-format", "json",
			"-q", beadMigrationDoltCountQuery,
		}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
	})
}

func writeDoltCountMetadata(t *testing.T, beadsDir, metadata string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

type comparableMigratedFixtureBead struct {
	ID                 string
	Title              string
	Description        string
	AcceptanceCriteria string
	Status             string
	Priority           int
	Type               string
	ParentID           string
	Owner              string
	EstimatedMinutes   int
	Tier               string
	Model              string
	DeferredUntil      string
	CloseReason        string
	CreatedAt          string
	UpdatedAt          string
	ClosedAt           string
	Dependencies       []string
	Tags               []string
	Labels             []string
	Metadata           map[string]string
	Notes              []string
}

func comparableFixtureBead(bead bdExportBead, source bool) comparableMigratedFixtureBead {
	description := bead.Description
	acceptanceCriteria := bead.AcceptanceCriteria
	if source {
		extractedAC, strippedDescription := expectedFixtureExtractAndStripAC(description)
		description = strippedDescription
		if strings.TrimSpace(acceptanceCriteria) == "" {
			acceptanceCriteria = extractedAC
		}
	}

	deps := make([]string, 0, len(bead.Dependencies))
	for _, dep := range bead.Dependencies {
		if strings.TrimSpace(dep.DependsOnID) == "" {
			continue
		}
		deps = append(deps, dep.DependsOnID+"\x00"+firstNonEmpty(dep.Type, "blocks"))
	}
	sort.Strings(deps)

	metadata := map[string]string{}
	for key, value := range bead.Metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		encoded, err := migrationMetadataValue(value)
		if err == nil {
			metadata[key] = encoded
		}
	}
	notes, _ := migrationNotes(bead.Notes)

	return comparableMigratedFixtureBead{
		ID:                 bead.ID,
		Title:              bead.Title,
		Description:        description,
		AcceptanceCriteria: acceptanceCriteria,
		Status:             normalizeMigrationInsertStatus(bead.Status),
		Priority:           bead.Priority,
		Type:               firstNonEmpty(bead.IssueType, bead.Type, "task"),
		ParentID:           firstNonEmpty(bead.ParentID, bead.Parent),
		Owner:              firstNonEmpty(bead.Owner, bead.Assignee),
		EstimatedMinutes:   bead.EstimatedMinutes,
		Tier:               bead.Tier,
		Model:              bead.Model,
		DeferredUntil:      firstNonEmpty(bead.DeferredUntil, bead.DeferUntil),
		CloseReason:        bead.CloseReason,
		CreatedAt:          firstNonEmpty(bead.CreatedAt, bead.UpdatedAt),
		UpdatedAt:          firstNonEmpty(bead.UpdatedAt, bead.CreatedAt),
		ClosedAt:           bead.ClosedAt,
		Dependencies:       deps,
		Tags:               sortedNonEmptyCopy(bead.Tags),
		Labels:             sortedNonEmptyCopy(bead.Labels),
		Metadata:           metadata,
		Notes:              sortedNonEmptyCopy(notes),
	}
}

func expectedFixtureExtractAndStripAC(description string) (string, string) {
	idx, headerLen := expectedFixtureACHeader(description)
	if idx < 0 {
		return "", description
	}

	bodyStart := idx + headerLen
	for bodyStart < len(description) && (description[bodyStart] == '\r' || description[bodyStart] == '\n') {
		bodyStart++
	}

	body := description[bodyStart:]
	acEnd := len(body)
	suffix := ""
	if next := strings.Index(body, "\n## "); next >= 0 {
		acEnd = next
		suffix = body[next+1:]
	}

	ac := strings.Trim(body[:acEnd], " \t\r\n")
	desc := strings.TrimRight(description[:idx], " \t\r\n")
	if suffix != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += strings.TrimLeft(suffix, "\r\n")
	}
	desc = strings.TrimRight(desc, " \t\r\n")
	return ac, desc
}

func expectedFixtureACHeader(description string) (int, int) {
	lower := strings.ToLower(description)
	for _, header := range []string{"## acceptance criteria", "acceptance criteria"} {
		if strings.HasPrefix(lower, header) {
			return 0, len(header)
		}
		if idx := strings.Index(lower, "\n"+header); idx >= 0 {
			return idx + 1, len(header)
		}
	}
	return -1, 0
}

func diffFixtureBeads(id string, got, want comparableMigratedFixtureBead) []string {
	gotValue := reflect.ValueOf(got)
	wantValue := reflect.ValueOf(want)
	gotType := gotValue.Type()

	var divergences []string
	for i := 0; i < gotValue.NumField(); i++ {
		field := gotType.Field(i).Name
		gotField := gotValue.Field(i).Interface()
		wantField := wantValue.Field(i).Interface()
		if !reflect.DeepEqual(gotField, wantField) {
			divergences = append(divergences, fmt.Sprintf("%s.%s: got %#v want %#v", id, field, gotField, wantField))
		}
	}
	return divergences
}

func TestMigrateFromDoltReconcileApplyIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	tmpDir := t.TempDir()
	initialJSONL := filepath.Join(tmpDir, "initial.jsonl")
	if err := os.WriteFile(initialJSONL, []byte(strings.Join([]string{
		`{"id":"oro-stale","title":"Old title","description":"Old description","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:04.9999Z","tags":["old"],"labels":["keep"],"metadata":{"k":"old"},"notes":["old note"]}`,
		`{"id":"oro-conflict","title":"Old tied title","description":"Same timestamp conflict","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:06Z"}`,
		`{"id":"oro-deleted","title":"Deleted in bd","description":"Only in sqlite","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write initial jsonl: %v", err)
	}
	reconcileJSONL := filepath.Join(tmpDir, "reconcile.jsonl")
	if err := os.WriteFile(reconcileJSONL, []byte(strings.Join([]string{
		`{"id":"oro-stale","title":"New title","description":"New description","status":"closed","priority":1,"issue_type":"bug","owner":"Aakash","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:05.1001Z","closed_at":"2026-01-02T03:04:05.1001Z","close_reason":"done","dependencies":[{"depends_on_id":"oro-new","type":"related-to"}],"tags":["new"],"labels":["keep","fresh"],"metadata":{"k":"new"},"notes":["fresh note"]}`,
		`{"id":"oro-conflict","title":"BD tied title","description":"Same timestamp conflict","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:06Z"}`,
		`{"id":"oro-new","title":"Created in bd","description":"New bead","status":"open","priority":3,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","tags":["created"]}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write reconcile jsonl: %v", err)
	}

	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", initialJSONL)
	dryRunOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"inserts: 1", "updates: 1", "deletes: 1", "conflicts: 1", "conflict: oro-conflict", "DRY RUN -- pass --apply to write changes"} {
		if !strings.Contains(dryRunOut, want) {
			t.Fatalf("dry-run reconcile output missing %q:\n%s", want, dryRunOut)
		}
	}
	var dryRunTitle string
	dbBeforeApply, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open dry-run db: %v", err)
	}
	if err := dbBeforeApply.QueryRow(`SELECT title FROM beads WHERE id='oro-stale' AND deleted=0`).Scan(&dryRunTitle); err != nil {
		_ = dbBeforeApply.Close()
		t.Fatalf("query dry-run bead: %v", err)
	}
	if err := dbBeforeApply.Close(); err != nil {
		t.Fatalf("close dry-run db: %v", err)
	}
	if dryRunTitle != "Old title" {
		t.Fatalf("plain --reconcile mutated title to %q, want Old title", dryRunTitle)
	}

	firstOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"inserts: 1", "updates: 1", "deletes: 1", "conflicts: 1", "conflict: oro-conflict"} {
		if !strings.Contains(firstOut, want) {
			t.Fatalf("first reconcile output missing %q:\n%s", want, firstOut)
		}
	}

	secondOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"inserts: 0", "updates: 0", "deletes: 0", "conflicts: 0"} {
		if !strings.Contains(secondOut, want) {
			t.Fatalf("second reconcile output missing %q:\n%s", want, secondOut)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open reconciled db: %v", err)
	}
	defer db.Close()

	var title, status, updatedAt string
	if err := db.QueryRow(`SELECT title, status, updated_at FROM beads WHERE id='oro-stale' AND deleted=0`).Scan(&title, &status, &updatedAt); err != nil {
		t.Fatalf("query updated bead: %v", err)
	}
	if title != "New title" || status != "closed" || updatedAt != "2026-01-02T03:04:05.1001Z" {
		t.Fatalf("updated bead = (%q, %q, %q), want New title/closed/source updated_at", title, status, updatedAt)
	}
	var conflictTitle string
	if err := db.QueryRow(`SELECT title FROM beads WHERE id='oro-conflict' AND deleted=0`).Scan(&conflictTitle); err != nil {
		t.Fatalf("query conflict bead: %v", err)
	}
	if conflictTitle != "BD tied title" {
		t.Fatalf("conflict title = %q, want bd-wins value", conflictTitle)
	}
	var deleted int
	if err := db.QueryRow(`SELECT deleted FROM beads WHERE id='oro-deleted'`).Scan(&deleted); err != nil {
		t.Fatalf("query deleted bead: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("oro-deleted deleted = %d, want 1", deleted)
	}
}

func TestMigrateFromDoltReconcileApplyBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	oroHome := filepath.Join(t.TempDir(), "oro-home")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")

	tmpDir := t.TempDir()
	initialJSONL := filepath.Join(tmpDir, "initial.jsonl")
	if err := os.WriteFile(initialJSONL, []byte(strings.Join([]string{
		`{"id":"oro-backup-delete","title":"Delete candidate","description":"Only in sqlite","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-backup-stale","title":"Old title","description":"Old description","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:04Z","tags":["old"],"labels":["keep"],"metadata":{"k":"old"},"notes":["old note"]}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write initial jsonl: %v", err)
	}
	reconcileJSONL := filepath.Join(tmpDir, "reconcile.jsonl")
	if err := os.WriteFile(reconcileJSONL, []byte(strings.Join([]string{
		`{"id":"oro-backup-new","title":"Created in bd","description":"New bead","status":"open","priority":3,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"oro-backup-stale","title":"New title","description":"New description","status":"closed","priority":1,"issue_type":"bug","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:05Z","closed_at":"2026-01-02T03:04:05Z","close_reason":"done","tags":["new"]}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write reconcile jsonl: %v", err)
	}

	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", initialJSONL)

	firstOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"backup snapshot:", "inserts: 1", "updates: 1", "deletes: 1", "conflicts: 0", "APPLIED"} {
		if !strings.Contains(firstOut, want) {
			t.Fatalf("first reconcile output missing %q:\n%s", want, firstOut)
		}
	}
	firstBackupPath := migrationOutputValue(t, firstOut, "backup snapshot:")
	if !strings.HasPrefix(firstBackupPath, filepath.Join(oroHome, "migrations")+string(os.PathSeparator)) {
		t.Fatalf("first backup path = %q, want under %s", firstBackupPath, filepath.Join(oroHome, "migrations"))
	}
	firstBackup := readMigrationBackupBeads(t, firstBackupPath)
	if got := firstBackup["oro-backup-stale"].Title; got != "Old title" {
		t.Fatalf("pre-apply backup stale title = %q, want Old title", got)
	}
	if got := firstBackup["oro-backup-delete"].Title; got != "Delete candidate" {
		t.Fatalf("pre-apply backup delete candidate title = %q, want Delete candidate", got)
	}
	if _, ok := firstBackup["oro-backup-new"]; ok {
		t.Fatalf("pre-apply backup contains post-apply insert oro-backup-new")
	}

	secondOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"backup snapshot:", "inserts: 0", "updates: 0", "deletes: 0", "conflicts: 0", "APPLIED"} {
		if !strings.Contains(secondOut, want) {
			t.Fatalf("second reconcile output missing %q:\n%s", want, secondOut)
		}
	}
	secondBackupPath := migrationOutputValue(t, secondOut, "backup snapshot:")
	if secondBackupPath == firstBackupPath {
		t.Fatalf("second reconcile reused first backup path %q", secondBackupPath)
	}
	secondBackup := readMigrationBackupBeads(t, secondBackupPath)
	if got := secondBackup["oro-backup-stale"].Title; got != "New title" {
		t.Fatalf("second pre-apply backup stale title = %q, want New title", got)
	}
	if got := secondBackup["oro-backup-new"].Title; got != "Created in bd" {
		t.Fatalf("second pre-apply backup new title = %q, want Created in bd", got)
	}
	if _, ok := secondBackup["oro-backup-delete"]; ok {
		t.Fatalf("second pre-apply backup contains soft-deleted row oro-backup-delete")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open reconciled db: %v", err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow(`SELECT title FROM beads WHERE id='oro-backup-stale' AND deleted=0`).Scan(&title); err != nil {
		t.Fatalf("query reconciled stale bead: %v", err)
	}
	if title != "New title" {
		t.Fatalf("reconciled stale title = %q, want New title", title)
	}

	dryRunApplyJSONL := filepath.Join(tmpDir, "dry-run-apply.jsonl")
	if err := os.WriteFile(dryRunApplyJSONL, []byte(`{"id":"oro-backup-stale","title":"Dry run apply must not mutate","description":"New description","status":"closed","priority":1,"issue_type":"bug","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:06Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write dry-run apply jsonl: %v", err)
	}
	dryRunApplyOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--dry-run", "--reconcile", "--apply", "--from-jsonl", dryRunApplyJSONL)
	for _, want := range []string{"updates: 1", "DRY RUN -- pass --apply to write changes"} {
		if !strings.Contains(dryRunApplyOut, want) {
			t.Fatalf("dry-run --reconcile --apply output missing %q:\n%s", want, dryRunApplyOut)
		}
	}
	for _, forbidden := range []string{"backup snapshot:", "APPLIED"} {
		if strings.Contains(dryRunApplyOut, forbidden) {
			t.Fatalf("dry-run --reconcile --apply output unexpectedly contains %q:\n%s", forbidden, dryRunApplyOut)
		}
	}
	if err := db.QueryRow(`SELECT title FROM beads WHERE id='oro-backup-stale' AND deleted=0`).Scan(&title); err != nil {
		t.Fatalf("query after dry-run apply: %v", err)
	}
	if title != "New title" {
		t.Fatalf("dry-run --reconcile --apply mutated title to %q, want New title", title)
	}

	if err := os.RemoveAll(filepath.Join(oroHome, "migrations")); err != nil {
		t.Fatalf("remove migrations dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oroHome, "migrations"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("create migrations path conflict: %v", err)
	}
	failureJSONL := filepath.Join(tmpDir, "failure.jsonl")
	if err := os.WriteFile(failureJSONL, []byte(`{"id":"oro-backup-stale","title":"Backup failure must not apply","description":"New description","status":"closed","priority":1,"issue_type":"bug","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T03:04:06Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write failure jsonl: %v", err)
	}
	out, cmdErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", failureJSONL)
	if cmdErr == nil {
		t.Fatalf("reconcile apply succeeded despite backup failure:\n%s", out)
	}
	for _, want := range []string{"write pre-reconcile backup", filepath.Join(oroHome, "migrations")} {
		if !strings.Contains(out+cmdErr.Error(), want) {
			t.Fatalf("backup failure output missing %q:\nerr=%v\nout=%s", want, cmdErr, out)
		}
	}
	var afterFailureTitle string
	if err := db.QueryRow(`SELECT title FROM beads WHERE id='oro-backup-stale' AND deleted=0`).Scan(&afterFailureTitle); err != nil {
		t.Fatalf("query after failed backup: %v", err)
	}
	if afterFailureTitle != "New title" {
		t.Fatalf("failed backup mutated title to %q, want New title", afterFailureTitle)
	}
}

func TestMigrateFromDoltReconcileDryRunDoesNotCreateDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing", "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	jsonlPath := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"oro-new","title":"Created in bd","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	out := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--from-jsonl", jsonlPath)
	for _, want := range []string{"inserts: 1", "updates: 0", "deletes: 0", "DRY RUN -- pass --apply to write changes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run reconcile output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("plain --reconcile created DB path %s: stat err=%v", dbPath, err)
	}
}

func TestMigrateFromDoltReconcileReportsBlockedStatusRemap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	tmpDir := t.TempDir()
	initialJSONL := filepath.Join(tmpDir, "initial.jsonl")
	if err := os.WriteFile(initialJSONL, []byte(strings.Join([]string{
		`{"id":"oro-existing","title":"Existing","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-delete-candidate","title":"Delete candidate","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-typed-invalid","title":"Typed invalid old","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write initial jsonl: %v", err)
	}
	reconcileJSONL := filepath.Join(tmpDir, "reconcile.jsonl")
	if err := os.WriteFile(reconcileJSONL, []byte(strings.Join([]string{
		`{"id":"oro-existing","title":"Existing","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"oro-blocked-reconcile","title":"Blocked reconcile","status":"blocked","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"oro-invalid-reconcile","title":"Invalid reconcile","status":"triaged","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"oro-typed-invalid","title":"Typed invalid new","status":"open","priority":"P1","issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		`{"id":"oro-unsafe-reconcile","title":"Unsafe reconcile","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","tags":["dup","dup"]}`,
		`{"id":"oro-after-unsafe","title":"After unsafe","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write reconcile jsonl: %v", err)
	}

	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", initialJSONL)
	out, cmdErr := runBeadMigrateCommandErr(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	if cmdErr == nil {
		t.Fatalf("reconcile apply succeeded despite validation error:\n%s", out)
	}
	for _, want := range []string{
		"inserts: 3",
		"deletes: 0",
		"migration errors: 3",
		"line 3: unknown status \"triaged\"",
		"line 4: decode bd export bead",
		"row oro-unsafe-reconcile: insert migrated tag for oro-unsafe-reconcile",
		"APPLIED",
	} {
		if !strings.Contains(out+cmdErr.Error(), want) {
			t.Fatalf("reconcile validation output missing %q:\nerr=%v\nout=%s", want, cmdErr, out)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open reconciled db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT status FROM beads WHERE id='oro-blocked-reconcile'`).Scan(&status); err != nil {
		t.Fatalf("query reconciled blocked bead: %v", err)
	}
	if status != "blocked" {
		t.Fatalf("reconciled blocked status = %q, want blocked", status)
	}
	var invalidCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-invalid-reconcile'`).Scan(&invalidCount); err != nil {
		t.Fatalf("query invalid reconcile bead: %v", err)
	}
	if invalidCount != 0 {
		t.Fatalf("invalid reconcile row persisted %d bead(s), want 0", invalidCount)
	}
	var typedTitle, typedDeleted string
	if err := db.QueryRow(`SELECT title, deleted FROM beads WHERE id='oro-typed-invalid'`).Scan(&typedTitle, &typedDeleted); err != nil {
		t.Fatalf("query typed invalid existing bead: %v", err)
	}
	if typedTitle != "Typed invalid old" || typedDeleted != "0" {
		t.Fatalf("typed invalid existing bead = title %q deleted %q, want preserved old/not deleted", typedTitle, typedDeleted)
	}
	var deleteCandidateDeleted int
	if err := db.QueryRow(`SELECT deleted FROM beads WHERE id='oro-delete-candidate'`).Scan(&deleteCandidateDeleted); err != nil {
		t.Fatalf("query delete candidate: %v", err)
	}
	if deleteCandidateDeleted != 0 {
		t.Fatalf("delete candidate deleted = %d, want preserved when source validation has errors", deleteCandidateDeleted)
	}
	var unsafeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-unsafe-reconcile'`).Scan(&unsafeCount); err != nil {
		t.Fatalf("query unsafe reconcile bead: %v", err)
	}
	if unsafeCount != 0 {
		t.Fatalf("unsafe reconcile row persisted %d bead(s), want 0", unsafeCount)
	}
	var afterUnsafeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM beads WHERE id='oro-after-unsafe' AND deleted=0`).Scan(&afterUnsafeCount); err != nil {
		t.Fatalf("query after unsafe bead: %v", err)
	}
	if afterUnsafeCount != 1 {
		t.Fatalf("after unsafe row count = %d, want 1", afterUnsafeCount)
	}
}

func TestMigrateFromDoltReconcileSameSecondTimestampTieAppliesBD(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	tmpDir := t.TempDir()
	initialJSONL := filepath.Join(tmpDir, "initial.jsonl")
	if err := os.WriteFile(initialJSONL, []byte(`{"id":"oro-tie","title":"SQLite stale title","description":"sqlite side","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T03:04:05.500Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write initial jsonl: %v", err)
	}
	reconcileJSONL := filepath.Join(tmpDir, "reconcile.jsonl")
	if err := os.WriteFile(reconcileJSONL, []byte(`{"id":"oro-tie","title":"BD authoritative title","description":"bd side","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T03:04:05Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write reconcile jsonl: %v", err)
	}

	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", initialJSONL)
	firstOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"updates: 0", "conflicts: 1", "conflict: oro-tie"} {
		if !strings.Contains(firstOut, want) {
			t.Fatalf("first reconcile output missing %q:\n%s", want, firstOut)
		}
	}

	secondOut := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"updates: 0", "conflicts: 0"} {
		if !strings.Contains(secondOut, want) {
			t.Fatalf("second reconcile output missing %q:\n%s", want, secondOut)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open reconciled db: %v", err)
	}
	defer db.Close()
	var title, updatedAt string
	if err := db.QueryRow(`SELECT title, updated_at FROM beads WHERE id='oro-tie' AND deleted=0`).Scan(&title, &updatedAt); err != nil {
		t.Fatalf("query tie bead: %v", err)
	}
	if title != "BD authoritative title" || updatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("tie bead = (%q, %q), want bd authoritative title and timestamp", title, updatedAt)
	}
}

func TestMigrateFromDoltReconcileSameSecondTimestampOnlyTieIsClean(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")

	tmpDir := t.TempDir()
	initialJSONL := filepath.Join(tmpDir, "initial.jsonl")
	if err := os.WriteFile(initialJSONL, []byte(`{"id":"oro-timestamp-only","title":"Same content","description":"only timestamp precision differs","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T03:04:05.500Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write initial jsonl: %v", err)
	}
	reconcileJSONL := filepath.Join(tmpDir, "reconcile.jsonl")
	if err := os.WriteFile(reconcileJSONL, []byte(`{"id":"oro-timestamp-only","title":"Same content","description":"only timestamp precision differs","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T03:04:05Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write reconcile jsonl: %v", err)
	}

	runBeadMigrateCommand(t, "migrate-from-dolt", "--from-jsonl", initialJSONL)
	out := runBeadMigrateCommand(t, "migrate-from-dolt", "--reconcile", "--apply", "--from-jsonl", reconcileJSONL)
	for _, want := range []string{"updates: 0", "conflicts: 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("reconcile output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "conflict: oro-timestamp-only") {
		t.Fatalf("timestamp-only tie surfaced conflict:\n%s", out)
	}
}

func runBeadMigrateCommand(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runBeadMigrateCommandErr(t, args...)
	if err != nil {
		t.Fatalf("oro bead %s error: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func runBeadMigrateCommandErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newBeadCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func writeMigrationJSONL(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.jsonl")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

func seedOldBeadSchemaForMigration(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open old schema db: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE beads (
    id                    TEXT PRIMARY KEY,
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    acceptance_criteria   TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('open','in_progress','closed')),
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
CREATE TABLE bead_deps (
    bead_id       TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    depends_on_id TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    type          TEXT NOT NULL DEFAULT 'blocks',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_by    TEXT,
    PRIMARY KEY (bead_id, depends_on_id, type)
);
`)
	if err != nil {
		t.Fatalf("seed old bead schema: %v", err)
	}
}

func migrationOutputValue(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("output missing prefix %q:\n%s", prefix, out)
	return ""
}

func readMigrationBackupBeads(t *testing.T, path string) map[string]bdExportBead {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration backup: %v", err)
	}
	rawBeads, err := decodeBDExport(data)
	if err != nil {
		t.Fatalf("decode migration backup: %v", err)
	}
	beads := make(map[string]bdExportBead, len(rawBeads))
	for _, raw := range rawBeads {
		var bead bdExportBead
		if err := json.Unmarshal(raw, &bead); err != nil {
			t.Fatalf("decode backup bead: %v", err)
		}
		beads[bead.ID] = bead
	}
	return beads
}

type fakeBeadMigrationRunner struct {
	outputs map[string][]byte
	errs    map[string]error
	calls   [][]string
}

func (f *fakeBeadMigrationRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	k := key(name, args...)
	if err := f.errs[k]; err != nil {
		return nil, err
	}
	return f.outputs[k], nil
}

func (f *fakeBeadMigrationRunner) count(name string, args ...string) int {
	k := key(name, args...)
	var count int
	for _, call := range f.calls {
		if key(call[0], call[1:]...) == k {
			count++
		}
	}
	return count
}

func restoreBeadMigrationRunner(t *testing.T, runner beadMigrationCommandRunner) {
	t.Helper()
	previous := defaultBeadMigrationRunner
	defaultBeadMigrationRunner = runner
	t.Cleanup(func() {
		defaultBeadMigrationRunner = previous
	})
}

func setupMigrationLockTestEnv(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")
	return dbPath
}

func setupSymlinkedMigrationDBEnv(t *testing.T) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	realDir := filepath.Join(tmpDir, "real")
	linkDir := filepath.Join(tmpDir, "link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("create real db dir: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("create db dir symlink: %v", err)
	}
	realDBPath := filepath.Join(realDir, "state.db")
	linkDBPath := filepath.Join(linkDir, "state.db")
	t.Setenv("ORO_DB_PATH", linkDBPath)
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")
	return realDBPath, linkDBPath
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root from %s", wd)
		}
	}
}
