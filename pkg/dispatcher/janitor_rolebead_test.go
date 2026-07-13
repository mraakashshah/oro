package dispatcher //nolint:testpackage // pins the internal cleanliness role-bead contract

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

type nilRoleCreateStore struct {
	DeferredStore
}

func (nilRoleCreateStore) Create(context.Context, beadstore.CreateParams) (*protocol.Bead, error) {
	return nil, nil
}

func TestJanitorRoleBeadLifecycle(t *testing.T) {
	assertEnsureRoleBeadSignature((*Dispatcher).ensureRoleBead)

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

	d.beads = nilRoleCreateStore{DeferredStore: beadstore.NewFakeStore()}
	if _, err := d.ensureRoleBead(t.Context(), "audit"); err == nil || !strings.Contains(err.Error(), "empty bead") {
		t.Fatalf("ensure audit role bead with nil create result error = %v, want empty bead", err)
	}
}

func assertEnsureRoleBeadSignature(_ func(*Dispatcher, context.Context, string) (string, error)) {}
