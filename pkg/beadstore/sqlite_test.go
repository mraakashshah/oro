package beadstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

func TestSQLiteStoreCreateShowExportAndMemory(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	var memoryTags []string
	var memoryDesc string
	store.memory = func(_ context.Context, tags []string, description string, maxTokens int) (string, error) {
		memoryTags = append([]string(nil), tags...)
		memoryDesc = description
		if maxTokens != 2000 {
			t.Fatalf("memory maxTokens = %d, want 2000", maxTokens)
		}
		return "relevant memory", nil
	}

	created, err := store.Create(ctx, CreateParams{
		ID:                 "oro-sql1",
		Title:              "Implement SQLite store",
		Type:               "task",
		Priority:           1,
		Description:        "persist beads",
		AcceptanceCriteria: "all methods work",
		ParentID:           "oro-epic",
		Tags:               []string{"phase-1", "sqlite"},
		Labels:             []string{"store"},
		Metadata:           map[string]string{"model": "haiku", "source": "test"},
		EstimatedMinutes:   12,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != "oro-sql1" || created.Status != "open" || created.Epic != "oro-epic" {
		t.Fatalf("created bead = %#v", created)
	}

	got, err := store.Show(ctx, "oro-sql1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got == nil {
		t.Fatal("Show returned nil for existing bead")
	}
	if got.Memory != "relevant memory" {
		t.Fatalf("Show Memory = %q, want callback result", got.Memory)
	}
	if got.Model != protocol.ModelHaiku {
		t.Fatalf("Show Model = %q, want metadata-promoted haiku", got.Model)
	}
	if !reflect.DeepEqual(got.Tags, []string{"phase-1", "sqlite"}) {
		t.Fatalf("Show Tags = %#v", got.Tags)
	}
	if got.Metadata["source"] != "test" {
		t.Fatalf("Show Metadata = %#v", got.Metadata)
	}
	if !reflect.DeepEqual(memoryTags, []string{"phase-1", "sqlite"}) || memoryDesc != "persist beads" {
		t.Fatalf("memory callback got tags=%#v desc=%q", memoryTags, memoryDesc)
	}

	missing, err := store.Show(ctx, "oro-missing")
	if err != nil {
		t.Fatalf("Show missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("Show missing = %#v, want nil", missing)
	}

	exported, err := store.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var rows []protocol.Bead
	for _, line := range splitJSONLines(string(exported)) {
		var bead protocol.Bead
		if err := json.Unmarshal([]byte(line), &bead); err != nil {
			t.Fatalf("unmarshal export line %q: %v", line, err)
		}
		rows = append(rows, bead)
	}
	if len(rows) != 1 || rows[0].ID != "oro-sql1" {
		t.Fatalf("exported rows = %#v", rows)
	}
}

func TestSQLiteStoreListsUseStatusAndDependencySemantics(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	mustCreate(t, store, CreateParams{ID: "oro-ready", Title: "ready", Priority: 2})
	mustCreate(t, store, CreateParams{ID: "oro-blocker", Title: "blocker", Priority: 1})
	mustCreate(t, store, CreateParams{ID: "oro-blocked", Title: "blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-progress", Title: "progress"})
	mustCreate(t, store, CreateParams{ID: "oro-closed1", Title: "closed 1"})
	mustCreate(t, store, CreateParams{ID: "oro-closed2", Title: "closed 2"})
	mustUpdate(t, store, "oro-blocked", UpdateParams{Priority: intPtr(0)})
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-blocked', 'oro-blocker', 'conditional-blocks')`)
	mustUpdate(t, store, "oro-progress", UpdateParams{Status: strPtr("in_progress")})
	mustClose(t, store, "oro-closed1", "done")
	mustClose(t, store, "oro-closed2", "done")

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ids(ready) != "oro-blocker,oro-ready" {
		t.Fatalf("Ready ids = %s", ids(ready))
	}

	blocked, err := store.Blocked(ctx)
	if err != nil {
		t.Fatalf("Blocked: %v", err)
	}
	if ids(blocked) != "oro-blocked" {
		t.Fatalf("Blocked ids = %s", ids(blocked))
	}

	progress, err := store.InProgress(ctx)
	if err != nil {
		t.Fatalf("InProgress: %v", err)
	}
	if ids(progress) != "oro-progress" {
		t.Fatalf("InProgress ids = %s", ids(progress))
	}

	closed, err := store.Closed(ctx, 1)
	if err != nil {
		t.Fatalf("Closed: %v", err)
	}
	if len(closed) != 1 {
		t.Fatalf("Closed len = %d, want capped to 1", len(closed))
	}

	mustClose(t, store, "oro-blocker", "unblocks child")
	ready, err = store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready after close: %v", err)
	}
	if ids(ready) != "oro-blocked,oro-ready" {
		t.Fatalf("Ready after blocker close ids = %s", ids(ready))
	}
}

func TestSQLiteStoreUpdateCloseAndChildQueries(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	mustCreate(t, store, CreateParams{ID: "oro-epic", Title: "epic", Type: "epic"})
	mustCreate(t, store, CreateParams{ID: "oro-child1", Title: "child 1", ParentID: "oro-epic", Tags: []string{"rebase"}})
	mustCreate(t, store, CreateParams{ID: "oro-child2", Title: "child 2", ParentID: "oro-epic", Tags: []string{"other"}})

	if has, err := store.HasChildren(ctx, "oro-empty"); err != nil || has {
		t.Fatalf("HasChildren empty = %v, %v; want false, nil", has, err)
	}
	if allClosed, err := store.AllChildrenClosed(ctx, "oro-empty"); err != nil || !allClosed {
		t.Fatalf("AllChildrenClosed empty = %v, %v; want true, nil", allClosed, err)
	}
	if has, err := store.HasChildren(ctx, "oro-epic"); err != nil || !has {
		t.Fatalf("HasChildren epic = %v, %v; want true, nil", has, err)
	}
	if allClosed, err := store.AllChildrenClosed(ctx, "oro-epic"); err != nil || allClosed {
		t.Fatalf("AllChildrenClosed with open child = %v, %v; want false, nil", allClosed, err)
	}

	if err := store.Update(ctx, "oro-child1", UpdateParams{
		Status:   strPtr("in_progress"),
		Priority: intPtr(0),
		Type:     strPtr("bug"),
		ParentID: strPtr(""),
		Owner:    strPtr("aakash"),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, err := store.Show(ctx, "oro-child1")
	if err != nil {
		t.Fatalf("Show updated: %v", err)
	}
	if updated.Status != "in_progress" || updated.Priority != 0 || updated.Type != "bug" || updated.Epic != "" || updated.Owner != "aakash" {
		t.Fatalf("updated bead = %#v", updated)
	}
	mustExec(t, store.db, `UPDATE beads SET deferred_until='2999-01-01T00:00:00Z' WHERE id='oro-child1'`)
	if err := store.Update(ctx, "oro-child1", UpdateParams{Status: strPtr("open")}); err != nil {
		t.Fatalf("Update reopen: %v", err)
	}
	var deferred sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT deferred_until FROM beads WHERE id='oro-child1'`).Scan(&deferred); err != nil {
		t.Fatalf("query deferred_until: %v", err)
	}
	if deferred.Valid {
		t.Fatalf("deferred_until still set after reopen: %q", deferred.String)
	}
	if err := store.Update(ctx, "oro-child1", UpdateParams{Status: strPtr("invalid")}); err == nil {
		t.Fatal("Update invalid status succeeded, want error")
	}

	tagged, err := store.FindByParentAndTag(ctx, "oro-epic", "other")
	if err != nil {
		t.Fatalf("FindByParentAndTag: %v", err)
	}
	if ids(tagged) != "oro-child2" {
		t.Fatalf("FindByParentAndTag ids = %s", ids(tagged))
	}

	mustClose(t, store, "oro-child1", "done")
	mustClose(t, store, "oro-child2", "done")
	if allClosed, err := store.AllChildrenClosed(ctx, "oro-epic"); err != nil || !allClosed {
		t.Fatalf("AllChildrenClosed with all closed = %v, %v; want true, nil", allClosed, err)
	}
	closed, err := store.Show(ctx, "oro-child2")
	if err != nil {
		t.Fatalf("Show closed: %v", err)
	}
	if closed.Status != "closed" || closed.CloseReason != "done" || closed.ClosedAt == "" {
		t.Fatalf("closed bead = %#v", closed)
	}
}

func TestSQLiteStoreRuntimeEventsAndRollback(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	created, err := store.Create(ctx, CreateParams{ID: "oro-runtime", Title: "runtime"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Priority != 0 {
		t.Fatalf("default zero priority = %d, want 0", created.Priority)
	}
	mustExec(t, store.db, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-runtime', 'worker-1', '/tmp/worktree', 'active')`)
	shown, err := store.Show(ctx, "oro-runtime")
	if err != nil {
		t.Fatalf("Show runtime: %v", err)
	}
	if shown.WorkerID != "worker-1" {
		t.Fatalf("WorkerID = %q, want worker-1", shown.WorkerID)
	}

	mustUpdate(t, store, "oro-runtime", UpdateParams{Status: strPtr("in_progress")})
	mustClose(t, store, "oro-runtime", "done")
	for _, eventType := range []string{"bead_created", "bead_updated", "bead_closed"} {
		if got := eventCount(t, store.db, eventType); got != 1 {
			t.Fatalf("%s count = %d, want 1", eventType, got)
		}
	}

	p0, err := store.Create(ctx, CreateParams{ID: "oro-p0", Title: "priority zero", Priority: 0})
	if err != nil {
		t.Fatalf("Create priority zero: %v", err)
	}
	if p0.Priority != 0 {
		t.Fatalf("explicit priority 0 = %d, want 0", p0.Priority)
	}

	if _, err := store.Create(ctx, CreateParams{ID: "oro-rollback", Title: "rollback", Tags: []string{"dup", "dup"}}); err == nil {
		t.Fatal("Create duplicate tags succeeded, want error")
	}
	missing, err := store.Show(ctx, "oro-rollback")
	if err != nil {
		t.Fatalf("Show rollback: %v", err)
	}
	if missing != nil {
		t.Fatalf("rolled back bead exists: %#v", missing)
	}

	if err := store.Update(ctx, "oro-missing", UpdateParams{Status: strPtr("open")}); !isBeadNotFound(err) {
		t.Fatalf("Update missing error = %v, want BeadNotFoundError", err)
	}
	if err := store.Close(ctx, "oro-missing", "done"); !isBeadNotFound(err) {
		t.Fatalf("Close missing error = %v, want BeadNotFoundError", err)
	}
}

func TestSQLiteStoreOpenAppliesDBUtilPragmas(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "nested", "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })

	var journalMode string
	if err := store.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := store.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	if _, err := store.Create(ctx, CreateParams{ID: "oro-open", Title: "opened store"}); err != nil {
		t.Fatalf("Create after OpenSQLiteStore: %v", err)
	}
}

func TestRaceSQLiteStoreConcurrentReadyShowClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })

	const beadID = "oro-race"
	if _, err := store.Create(ctx, CreateParams{ID: beadID, Title: "race"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			if _, err := store.Ready(ctx); err != nil {
				errs <- err
				return
			}
			shown, err := store.Show(ctx, beadID)
			if err != nil {
				errs <- err
				return
			}
			if shown == nil {
				errs <- errors.New("Show returned nil for race bead")
				return
			}
			if err := store.Close(ctx, beadID, "race"); err != nil {
				errs <- err
				return
			}
		}()
	}

	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
		close(errs)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("concurrent Ready/Show/Close did not finish: %v", ctx.Err())
	}

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Ready/Show/Close: %v", err)
		}
	}

	closed, err := store.Show(context.Background(), beadID)
	if err != nil {
		t.Fatalf("Show after concurrent close: %v", err)
	}
	if closed == nil || closed.Status != "closed" || closed.CloseReason != "race" {
		t.Fatalf("closed bead = %#v", closed)
	}
	ready, err := store.Ready(context.Background())
	if err != nil {
		t.Fatalf("Ready after concurrent close: %v", err)
	}
	if ids(ready) != "" {
		t.Fatalf("Ready after concurrent close ids = %s, want none", ids(ready))
	}
}

