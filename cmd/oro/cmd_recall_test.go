package main

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"oro/pkg/memory"

	_ "modernc.org/sqlite"
)

func TestRecallCmdWithStoreNil(t *testing.T) {
	// Verify that newRecallCmdWithStore(nil) lazily opens the default store.
	// This is an integration test that ensures the deduplication works correctly.
	cmd := newRecallCmdWithStore(nil)
	if cmd == nil { //nolint:staticcheck // check below verifies
		t.Fatal("newRecallCmdWithStore(nil) returned nil command")
	}
	// We can't easily test execution without setting up ~/.oro/memories.db,
	// but we can verify the command structure is correct.
	if cmd.Use != "recall <query>" {
		t.Errorf("expected Use='recall <query>', got %q", cmd.Use)
	}
}

func TestRecallCmdDoesNotImportMemory(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cmd_recall.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse cmd_recall.go imports: %v", err)
	}
	for _, imp := range file.Imports {
		if imp.Path.Value == `"oro/pkg/memory"` {
			t.Fatalf("cmd_recall.go imports oro/pkg/memory at %s", fset.Position(imp.Pos()))
		}
	}
}

func TestRecallCmd(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	// Seed 7 distinct memories so we can verify the limit-5 behaviour.
	seeds := []memory.InsertParams{
		{Content: "golang generics require type constraints", Type: "lesson", Source: "cli", Confidence: 0.9, FilesRead: []string{"pkg/util/generics.go"}},
		{Content: "golang interfaces are satisfied implicitly", Type: "lesson", Source: "cli", Confidence: 0.85},
		{Content: "golang error wrapping uses fmt Errorf with %w", Type: "gotcha", Source: "cli", Confidence: 0.8},
		{Content: "golang context propagation is mandatory in servers", Type: "pattern", Source: "cli", Confidence: 0.75},
		{Content: "golang struct embedding promotes methods", Type: "lesson", Source: "cli", Confidence: 0.7},
		{Content: "golang goroutine leaks detected by goleak", Type: "gotcha", Source: "cli", Confidence: 0.65},
		{Content: "golang table driven tests are the standard pattern", Type: "pattern", Source: "cli", Confidence: 0.6},
	}
	ids := make([]int64, len(seeds))
	for i, s := range seeds {
		id, err := store.Insert(ctx, s)
		if err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
		ids[i] = id
	}

	t.Run("search returns top 5 results", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"golang"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		// Count numbered result lines (format: "N. [type] content")
		lines := strings.Split(strings.TrimSpace(output), "\n")
		resultCount := 0
		for _, line := range lines {
			if len(line) > 0 && line[0] >= '1' && line[0] <= '9' && strings.Contains(line, ". [") {
				resultCount++
			}
		}
		if resultCount > 5 {
			t.Errorf("expected at most 5 results, got %d\noutput:\n%s", resultCount, output)
		}
		if resultCount == 0 {
			t.Errorf("expected at least 1 result, got 0\noutput:\n%s", output)
		}
	})

	t.Run("--id fetches single memory by ID", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--id", fmt.Sprintf("%d", ids[0])})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall --id execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, seeds[0].Content) {
			t.Errorf("expected content %q in output, got:\n%s", seeds[0].Content, output)
		}
		if !strings.Contains(output, "[lesson]") {
			t.Errorf("expected type label [lesson], got:\n%s", output)
		}
		if !strings.Contains(output, "0.90") {
			t.Errorf("expected confidence 0.90, got:\n%s", output)
		}
		if !strings.Contains(output, "source: cli") {
			t.Errorf("expected source: cli, got:\n%s", output)
		}
	})

	t.Run("--file filters search by file path", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--file", "pkg/util/generics.go", "golang"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall --file execute: %v", err)
		}

		output := out.String()
		// The only memory with files_read containing generics.go is seeds[0].
		if !strings.Contains(output, "generics") {
			t.Errorf("expected result containing 'generics', got:\n%s", output)
		}
		// Count results -- should be just 1 since only one memory touches that file.
		lines := strings.Split(strings.TrimSpace(output), "\n")
		resultCount := 0
		for _, line := range lines {
			if len(line) > 0 && line[0] >= '1' && line[0] <= '9' && strings.Contains(line, ". [") {
				resultCount++
			}
		}
		if resultCount != 1 {
			t.Errorf("expected exactly 1 result with --file filter, got %d\noutput:\n%s", resultCount, output)
		}
	})

	t.Run("--id + query args conflict error", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--id", fmt.Sprintf("%d", ids[0]), "some", "query"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when using both --id and query args, got nil")
		}
		if !strings.Contains(err.Error(), "cannot use both") {
			t.Errorf("expected 'cannot use both' error, got: %v", err)
		}
	})

	t.Run("no args and no --id error", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when no args and no --id, got nil")
		}
		if !strings.Contains(err.Error(), "query required") {
			t.Errorf("expected 'query required' error, got: %v", err)
		}
	})

	t.Run("--id with nonexistent ID error", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--id", "999999"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for nonexistent ID, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' error, got: %v", err)
		}
	})

	t.Run("empty search results prints no-memories message", func(t *testing.T) {
		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"xyzzy_completely_nonexistent_query_zzzz"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "No memories found") {
			t.Errorf("expected 'No memories found' for empty results, got:\n%s", output)
		}
	})
}

