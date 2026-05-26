package memoryboundary_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"oro/internal/memoryboundary"
	"oro/pkg/dbutil"
	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

func setupBoundaryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return db
}

func TestStoreAdaptsCoreMemoryOperations(t *testing.T) {
	ctx := context.Background()
	store := memoryboundary.NewStore(setupBoundaryDB(t))

	id, err := store.Insert(ctx, protocol.MemoryInsertParams{
		Content:    "adapter boundary remembers focused regression tests",
		Type:       "lesson",
		Tags:       []string{"adapter", "boundary"},
		Source:     "test",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.Content != "adapter boundary remembers focused regression tests" {
		t.Fatalf("content = %q", got.Content)
	}

	listed, err := store.ListMemories(ctx, protocol.MemoryListOpts{Type: "lesson", Tag: "adapter", Limit: 10})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("listed = %#v, want one row with id %d", listed, id)
	}

	results, err := store.Search(ctx, "focused regression", protocol.MemorySearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 || results[0].ID != id {
		t.Fatalf("search results = %#v, want id %d", results, id)
	}

	dumped, err := store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("dump all: %v", err)
	}
	if len(dumped) != 1 || dumped[0].ID != id {
		t.Fatalf("dumped = %#v, want one row with id %d", dumped, id)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	dumped, err = store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("dump after delete: %v", err)
	}
	if len(dumped) != 0 {
		t.Fatalf("dump after delete = %#v, want empty", dumped)
	}

	store.SetProject("adapter-project")
	store.ClearMemoryProjectScope()
	merged, pruned, err := store.ConsolidateMemories(ctx, protocol.MemoryConsolidateOpts{DryRun: true})
	if err != nil {
		t.Fatalf("consolidate memories: %v", err)
	}
	if merged != 0 || pruned != 0 {
		t.Fatalf("consolidate empty store = (%d, %d), want (0, 0)", merged, pruned)
	}
}

func TestWorkerStoreAndSpawner(t *testing.T) {
	store := memoryboundary.NewWorkerStore(setupBoundaryDB(t))
	if !store.HasEmbedder() {
		t.Fatal("NewWorkerStore must attach an embedder")
	}
	if err := store.SaveVocab(context.Background()); err != nil {
		t.Fatalf("save vocab: %v", err)
	}
	if memoryboundary.NewExtractSpawner() == nil {
		t.Fatal("NewExtractSpawner returned nil")
	}
}

func TestDispatcherMemoryServicesAdaptLegacyHooks(t *testing.T) {
	ctx := context.Background()
	db := setupBoundaryDB(t)
	services := memoryboundary.NewDispatcherMemoryServices(db)
	if services.Store == nil {
		t.Fatal("dispatcher store is nil")
	}
	if !services.Store.HasEmbedder() {
		t.Fatal("dispatcher store must use worker memory store with embedder")
	}

	if err := services.InsertRejection(ctx, "oro-test", "worker-1", "needs a narrower boundary"); err != nil {
		t.Fatalf("insert rejection: %v", err)
	}
	rejections, err := services.GetRejections(ctx, "oro-test")
	if err != nil {
		t.Fatalf("get rejections: %v", err)
	}
	if len(rejections) != 1 || rejections[0].Feedback != "needs a narrower boundary" {
		t.Fatalf("rejections = %#v", rejections)
	}

	id, err := services.Store.Insert(ctx, protocol.MemoryInsertParams{
		Content:    "dream action target",
		Type:       "lesson",
		Source:     "test",
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert dream target: %v", err)
	}
	if err := services.ExecuteDream(ctx, []dispatcher.DreamAction{{Kind: "DELETE", ID: id}}, func(msg string) { t.Log(msg) }); err != nil {
		t.Fatalf("execute dream: %v", err)
	}
	memories, err := services.Store.DumpAll(ctx)
	if err != nil {
		t.Fatalf("dump after dream: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("memories after delete dream = %#v, want empty", memories)
	}

	if _, _, err := services.Consolidate(ctx); err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if _, err := services.TrimSearchEvents(ctx, time.Hour); err != nil {
		t.Fatalf("trim search events: %v", err)
	}
	if services.HandoffInserter(nil) == nil {
		t.Fatal("handoff inserter is nil")
	}
}

func TestStoreWrapsClosedDBErrors(t *testing.T) {
	ctx := context.Background()
	db := setupBoundaryDB(t)
	store := memoryboundary.NewWorkerStore(db)
	services := memoryboundary.NewDispatcherMemoryServices(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := store.Insert(ctx, protocol.MemoryInsertParams{
		Content:    "closed db insert",
		Type:       "lesson",
		Source:     "test",
		Confidence: 0.8,
	}); err == nil {
		t.Fatal("insert on closed db succeeded")
	}
	if _, err := store.GetByID(ctx, 1); err == nil {
		t.Fatal("get by id on closed db succeeded")
	}
	if err := store.Delete(ctx, 1); err == nil {
		t.Fatal("delete on closed db succeeded")
	}
	if _, err := store.Search(ctx, "closed", protocol.MemorySearchOpts{Limit: 1}); err == nil {
		t.Fatal("search on closed db succeeded")
	}
	if _, err := store.DumpAll(ctx); err == nil {
		t.Fatal("dump all on closed db succeeded")
	}
	if err := store.SaveVocab(ctx); err == nil {
		t.Fatal("save vocab on closed db succeeded")
	}
	if _, err := store.ListMemories(ctx, protocol.MemoryListOpts{Limit: 1}); err == nil {
		t.Fatal("list memories on closed db succeeded")
	}
	if _, _, err := store.ConsolidateMemories(ctx, protocol.MemoryConsolidateOpts{}); err == nil {
		t.Fatal("consolidate memories on closed db succeeded")
	}
	if _, err := services.GetRejections(ctx, "oro-test"); err == nil {
		t.Fatal("get rejections on closed db succeeded")
	}
	if _, err := services.TrimSearchEvents(ctx, time.Hour); err == nil {
		t.Fatal("trim search events on closed db succeeded")
	}
}
