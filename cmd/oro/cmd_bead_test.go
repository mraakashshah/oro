package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newBeadCmdWithStore(store beadstore.Store) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "bead",
		Short: "Manage native Oro beads (legacy alias for task)",
		Long:  "Manage native Oro beads. Legacy alias for the task command.",
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON output")

	cmd.AddCommand(
		newBeadReadyCmd(store),
		newBeadListCmd(store),
		newBeadShowCmd(store),
		newBeadCreateCmd(store),
		newBeadUpdateCmd(store),
		newBeadCloseCmd(store),
		newBeadDeleteCmd(store),
		newBeadReopenCmd(store),
		newBeadDeferCmd(store),
		newBeadUndeferCmd(store),
		newBeadBlockedCmd(store),
		newBeadClosedCmd(store),
		newBeadDepCmd(store),
		newTestBeadTagCmd(store),
		newTestBeadMetaCmd(store),
		newTestBeadNoteCmd(store),
		newTestBeadStubCmd(store, "search <query>", "Search beads", cobra.ExactArgs(1)),
		newBeadExportCmd(store),
		newTestBeadStubCmd(store, "import <path>", "Import bead snapshot", cobra.ExactArgs(1)),
		newTestBeadStubCmd(store, "doctor", "Check bead-store health", cobra.NoArgs),
		newBeadStatusCmd(store),
	)

	return cmd
}

func newTestBeadTagCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{Use: "tag", Short: "Manage bead tags"}
	cmd.AddCommand(
		newTestBeadStubCmd(store, "add <bead-id> <tag>...", "Add tags to a bead", cobra.MinimumNArgs(2)),
		newTestBeadStubCmd(store, "rm <bead-id> <tag>...", "Remove tags from a bead", cobra.MinimumNArgs(2)),
	)
	return cmd
}

func newTestBeadMetaCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{Use: "meta", Short: "Manage bead metadata"}
	cmd.AddCommand(
		newTestBeadStubCmd(store, "set <bead-id> <key=value>", "Set bead metadata", cobra.ExactArgs(2)),
		newTestBeadStubCmd(store, "get <bead-id> <key>", "Get bead metadata", cobra.ExactArgs(2)),
		newTestBeadStubCmd(store, "rm <bead-id> <key>", "Remove bead metadata", cobra.ExactArgs(2)),
	)
	return cmd
}

func newTestBeadNoteCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{Use: "note", Short: "Manage bead notes"}
	cmd.AddCommand(
		newBeadNoteAddCmd(store),
		newTestBeadStubCmd(store, "list <bead-id>", "List bead notes", cobra.ExactArgs(1)),
	)
	return cmd
}

func newTestBeadStubCmd(store beadstore.Store, use, short string, args cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = store
			err := fmt.Errorf("%s is not implemented yet", cmd.CommandPath())
			return writeBeadCommandErrorIfJSON(cmd, "unsupported", err)
		},
	}
}

func TestRootCommandOmitsBead(t *testing.T) {
	root := newRootCmd()

	for _, cmd := range root.Commands() {
		if cmd.Name() == "bead" {
			t.Fatal("root command registered retired bead subcommand")
		}
	}
}

func TestBeadCommandHelpExposesSubcommands(t *testing.T) {
	cmd := newBeadCmdWithStore(nil)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected help error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"blocked",
		"closed",
		"create",
		"close",
		"defer",
		"dep",
		"doctor",
		"export",
		"import",
		"list",
		"meta",
		"note",
		"ready",
		"reopen",
		"search",
		"show",
		"status",
		"tag",
		"undefer",
		"update",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bead help missing %q:\n%s", want, out)
		}
	}
}

func TestBeadDepCommandHelpExposesSubcommands(t *testing.T) {
	cmd := newBeadCmdWithStore(nil)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"dep", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected dep help error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"add", "cycles", "rm", "list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bead dep help missing %q:\n%s", want, out)
		}
	}
}

type nilCreateBeadStore struct {
	*beadstore.FakeStore
}

func (s nilCreateBeadStore) Create(context.Context, beadstore.CreateParams) (*protocol.Bead, error) {
	return nil, nil
}

func TestCreateBeadFromParamsRejectsNilCreatedBead(t *testing.T) {
	_, err := createBeadFromParams(context.Background(), nilCreateBeadStore{FakeStore: beadstore.NewFakeStore()}, beadstore.CreateParams{
		Title: "nil create",
		Type:  "task",
	})
	if err == nil {
		t.Fatal("createBeadFromParams error = nil, want error")
	}
	if !strings.Contains(err.Error(), "nil bead") {
		t.Fatalf("createBeadFromParams error = %v, want nil bead detail", err)
	}
}

