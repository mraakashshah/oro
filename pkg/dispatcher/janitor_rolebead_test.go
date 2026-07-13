package dispatcher //nolint:testpackage // pins the internal cleanliness role-bead contract

import (
	"context"
	"testing"

	"oro/pkg/beadstore"
)

func TestJanitorRoleBeadLifecycle(t *testing.T) {
	var _ func(*Dispatcher, context.Context, string) (string, error) = (*Dispatcher).ensureRoleBead

	d, _, _, _, _, _ := newTestDispatcher(t)
	store := beadstore.NewFakeStore()
	d.beads = store

	firstID, err := d.ensureRoleBead(t.Context(), "janitor")
	if err != nil {
		t.Fatalf("ensure first janitor role bead: %v", err)
	}
	secondID, err := d.ensureRoleBead(t.Context(), "janitor")
	if err != nil {
		t.Fatalf("ensure second janitor role bead: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("rediscovered janitor role bead = %q, want %q", secondID, firstID)
	}

	role, err := store.Show(t.Context(), firstID)
	if err != nil {
		t.Fatalf("show janitor role bead: %v", err)
	}
	if role == nil || role.Status != "closed" || role.Metadata[cleanlinessRoleMetadataKey] != "janitor" {
		t.Fatalf("janitor role bead = %#v, want atomically closed meta_role=janitor", role)
	}
	ready, err := store.Ready(t.Context())
	if err != nil {
		t.Fatalf("list ready beads: %v", err)
	}
	for _, bead := range ready {
		if bead.ID == firstID {
			t.Fatalf("closed janitor role bead %q appeared in Ready", firstID)
		}
	}
}
