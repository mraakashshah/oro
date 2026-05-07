//nolint:testpackage // These tests exercise SQLiteStore internals such as pragmas, callbacks, and rollback state.
package beadstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
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

	mustCreate(t, store, CreateParams{ID: "oro-epic", Title: "epic", Type: "epic"})
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
	if len(rows) != 2 || rows[1].ID != "oro-sql1" {
		t.Fatalf("exported rows = %#v", rows)
	}
}

func TestSQLiteStoreCreateRejectsMissingOrDeletedParent(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	if _, err := store.Create(ctx, CreateParams{ID: "oro-child-missing", Title: "child", ParentID: "oro-missing-parent"}); !isBeadNotFound(err) {
		t.Fatalf("Create with missing parent error = %v, want BeadNotFoundError", err)
	}
	if shown, err := store.Show(ctx, "oro-child-missing"); err != nil || shown != nil {
		t.Fatalf("Show child after missing-parent create = %#v, %v; want nil, nil", shown, err)
	}

	mustCreate(t, store, CreateParams{ID: "oro-deleted-parent", Title: "deleted parent"})
	mustExec(t, store.db, `UPDATE beads SET deleted=1 WHERE id='oro-deleted-parent'`)
	if _, err := store.Create(ctx, CreateParams{ID: "oro-child-deleted", Title: "child", ParentID: "oro-deleted-parent"}); !isBeadNotFound(err) {
		t.Fatalf("Create with deleted parent error = %v, want BeadNotFoundError", err)
	}
	if shown, err := store.Show(ctx, "oro-child-deleted"); err != nil || shown != nil {
		t.Fatalf("Show child after deleted-parent create = %#v, %v; want nil, nil", shown, err)
	}
}

func TestSQLiteStoreUpdateRejectsMissingOrDeletedParent(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	mustCreate(t, store, CreateParams{ID: "oro-child", Title: "child"})

	missingParent := "oro-missing-parent"
	if err := store.Update(ctx, "oro-child", UpdateParams{ParentID: &missingParent}); !isBeadNotFound(err) {
		t.Fatalf("Update with missing parent error = %v, want BeadNotFoundError", err)
	}
	shown, err := store.Show(ctx, "oro-child")
	if err != nil {
		t.Fatalf("Show child after missing-parent update: %v", err)
	}
	if shown.Epic != "" {
		t.Fatalf("child parent after missing-parent update = %q, want empty", shown.Epic)
	}

	mustCreate(t, store, CreateParams{ID: "oro-deleted-parent", Title: "deleted parent"})
	mustExec(t, store.db, `UPDATE beads SET deleted=1 WHERE id='oro-deleted-parent'`)
	deletedParent := "oro-deleted-parent"
	if err := store.Update(ctx, "oro-child", UpdateParams{ParentID: &deletedParent}); !isBeadNotFound(err) {
		t.Fatalf("Update with deleted parent error = %v, want BeadNotFoundError", err)
	}
	shown, err = store.Show(ctx, "oro-child")
	if err != nil {
		t.Fatalf("Show child after deleted-parent update: %v", err)
	}
	if shown.Epic != "" {
		t.Fatalf("child parent after deleted-parent update = %q, want empty", shown.Epic)
	}

	mustCreate(t, store, CreateParams{ID: "oro-active-parent", Title: "active parent"})
	activeParent := "oro-active-parent"
	if err := store.Update(ctx, "oro-child", UpdateParams{ParentID: &activeParent}); err != nil {
		t.Fatalf("Update with active parent: %v", err)
	}
	clearParent := ""
	if err := store.Update(ctx, "oro-child", UpdateParams{ParentID: &clearParent}); err != nil {
		t.Fatalf("Update clear parent: %v", err)
	}
	shown, err = store.Show(ctx, "oro-child")
	if err != nil {
		t.Fatalf("Show child after clearing parent: %v", err)
	}
	if shown.Epic != "" {
		t.Fatalf("child parent after clearing parent = %q, want empty", shown.Epic)
	}
}