func TestBeadShowJSONEmitsOroNativeSchema(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:                 "oro-json1",
		Title:              "JSON output",
		Status:             "open",
		Priority:           1,
		Epic:               "oro-parent",
		Type:               "task",
		AcceptanceCriteria: "jq parses",
	})
	cmd := newBeadCmdWithStore(store)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"show", "oro-json1", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead show --json error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("bead show --json emitted invalid JSON: %v\n%s", err, buf.String())
	}
	if got["id"] != "oro-json1" {
		t.Fatalf("id = %#v, want oro-json1 in:\n%s", got["id"], buf.String())
	}
	if got["parent_id"] != "oro-parent" {
		t.Fatalf("parent_id = %#v, want oro-parent in:\n%s", got["parent_id"], buf.String())
	}
	if got["type"] != "task" {
		t.Fatalf("type = %#v, want task in:\n%s", got["type"], buf.String())
	}
	if _, ok := got["parent"]; ok {
		t.Fatalf("legacy parent key present in oro-native JSON:\n%s", buf.String())
	}
	if _, ok := got["issue_type"]; ok {
		t.Fatalf("legacy issue_type key present in oro-native JSON:\n%s", buf.String())
	}
}

func TestBeadShowJSONMissingBeadEmitsErrorObject(t *testing.T) {
	store := beadstore.NewFakeStore()
	out := executeBeadCommand(t, store, "show", "oro-missing", "--json")

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bead show missing --json emitted invalid JSON: %v\n%s", err, out)
	}
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false in:\n%s", got["ok"], out)
	}
	if got["error"] != "show" {
		t.Fatalf("error = %#v, want show in:\n%s", got["error"], out)
	}
}

func TestBeadReadyJSONEmitsOroNativeArray(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-ready1", Title: "Ready", Status: "open", Priority: 1, Type: "task", Epic: "oro-parent"},
		protocol.Bead{ID: "oro-blocker", Title: "Blocker", Status: "blocked", Priority: 0, Type: "task"},
	)
	out := executeBeadCommand(t, store, "ready", "--json")

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bead ready --json emitted invalid JSON: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("ready count = %d, want 1 in:\n%s", len(got), out)
	}
	if got[0]["id"] != "oro-ready1" {
		t.Fatalf("id = %#v, want oro-ready1 in:\n%s", got[0]["id"], out)
	}
	if got[0]["parent_id"] != "oro-parent" {
		t.Fatalf("parent_id = %#v, want oro-parent in:\n%s", got[0]["parent_id"], out)
	}
	if _, ok := got[0]["issue_type"]; ok {
		t.Fatalf("legacy issue_type key present in oro-native JSON:\n%s", out)
	}
}

func TestBeadListJSONFiltersOroNativeArray(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-list1", Title: "List", Status: "open", Priority: 1, Type: "task", Epic: "oro-parent", Tags: []string{"cli"}},
		protocol.Bead{ID: "oro-other", Title: "Other", Status: "open", Priority: 2, Type: "task", Epic: "oro-parent"},
		protocol.Bead{ID: "oro-closed", Title: "Closed", Status: "closed", Priority: 0, Type: "task", Epic: "oro-parent", Tags: []string{"cli"}, ClosedAt: "2026-04-28T00:00:00Z"},
	)
	out := executeBeadCommand(t, store, "list", "--parent", "oro-parent", "--tag", "cli", "--json")

	got := decodeBeadJSONArray(t, out)
	if len(got) != 2 {
		t.Fatalf("list count = %d, want 2 in:\n%s", len(got), out)
	}
	if !beadJSONArrayHasID(got, "oro-list1") || !beadJSONArrayHasID(got, "oro-closed") {
		t.Fatalf("list JSON = %#v, want open and closed matching beads in:\n%s", got, out)
	}
	for _, bead := range got {
		if bead["id"] == "oro-list1" && bead["parent_id"] != "oro-parent" {
			t.Fatalf("parent_id = %#v, want oro-parent in:\n%s", bead["parent_id"], out)
		}
		if _, ok := bead["issue_type"]; ok {
			t.Fatalf("legacy issue_type key present in oro-native JSON:\n%s", out)
		}
	}
}

func TestBeadBlockedAndClosedJSONEmitOroNativeArrays(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-blocked", Title: "Blocked", Status: "blocked", Priority: 1, Type: "task"},
		protocol.Bead{ID: "oro-closed", Title: "Closed", Status: "closed", Priority: 2, Type: "task", ClosedAt: "2026-04-28T00:00:00Z"},
	)

	blocked := decodeBeadJSONArray(t, executeBeadCommand(t, store, "blocked", "--json"))
	if len(blocked) != 1 || blocked[0]["id"] != "oro-blocked" {
		t.Fatalf("blocked JSON = %#v, want oro-blocked", blocked)
	}

	closed := decodeBeadJSONArray(t, executeBeadCommand(t, store, "closed", "--limit", "1", "--json"))
	if len(closed) != 1 || closed[0]["id"] != "oro-closed" {
		t.Fatalf("closed JSON = %#v, want oro-closed", closed)
	}
}

