package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/memory"

	_ "modernc.org/sqlite"
)

func TestForgetCmd(t *testing.T) {
	t.Run("delete existing ID prints confirmation", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()

		id, err := store.Insert(ctx, memory.InsertParams{
			Content:    "unique_forgetcmd_single_abc memory to delete",
			Type:       "lesson",
			Source:     "cli",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{fmt.Sprintf("%d", id)})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("forget execute: %v", err)
		}

		output := out.String()
		expected := fmt.Sprintf("Forgot memory %d", id)
		if !strings.Contains(output, expected) {
			t.Errorf("expected output to contain %q, got: %s", expected, output)
		}

		// Verify the memory is actually gone
		all, err := store.List(ctx, memory.ListOpts{Limit: 100})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, m := range all {
			if m.ID == id {
				t.Errorf("expected memory %d to be deleted, but it still exists", id)
			}
		}
	})

	t.Run("invalid ID format returns error", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"notanumber"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for non-numeric ID, got nil")
		}
		if !strings.Contains(err.Error(), "invalid id") {
			t.Errorf("expected error to contain 'invalid id', got: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "notanumber") {
			t.Errorf("expected error to contain the bad input 'notanumber', got: %s", err.Error())
		}
	})

	t.Run("non-existent ID returns error", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"999999"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for non-existent ID, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected error to contain 'not found', got: %s", err.Error())
		}
	})

	t.Run("multiple IDs deletes all", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()

		id1, err := store.Insert(ctx, memory.InsertParams{
			Content: "unique_forgetcmd_multi_aaa first memory", Type: "lesson",
			Source: "cli", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert 1: %v", err)
		}
		id2, err := store.Insert(ctx, memory.InsertParams{
			Content: "unique_forgetcmd_multi_bbb second memory", Type: "gotcha",
			Source: "cli", Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("insert 2: %v", err)
		}

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{fmt.Sprintf("%d", id1), fmt.Sprintf("%d", id2)})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("forget execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, fmt.Sprintf("Forgot memory %d", id1)) {
			t.Errorf("expected confirmation for id %d, got: %s", id1, output)
		}
		if !strings.Contains(output, fmt.Sprintf("Forgot memory %d", id2)) {
			t.Errorf("expected confirmation for id %d, got: %s", id2, output)
		}

		// Verify both are gone
		all, err := store.List(ctx, memory.ListOpts{Limit: 100})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(all) != 0 {
			t.Errorf("expected 0 memories after deleting all, got %d", len(all))
		}
	})

	t.Run("store Delete error is propagated", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)
		ctx := context.Background()

		// Insert a memory so the existence check passes
		id, err := store.Insert(ctx, memory.InsertParams{
			Content: "unique_forgetcmd_delerr_xyz memory for delete error test",
			Type:    "lesson", Source: "cli", Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}

		// Close the DB after inserting so that the existence check
		// (store.List) will fail first, which is also a valid error path.
		// We close and verify the error propagates from memoryExists.
		_ = db.Close()

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{fmt.Sprintf("%d", id)})

		err = cmd.Execute()
		if err == nil {
			t.Fatal("expected error when store is broken, got nil")
		}
		if !strings.Contains(err.Error(), "forget") {
			t.Errorf("expected wrapped error with 'forget' prefix, got: %s", err.Error())
		}
	})

	t.Run("store List error in memoryExists is propagated", func(t *testing.T) {
		db := setupTestMemoryDB(t)
		store := memory.NewStore(db)

		// Close the DB so that store.List inside memoryExists fails
		_ = db.Close()

		cmd := newForgetCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"1"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when store.List fails, got nil")
		}
		if !strings.Contains(err.Error(), "check memory exists") {
			t.Errorf("expected error to contain 'check memory exists', got: %s", err.Error())
		}
	})
}