func TestSQLiteStoreListsUseStatusAndDependencySemantics(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	mustCreate(t, store, CreateParams{ID: "oro-ready", Title: "ready", Priority: 2})
	mustCreate(t, store, CreateParams{ID: "oro-blocker", Title: "blocker", Priority: 1})
	mustCreate(t, store, CreateParams{ID: "oro-blocked", Title: "blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-manual-blocked", Title: "manual blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-stale-deferred-manual-blocked", Title: "stale deferred manual blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-parent", Title: "parent"})
	mustCreate(t, store, CreateParams{ID: "oro-child", Title: "child"})
	mustCreate(t, store, CreateParams{ID: "oro-missing-parent-child", Title: "missing parent child"})
	mustCreate(t, store, CreateParams{ID: "oro-deferred-blocked", Title: "deferred blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-past-deferred-blocked", Title: "past deferred blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-deferred-hard-blocked", Title: "deferred hard blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-assigned", Title: "assigned"})
	mustCreate(t, store, CreateParams{ID: "oro-assigned-blocked", Title: "assigned blocked"})
	mustCreate(t, store, CreateParams{ID: "oro-progress", Title: "progress"})
	mustCreate(t, store, CreateParams{ID: "oro-closed1", Title: "closed 1"})
	mustCreate(t, store, CreateParams{ID: "oro-closed2", Title: "closed 2"})
	mustUpdate(t, store, "oro-blocked", UpdateParams{Priority: intPtr(0)})
	mustUpdate(t, store, "oro-manual-blocked", UpdateParams{Status: strPtr("blocked")})
	mustExec(t, store.db, `UPDATE beads SET deferred_until='2999-01-01T00:00:00Z' WHERE id='oro-stale-deferred-manual-blocked'`)
	mustUpdate(t, store, "oro-stale-deferred-manual-blocked", UpdateParams{Status: strPtr("blocked")})
	var staleManualDeferred sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT deferred_until FROM beads WHERE id='oro-stale-deferred-manual-blocked'`).Scan(&staleManualDeferred); err != nil {
		t.Fatalf("query stale deferred manual blocked: %v", err)
	}
	if staleManualDeferred.Valid {
		t.Fatalf("blocked update left deferred_until set: %q", staleManualDeferred.String)
	}
	mustExec(t, store.db, `UPDATE beads SET deferred_until='2999-01-01T00:00:00Z' WHERE id='oro-stale-deferred-manual-blocked'`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-blocked', 'oro-blocker', 'conditional-blocks')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-assigned-blocked', 'oro-blocker', 'conditional-blocks')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-child', 'oro-parent', 'parent-child')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-missing-parent-child', 'oro-missing-parent', 'parent-child')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-deferred-blocked', 'oro-parent', 'parent-child')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-past-deferred-blocked', 'oro-parent', 'parent-child')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-deferred-hard-blocked', 'oro-blocker', 'blocks')`)
	mustExec(t, store.db, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-assigned', 'worker-1', '/tmp/assigned', 'active')`)
	mustExec(t, store.db, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-assigned-blocked', 'worker-2', '/tmp/assigned-blocked', 'active')`)
	if err := store.Defer(ctx, "oro-deferred-blocked", "2999-01-01T00:00:00Z"); err != nil {
		t.Fatalf("Defer deferred blocked: %v", err)
	}
	if err := store.Defer(ctx, "oro-deferred-hard-blocked", "2999-01-01T00:00:00Z"); err != nil {
		t.Fatalf("Defer deferred hard blocked: %v", err)
	}
	mustExec(t, store.db, `UPDATE beads SET deferred_until='2000-01-01T00:00:00Z' WHERE id='oro-past-deferred-blocked'`)
	mustUpdate(t, store, "oro-progress", UpdateParams{Status: strPtr("in_progress")})
	mustClose(t, store, "oro-closed1", "done")
	mustClose(t, store, "oro-closed2", "done")

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ids(ready) != "oro-parent,oro-child,oro-missing-parent-child,oro-past-deferred-blocked,oro-blocker,oro-ready" {
		t.Fatalf("Ready ids = %s", ids(ready))
	}

	blocked, err := store.Blocked(ctx)
	if err != nil {
		t.Fatalf("Blocked: %v", err)
	}
	if ids(blocked) != "oro-blocked,oro-manual-blocked,oro-stale-deferred-manual-blocked,oro-deferred-hard-blocked" {
		t.Fatalf("Blocked ids = %s", ids(blocked))
	}
	counts, err := store.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts != (StatusCounts{Open: 11, InProgress: 3, Closed: 2}) {
		t.Fatalf("CountByStatus = %#v, want active assignments counted as in_progress", counts)
	}
	var assignedReadyCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready WHERE id=?`, "oro-assigned").Scan(&assignedReadyCount); err != nil {
		t.Fatalf("query beads_ready for active assignment: %v", err)
	}
	if assignedReadyCount != 0 {
		t.Fatalf("beads_ready included active assignment oro-assigned")
	}
	var assignedBlockedCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_blocked WHERE id=?`, "oro-assigned-blocked").Scan(&assignedBlockedCount); err != nil {
		t.Fatalf("query beads_blocked for active assignment: %v", err)
	}
	if assignedBlockedCount != 0 {
		t.Fatalf("beads_blocked included active assignment oro-assigned-blocked")
	}

	mustClose(t, store, "oro-parent", "parent done")
	ready, err = store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready after parent close: %v", err)
	}
	if ids(ready) != "oro-child,oro-missing-parent-child,oro-past-deferred-blocked,oro-blocker,oro-ready" {
		t.Fatalf("Ready ids after parent close = %s", ids(ready))
	}

	progress, err := store.InProgress(ctx)
	if err != nil {
		t.Fatalf("InProgress: %v", err)
	}
	if ids(progress) != "oro-progress,oro-assigned,oro-assigned-blocked" {
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
	if ids(ready) != "oro-blocked,oro-child,oro-missing-parent-child,oro-past-deferred-blocked,oro-ready" {
		t.Fatalf("Ready after blocker close ids = %s", ids(ready))
	}
}

