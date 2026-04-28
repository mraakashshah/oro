package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

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
	cmd := newBeadCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("oro bead %s error: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
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
