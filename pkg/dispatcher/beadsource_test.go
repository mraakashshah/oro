package dispatcher //nolint:testpackage // white-box tests for CLIBeadSource

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// --- Mock CommandRunner ---

// mockCommandRunner records calls and returns pre-configured output or errors.
type mockCommandRunner struct {
	calls  []mockCall
	output []byte
	err    error
	// callFn, if set, overrides output/err based on the call.
	callFn func(ctx context.Context, name string, args ...string) ([]byte, error)
}

type mockCall struct {
	Name string
	Args []string
}

func (m *mockCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	if m.callFn != nil {
		return m.callFn(ctx, name, args...)
	}
	return m.output, m.err
}

// --- Tests ---

func TestCLIBeadSource_Ready_ParsesJSON(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "abc.1", Title: "Implement widget", Priority: 1},
		{ID: "def.2", Title: "Fix bug", Priority: 2},
	}
	data, err := json.Marshal(beads)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	got, err := src.Ready(context.Background())
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(got))
	}
	if got[0].ID != "abc.1" {
		t.Errorf("bead[0].ID: got %q, want %q", got[0].ID, "abc.1")
	}
	if got[1].Title != "Fix bug" {
		t.Errorf("bead[1].Title: got %q, want %q", got[1].Title, "Fix bug")
	}

	// Verify the correct command was called.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "bd" {
		t.Errorf("command name: got %q, want %q", call.Name, "bd")
	}
	// Should include "ready" and "--json" in args.
	if !sliceContains(call.Args, "ready") {
		t.Errorf("expected 'ready' in args, got %v", call.Args)
	}
	if !sliceContains(call.Args, "--json") {
		t.Errorf("expected '--json' in args, got %v", call.Args)
	}
}

func TestCLIBeadSource_Ready_ParsesModelField(t *testing.T) {
	beads := []protocol.Bead{
		{ID: "abc.1", Title: "Opus task", Priority: 1, Model: "claude-opus-4-6"},
		{ID: "def.2", Title: "Sonnet task", Priority: 2, Model: "claude-sonnet-4-5-20250929"},
		{ID: "ghi.3", Title: "Default task", Priority: 3}, // no model
	}
	data, err := json.Marshal(beads)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	got, err := src.Ready(context.Background())
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if got[0].Model != "claude-opus-4-6" {
		t.Errorf("bead[0].Model: got %q, want %q", got[0].Model, "claude-opus-4-6")
	}
	if got[1].Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("bead[1].Model: got %q, want %q", got[1].Model, "claude-sonnet-4-5-20250929")
	}
	if got[2].Model != "" {
		t.Errorf("bead[2].Model: got %q, want empty", got[2].Model)
	}
}

func TestCLIBeadSource_Show_ParsesModelField(t *testing.T) {
	detail := protocol.BeadDetail{
		ID:                 "abc.1",
		Title:              "Sonnet task",
		AcceptanceCriteria: "Widget renders",
		Model:              "claude-sonnet-4-5-20250929",
	}
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	got, err := src.Show(context.Background(), "abc.1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model: got %q, want %q", got.Model, "claude-sonnet-4-5-20250929")
	}
}

func TestCLIBeadSource_Ready_EmptyList(t *testing.T) {
	runner := &mockCommandRunner{output: []byte("[]")}
	src := NewCLIBeadSource(runner)

	got, err := src.Ready(context.Background())
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 beads, got %d", len(got))
	}
}

func TestCLIBeadSource_Ready_CommandError(t *testing.T) {
	runner := &mockCommandRunner{err: fmt.Errorf("bd not found")}
	src := NewCLIBeadSource(runner)

	_, err := src.Ready(context.Background())
	if err == nil {
		t.Fatal("expected error from Ready when command fails")
	}
}

func TestCLIBeadSource_Ready_InvalidJSON(t *testing.T) {
	runner := &mockCommandRunner{output: []byte("not json")}
	src := NewCLIBeadSource(runner)

	_, err := src.Ready(context.Background())
	if err == nil {
		t.Fatal("expected error from Ready when output is invalid JSON")
	}
}