func TestSQLiteStoreParentChildDoesNotBlockChildReadiness(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	mustCreate(t, store, CreateParams{ID: "oro-parent", Title: "parent", Type: "epic", Priority: 1})
	mustCreate(t, store, CreateParams{ID: "oro-child", Title: "child", Priority: 0})
	mustCreate(t, store, CreateParams{ID: "oro-blocker", Title: "blocker", Priority: 1})
	mustCreate(t, store, CreateParams{ID: "oro-explicitly-blocked", Title: "explicitly blocked", Priority: 0})
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-child', 'oro-parent', 'parent-child')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-explicitly-blocked', 'oro-parent', 'parent-child')`)
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('oro-explicitly-blocked', 'oro-blocker', 'blocks')`)

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ids(ready) != "oro-child,oro-parent,oro-blocker" {
		t.Fatalf("Ready ids = %s, want parent-child child ready and explicitly blocked child absent", ids(ready))
	}

	blocked, err := store.Blocked(ctx)
	if err != nil {
		t.Fatalf("Blocked: %v", err)
	}
	if ids(blocked) != "oro-explicitly-blocked" {
		t.Fatalf("Blocked ids = %s, want only explicit blocking dependency", ids(blocked))
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

func TestSQLiteStoreDeleteSoftDeletesAndHidesLeaf(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	mustCreate(t, store, CreateParams{ID: "oro-delete", Title: "delete me", Priority: 0})
	mustCreate(t, store, CreateParams{ID: "oro-keep", Title: "keep me", Priority: 1})

	if err := store.Delete(ctx, "oro-delete", "cleanup"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var deleted int
	var status, reason string
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT deleted, status, close_reason FROM beads WHERE id='oro-delete'`,
	).Scan(&deleted, &status, &reason); err != nil {
		t.Fatalf("query deleted row: %v", err)
	}
	if deleted != 1 || status != "open" || reason != "cleanup" {
		t.Fatalf("deleted row = deleted %d status %q reason %q, want deleted/open/cleanup", deleted, status, reason)
	}

	shown, err := store.Show(ctx, "oro-delete")
	if err != nil {
		t.Fatalf("Show deleted: %v", err)
	}
	if shown != nil {
		t.Fatalf("Show deleted = %#v, want nil", shown)
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if ids(ready) != "oro-keep" {
		t.Fatalf("Ready ids = %s, want oro-keep", ids(ready))
	}

	counts, err := store.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts != (StatusCounts{Open: 1}) {
		t.Fatalf("CountByStatus = %#v, want only non-deleted open bead", counts)
	}

	exported, err := store.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if bytes.Contains(exported, []byte("oro-delete")) || !bytes.Contains(exported, []byte("oro-keep")) {
		t.Fatalf("Export should omit deleted bead and include active bead:\n%s", exported)
	}

	if got := eventCount(t, store.db, "bead_deleted"); got != 1 {
		t.Fatalf("bead_deleted event count = %d, want 1", got)
	}
	if err := store.Delete(ctx, "oro-delete", "again"); !isBeadNotFound(err) {
		t.Fatalf("second Delete error = %v, want BeadNotFoundError", err)
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

func TestSQLiteStoreOptionsAndGeneratedID(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}

	var fetched bool
	store := NewSQLiteStore(db, WithMemoryFetcher(func(_ context.Context, tags []string, description string, maxTokens int) (string, error) {
		fetched = true
		if !reflect.DeepEqual(tags, []string{"generated"}) || description != "created without explicit id" || maxTokens != 2000 {
			t.Fatalf("memory fetch inputs tags=%#v description=%q maxTokens=%d", tags, description, maxTokens)
		}
		return "memory for generated bead", nil
	}))

	if _, err := store.Create(ctx, CreateParams{}); err == nil {
		t.Fatal("Create blank title succeeded, want error")
	}

	created, err := store.Create(ctx, CreateParams{
		Title:       "generated",
		Description: "created without explicit id",
		Tags:        []string{"generated"},
	})
	if err != nil {
		t.Fatalf("Create generated id: %v", err)
	}
	if !strings.HasPrefix(created.ID, "oro-") || len(created.ID) <= len("oro-") {
		t.Fatalf("generated ID = %q, want nonempty oro-* id", created.ID)
	}

	shown, err := store.Show(ctx, created.ID)
	if err != nil {
		t.Fatalf("Show generated id: %v", err)
	}
	if !fetched || shown.Memory != "memory for generated bead" {
		t.Fatalf("Show memory fetched=%v memory=%q, want callback result", fetched, shown.Memory)
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

func TestParityStoreLifecycleMethods(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-parent", Title: "parent", Type: "epic"})
			created, err := fixture.store.Create(ctx, CreateParams{
				ID:                 "oro-lifecycle",
				Title:              "lifecycle",
				Type:               "task",
				Priority:           2,
				Description:        "exercise store methods",
				AcceptanceCriteria: "parity",
				ParentID:           "oro-parent",
				Tags:               []string{"phase-1", "store"},
				Labels:             []string{"beadstore"},
				Metadata:           map[string]string{"model": "haiku", "source": "parity"},
				EstimatedMinutes:   13,
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if created.ID != "oro-lifecycle" || created.Status != "open" || created.Epic != "oro-parent" || created.Type != "task" {
				t.Fatalf("created bead = %#v", created)
			}

			shown, err := fixture.store.Show(ctx, "oro-lifecycle")
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if shown == nil || shown.ID != created.ID || shown.Description != "exercise store methods" || shown.Metadata["source"] != "parity" {
				t.Fatalf("shown bead = %#v", shown)
			}
			missing, err := fixture.store.Show(ctx, "oro-missing")
			if err != nil {
				t.Fatalf("Show missing: %v", err)
			}
			if missing != nil {
				t.Fatalf("Show missing = %#v, want nil", missing)
			}

			status := "in_progress"
			priority := 0
			beadType := "bug"
			acceptance := "updated acceptance"
			notes := "first note"
			parent := ""
			owner := "worker-1"
			if err := fixture.store.Update(ctx, "oro-lifecycle", UpdateParams{
				Status:             &status,
				Priority:           &priority,
				Type:               &beadType,
				AcceptanceCriteria: &acceptance,
				Notes:              &notes,
				ParentID:           &parent,
				Owner:              &owner,
			}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			updated, err := fixture.store.Show(ctx, "oro-lifecycle")
			if err != nil {
				t.Fatalf("Show updated: %v", err)
			}
			if updated.Status != "in_progress" || updated.Priority != 0 || updated.Type != "bug" ||
				updated.AcceptanceCriteria != acceptance || updated.Notes != notes || updated.Epic != "" || updated.Owner != owner {
				t.Fatalf("updated bead = %#v", updated)
			}

			if err := fixture.store.Close(ctx, "oro-lifecycle", "done"); err != nil {
				t.Fatalf("Close: %v", err)
			}
			closed, err := fixture.store.Show(ctx, "oro-lifecycle")
			if err != nil {
				t.Fatalf("Show closed: %v", err)
			}
			if closed.Status != "closed" || closed.CloseReason != "done" || closed.ClosedAt == "" {
				t.Fatalf("closed bead = %#v", closed)
			}

			if err := fixture.store.Update(ctx, "oro-missing", UpdateParams{Status: &status}); !isBeadNotFound(err) {
				t.Fatalf("Update missing error = %v, want BeadNotFoundError", err)
			}
			if err := fixture.store.Close(ctx, "oro-missing", "done"); !isBeadNotFound(err) {
				t.Fatalf("Close missing error = %v, want BeadNotFoundError", err)
			}
		})
	}
}

func TestParityStoreListMethods(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-ready", Title: "ready", Priority: 2})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-blocker", Title: "blocker", Priority: 1})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-blocked", Title: "blocked", Priority: 0})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-assigned", Title: "assigned", Priority: 0})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-assigned-blocked", Title: "assigned blocked", Priority: 0})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-progress", Title: "progress", Priority: 0})
			fixture.addDependency(t, "oro-blocked", "oro-blocker", "conditional-blocks")
			fixture.addDependency(t, "oro-assigned-blocked", "oro-blocker", "conditional-blocks")
			fixture.assignActive(t, "oro-assigned", "worker-1")
			fixture.assignActive(t, "oro-assigned-blocked", "worker-2")

			status := "in_progress"
			if err := fixture.store.Update(ctx, "oro-progress", UpdateParams{Status: &status}); err != nil {
				t.Fatalf("Update progress: %v", err)
			}

			ready, err := fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready: %v", err)
			}
			if ids(ready) != "oro-blocker,oro-ready" {
				t.Fatalf("Ready ids = %s, want oro-blocker,oro-ready", ids(ready))
			}

			blocked, err := fixture.store.Blocked(ctx)
			if err != nil {
				t.Fatalf("Blocked: %v", err)
			}
			if ids(blocked) != "oro-blocked" {
				t.Fatalf("Blocked ids = %s, want oro-blocked", ids(blocked))
			}

			inProgress, err := fixture.store.InProgress(ctx)
			if err != nil {
				t.Fatalf("InProgress: %v", err)
			}
			if sortedIDs(inProgress) != "oro-assigned,oro-assigned-blocked,oro-progress" {
				t.Fatalf("InProgress ids = %s, want assigned and status in-progress beads", sortedIDs(inProgress))
			}

			if err := fixture.store.Close(ctx, "oro-blocker", "unblocks children"); err != nil {
				t.Fatalf("Close blocker: %v", err)
			}
			ready, err = fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready after blocker close: %v", err)
			}
			if ids(ready) != "oro-blocked,oro-ready" {
				t.Fatalf("Ready after blocker close ids = %s, want oro-blocked,oro-ready", ids(ready))
			}
		})
	}
}

func TestParityClosedLimitSemantics(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-closed-a", Title: "closed a"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-closed-b", Title: "closed b"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-open", Title: "open"})
			if err := fixture.store.Close(ctx, "oro-closed-a", "done"); err != nil {
				t.Fatalf("Close a: %v", err)
			}
			if err := fixture.store.Close(ctx, "oro-closed-b", "done"); err != nil {
				t.Fatalf("Close b: %v", err)
			}

			closed, err := fixture.store.Closed(ctx, 1)
			if err != nil {
				t.Fatalf("Closed positive limit: %v", err)
			}
			if len(closed) != 1 || closed[0].Status != "closed" {
				t.Fatalf("Closed positive limit = %#v, want one closed bead", closed)
			}

			closed, err = fixture.store.Closed(ctx, 0)
			if err != nil {
				t.Fatalf("Closed zero limit: %v", err)
			}
			if len(closed) != 0 {
				t.Fatalf("Closed zero limit len = %d, want 0", len(closed))
			}

			closed, err = fixture.store.Closed(ctx, -1)
			if err != nil {
				t.Fatalf("Closed negative limit: %v", err)
			}
			if len(closed) != 0 {
				t.Fatalf("Closed negative limit len = %d, want 0", len(closed))
			}
		})
	}
}

func TestParityChildQueries(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-empty", Title: "empty epic", Type: "epic"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-epic", Title: "epic", Type: "epic"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-child-open", Title: "open child", ParentID: "oro-epic", Tags: []string{"phase-1"}})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-child-closed", Title: "closed child", ParentID: "oro-epic", Tags: []string{"phase-2"}})

			if has, err := fixture.store.HasChildren(ctx, "oro-empty"); err != nil || has {
				t.Fatalf("HasChildren empty = %v, %v; want false, nil", has, err)
			}
			if allClosed, err := fixture.store.AllChildrenClosed(ctx, "oro-empty"); err != nil || !allClosed {
				t.Fatalf("AllChildrenClosed empty = %v, %v; want true, nil", allClosed, err)
			}
			if has, err := fixture.store.HasChildren(ctx, "oro-epic"); err != nil || !has {
				t.Fatalf("HasChildren epic = %v, %v; want true, nil", has, err)
			}
			if allClosed, err := fixture.store.AllChildrenClosed(ctx, "oro-epic"); err != nil || allClosed {
				t.Fatalf("AllChildrenClosed with open child = %v, %v; want false, nil", allClosed, err)
			}

			tagged, err := fixture.store.FindByParentAndTag(ctx, "oro-epic", "phase-1")
			if err != nil {
				t.Fatalf("FindByParentAndTag: %v", err)
			}
			if ids(tagged) != "oro-child-open" {
				t.Fatalf("FindByParentAndTag ids = %s, want oro-child-open", ids(tagged))
			}

			if err := fixture.store.Close(ctx, "oro-child-open", "done"); err != nil {
				t.Fatalf("Close child open: %v", err)
			}
			if err := fixture.store.Close(ctx, "oro-child-closed", "done"); err != nil {
				t.Fatalf("Close child closed: %v", err)
			}
			if allClosed, err := fixture.store.AllChildrenClosed(ctx, "oro-epic"); err != nil || !allClosed {
				t.Fatalf("AllChildrenClosed after close = %v, %v; want true, nil", allClosed, err)
			}
		})
	}
}

func TestParityExportSnapshot(t *testing.T) {
	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-export-b", Title: "export b", Tags: []string{"b"}})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-export-a", Title: "export a", Tags: []string{"a"}})
			if err := fixture.store.Close(ctx, "oro-export-a", "done"); err != nil {
				t.Fatalf("Close export a: %v", err)
			}

			data, err := fixture.store.Export(ctx)
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			got := map[string]protocol.Bead{}
			for _, line := range splitJSONLines(string(data)) {
				var bead protocol.Bead
				if err := json.Unmarshal([]byte(line), &bead); err != nil {
					t.Fatalf("export line is not JSON bead: %v", err)
				}
				got[bead.ID] = bead
			}
			if len(got) != 2 {
				t.Fatalf("Export bead count = %d, want 2: %#v", len(got), got)
			}
			if got["oro-export-a"].Status != "closed" || got["oro-export-a"].CloseReason != "done" {
				t.Fatalf("Export closed bead = %#v", got["oro-export-a"])
			}
			if got["oro-export-b"].Status != "open" || !reflect.DeepEqual(got["oro-export-b"].Tags, []string{"b"}) {
				t.Fatalf("Export open bead = %#v", got["oro-export-b"])
			}
		})
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

func TestParityDependencyAndStatusAPIs(t *testing.T) {
	for _, fixture := range newDependencyFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-open", Title: "open"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-progress", Title: "progress"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-closed", Title: "closed"})
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-dependent", Title: "dependent"})

			status := "in_progress"
			if err := fixture.store.Update(ctx, "oro-progress", UpdateParams{Status: &status}); err != nil {
				t.Fatalf("Update progress: %v", err)
			}
			if err := fixture.store.Close(ctx, "oro-closed", "done"); err != nil {
				t.Fatalf("Close closed: %v", err)
			}

			counts, err := fixture.store.CountByStatus(ctx)
			if err != nil {
				t.Fatalf("CountByStatus: %v", err)
			}
			if counts != (StatusCounts{Open: 2, InProgress: 1, Closed: 1}) {
				t.Fatalf("CountByStatus = %#v, want 2 open, 1 in_progress, 1 closed", counts)
			}

			if err := fixture.store.AddDependency(ctx, "oro-dependent", "oro-open", ""); err != nil {
				t.Fatalf("AddDependency default type: %v", err)
			}
			if err := fixture.store.AddDependency(ctx, "oro-dependent", "oro-open", ""); err != nil {
				t.Fatalf("AddDependency duplicate default type: %v", err)
			}
			if err := fixture.store.AddDependency(ctx, "oro-dependent", "oro-progress", "conditional-blocks"); err != nil {
				t.Fatalf("AddDependency conditional type: %v", err)
			}

			deps, err := fixture.store.ListDependencies(ctx, "oro-dependent")
			if err != nil {
				t.Fatalf("ListDependencies: %v", err)
			}
			if got, want := dependencySummary(deps), "oro-open:blocks,oro-progress:conditional-blocks"; got != want {
				t.Fatalf("dependencies = %s, want %s", got, want)
			}

			deps[0].Type = "mutated"
			deps, err = fixture.store.ListDependencies(ctx, "oro-dependent")
			if err != nil {
				t.Fatalf("ListDependencies after mutation: %v", err)
			}
			if got, want := dependencySummary(deps), "oro-open:blocks,oro-progress:conditional-blocks"; got != want {
				t.Fatalf("dependencies after caller mutation = %s, want %s", got, want)
			}

			if err := fixture.store.RemoveDependency(ctx, "oro-dependent", "oro-open"); err != nil {
				t.Fatalf("RemoveDependency: %v", err)
			}
			deps, err = fixture.store.ListDependencies(ctx, "oro-dependent")
			if err != nil {
				t.Fatalf("ListDependencies after remove: %v", err)
			}
			if got, want := dependencySummary(deps), "oro-progress:conditional-blocks"; got != want {
				t.Fatalf("dependencies after remove = %s, want %s", got, want)
			}

			if err := fixture.store.AddDependency(ctx, "oro-dependent", "oro-dependent", "blocks"); err == nil {
				t.Fatal("AddDependency self-reference succeeded, want error")
			}
			if err := fixture.store.AddDependency(ctx, "oro-missing", "oro-open", "blocks"); !isBeadNotFound(err) {
				t.Fatalf("AddDependency missing dependent error = %v, want BeadNotFoundError", err)
			}
			if err := fixture.store.AddDependency(ctx, "oro-dependent", "oro-missing", "blocks"); !isBeadNotFound(err) {
				t.Fatalf("AddDependency missing blocker error = %v, want BeadNotFoundError", err)
			}
			if err := fixture.store.RemoveDependency(ctx, "oro-missing", "oro-open"); !isBeadNotFound(err) {
				t.Fatalf("RemoveDependency missing error = %v, want BeadNotFoundError", err)
			}
			if _, err := fixture.store.ListDependencies(ctx, "oro-missing"); !isBeadNotFound(err) {
				t.Fatalf("ListDependencies missing error = %v, want BeadNotFoundError", err)
			}
		})
	}
}

func TestParityUpdateValidatesStatusTransitions(t *testing.T) {
	validStatuses := []string{"in_progress", "blocked", "closed", "open"}
	invalidStatuses := []string{"ready", "deferred", ""}

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

func TestLoadOrInitShadowStartedAt(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	startedAt, err := LoadOrInitShadowStartedAt(ctx, store.db)
	if err != nil {
		t.Fatalf("LoadOrInitShadowStartedAt initialize: %v", err)
	}
	if startedAt.IsZero() {
		t.Fatal("LoadOrInitShadowStartedAt returned zero time after initialize")
	}

	reloaded, err := LoadOrInitShadowStartedAt(ctx, store.db)
	if err != nil {
		t.Fatalf("LoadOrInitShadowStartedAt reload: %v", err)
	}
	if !reloaded.Equal(startedAt) {
		t.Fatalf("reload time = %s, want initialized time %s", reloaded.Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano))
	}

	mustExec(t, store.db, `UPDATE kv_store SET value='not-a-time' WHERE key=?`, shadowStartedAtKey)
	if _, err := LoadOrInitShadowStartedAt(ctx, store.db); err == nil {
		t.Fatal("LoadOrInitShadowStartedAt invalid persisted time succeeded, want error")
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

func TestParityExpiredDeferUntilSurfacesBead(t *testing.T) {
	const expiredUntil = "2000-01-01T00:00:00Z"

	for _, fixture := range newParityFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			mustCreateStore(t, fixture.store, CreateParams{ID: "oro-expired-defer", Title: "expired defer"})

			if err := fixture.store.Defer(ctx, "oro-expired-defer", expiredUntil); err != nil {
				t.Fatalf("Defer: %v", err)
			}
			ready, err := fixture.store.Ready(ctx)
			if err != nil {
				t.Fatalf("Ready after expired defer: %v", err)
			}
			if ids(ready) != "oro-expired-defer" {
				t.Fatalf("Ready after expired defer ids = %s, want oro-expired-defer", ids(ready))
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

// TestSQLiteStoreAddDependencyRejectsSelfBlock locks in the source==target
// rejection at the store layer. Covers oro-qafy (a): a dependency edge from a
// bead to itself must be refused before any insertion happens. The CLI guard
// in cmd_bead.go provides the worker-context layer; this test covers the
// underlying store invariant.
func TestSQLiteStoreAddDependencyRejectsSelfBlock(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	if _, err := store.Create(ctx, CreateParams{ID: "oro-x", Title: "x", Type: "task"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := store.AddDependency(ctx, "oro-x", "oro-x", "blocks")
	if err == nil {
		t.Fatalf("expected self-block rejection, got nil")
	}
	if !strings.Contains(err.Error(), "itself") {
		t.Fatalf("expected error containing 'itself', got %v", err)
	}

	bead, err := store.Show(ctx, "oro-x")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if bead == nil || len(bead.Dependencies) != 0 {
		t.Fatalf("bead deps = %#v, want none", bead)
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

type dependencyStore interface {
	Store
	AddDependency(context.Context, string, string, string) error
	RemoveDependency(context.Context, string, string) error
	ListDependencies(context.Context, string) ([]protocol.Dependency, error)
	CountByStatus(context.Context) (StatusCounts, error)
}

type parityFixture struct {
	name          string
	store         parityStore
	addDependency func(t *testing.T, beadID, dependsOnID, depType string)
	assignActive  func(t *testing.T, beadID, workerID string)
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
			assignActive: func(t *testing.T, beadID, workerID string) {
				t.Helper()
				mustExec(t, sqliteStore.db, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`, beadID, workerID, "/tmp/"+beadID)
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
			assignActive: func(t *testing.T, beadID, workerID string) {
				t.Helper()
				fakeStore.mu.Lock()
				defer fakeStore.mu.Unlock()
				bead, ok := fakeStore.beads[beadID]
				if !ok {
					t.Fatalf("missing fake bead %s", beadID)
				}
				bead.WorkerID = workerID
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

type dependencyFixture struct {
	name  string
	store dependencyStore
}

func newDependencyFixtures(t *testing.T) []dependencyFixture {
	t.Helper()
	return []dependencyFixture{
		{name: "sqlite", store: newTestSQLiteStore(t)},
		{name: "fake", store: NewFakeStore()},
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

func sortedIDs(beads []protocol.Bead) string {
	values := make([]string, 0, len(beads))
	for _, bead := range beads {
		values = append(values, bead.ID)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func dependencySummary(deps []protocol.Dependency) string {
	values := make([]string, 0, len(deps))
	for _, dep := range deps {
		values = append(values, dep.DependsOnID+":"+dep.Type)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
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