func TestBeadCreateJSONEmitsCreatedBead(t *testing.T) {
	store := beadstore.NewFakeStore()
	out := executeBeadCommand(
		t,
		store,
		"create",
		"--title", "Created from CLI",
		"--type", "task",
		"--priority", "1",
		"--parent", "oro-parent",
		"--acceptance-criteria", "jq parses",
		"--tag", "cli",
		"--json",
	)

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bead create --json emitted invalid JSON: %v\n%s", err, out)
	}
	if got["title"] != "Created from CLI" {
		t.Fatalf("title = %#v, want Created from CLI in:\n%s", got["title"], out)
	}
	if got["parent_id"] != "oro-parent" {
		t.Fatalf("parent_id = %#v, want oro-parent in:\n%s", got["parent_id"], out)
	}
	if got["type"] != "task" {
		t.Fatalf("type = %#v, want task in:\n%s", got["type"], out)
	}
}

func TestTaskCreateRejectsPremortemType(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cmd  func(beadstore.Store) *cobra.Command
		args []string
		id   string
	}{
		{
			name: "task",
			cmd:  newTaskCmdWithStore,
			args: []string{"create", "--id", "oro-task-premortem", "--title", "blocked", "--type", "premortem"},
			id:   "oro-task-premortem",
		},
		{
			name: "bead",
			cmd:  newBeadCmdWithStore,
			args: []string{"create", "--id", "oro-bead-premortem", "--title", "blocked", "--type", "PREMORTEM"},
			id:   "oro-bead-premortem",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("OpenSQLiteStore: %v", err)
			}
			cmd := tc.cmd(store)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)

			err = cmd.Execute()
			if err == nil {
				t.Fatalf("%s create premortem unexpectedly succeeded:\n%s", tc.name, out.String())
			}
			if !strings.Contains(strings.ToLower(err.Error()), "premortem") {
				t.Fatalf("%s create premortem error = %v, want premortem mentioned", tc.name, err)
			}
			bead, showErr := store.Show(ctx, tc.id)
			if showErr != nil {
				t.Fatalf("Show %s: %v", tc.id, showErr)
			}
			if bead != nil {
				t.Fatalf("%s created premortem row: %#v", tc.name, bead)
			}
		})
	}
}