func TestParityReadyTracksOpenAndClosedDependencies(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-ready", Title: "ready", Priority: 2})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-blocker", Title: "blocker", Priority: 1})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-blocked", Title: "blocked", Priority: 0})
			fixture.addDependency(t, "oro-blocked", "oro-blocker", "blocks")

			ready, err := fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready: %v", err)
			}
			if ids(ready) != "oro-blocker,oro-ready" {
				t.Fatalf("Ready ids with open dependency = %s", ids(ready))
			}

			if err := fixture.store.Close(ctx, "oro-blocker", "unblocks dependent"); err != nil {
				t.Fatalf("Close blocker: %v", err)
			}
			ready, err = fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready after dependency close: %v", err)
			}
			if ids(ready) != "oro-blocked,oro-ready" {
				t.Fatalf("Ready ids with closed dependency = %s", ids(ready))
			}
		})
	}
}

func TestParityUpdateValidatesStatusTransitions(t *testing.T) {
	validStatuses := []string{"in_progress", "closed", "open"}
	invalidStatuses := []string{"ready", "blocked", "deferred", ""}

	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-status", Title: "status"})

			for _, status := range validStatuses {
				if err := fixture.store.Update(ctx, "oro-status", UpdateParams{Status: &status}); err != nil {
					t.Fatalf("Update valid status %q: %v", status, err)
				}
				shown, err := fixture.store.Show(ctx, "oro-status")
				if err != nil {
					t.Fatalf("Show after valid status %q: %v", status, err)
				}
				if shown.Status != status {
					t.Fatalf("status after valid update = %q, want %q", shown.Status, status)
				}
			}

			for _, status := range invalidStatuses {
				if err := fixture.store.Update(ctx, "oro-status", UpdateParams{Status: &status}); err == nil {
					t.Fatalf("Update invalid status %q succeeded", status)
				}
			}
		})
	}
}

