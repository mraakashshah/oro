package dispatcher //nolint:testpackage // white-box tests need access to unexported store selection helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"oro/pkg/beadstore"
	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestSelectStore(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		wantType any
	}{
		{name: "default", mode: "", wantType: &beadstore.FakeStore{}},
		{name: "cli", mode: "cli", wantType: &beadstore.FakeStore{}},
		{name: "shadow", mode: "shadow", wantType: &beadstore.ShadowStore{}},
		{name: "sqlite", mode: "sqlite", wantType: &beadstore.SQLiteStore{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mode == "" {
				t.Setenv("ORO_BEADSOURCE_MODE", "")
				if err := os.Unsetenv("ORO_BEADSOURCE_MODE"); err != nil {
					t.Fatalf("unset mode: %v", err)
				}
			} else {
				t.Setenv("ORO_BEADSOURCE_MODE", tt.mode)
			}

			db := newTestDB(t)
			var beadSrc DeferredStore = beadstore.NewFakeStore()
			if tt.name == "sqlite" {
				beadSrc = beadstore.NewSQLiteStore(db)
			}
			gitRunner := &mockGitRunner{}
			spawnMock := &mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}
			sockPath := fmt.Sprintf("/tmp/oro-select-store-%d.sock", time.Now().UnixNano())
			t.Cleanup(func() { _ = os.Remove(sockPath) })

			d, err := New(
				Config{SocketPath: sockPath, DBPath: ":memory:"},
				db,
				merge.NewCoordinator(gitRunner),
				ops.NewSpawner(spawnMock),
				beadSrc,
				&mockWorktreeManager{created: make(map[string]string)},
				&mockEscalator{},
				nil,
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			gotType := reflect.TypeOf(d.beads)
			wantType := reflect.TypeOf(tt.wantType)
			if gotType != wantType {
				t.Fatalf("selected store type = %v, want %v", gotType, wantType)
			}
		})
	}
}

