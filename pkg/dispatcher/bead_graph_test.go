package dispatcher_test

import (
	"context"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/dispatcher"
)

func TestCreateBeadGraphCreatesChildren(t *testing.T) {
	ctx := context.Background()
	store := beadstore.NewFakeStore()

	got, err := dispatcher.CreateBeadGraph(ctx, store, "parent-1", []beadstore.CreateParams{
		{ID: "child-1", Title: "one", Type: "task"},
		{ID: "child-2", Title: "two", Type: "bug", ParentID: "wrong-parent"},
		{ID: "child-3", Title: "three", Type: "chore"},
	})
	if err != nil {
		t.Fatalf("CreateBeadGraph: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	for i, bead := range got {
		if bead.Epic != "parent-1" {
			t.Fatalf("got[%d].Epic = %q, want parent-1", i, bead.Epic)
		}
	}
}