func TestCLIBeadSource_Show_ParsesJSON(t *testing.T) {
	detail := protocol.BeadDetail{
		ID:                 "abc.1",
		Title:              "Implement widget",
		AcceptanceCriteria: "Widget renders correctly",
	}
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	got, err := src.Show(context.Background(), "abc.1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "abc.1" {
		t.Errorf("ID: got %q, want %q", got.ID, "abc.1")
	}
	if got.Title != "Implement widget" {
		t.Errorf("Title: got %q, want %q", got.Title, "Implement widget")
	}
	if got.AcceptanceCriteria != "Widget renders correctly" {
		t.Errorf("AcceptanceCriteria: got %q, want %q", got.AcceptanceCriteria, "Widget renders correctly")
	}

	// Verify correct command.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "bd" {
		t.Errorf("command name: got %q, want %q", call.Name, "bd")
	}
	if !sliceContains(call.Args, "show") {
		t.Errorf("expected 'show' in args, got %v", call.Args)
	}
	if !sliceContains(call.Args, "abc.1") {
		t.Errorf("expected 'abc.1' in args, got %v", call.Args)
	}
	if !sliceContains(call.Args, "--json") {
		t.Errorf("expected '--json' in args, got %v", call.Args)
	}
}

func TestCLIBeadSource_Show_CommandError(t *testing.T) {
	runner := &mockCommandRunner{err: fmt.Errorf("bead not found")}
	src := NewCLIBeadSource(runner)

	_, err := src.Show(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error from Show when command fails")
	}
}

func TestCLIBeadSource_Show_ArrayJSON(t *testing.T) {
	// bd show --json returns an array: [{...}]
	detail := protocol.BeadDetail{
		ID:                 "abc.1",
		Title:              "Implement widget",
		AcceptanceCriteria: "Widget renders correctly",
	}
	data, err := json.Marshal([]protocol.BeadDetail{detail})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	got, err := src.Show(context.Background(), "abc.1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "abc.1" {
		t.Errorf("ID: got %q, want %q", got.ID, "abc.1")
	}
	if got.Title != "Implement widget" {
		t.Errorf("Title: got %q, want %q", got.Title, "Implement widget")
	}
}

func TestCLIBeadSource_Show_EmptyArray(t *testing.T) {
	data, _ := json.Marshal([]protocol.BeadDetail{})
	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	_, err := src.Show(context.Background(), "abc.1")
	if err == nil {
		t.Fatal("expected error from Show when array is empty")
	}
}

func TestCLIBeadSource_Show_InvalidJSON(t *testing.T) {
	runner := &mockCommandRunner{output: []byte("not json")}
	src := NewCLIBeadSource(runner)

	_, err := src.Show(context.Background(), "abc.1")
	if err == nil {
		t.Fatal("expected error from Show when output is invalid JSON")
	}
}

