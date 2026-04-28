package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"oro/pkg/beadstore"
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
		{name: "default", mode: "", wantType: &CLIStore{}},
		{name: "cli", mode: "cli", wantType: &CLIStore{}},
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
			runner := &mockCommandRunner{}
			beadSrc := NewCLIStore(runner)
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

	store, err := selectStore(ctx, "shadow", primary, db, nil)
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