func TestParityCloseIsIdempotent(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-close", Title: "close"})

			if err := fixture.store.Close(ctx, "oro-close", "first"); err != nil {
				t.Fatalf("Close first: %v", err)
			}
			first, err := fixture.store.Show(ctx, "oro-close")
			if err != nil {
				t.Fatalf("Show after first close: %v", err)
			}
			if first.Status != "closed" || first.CloseReason != "first" || first.ClosedAt == "" {
				t.Fatalf("first close state = %#v", first)
			}

			if err := fixture.store.Close(ctx, "oro-close", "second"); err != nil {
				t.Fatalf("Close second: %v", err)
			}
			second, err := fixture.store.Show(ctx, "oro-close")
			if err != nil {
				t.Fatalf("Show after second close: %v", err)
			}
			if second.Status != "closed" || second.CloseReason != first.CloseReason || second.ClosedAt != first.ClosedAt {
				t.Fatalf("second close state = %#v, want reason %q closed_at %q", second, first.CloseReason, first.ClosedAt)
			}
		})
	}
}

func TestParityDeferUndeferRoundTrips(t *testing.T) {
	const until = "2999-01-01T00:00:00Z"

	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-deferred", Title: "deferred"})

			if err := fixture.store.Defer(ctx, "oro-deferred", until); err != nil {
				t.Fatalf("Defer: %v", err)
			}
			if got := fixture.deferredUntil(t, "oro-deferred"); got != until {
				t.Fatalf("deferred_until after Defer = %q, want %q", got, until)
			}
			ready, err := fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready after Defer: %v", err)
			}
			if ids(ready) != "" {
				t.Fatalf("Ready after Defer ids = %s, want none", ids(ready))
			}

			if err := fixture.store.Undefer(ctx, "oro-deferred"); err != nil {
				t.Fatalf("Undefer: %v", err)
			}
			if got := fixture.deferredUntil(t, "oro-deferred"); got != "" {
				t.Fatalf("deferred_until after Undefer = %q, want empty", got)
			}
			ready, err = fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready after Undefer: %v", err)
			}
			if ids(ready) != "oro-deferred" {
				t.Fatalf("Ready after Undefer ids = %s", ids(ready))
			}
		})
	}
}