func TestCLIBeadSource_Close_Success(t *testing.T) {
	runner := &mockCommandRunner{output: []byte("")}
	src := NewCLIBeadSource(runner)

	err := src.Close(context.Background(), "abc.1", "Completed successfully")
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify correct command.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "bd" {
		t.Errorf("command name: got %q, want %q", call.Name, "bd")
	}
	if !sliceContains(call.Args, "close") {
		t.Errorf("expected 'close' in args, got %v", call.Args)
	}
	if !sliceContains(call.Args, "abc.1") {
		t.Errorf("expected 'abc.1' in args, got %v", call.Args)
	}
	// Should include --reason flag with the reason.
	foundReason := false
	for _, arg := range call.Args {
		if arg == `--reason=Completed successfully` {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Errorf("expected '--reason=Completed successfully' in args, got %v", call.Args)
	}
}

func TestCLIBeadSource_Close_CommandError(t *testing.T) {
	runner := &mockCommandRunner{err: fmt.Errorf("close failed")}
	src := NewCLIBeadSource(runner)

	err := src.Close(context.Background(), "abc.1", "Done")
	if err == nil {
		t.Fatal("expected error from Close when command fails")
	}
}

func TestCLIBeadSource_Sync_Success(t *testing.T) {
	runner := &mockCommandRunner{output: []byte("")}
	src := NewCLIBeadSource(runner)

	err := src.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "bd" {
		t.Errorf("command name: got %q, want %q", call.Name, "bd")
	}
	if !sliceContains(call.Args, "sync") {
		t.Errorf("expected 'sync' in args, got %v", call.Args)
	}
	if !sliceContains(call.Args, "--flush-only") {
		t.Errorf("expected '--flush-only' in args, got %v", call.Args)
	}
}

func TestCLIBeadSource_Sync_CommandError(t *testing.T) {
	runner := &mockCommandRunner{err: fmt.Errorf("sync failed")}
	src := NewCLIBeadSource(runner)

	err := src.Sync(context.Background())
	if err == nil {
		t.Fatal("expected error from Sync when command fails")
	}
}

func TestBead_ResolveModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"empty defaults to sonnet", "", protocol.DefaultModel},
		{"explicit sonnet", protocol.ModelSonnet, protocol.ModelSonnet},
		{"explicit opus", protocol.ModelOpus, protocol.ModelOpus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := protocol.Bead{ID: "test", Model: tt.model}
			if got := b.ResolveModel(); got != tt.want {
				t.Errorf("ResolveModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBead_ResolveModel_ByEstimate(t *testing.T) {
	tests := []struct {
		name     string
		estimate int
		model    string
		want     string
	}{
		// Explicit model always wins, regardless of estimate.
		{"explicit model overrides estimate", 3, protocol.ModelOpus, protocol.ModelOpus},
		{"explicit sonnet overrides short estimate", 2, protocol.ModelSonnet, protocol.ModelSonnet},

		// Estimate-based routing when Model is empty.
		{"3min routes to haiku", 3, "", protocol.ModelHaiku},
		{"5min routes to haiku", 5, "", protocol.ModelHaiku},
		{"6min routes to sonnet", 6, "", protocol.ModelSonnet},
		{"0min (unset) routes to sonnet", 0, "", protocol.ModelSonnet},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := protocol.Bead{ID: "test", EstimatedMinutes: tt.estimate, Model: tt.model}
			if got := b.ResolveModel(); got != tt.want {
				t.Errorf("ResolveModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelConstants(t *testing.T) {
	if protocol.ModelOpus != "claude-opus-4-6" {
		t.Errorf("protocol.ModelOpus = %q, want %q", protocol.ModelOpus, "claude-opus-4-6")
	}
	if protocol.ModelSonnet != "claude-sonnet-4-5-20250929" {
		t.Errorf("protocol.ModelSonnet = %q, want %q", protocol.ModelSonnet, "claude-sonnet-4-5-20250929")
	}
	if protocol.ModelHaiku != "claude-haiku-4-5-20251001" {
		t.Errorf("protocol.ModelHaiku = %q, want %q", protocol.ModelHaiku, "claude-haiku-4-5-20251001")
	}
	if protocol.DefaultModel != protocol.ModelSonnet {
		t.Errorf("protocol.DefaultModel = %q, want %q (same as protocol.ModelSonnet)", protocol.DefaultModel, protocol.ModelSonnet)
	}
}

func TestBead_TypeField_JSON(t *testing.T) {
	// Verify JSON round-trip with the Type field.
	b := protocol.Bead{ID: "test-1", Title: "Fix login", Priority: 1, Type: "bug"}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.Bead
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "bug" {
		t.Errorf("Type: got %q, want %q", got.Type, "bug")
	}

	// Verify omitempty: empty type should not appear in JSON.
	b2 := protocol.Bead{ID: "test-2", Title: "No type"}
	data2, _ := json.Marshal(b2)
	if strings.Contains(string(data2), "issue_type") {
		t.Errorf("expected issue_type to be omitted for empty Type, got: %s", data2)
	}
}

func TestCLIBeadSource_Create(t *testing.T) {
	t.Run("with_parent", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-abc"}`)}
		src := NewCLIBeadSource(runner)

		id, err := src.Create(context.Background(), "Fix login bug", "bug", 1, "Login fails on retry", "oro-parent", "")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-abc" {
			t.Errorf("ID: got %q, want %q", id, "oro-abc")
		}

		// Verify correct command.
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(runner.calls))
		}
		call := runner.calls[0]
		if call.Name != "bd" {
			t.Errorf("command name: got %q, want %q", call.Name, "bd")
		}
		if !sliceContains(call.Args, "create") {
			t.Errorf("expected 'create' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--title=Fix login bug") {
			t.Errorf("expected '--title=Fix login bug' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--type=bug") {
			t.Errorf("expected '--type=bug' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--priority=0") {
			t.Errorf("bugs must be P0: expected '--priority=0' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--description=Login fails on retry") {
			t.Errorf("expected '--description=Login fails on retry' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--parent=oro-parent") {
			t.Errorf("expected '--parent=oro-parent' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--json") {
			t.Errorf("expected '--json' in args, got %v", call.Args)
		}
	})

	t.Run("without_parent", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-xyz"}`)}
		src := NewCLIBeadSource(runner)

		id, err := src.Create(context.Background(), "Add feature", "task", 2, "New feature desc", "", "")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-xyz" {
			t.Errorf("ID: got %q, want %q", id, "oro-xyz")
		}

		// Verify --parent is NOT in args when parent is empty.
		call := runner.calls[0]
		for _, arg := range call.Args {
			if strings.HasPrefix(arg, "--parent=") {
				t.Errorf("expected no --parent arg when parent is empty, got %v", call.Args)
			}
		}
	})

	t.Run("command_error", func(t *testing.T) {
		runner := &mockCommandRunner{err: fmt.Errorf("bd create failed")}
		src := NewCLIBeadSource(runner)

		_, err := src.Create(context.Background(), "Title", "task", 1, "Desc", "", "")
		if err == nil {
			t.Fatal("expected error from Create when command fails")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte("not json")}
		src := NewCLIBeadSource(runner)

		_, err := src.Create(context.Background(), "Title", "task", 1, "Desc", "", "")
		if err == nil {
			t.Fatal("expected error from Create when output is invalid JSON")
		}
	})

	t.Run("with_acceptance_criteria", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-test"}`)}
		src := NewCLIBeadSource(runner)

		ac := "- [ ] Test passes\n- [ ] Code compiles"
		id, err := src.Create(context.Background(), "Test task", "task", 2, "Test description", "", ac)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-test" {
			t.Errorf("ID: got %q, want %q", id, "oro-test")
		}

		// Verify --acceptance-criteria flag is present.
		call := runner.calls[0]
		if !sliceContains(call.Args, "--acceptance-criteria="+ac) {
			t.Errorf("expected '--acceptance-criteria=%s' in args, got %v", ac, call.Args)
		}
	})

	t.Run("empty_acceptance_criteria_omitted", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-test2"}`)}
		src := NewCLIBeadSource(runner)

		id, err := src.Create(context.Background(), "Test task", "task", 2, "Test description", "", "")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-test2" {
			t.Errorf("ID: got %q, want %q", id, "oro-test2")
		}

		// Verify --acceptance-criteria flag is NOT in args when empty.
		call := runner.calls[0]
		for _, arg := range call.Args {
			if strings.HasPrefix(arg, "--acceptance-criteria=") {
				t.Errorf("expected no --acceptance-criteria arg when AC is empty, got %v", call.Args)
			}
		}
	})
}

func TestExtractACFromDescription(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "extracts AC section",
			desc: "Some description.\n\n## Acceptance Criteria\n- [ ] First criterion\n- [ ] Second criterion",
			want: "- [ ] First criterion\n- [ ] Second criterion",
		},
		{
			name: "stops at next H2 header",
			desc: "Description.\n\n## Acceptance Criteria\n- [ ] Do the thing\n\n## Fix\nSome fix details.",
			want: "- [ ] Do the thing",
		},
		{
			name: "no AC section returns empty",
			desc: "Just a plain description with no acceptance criteria.",
			want: "",
		},
		{
			name: "empty description returns empty",
			desc: "",
			want: "",
		},
		{
			name: "AC section with content before it",
			desc: "## Fix\nDo X.\n\n## Acceptance Criteria\n- [ ] Widget renders\n- [ ] Tests pass",
			want: "- [ ] Widget renders\n- [ ] Tests pass",
		},
		{
			name: "extracts lowercase 'acceptance criteria'",
			desc: "## Context\nSome context.\n\n## Acceptance criteria\n- [ ] Works case-insensitively\n- [ ] Tests pass",
			want: "- [ ] Works case-insensitively\n- [ ] Tests pass",
		},
		{
			name: "extracts uppercase ACCEPTANCE CRITERIA without ##",
			desc: "## Description\nSome description.\n\nACCEPTANCE CRITERIA\n- [ ] Uppercase works\n- [ ] No hash marks needed",
			want: "- [ ] Uppercase works\n- [ ] No hash marks needed",
		},
		{
			name: "extracts mixed case Acceptance Criteria",
			desc: "## Context\nContext here.\n\n## acceptance Criteria\n- [ ] Mixed case works",
			want: "- [ ] Mixed case works",
		},
		{
			name: "extracts ACCEPTANCE CRITERIA with ##",
			desc: "## ACCEPTANCE CRITERIA\n- [ ] All caps with hashes\n- [ ] Should work",
			want: "- [ ] All caps with hashes\n- [ ] Should work",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractACFromDescription(tt.desc)
			if got != tt.want {
				t.Errorf("extractACFromDescription():\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestCLIBeadSource_Show_ExtractsACFromDescription(t *testing.T) {
	// Simulate real bd show --json output: no acceptance_criteria field,
	// AC embedded in description markdown.
	raw := `[{"id":"oro-k9lk","title":"Fix AC parsing","description":"Some context.\n\n## Acceptance Criteria\n- [ ] Parser works\n- [ ] Tests pass","status":"open","priority":0}]`

	runner := &mockCommandRunner{output: []byte(raw)}
	src := NewCLIBeadSource(runner)

	got, err := src.Show(context.Background(), "oro-k9lk")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.AcceptanceCriteria == "" {
		t.Fatal("expected AcceptanceCriteria to be extracted from description, got empty")
	}
	if !strings.Contains(got.AcceptanceCriteria, "Parser works") {
		t.Errorf("AcceptanceCriteria missing expected content, got: %q", got.AcceptanceCriteria)
	}
}

func TestCLIBeadSource_Show_NoACInDescription(t *testing.T) {
	raw := `[{"id":"oro-abc","title":"No AC bead","description":"Just a plain description.","status":"open","priority":2}]`

	runner := &mockCommandRunner{output: []byte(raw)}
	src := NewCLIBeadSource(runner)

	got, err := src.Show(context.Background(), "oro-abc")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.AcceptanceCriteria != "" {
		t.Errorf("expected empty AcceptanceCriteria for bead without AC section, got: %q", got.AcceptanceCriteria)
	}
}

func TestCLIBeadSource_Show_ExplicitACFieldTakesPrecedence(t *testing.T) {
	// If the JSON already has acceptance_criteria populated, don't override it.
	detail := protocol.BeadDetail{
		ID:                 "abc.1",
		Title:              "Has explicit AC",
		Description:        "Desc.\n\n## Acceptance Criteria\n- [ ] From description",
		AcceptanceCriteria: "Explicit AC value",
	}
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	runner := &mockCommandRunner{output: data}
	src := NewCLIBeadSource(runner)

	got, err := src.Show(context.Background(), "abc.1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.AcceptanceCriteria != "Explicit AC value" {
		t.Errorf("expected explicit AC to take precedence, got: %q", got.AcceptanceCriteria)
	}
}

func TestCLIBeadSource_ImplementsBeadSource(t *testing.T) {
	// Compile-time check that CLIBeadSource implements BeadSource.
	var _ BeadSource = (*CLIBeadSource)(nil)
}

func TestCLIBeadSource_AllChildrenClosed(t *testing.T) {
	t.Run("returns true when no open children", func(t *testing.T) {
		// bd list --parent=epic-123 --status=open --json returns []
		runner := &mockCommandRunner{output: []byte("[]")}
		src := NewCLIBeadSource(runner)

		got, err := src.AllChildrenClosed(context.Background(), "epic-123")
		if err != nil {
			t.Fatalf("AllChildrenClosed: %v", err)
		}
		if !got {
			t.Errorf("AllChildrenClosed: got false, want true (no open children)")
		}

		// Verify correct command.
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(runner.calls))
		}
		call := runner.calls[0]
		if call.Name != "bd" {
			t.Errorf("command name: got %q, want %q", call.Name, "bd")
		}
		if !sliceContains(call.Args, "list") {
			t.Errorf("expected 'list' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--parent=epic-123") {
			t.Errorf("expected '--parent=epic-123' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--status=open") {
			t.Errorf("expected '--status=open' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--json") {
			t.Errorf("expected '--json' in args, got %v", call.Args)
		}
	})

	t.Run("returns false when open children exist", func(t *testing.T) {
		// bd list returns a non-empty list of open children
		openChildren := []protocol.Bead{
			{ID: "child-1", Title: "Child task 1", Priority: 2},
			{ID: "child-2", Title: "Child task 2", Priority: 2},
		}
		data, _ := json.Marshal(openChildren)
		runner := &mockCommandRunner{output: data}
		src := NewCLIBeadSource(runner)

		got, err := src.AllChildrenClosed(context.Background(), "epic-456")
		if err != nil {
			t.Fatalf("AllChildrenClosed: %v", err)
		}
		if got {
			t.Errorf("AllChildrenClosed: got true, want false (open children exist)")
		}
	})

	t.Run("returns error on command failure", func(t *testing.T) {
		runner := &mockCommandRunner{err: fmt.Errorf("bd list failed")}
		src := NewCLIBeadSource(runner)

		_, err := src.AllChildrenClosed(context.Background(), "epic-789")
		if err == nil {
			t.Fatal("expected error from AllChildrenClosed when command fails")
		}
	})

	t.Run("returns error on invalid JSON", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte("not json")}
		src := NewCLIBeadSource(runner)

		_, err := src.AllChildrenClosed(context.Background(), "epic-999")
		if err == nil {
			t.Fatal("expected error from AllChildrenClosed when output is invalid JSON")
		}
	})
}

func TestCLIBeadSource_CreateWithAcceptanceCriteria(t *testing.T) {
	t.Run("adds_ac_flag_when_non_empty", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-test"}`)}
		src := NewCLIBeadSource(runner)

		id, err := src.Create(context.Background(), "Fix bug", "bug", 1, "Bug description", "", "Test passes and verified")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-test" {
			t.Errorf("ID: got %q, want %q", id, "oro-test")
		}

		// Verify --acceptance-criteria flag is present with the AC value.
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(runner.calls))
		}
		call := runner.calls[0]
		if !sliceContains(call.Args, "--acceptance-criteria=Test passes and verified") {
			t.Errorf("expected '--acceptance-criteria=Test passes and verified' in args, got %v", call.Args)
		}
	})

	t.Run("omits_ac_flag_when_empty", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-test2"}`)}
		src := NewCLIBeadSource(runner)

		id, err := src.Create(context.Background(), "Fix bug", "bug", 1, "Bug description", "", "")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-test2" {
			t.Errorf("ID: got %q, want %q", id, "oro-test2")
		}

		// Verify --acceptance-criteria flag is NOT present when empty.
		call := runner.calls[0]
		for _, arg := range call.Args {
			if strings.HasPrefix(arg, "--acceptance-criteria=") {
				t.Errorf("expected no --acceptance-criteria arg when AC is empty, got %v", call.Args)
			}
		}
	})

	t.Run("includes_ac_with_parent", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte(`{"id":"oro-test3"}`)}
		src := NewCLIBeadSource(runner)

		id, err := src.Create(context.Background(), "Subtask", "task", 2, "Task desc", "oro-parent", "Subtask completed")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != "oro-test3" {
			t.Errorf("ID: got %q, want %q", id, "oro-test3")
		}

		// Verify both --parent and --acceptance-criteria are present.
		call := runner.calls[0]
		if !sliceContains(call.Args, "--parent=oro-parent") {
			t.Errorf("expected '--parent=oro-parent' in args, got %v", call.Args)
		}
		if !sliceContains(call.Args, "--acceptance-criteria=Subtask completed") {
			t.Errorf("expected '--acceptance-criteria=Subtask completed' in args, got %v", call.Args)
		}
	})
}

// sliceContains checks if a string slice contains a given string.
func sliceContains(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