func TestBeadCreateShowSQLiteRoundTripsParentWithoutDependency(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := beadstore.OpenSQLiteStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(
		t,
		store,
		"create",
		"--id", "oro-parent-rt",
		"--title", "parent",
		"--type", "epic",
		"--description", "parent description",
		"--acceptance-criteria", "parent ac",
	)
	id := strings.TrimSpace(executeBeadCommand(
		t,
		store,
		"create",
		"--title", "t",
		"--type", "task",
		"--description", "d",
		"--acceptance-criteria", "ac",
		"--parent", "oro-parent-rt",
		"--tag", "cli",
		"--tag", "roundtrip",
	))
	if id == "" {
		t.Fatal("bead create emitted empty id")
	}

	got := decodeBeadJSONObject(t, executeBeadCommand(t, store, "show", id, "--json"))
	if got["title"] != "t" || got["description"] != "d" || got["acceptance_criteria"] != "ac" {
		t.Fatalf("show JSON did not round-trip fields: %#v", got)
	}
	if got["parent_id"] != "oro-parent-rt" {
		t.Fatalf("parent_id = %#v, want oro-parent-rt", got["parent_id"])
	}
	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "cli" || tags[1] != "roundtrip" {
		t.Fatalf("tags = %#v, want [cli roundtrip]", got["tags"])
	}
	deps, ok := got["dependencies"].([]any)
	if !ok {
		t.Fatalf("dependencies = %#v, want empty array", got["dependencies"])
	}
	if len(deps) != 0 {
		t.Fatalf("dependencies count = %d, want 0: %#v", len(deps), deps)
	}

	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	var depRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_deps WHERE bead_id=?`, id).Scan(&depRows); err != nil {
		t.Fatalf("count bead_deps: %v", err)
	}
	if depRows != 0 {
		t.Fatalf("bead_deps rows for %s = %d, want 0", id, depRows)
	}
}

func TestCmdTaskCreateShowUpdateCloseRoundTripThroughBinary(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "oro")
	dbPath := filepath.Join(tmpDir, "state.db")
	oroHome := filepath.Join(tmpDir, "oro-home")
	root := repoRoot(t)
	cmdDir := filepath.Join(root, "cmd", "oro")

	stage := exec.Command("make", "stage-assets")
	stage.Dir = root
	stage.Env = os.Environ()
	if out, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("stage assets: %v\n%s", err, out)
	}

	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = cmdDir
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build oro binary: %v\n%s", err, out)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(binPath, args...)
		cmd.Env = append(os.Environ(),
			"ORO_HOME="+oroHome,
			"ORO_DB_PATH="+dbPath,
			"ORO_PROJECT=",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("oro %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	created := decodeBeadJSONObject(t, run(
		"task", "create",
		"--id", "oro-e2e1",
		"--title", "Binary task",
		"--type", "task",
		"--priority", "2",
		"--description", "created through the oro binary",
		"--acceptance-criteria", "create show update close",
		"--tag", "cli",
		"--json",
	))
	if created["id"] != "oro-e2e1" || created["status"] != "open" {
		t.Fatalf("created task = %#v, want open oro-e2e1", created)
	}

	shown := decodeBeadJSONObject(t, run("task", "show", "oro-e2e1", "--json"))
	if shown["title"] != "Binary task" || shown["acceptance_criteria"] != "create show update close" {
		t.Fatalf("shown task did not round-trip create fields: %#v", shown)
	}

	updated := decodeBeadJSONObject(t, run(
		"task", "update", "oro-e2e1",
		"--status", "in_progress",
		"--priority", "0",
		"--type", "bug",
		"--owner", "worker",
		"--acceptance", "updated acceptance",
		"--notes", "updated by e2e",
		"--json",
	))
	if updated["status"] != "in_progress" || updated["priority"] != float64(0) || updated["type"] != "bug" || updated["owner"] != "worker" {
		t.Fatalf("updated task did not round-trip update fields: %#v", updated)
	}
	if updated["acceptance_criteria"] != "updated acceptance" || updated["notes"] != "updated by e2e" {
		t.Fatalf("updated task did not round-trip text fields: %#v", updated)
	}

	closed := decodeBeadJSONObject(t, run("task", "close", "oro-e2e1", "--reason", "verified", "--json"))
	if closed["status"] != "closed" || closed["close_reason"] != "verified" {
		t.Fatalf("closed task did not round-trip close fields: %#v", closed)
	}
}

func TestBeadNoteAddAppendsNotes(t *testing.T) {
	store := beadstore.NewFakeStore()

	created := decodeBeadJSONObject(t, executeTaskCommand(t, store,
		"create",
		"--id", "oro-note1",
		"--title", "Note task",
		"--type", "task",
		"--json",
	))
	if created["id"] != "oro-note1" {
		t.Fatalf("created task = %#v, want oro-note1", created)
	}

	updated := decodeBeadJSONObject(t, executeTaskCommand(t, store,
		"update", "oro-note1",
		"--notes", "first note",
		"--json",
	))
	if updated["notes"] != "first note" {
		t.Fatalf("updated notes = %#v, want first note", updated["notes"])
	}

	executeTaskCommand(t, store, "note", "add", "oro-note1", "second note")

	shown := decodeBeadJSONObject(t, executeTaskCommand(t, store, "show", "oro-note1", "--json"))
	if shown["notes"] != "first note\n\nsecond note" {
		t.Fatalf("shown notes = %#v, want appended notes", shown["notes"])
	}
}

func TestBeadUpdateAndCloseJSONEmitMutatedBead(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:       "oro-mutate",
		Title:    "Mutate",
		Status:   "open",
		Priority: 2,
		Type:     "task",
	})

	updated := decodeBeadJSONObject(t, executeBeadCommand(t, store, "update", "oro-mutate", "--priority", "0", "--type", "bug", "--json"))
	if updated["id"] != "oro-mutate" || updated["priority"] != float64(0) || updated["type"] != "bug" {
		t.Fatalf("update JSON = %#v, want mutated bead", updated)
	}

	closed := decodeBeadJSONObject(t, executeBeadCommand(t, store, "close", "oro-mutate", "--reason", "done", "--json"))
	if closed["status"] != "closed" || closed["close_reason"] != "done" {
		t.Fatalf("close JSON = %#v, want closed bead with reason", closed)
	}
}

// TestBeadCloseRefusesWorkerSelfClose proves that an Oro worker subprocess
// (ORO_WORKER=1) cannot close its currently assigned bead via the CLI. The
// dispatcher remains the sole closer/integrator; the worker emits DONE and
// lets the dispatcher run the close path. See oro-t5ha.
func TestBeadCloseRefusesWorkerSelfClose(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:       "oro-spf1a",
		Title:    "Self-close fixture",
		Status:   "open",
		Priority: 1,
		Type:     "task",
	})

	t.Setenv("ORO_WORKER", "1")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-spf1a")

	cmd := newBeadCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"close", "oro-spf1a", "--reason", "self"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error refusing self-close, got nil; output=%s", out.String())
	}
	if !strings.Contains(err.Error(), "self-close") && !strings.Contains(err.Error(), "ORO_WORKER_BEAD_ID") {
		t.Fatalf("expected self-close refusal error, got %v", err)
	}

	bead, ferr := store.Show(context.Background(), "oro-spf1a")
	if ferr != nil {
		t.Fatalf("store.Show: %v", ferr)
	}
	if bead == nil {
		t.Fatalf("bead oro-spf1a missing from store")
	}
	if bead.Status != "open" {
		t.Fatalf("bead status = %q, want open (worker self-close must not mutate)", bead.Status)
	}
}

// TestBeadCloseSelfCloseGuardScopedToAssignedBead confirms the guard scopes
// to the worker's own assigned bead — it does not block closing unrelated
// beads (e.g. a worker auxiliary bead it created during work). See oro-t5ha.
func TestBeadCloseSelfCloseGuardScopedToAssignedBead(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:       "oro-other",
		Title:    "Aux",
		Status:   "open",
		Priority: 2,
		Type:     "task",
	})

	t.Setenv("ORO_WORKER", "1")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-spf1a")

	closed := decodeBeadJSONObject(t, executeBeadCommand(t, store, "close", "oro-other", "--reason", "ok", "--json"))
	if closed["status"] != "closed" {
		t.Fatalf("close JSON = %#v, want closed bead", closed)
	}
}

// TestBeadCloseSelfCloseGuardInactiveOutsideWorker proves the guard only fires
// for workers — dispatcher/manager processes (no ORO_WORKER) close beads
// normally even when ORO_WORKER_BEAD_ID happens to match. See oro-t5ha.
func TestBeadCloseSelfCloseGuardInactiveOutsideWorker(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:       "oro-spf1a",
		Title:    "Coordinator-driven close",
		Status:   "open",
		Priority: 1,
		Type:     "task",
	})

	t.Setenv("ORO_WORKER", "")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-spf1a")

	closed := decodeBeadJSONObject(t, executeBeadCommand(t, store, "close", "oro-spf1a", "--reason", "manual", "--json"))
	if closed["status"] != "closed" {
		t.Fatalf("close JSON = %#v, want closed bead", closed)
	}
}

func TestCmdBeadDependencyRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "oro-blocker", "--title", "blocker")
	executeBeadCommand(t, store, "create", "--id", "oro-blocked", "--title", "blocked")

	added := decodeBeadJSONObject(t, executeBeadCommand(t, store, "dep", "add", "oro-blocked", "oro-blocker", "--type", "blocks", "--json"))
	if added["id"] != "oro-blocked" {
		t.Fatalf("dep add JSON id = %#v, want oro-blocked", added["id"])
	}
	deps, ok := added["dependencies"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("dep add dependencies = %#v, want one dependency", added["dependencies"])
	}

	listed := decodeDependencyJSONArray(t, executeBeadCommand(t, store, "dep", "list", "oro-blocked", "--json"))
	if len(listed) != 1 || listed[0]["depends_on_id"] != "oro-blocker" || listed[0]["type"] != "blocks" {
		t.Fatalf("dep list = %#v, want oro-blocked -> oro-blocker blocks", listed)
	}

	ready := decodeBeadJSONArray(t, executeBeadCommand(t, store, "ready", "--json"))
	if beadJSONArrayHasID(ready, "oro-blocked") {
		t.Fatalf("ready included blocked bead before dependency closed: %#v", ready)
	}
	if !beadJSONArrayHasID(ready, "oro-blocker") {
		t.Fatalf("ready did not include blocker before close: %#v", ready)
	}
	blocked := decodeBeadJSONArray(t, executeBeadCommand(t, store, "list", "--status=blocked", "--json"))
	if !beadJSONArrayHasID(blocked, "oro-blocked") {
		t.Fatalf("list --status=blocked omitted dependency-blocked bead: %#v", blocked)
	}

	removed := decodeBeadJSONObject(t, executeBeadCommand(t, store, "dep", "rm", "oro-blocked", "oro-blocker", "--json"))
	removedDeps, ok := removed["dependencies"].([]any)
	if !ok || len(removedDeps) != 0 {
		t.Fatalf("dep rm dependencies = %#v, want empty", removed["dependencies"])
	}

	executeBeadCommand(t, store, "dep", "add", "oro-blocked", "oro-blocker", "--json")
	executeBeadCommand(t, store, "close", "oro-blocker", "--reason", "done")
	ready = decodeBeadJSONArray(t, executeBeadCommand(t, store, "ready", "--json"))
	if !beadJSONArrayHasID(ready, "oro-blocked") {
		t.Fatalf("ready did not include unblocked bead after dependency close: %#v", ready)
	}
}

func TestBeadListInProgressIncludesActiveAssignments(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-assigned", Title: "Assigned", Status: "open", WorkerID: "worker-1"},
		protocol.Bead{ID: "oro-progress", Title: "Progress", Status: "in_progress"},
	)

	got := decodeBeadJSONArray(t, executeBeadCommand(t, store, "list", "--status=in_progress", "--json"))
	if !beadJSONArrayHasID(got, "oro-assigned") || !beadJSONArrayHasID(got, "oro-progress") {
		t.Fatalf("list --status=in_progress = %#v, want assigned and explicit in-progress beads", got)
	}
	status := decodeBeadJSONObject(t, executeBeadCommand(t, store, "status", "--json"))
	if status["open"] != float64(0) || status["in_progress"] != float64(2) || status["closed"] != float64(0) {
		t.Fatalf("status = %#v, want assigned open bead counted as in_progress", status)
	}
}

func TestBeadListDefaultIncludesInProgress(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-ready", Title: "Ready", Status: "open"},
		protocol.Bead{ID: "oro-progress", Title: "Progress", Status: "in_progress"},
		protocol.Bead{ID: "oro-closed", Title: "Closed", Status: "closed"},
	)

	got := decodeBeadJSONArray(t, executeBeadCommand(t, store, "list", "--json"))
	if !beadJSONArrayHasID(got, "oro-ready") || !beadJSONArrayHasID(got, "oro-progress") {
		t.Fatalf("list default = %#v, want ready and in-progress beads", got)
	}
	if beadJSONArrayHasID(got, "oro-closed") {
		t.Fatalf("list default included closed bead: %#v", got)
	}
}

func TestBeadListStatusOpenIncludesBlockedOpenBeads(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-blocker", Title: "Blocker", Status: "open"},
		protocol.Bead{ID: "oro-blocked", Title: "Blocked", Status: "open", Dependencies: []protocol.Dependency{
			{IssueID: "oro-blocked", DependsOnID: "oro-blocker", Type: "blocks"},
		}},
		protocol.Bead{ID: "oro-progress", Title: "Progress", Status: "in_progress"},
	)

	got := decodeBeadJSONArray(t, executeBeadCommand(t, store, "list", "--status=open", "--json"))
	if !beadJSONArrayHasID(got, "oro-blocker") || !beadJSONArrayHasID(got, "oro-blocked") {
		t.Fatalf("list --status=open = %#v, want all open beads including blocked", got)
	}
	if beadJSONArrayHasID(got, "oro-progress") {
		t.Fatalf("list --status=open included in-progress bead: %#v", got)
	}
}

func TestCmdBeadLifecycleSubcommands(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "oro-life", "--title", "lifecycle")

	status := decodeBeadJSONObject(t, executeBeadCommand(t, store, "status", "--json"))
	if status["open"] != float64(1) || status["in_progress"] != float64(0) || status["closed"] != float64(0) {
		t.Fatalf("initial status = %#v, want one open bead", status)
	}

	deferred := decodeBeadJSONObject(t, executeBeadCommand(t, store, "defer", "oro-life", "--until", "2999-01-01T00:00:00Z", "--json"))
	if deferred["id"] != "oro-life" {
		t.Fatalf("defer JSON = %#v, want oro-life", deferred)
	}
	if ready := decodeBeadJSONArray(t, executeBeadCommand(t, store, "ready", "--json")); beadJSONArrayHasID(ready, "oro-life") {
		t.Fatalf("ready included future-deferred bead: %#v", ready)
	}

	undeferred := decodeBeadJSONObject(t, executeBeadCommand(t, store, "undefer", "oro-life", "--json"))
	if undeferred["id"] != "oro-life" {
		t.Fatalf("undefer JSON = %#v, want oro-life", undeferred)
	}
	if ready := decodeBeadJSONArray(t, executeBeadCommand(t, store, "ready", "--json")); !beadJSONArrayHasID(ready, "oro-life") {
		t.Fatalf("ready did not include undeferred bead: %#v", ready)
	}

	executeBeadCommand(t, store, "close", "oro-life", "--reason", "done")
	reopened := decodeBeadJSONObject(t, executeBeadCommand(t, store, "reopen", "oro-life", "--json"))
	if reopened["status"] != "open" || reopened["closed_at"] != nil || reopened["close_reason"] != nil {
		t.Fatalf("reopen JSON = %#v, want open bead without close metadata", reopened)
	}
}

func TestBeadExportJSONEmitsOroNativeArray(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:              "oro-export",
		Title:           "Export",
		ContractVersion: 2,
		Draft:           true,
		Status:          "open",
		Priority:        1,
		Type:            "task",
		Epic:            "oro-parent",
	})

	got := decodeBeadJSONArray(t, executeBeadCommand(t, store, "export", "--json"))
	if len(got) != 1 {
		t.Fatalf("export count = %d, want 1", len(got))
	}
	if got[0]["id"] != "oro-export" || got[0]["parent_id"] != "oro-parent" {
		t.Fatalf("export JSON = %#v, want oro-native exported bead", got[0])
	}
	if got[0]["contract_version"] != float64(2) || got[0]["draft"] != true {
		t.Fatalf("export contract fields = version %#v, draft %#v; want 2, true", got[0]["contract_version"], got[0]["draft"])
	}
	if _, ok := got[0]["issue_type"]; ok {
		t.Fatalf("legacy issue_type key present in oro-native JSON")
	}
}

func TestBeadDepJSONRoundTripsFakeStore(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "oro-a", Title: "dependent", Status: "open", Type: "task"},
		protocol.Bead{ID: "oro-b", Title: "blocker", Status: "open", Type: "task"},
	)

	added := decodeBeadJSONObject(t, executeBeadCommand(t, store, "dep", "add", "oro-a", "oro-b", "--type", "conditional-blocks", "--json"))
	deps, ok := added["dependencies"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("dep add dependencies = %#v, want one dependency", added["dependencies"])
	}

	listed := decodeDependencyJSONArray(t, executeBeadCommand(t, store, "dep", "list", "oro-a", "--json"))
	if len(listed) != 1 || listed[0]["depends_on_id"] != "oro-b" || listed[0]["type"] != "conditional-blocks" {
		t.Fatalf("dep list = %#v, want conditional-blocks dependency", listed)
	}

	removed := decodeBeadJSONObject(t, executeBeadCommand(t, store, "dep", "rm", "oro-a", "oro-b", "--json"))
	removedDeps, ok := removed["dependencies"].([]any)
	if !ok || len(removedDeps) != 0 {
		t.Fatalf("dep rm dependencies = %#v, want empty", removed["dependencies"])
	}
}

// TestBeadDepAddRefusesWorkerAddDepDepAddOnAssignedBeadLeafBeadWorkerDepAddSelf
// proves that an Oro worker subprocess (ORO_WORKER=1) cannot add a dependency
// edge whose source matches its currently assigned bead. This blocks the
// leaf-bead self-decomposition pattern (oro-xs1a) where a worker assigned a
// type=task bead added phantom blocks-deps onto itself, corrupting the bead
// queue. Also covers oro-qafy's CLI guard requirement for dep-add on the
// caller's assigned bead.
func TestBeadDepAddRefusesWorkerAddDepDepAddOnAssignedBeadLeafBeadWorkerDepAddSelf(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "oro-leaf", "--title", "leaf bead", "--type", "task")
	executeBeadCommand(t, store, "create", "--id", "oro-phantom", "--title", "phantom child")

	t.Setenv("ORO_WORKER", "1")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-leaf")

	cmd := newBeadCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dep", "add", "oro-leaf", "oro-phantom", "--type", "blocks"})

	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected guard error refusing worker self-decomposition, got nil; output=%s", out.String())
	} else if !strings.Contains(err.Error(), "ORO_WORKER_BEAD_ID") && !strings.Contains(err.Error(), "self-dep") {
		t.Fatalf("expected ORO_WORKER_BEAD_ID guard error, got %v", err)
	}

	bead, ferr := store.Show(context.Background(), "oro-leaf")
	if ferr != nil {
		t.Fatalf("store.Show: %v", ferr)
	}
	if bead == nil {
		t.Fatalf("bead oro-leaf missing")
	}
	if len(bead.Dependencies) != 0 {
		t.Fatalf("bead deps = %#v, want none (guard must prevent dep insertion)", bead.Dependencies)
	}
}

// TestBeadDepAddEpicDecompAllowed proves the worker-dep-add guard only
// fires when the source matches the worker's own assigned bead. A legitimate
// epic decomposition adds blocks-deps from the parent epic to newly created
// children — the source there is the parent (not the worker's assigned bead),
// so the guard must not block it. Covers oro-xs1a's EpicDecomp acceptance.
func TestBeadDepAddEpicDecompAllowed(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "oro-parent-epic", "--title", "parent epic", "--type", "epic")
	executeBeadCommand(t, store, "create", "--id", "oro-decomp-task", "--title", "task running decomp", "--type", "task")
	executeBeadCommand(t, store, "create", "--id", "oro-child", "--title", "spawned child")

	t.Setenv("ORO_WORKER", "1")
	t.Setenv("ORO_WORKER_BEAD_ID", "oro-decomp-task")

	executeBeadCommand(t, store, "dep", "add", "oro-parent-epic", "oro-child", "--type", "blocks", "--json")

	parent, ferr := store.Show(context.Background(), "oro-parent-epic")
	if ferr != nil {
		t.Fatalf("store.Show: %v", ferr)
	}
	if len(parent.Dependencies) != 1 || parent.Dependencies[0].DependsOnID != "oro-child" {
		t.Fatalf("parent deps = %#v, want one dep on oro-child", parent.Dependencies)
	}
}

func TestBeadDepCyclesPrintsCycleAndExitsOne(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := beadstore.OpenSQLiteStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "A", "--title", "A")
	executeBeadCommand(t, store, "create", "--id", "B", "--title", "B")
	mustExecBeadTest(t, dbPath, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "A", "B", "blocks")
	mustExecBeadTest(t, dbPath, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "B", "A", "blocks")

	cmd := newBeadCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dep", "cycles"})

	err = cmd.Execute()
	if err == nil {
		t.Fatalf("bead dep cycles error = nil, want non-zero exit; output=%s", out.String())
	}
	if !strings.Contains(out.String(), "A → B → A") {
		t.Fatalf("bead dep cycles output = %q, want cycle", out.String())
	}
}

func TestBeadDepCyclesJSON(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := beadstore.OpenSQLiteStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "A", "--title", "A")
	executeBeadCommand(t, store, "create", "--id", "B", "--title", "B")
	mustExecBeadTest(t, dbPath, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "A", "B", "blocks")
	mustExecBeadTest(t, dbPath, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "B", "A", "blocks")

	cmd := newBeadCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dep", "cycles", "--json"})

	err = cmd.Execute()
	if err == nil {
		t.Fatalf("bead dep cycles --json error = nil, want non-zero exit; output=%s", out.String())
	}
	var got struct {
		Cycles [][]string `json:"cycles"`
	}
	if json.Unmarshal(out.Bytes(), &got) != nil {
		t.Fatalf("bead dep cycles --json emitted invalid JSON: %s", out.String())
	}
	if len(got.Cycles) != 1 || strings.Join(got.Cycles[0], " -> ") != "A -> B -> A" {
		t.Fatalf("cycles = %#v, want [[A B A]]", got.Cycles)
	}
}

func TestBeadDepCyclesAcyclicExitsZero(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	executeBeadCommand(t, store, "create", "--id", "A", "--title", "A")
	executeBeadCommand(t, store, "create", "--id", "B", "--title", "B")
	executeBeadCommand(t, store, "dep", "add", "A", "B")

	cmd := newBeadCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dep", "cycles"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead dep cycles error = %v; output=%s", err, out.String())
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("bead dep cycles output = %q, want empty", out.String())
	}
}

func executeBeadCommand(t *testing.T, store beadstore.Store, args ...string) string {
	t.Helper()

	cmd := newBeadCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bead %s error: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func mustExecBeadTest(t *testing.T, dbPath, query string, args ...any) {
	t.Helper()

	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func decodeBeadJSONObject(t *testing.T, out string) map[string]any {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON object: %v\n%s", err, out)
	}
	return got
}

func decodeBeadJSONArray(t *testing.T, out string) []map[string]any {
	t.Helper()

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON array: %v\n%s", err, out)
	}
	return got
}

func decodeDependencyJSONArray(t *testing.T, out string) []map[string]any {
	t.Helper()

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid dependency JSON array: %v\n%s", err, out)
	}
	return got
}

func beadJSONArrayHasID(beads []map[string]any, id string) bool {
	for _, bead := range beads {
		if bead["id"] == id {
			return true
		}
	}
	return false
}

func TestBeadCreateWithParentDoesNotSpawnPremortem(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := beadstore.NewSQLiteStore(db)

	executeBeadCommand(t, store,
		"create",
		"--id", "epic-cli",
		"--title", "Epic from CLI",
		"--type", "epic",
		"--acceptance-criteria", "n/a",
	)

	for i := range 6 {
		executeBeadCommand(t, store,
			"create",
			"--title", "child",
			"--type", "task",
			"--parent", "epic-cli",
			"--acceptance-criteria", "ac",
		)
		_ = i
	}

	beads := decodeBeadJSONArray(t, executeBeadCommand(t, store, "list", "--json"))
	var taskChildren, premortemChildren int
	for _, b := range beads {
		if b["parent_id"] != "epic-cli" {
			continue
		}
		switch b["type"] {
		case "task":
			taskChildren++
		case "premortem":
			premortemChildren++
		}
	}
	if taskChildren != 6 || premortemChildren != 0 {
		t.Errorf("CLI-created children: tasks=%d premortem=%d, want tasks=6 premortem=0; beads=%v", taskChildren, premortemChildren, beads)
	}
}

// TestTaskCreateTierFlag verifies that `oro task create --tier <value>`:
//  1. Accepts fast|balanced|deep|background and persists to the DB tier column.
//  2. Rejects unknown tier values.
//  3. Leaves tier unset when --tier is not provided.
func TestTaskCreateTierFlag(t *testing.T) {
	t.Run("deep_tier_persisted", func(t *testing.T) {
		ctx := context.Background()
		store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("OpenSQLiteStore: %v", err)
		}

		out := executeTaskCommand(t, store, "create", "--title", "t", "--tier", "deep", "--json")
		got := decodeBeadJSONObject(t, out)
		if got["tier"] != "deep" {
			t.Fatalf("tier = %#v, want deep", got["tier"])
		}
	})

	for _, tier := range []string{"fast", "balanced", "deep", "background"} {
		tier := tier
		t.Run("accepts_"+tier, func(t *testing.T) {
			ctx := context.Background()
			store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("OpenSQLiteStore: %v", err)
			}

			out := executeTaskCommand(t, store, "create", "--title", "t", "--tier", tier, "--json")
			got := decodeBeadJSONObject(t, out)
			if got["tier"] != tier {
				t.Fatalf("tier = %#v, want %s", got["tier"], tier)
			}
		})
	}

	t.Run("rejects_unknown_tier", func(t *testing.T) {
		ctx := context.Background()
		store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("OpenSQLiteStore: %v", err)
		}

		cmd := newTaskCmdWithStore(store)
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"create", "--title", "t", "--tier", "invalid", "--json"})
		_ = cmd.Execute()
		got := decodeBeadJSONObject(t, buf.String())
		if got["ok"] != false {
			t.Fatalf("expected ok=false for unknown tier, got output=%s", buf.String())
		}
	})

	t.Run("empty_tier_is_unset", func(t *testing.T) {
		ctx := context.Background()
		store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatalf("OpenSQLiteStore: %v", err)
		}

		out := executeTaskCommand(t, store, "create", "--title", "t", "--json")
		got := decodeBeadJSONObject(t, out)
		if tier, ok := got["tier"]; ok && tier != "" && tier != nil {
			t.Fatalf("tier = %#v, want absent/empty when not set", tier)
		}
	})
}

func executeTaskCommand(t *testing.T, store beadstore.Store, args ...string) string {
	t.Helper()

	cmd := newTaskCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("task %s error: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}