func TestParityUpdateOpenClearsDeferredUntil(t *testing.T) {
	const until = "2999-01-01T00:00:00Z"

	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-reopen", Title: "reopen"})
			if err := fixture.store.Defer(ctx, "oro-reopen", until); err != nil {
				t.Fatalf("Defer: %v", err)
			}

			status := "open"
			if err := fixture.store.Update(ctx, "oro-reopen", UpdateParams{Status: &status}); err != nil {
				t.Fatalf("Update open: %v", err)
			}
			if got := fixture.deferredUntil(t, "oro-reopen"); got != "" {
				t.Fatalf("deferred_until after Update(open) = %q, want empty", got)
			}
			ready, err := fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready after Update(open): %v", err)
			}
			if ids(ready) != "oro-reopen" {
				t.Fatalf("Ready after Update(open) ids = %s", ids(ready))
			}
		})
	}
}

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		t.Fatalf("migrate runtime schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(context.Background(), db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	return NewSQLiteStore(db)
}

type parityStore interface {
	Store
	Defer(context.Context, string, string) error
	Undefer(context.Context, string) error
}

type parityFixture struct {
	name          string
	store         parityStore
	addDependency func(t *testing.T, beadID, dependsOnID, depType string)
	deferredUntil func(t *testing.T, id string) string
}

