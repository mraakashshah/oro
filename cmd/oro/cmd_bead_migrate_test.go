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
	"strconv"
	"strings"
	"testing"
	"time"
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
