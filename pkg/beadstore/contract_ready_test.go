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

	if _, err := store.Create(ctx, CreateParams{
		ID:              "ready-control",
		Title:           "Ready control",
		ContractVersion: 2,
	}); err != nil {
		t.Fatalf("Create ready control: %v", err)
	}
	created, err := store.Create(ctx, CreateParams{
		ID:              "incomplete-draft",
		Title:           "Incomplete draft",
		ContractVersion: 2,
		Draft:           true,
	})
	if err != nil {
		t.Fatalf("Create incomplete draft: %v", err)
	}
	if created.ContractVersion != 2 || !created.Draft || created.AcceptanceCriteria != "" {
		t.Fatalf("created contract fields = version %d, draft %t", created.ContractVersion, created.Draft)
	}
	if _, err := store.Create(ctx, CreateParams{
		ID:                 "updated-parent",
		Title:              "Updated parent",
		Status:             "closed",
		ContractVersion:    2,
		AcceptanceCriteria: "parent acceptance",
	}); err != nil {
		t.Fatalf("Create updated parent: %v", err)
	}
	if _, err := store.Create(ctx, CreateParams{
		ID:                 "draft-v2",
		Title:              "Initial title",
		Description:        "Initial description",
		AcceptanceCriteria: "initial acceptance",
		EstimatedMinutes:   2,
		Type:               "bug",
		Priority:           4,
		ContractVersion:    1,
	}); err != nil {
		t.Fatalf("Create update target: %v", err)
	}

	title := "Updated draft title"
	description := "Updated draft description"
	acceptance := "Test: pkg/beadstore/contract_ready_test.go:TestDraftExcludedFromEveryReadyPath\nCmd: go test ./pkg/beadstore\nAssert: PASS\nRead: pkg/beadstore/sqlite.go:Ready"
	estimate := 5
	beadType := "task"
	priority := 1
	parent := "updated-parent"
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
		stored.Type != beadType || stored.Priority != priority || stored.Epic != parent || stored.Owner != owner ||
		stored.Notes != notes || stored.ContractVersion != contractVersion || !stored.Draft {
		t.Fatalf("stored draft = %#v", stored)
	}

	assertOnlyControlReady := func(source string, beads []protocol.Bead) {
		t.Helper()
		if len(beads) != 1 || beads[0].ID != "ready-control" {
			t.Fatalf("%s Ready = %#v, want only ready-control", source, beads)
		}
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatalf("Store.Ready: %v", err)
	}
	assertOnlyControlReady("Store", ready)
	if err := store.WithReadTx(ctx, func(tx ReadTx) error {
		ready, err := tx.Ready(ctx)
		if err != nil {
			return err
		}
		assertOnlyControlReady("ReadTx", ready)
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