func newParityFixtures(t *testing.T) []parityFixture {
	t.Helper()

	sqliteStore := newTestSQLiteStore(t)
	fakeStore := NewFakeStore()

	return []parityFixture{
		{
			name:  "sqlite",
			store: sqliteStore,
			addDependency: func(t *testing.T, beadID, dependsOnID, depType string) {
				t.Helper()
				mustExec(t, sqliteStore.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, beadID, dependsOnID, depType)
			},
			deferredUntil: func(t *testing.T, id string) string {
				t.Helper()
				var deferred sql.NullString
				if err := sqliteStore.db.QueryRowContext(context.Background(), `SELECT deferred_until FROM beads WHERE id=?`, id).Scan(&deferred); err != nil {
					t.Fatalf("query deferred_until: %v", err)
				}
				if !deferred.Valid {
					return ""
				}
				return deferred.String
			},
		},
		{
			name:  "fake",
			store: fakeStore,
			addDependency: func(t *testing.T, beadID, dependsOnID, depType string) {
				t.Helper()
				fakeStore.mu.Lock()
				defer fakeStore.mu.Unlock()
				bead, ok := fakeStore.beads[beadID]
				if !ok {
					t.Fatalf("missing fake bead %s", beadID)
				}
				bead.Dependencies = append(bead.Dependencies, protocol.Dependency{
					IssueID:     beadID,
					DependsOnID: dependsOnID,
					Type:        depType,
				})
				fakeStore.beads[beadID] = bead
			},
			deferredUntil: func(t *testing.T, id string) string {
				t.Helper()
				shown, err := fakeStore.Show(context.Background(), id)
				if err != nil {
					t.Fatalf("Show %s: %v", id, err)
				}
				if shown == nil {
					t.Fatalf("missing fake bead %s", id)
				}
				return shown.DeferUntil
			},
		},
	}
}

func mustCreateStore(t *testing.T, store Store, params CreateParams) {
	t.Helper()
	if _, err := store.Create(context.Background(), params); err != nil {
		t.Fatalf("Create(%s): %v", params.ID, err)
	}
}

func mustCreate(t *testing.T, store *SQLiteStore, params CreateParams) {
	t.Helper()
	if _, err := store.Create(context.Background(), params); err != nil {
		t.Fatalf("Create(%s): %v", params.ID, err)
	}
}

func mustUpdate(t *testing.T, store *SQLiteStore, id string, params UpdateParams) {
	t.Helper()
	if err := store.Update(context.Background(), id, params); err != nil {
		t.Fatalf("Update(%s): %v", id, err)
	}
}

func mustClose(t *testing.T, store *SQLiteStore, id, reason string) {
	t.Helper()
	if err := store.Close(context.Background(), id, reason); err != nil {
		t.Fatalf("Close(%s): %v", id, err)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func eventCount(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE type=?`, eventType).Scan(&count); err != nil {
		t.Fatalf("count events %s: %v", eventType, err)
	}
	return count
}

func ids(beads []protocol.Bead) string {
	var out string
	for i, bead := range beads {
		if i > 0 {
			out += ","
		}
		out += bead.ID
	}
	return out
}

func splitJSONLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func isBeadNotFound(err error) bool {
	var notFound *protocol.BeadNotFoundError
	return err != nil && errors.As(err, &notFound)
}