func TestSelectStoreSQLiteReturnsPlainStoreWithoutMemoryFetcher(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	seedStore := beadstore.NewSQLiteStore(db)
	if _, err := seedStore.Create(ctx, beadstore.CreateParams{
		ID:          "oro-no-dispatcher-memory",
		Title:       "sqlite",
		Description: "dispatch selected store should not enrich show",
		Tags:        []string{"sqlite"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	memories := memory.NewStore(db)
	if _, err := memories.Insert(ctx, memory.InsertParams{
		Content:    "dispatch selected store should not enrich show",
		Type:       "lesson",
		Tags:       []string{"sqlite"},
		Source:     "self_report",
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	withMemory := beadstore.NewSQLiteStore(db, beadstore.WithMemoryFetcher(func(ctx context.Context, tags []string, description string, maxTokens int) (string, error) {
		return memory.ForPrompt(ctx, memories, tags, description, maxTokens)
	}))
	enriched, err := withMemory.Show(ctx, "oro-no-dispatcher-memory")
	if err != nil {
		t.Fatalf("control Show with memory fetcher: %v", err)
	}
	if enriched.Memory == "" {
		t.Fatalf("control Show with memory fetcher left Memory empty; seeded memory is not observable")
	}

	store, err := selectStore(ctx, "sqlite", beadstore.NewFakeStore(), db)
	if err != nil {
		t.Fatalf("selectStore: %v", err)
	}
	shown, err := store.Show(ctx, "oro-no-dispatcher-memory")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown.Memory != "" {
		t.Fatalf("selected sqlite store Show Memory = %q, want empty", shown.Memory)
	}
}

func TestSelectStoreSQLiteDoesNotFetchLegacyPromptMemory(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	seedStore := beadstore.NewSQLiteStore(db)
	if _, err := seedStore.Create(ctx, beadstore.CreateParams{
		ID:          "oro-no-legacy-memory",
		Title:       "sqlite",
		Description: "sqlite show must not fetch legacy prompt memory",
		Tags:        []string{"sqlite"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	memories := memory.NewStore(db)
	if _, err := memories.Insert(ctx, memory.InsertParams{
		Content:    "sqlite show must not fetch legacy prompt memory",
		Type:       "lesson",
		Tags:       []string{"sqlite"},
		Source:     "self_report",
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM memory_read_events`); err != nil {
		t.Fatalf("clear memory_read_events: %v", err)
	}

	store, err := selectStore(ctx, "sqlite", beadstore.NewFakeStore(), db)
	if err != nil {
		t.Fatalf("selectStore: %v", err)
	}
	shown, err := store.Show(ctx, "oro-no-legacy-memory")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown == nil {
		t.Fatalf("Show returned nil, want sqlite bead")
	}
	if shown.Memory != "" {
		t.Fatalf("selected sqlite store Show Memory = %q, want empty", shown.Memory)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_read_events WHERE operation = "for_prompt"`).Scan(&count); err != nil {
		t.Fatalf("count for_prompt memory_read_events: %v", err)
	}
	if count != 0 {
		t.Fatalf("for_prompt memory_read_events count = %d, want 0", count)
	}
}

func TestPromptTelemetryFixtureCountsForPromptReads(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	memories := memory.NewStore(db)

	if _, err := memories.Insert(ctx, memory.InsertParams{
		Content:    "dispatch selected store should not enrich show",
		Type:       "lesson",
		Tags:       []string{"sqlite"},
		Source:     "self_report",
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM memory_read_events`); err != nil {
		t.Fatalf("clear memory_read_events: %v", err)
	}

	if _, err := memory.ForPrompt(ctx, memories, []string{"sqlite"}, "dispatch selected store should not enrich show", 500); err != nil {
		t.Fatalf("ForPrompt: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_read_events WHERE operation = "for_prompt"`).Scan(&count); err != nil {
		t.Fatalf("count for_prompt memory_read_events: %v", err)
	}
	if count != 1 {
		t.Fatalf("for_prompt memory_read_events count = %d, want 1", count)
	}
}

func TestSelectStoreSQLiteReturnsPlainSQLiteStore(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	primary := beadstore.NewFakeStore()

	store, err := selectStore(ctx, " sqlite ", primary, db)
	if err != nil {
		t.Fatalf("selectStore: %v", err)
	}
	if store == primary {
		t.Fatalf("selectStore returned primary FakeStore, want plain SQLiteStore")
	}
	if _, ok := store.(*beadstore.ShadowStore); ok {
		t.Fatalf("selectStore returned %T, want plain SQLiteStore", store)
	}
	if _, ok := store.(*beadstore.SQLiteStore); !ok {
		t.Fatalf("selectStore returned %T, want *beadstore.SQLiteStore", store)
	}

	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:    "oro-sqlite-selection",
		Title: "sqlite selection",
	}); err != nil {
		t.Fatalf("Create after selectStore migration: %v", err)
	}
}

func TestSelectStoreSQLiteLeavesMemoryFetcherNil(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	store, err := selectStore(ctx, "sqlite", beadstore.NewFakeStore(), db)
	if err != nil {
		t.Fatalf("selectStore: %v", err)
	}
	sqliteStore, ok := store.(*beadstore.SQLiteStore)
	if !ok {
		t.Fatalf("selectStore returned %T, want *beadstore.SQLiteStore", store)
	}

	memoryField := reflect.ValueOf(sqliteStore).Elem().FieldByName("memory")
	memoryField = reflect.NewAt(memoryField.Type(), unsafe.Pointer(memoryField.UnsafeAddr())).Elem()
	if !memoryField.IsNil() {
		t.Fatalf("selected sqlite store memory fetcher is installed, want nil")
	}
}

func TestSelectStoreSQLiteReadsSQLiteStoreNotPrimary(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	sqliteStore := beadstore.NewSQLiteStore(db)
	if _, err := sqliteStore.Create(ctx, beadstore.CreateParams{
		ID:          "oro-sqlite-selected",
		Title:       "sqlite title",
		Description: "stored in sqlite",
	}); err != nil {
		t.Fatalf("seed sqlite bead: %v", err)
	}
	primary := beadstore.NewFakeStore(protocol.Bead{
		ID:        "oro-sqlite-selected",
		Title:     "primary title",
		Status:    "open",
		Priority:  1,
		UpdatedAt: "2026-05-25T00:00:00Z",
	})

	store, err := selectStore(ctx, "sqlite", primary, db)
	if err != nil {
		t.Fatalf("selectStore: %v", err)
	}
	if _, ok := store.(*beadstore.ShadowStore); ok {
		t.Fatalf("selectStore returned %T, want direct sqlite store", store)
	}
	shown, err := store.Show(ctx, "oro-sqlite-selected")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown == nil {
		t.Fatalf("Show returned nil, want sqlite bead")
	}
	if shown.Title != "sqlite title" {
		t.Fatalf("Show title = %q, want sqlite title", shown.Title)
	}
}

func TestSelectStoreShadowLogsDivergenceEvent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	primary := beadstore.NewFakeStore(protocol.Bead{
		ID:        "same",
		Title:     "primary",
		Status:    "open",
		Priority:  1,
		UpdatedAt: "2026-04-28T09:00:00Z",
	})
	secondary := beadstore.NewFakeStore(protocol.Bead{
		ID:        "same",
		Title:     "secondary",
		Status:    "open",
		Priority:  1,
		UpdatedAt: "2026-04-28T09:00:00Z",
	})

	store, err := selectStore(ctx, "shadow", primary, db)
	if err != nil {
		t.Fatalf("selectStore: %v", err)
	}
	shadow, ok := store.(*beadstore.ShadowStore)
	if !ok {
		t.Fatalf("selectStore returned %T, want *beadstore.ShadowStore", store)
	}
	secondaryField := reflect.ValueOf(shadow).Elem().FieldByName("secondary")
	secondaryField = reflect.NewAt(secondaryField.Type(), unsafe.Pointer(secondaryField.UnsafeAddr())).Elem()
	secondaryField.Set(reflect.ValueOf(secondary))

	if _, err := shadow.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	var eventType, source, payload string
	if err := db.QueryRowContext(ctx, `SELECT type, source, payload FROM events`).Scan(&eventType, &source, &payload); err != nil {
		t.Fatalf("query divergence event: %v", err)
	}
	if eventType != "beadstore_divergence" {
		t.Fatalf("event type = %q, want beadstore_divergence", eventType)
	}
	if source != "beadstore_shadow" {
		t.Fatalf("source = %q, want beadstore_shadow", source)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("payload is not structured JSON: %v; payload=%s", err, payload)
	}
	if decoded["operation"] != "Ready" || decoded["kind"] != "real" || decoded["reason"] != "bead result mismatch" {
		t.Fatalf("payload = %#v, want Ready real bead result mismatch", decoded)
	}
}

func TestSelectStorePersistsShadowStartedAt(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	primary := beadstore.NewFakeStore()

	first, err := selectStore(ctx, "shadow", primary, db)
	if err != nil {
		t.Fatalf("first selectStore: %v", err)
	}
	firstShadow, ok := first.(*beadstore.ShadowStore)
	if !ok {
		t.Fatalf("first selectStore returned %T, want *beadstore.ShadowStore", first)
	}
	firstStartedAt := shadowStartedAt(t, firstShadow)

	var stored string
	if err := db.QueryRowContext(ctx, `SELECT value FROM kv_store WHERE key = 'beadstore_shadow_started_at'`).Scan(&stored); err != nil {
		t.Fatalf("query shadow start kv row: %v", err)
	}
	if stored != firstStartedAt.Format(time.RFC3339Nano) {
		t.Fatalf("stored shadow start = %q, want %q", stored, firstStartedAt.Format(time.RFC3339Nano))
	}

	updatedAfterWindow := firstStartedAt.Add(time.Second).Format(time.RFC3339Nano)
	updatedBeforeWindow := firstStartedAt.Add(-time.Second).Format(time.RFC3339Nano)
	primary = beadstore.NewFakeStore(protocol.Bead{
		ID:        "hot",
		Title:     "primary",
		Status:    "open",
		Priority:  1,
		UpdatedAt: updatedAfterWindow,
	})
	second, err := selectStore(ctx, "shadow", primary, db)
	if err != nil {
		t.Fatalf("second selectStore: %v", err)
	}
	secondShadow, ok := second.(*beadstore.ShadowStore)
	if !ok {
		t.Fatalf("second selectStore returned %T, want *beadstore.ShadowStore", second)
	}
	secondStartedAt := shadowStartedAt(t, secondShadow)
	if !secondStartedAt.Equal(firstStartedAt) {
		t.Fatalf("second shadowStartedAt = %s, want persisted %s", secondStartedAt.Format(time.RFC3339Nano), firstStartedAt.Format(time.RFC3339Nano))
	}

	secondary := beadstore.NewFakeStore(protocol.Bead{
		ID:        "hot",
		Title:     "secondary",
		Status:    "open",
		Priority:  1,
		UpdatedAt: updatedBeforeWindow,
	})
	secondaryField := reflect.ValueOf(secondShadow).Elem().FieldByName("secondary")
	secondaryField = reflect.NewAt(secondaryField.Type(), unsafe.Pointer(secondaryField.UnsafeAddr())).Elem()
	secondaryField.Set(reflect.ValueOf(secondary))

	if _, err := secondShadow.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kv_store WHERE key = 'beadstore_shadow_started_at'`).Scan(&count); err != nil {
		t.Fatalf("count shadow start kv rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("shadow start kv rows = %d, want 1", count)
	}
	var kind string
	if err := db.QueryRowContext(ctx, `SELECT json_extract(payload, '$.kind') FROM events WHERE type = 'beadstore_divergence'`).Scan(&kind); err != nil {
		t.Fatalf("query divergence event kind: %v", err)
	}
	if kind != "drift" {
		t.Fatalf("divergence kind = %q, want drift from persisted shadow window", kind)
	}
}

func TestSelectStoreCreatesShadowStartedAtOnLegacyDB(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := db.ExecContext(ctx, `DROP TABLE kv_store`); err != nil {
		t.Fatalf("drop kv_store: %v", err)
	}

	store, err := selectStore(ctx, "shadow", beadstore.NewFakeStore(), db)
	if err != nil {
		t.Fatalf("selectStore shadow on legacy db: %v", err)
	}
	if _, ok := store.(*beadstore.ShadowStore); !ok {
		t.Fatalf("selectStore returned %T, want *beadstore.ShadowStore", store)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT value FROM kv_store WHERE key = 'beadstore_shadow_started_at'`).Scan(&stored); err != nil {
		t.Fatalf("query initialized shadow start: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, stored); err != nil {
		t.Fatalf("stored shadow start %q is not RFC3339Nano: %v", stored, err)
	}
}

func TestSelectStoreRejectsMalformedShadowStartedAt(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO kv_store (key, value, updated_at) VALUES ('beadstore_shadow_started_at', 'not-a-time', '2026-04-28T00:00:00Z')`); err != nil {
		t.Fatalf("seed malformed shadow start: %v", err)
	}

	if _, err := selectStore(ctx, "shadow", beadstore.NewFakeStore(), db); err == nil {
		t.Fatalf("selectStore shadow succeeded with malformed shadow start")
	} else if !strings.Contains(err.Error(), "beadstore_shadow_started_at") {
		t.Fatalf("error = %v, want shadow start key", err)
	}
}

func shadowStartedAt(t *testing.T, store *beadstore.ShadowStore) time.Time {
	t.Helper()
	field := reflect.ValueOf(store).Elem().FieldByName("shadowStartedAt")
	field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	startedAt, ok := field.Interface().(time.Time)
	if !ok {
		t.Fatalf("shadowStartedAt field has type %T, want time.Time", field.Interface())
	}
	return startedAt
}