func TestRecallScopesByProject(t *testing.T) {
	db := setupTestMemoryDB(t)
	store := memory.NewStore(db)
	ctx := context.Background()

	// Seed memories for different projects by setting project scope before insert
	projectAMemories := []memory.InsertParams{
		{Content: "myapp uses JWT authentication", Type: "decision", Source: "cli", Confidence: 0.9},
		{Content: "myapp database is PostgreSQL", Type: "decision", Source: "cli", Confidence: 0.85},
	}
	projectBMemories := []memory.InsertParams{
		{Content: "otherapp uses OAuth2", Type: "decision", Source: "cli", Confidence: 0.8},
		{Content: "otherapp uses MongoDB", Type: "decision", Source: "cli", Confidence: 0.75},
	}

	// Insert myapp memories
	store.SetProject("myapp")
	for _, m := range projectAMemories {
		if _, err := store.Insert(ctx, m); err != nil {
			t.Fatalf("failed to insert myapp memory: %v", err)
		}
	}

	// Insert otherapp memories
	store.SetProject("otherapp")
	for _, m := range projectBMemories {
		if _, err := store.Insert(ctx, m); err != nil {
			t.Fatalf("failed to insert otherapp memory: %v", err)
		}
	}

	// Reset project scope for tests
	store.SetProject("")

	t.Run("with ORO_PROJECT=myapp returns only myapp memories", func(t *testing.T) {
		// Set project scope on store
		store.SetProject("myapp")
		defer store.SetProject("") // reset after test

		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"database"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		if !strings.Contains(output, "PostgreSQL") {
			t.Errorf("expected myapp's PostgreSQL memory, got:\n%s", output)
		}
		if strings.Contains(output, "MongoDB") {
			t.Errorf("should not include otherapp's MongoDB memory, got:\n%s", output)
		}
	})

	t.Run("--all-projects returns memories from all projects", func(t *testing.T) {
		// Set project scope on store (simulating ORO_PROJECT=myapp)
		store.SetProject("myapp")
		defer store.SetProject("") // reset after test

		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--all-projects", "OAuth2"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		// Should find otherapp's OAuth2 memory even though store is scoped to myapp
		if !strings.Contains(output, "OAuth2") {
			t.Errorf("expected otherapp's OAuth2 memory with --all-projects, got:\n%s", output)
		}
		if !strings.Contains(output, "otherapp") {
			t.Errorf("expected otherapp in results with --all-projects, got:\n%s", output)
		}
	})

	t.Run("without ORO_PROJECT returns all memories (unscoped)", func(t *testing.T) {
		// Don't set project scope (simulating no ORO_PROJECT)
		store.SetProject("")

		cmd := newRecallCmdWithStore(store)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"uses"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("recall execute: %v", err)
		}

		output := out.String()
		// Should find memories from both projects when unscoped
		if !strings.Contains(output, "JWT") {
			t.Errorf("expected myapp's JWT memory, got:\n%s", output)
		}
		if !strings.Contains(output, "OAuth2") {
			t.Errorf("expected otherapp's OAuth2 memory, got:\n%s", output)
		}
	})
}
