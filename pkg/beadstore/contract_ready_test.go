//nolint:testpackage // Exercises SQLiteStore's read transaction directly.
package beadstore

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

func TestDraftExcludedFromEveryReadyPath(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)

	created, err := store.Create(ctx, CreateParams{
		ID:              "draft-v2",
		Title:           "Draft title",
		ContractVersion: 2,
		Draft:           true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ContractVersion != 2 || !created.Draft {
		t.Fatalf("created contract fields = version %d, draft %t", created.ContractVersion, created.Draft)
	}

	title := "Updated draft title"
	description := "Updated draft description"
	acceptance := "Test: pkg/beadstore/contract_ready_test.go:TestDraftExcludedFromEveryReadyPath\nCmd: go test ./pkg/beadstore\nAssert: PASS\nRead: pkg/beadstore/sqlite.go:Ready"
	estimate := 5
	beadType := "task"
	priority := 1
	parent := ""
	owner := "owner"
	notes := "draft note"
	contractVersion := 2
	draft := true
	if err := store.Update(ctx, "draft-v2", UpdateParams{
		Title:              &title,
		Description:        &description,
		AcceptanceCriteria: &acceptance,
		EstimatedMinutes:   &estimate,
		Type:               &beadType,
		Priority:           &priority,
		ParentID:           &parent,
		Owner:              &owner,
		Notes:              &notes,
		ContractVersion:    &contractVersion,
		Draft:              &draft,
	}); err != nil {
		t.Fatalf("Update draft fields: %v", err)
	}

	stored, err := store.Show(ctx, "draft-v2")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if stored == nil || stored.Title != title || stored.Description != description ||
		stored.AcceptanceCriteria != acceptance || stored.EstimatedMinutes != estimate ||
		stored.Type != beadType || stored.Priority != priority || stored.Owner != owner ||
		stored.Notes != notes || stored.ContractVersion != contractVersion || !stored.Draft {
		t.Fatalf("stored draft = %#v", stored)
	}

	assertDraftAbsent := func(source string, beads []protocol.Bead) {
		t.Helper()
		for _, bead := range beads {
			if bead.ID == "draft-v2" {
				t.Fatalf("%s Ready included draft: %#v", source, bead)
			}
		}
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Store.Ready: %v", err)
	}
	assertDraftAbsent("Store", ready)
	if err := store.WithReadTx(ctx, func(tx ReadTx) error {
		ready, err := tx.Ready(ctx)
		if err != nil {
			return err
		}
		assertDraftAbsent("ReadTx", ready)
		return nil
	}); err != nil {
		t.Fatalf("WithReadTx: %v", err)
	}

	publish := false
	err = store.Update(ctx, "draft-v2", UpdateParams{Draft: &publish})
	if err == nil || !strings.Contains(err.Error(), "publish") {
		t.Fatalf("Update clearing draft error = %v, want publish-only error", err)
	}
}
