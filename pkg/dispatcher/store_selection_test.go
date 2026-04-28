package dispatcher

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/merge"
	"oro/pkg/ops"
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
