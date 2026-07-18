package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// dupReadStore injects extra into both InProgress and Ready to simulate overlap for dedup testing.
type dupReadStore struct {
	beadstore.Store
	extra protocol.Bead
}

func (d *dupReadStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := d.Store.InProgress(ctx)
	return append(beads, d.extra), err
}

func (d *dupReadStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	beads, err := d.Store.Ready(ctx)
	return append(beads, d.extra), err
}

func TestRootCommandIncludesTask(t *testing.T) {
	root := newRootCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "task" {
			return
		}
	}
	t.Fatal("root command did not register task subcommand")
}

func TestHelpIncludesTaskCommand(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help error: %v", err)
	}

	if !strings.Contains(out.String(), "task       Manage native Oro tasks") {
		t.Fatalf("categorized help missing task command:\n%s", out.String())
	}
}

func TestTaskCommandIsCanonical(t *testing.T) {
	root := newRootCmd()

	var foundTask bool
	for _, cmd := range root.Commands() {
		switch cmd.Name() {
		case "task":
			foundTask = true
		case "bead":
			t.Fatal("root command registered retired bead compatibility alias")
		}
	}
	if !foundTask {
		t.Fatal("root command did not register canonical task subcommand")
	}
}

func TestTaskCommandSubcommandParity(t *testing.T) {
	taskCmd := newTaskCmdWithStore(nil)

	taskSubs := map[string]*cobra.Command{}
	for _, sub := range taskCmd.Commands() {
		taskSubs[sub.Name()] = sub
	}

	want := []string{
		"blocked",
		"closed",
		"close",
		"create",
		"defer",
		"delete",
		"dep",
		"export",
		"list",
		"note",
		"ready",
		"reopen",
		"show",
		"status",
		"undefer",
		"update",
	}
	for _, name := range want {
		if taskSubs[name] == nil {
			t.Fatalf("task command missing supported subcommand %q", name)
		}
	}

	for _, unsupported := range []string{"doctor", "import", "meta", "migrate-from-dolt", "search", "tag"} {
		if taskSubs[unsupported] != nil {
			t.Fatalf("task command exposes unsupported legacy command %q", unsupported)
		}
	}
}

func TestTaskCommandDoesNotExposeLegacyBeadStubs(t *testing.T) {
	for _, args := range [][]string{
		{"search", "query"},
		{"import", "snapshot.json"},
		{"doctor"},
		{"tag", "add", "oro-1", "cli"},
		{"meta", "set", "oro-1", "key=value"},
		{"note", "list", "oro-1"},
	} {
		cmd := newTaskCmdWithStore(beadstore.NewFakeStore())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("task %s unexpectedly succeeded", strings.Join(args, " "))
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("task %s returned non-Cobra unknown-command error:\nerr=%v\nout=%s", strings.Join(args, " "), err, out.String())
		}
		if strings.Contains(err.Error(), "not implemented yet") || strings.Contains(out.String(), "not implemented yet") {
			t.Fatalf("task %s returned stub error instead of Cobra unknown-command behavior:\nerr=%v\nout=%s", strings.Join(args, " "), err, out.String())
		}
	}
}

func TestBeadRootCommandFactoryIsTestOnly(t *testing.T) {
	production, err := filepath.Glob("cmd_*.go")
	if err != nil {
		t.Fatalf("glob production command files: %v", err)
	}
	for _, path := range production {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(data)
		for _, retired := range []string{"func newBeadCmdWithStore", "func newBeadStubCmd"} {
			if strings.Contains(src, retired) {
				t.Fatalf("%s still compiles retired bead compatibility factory %q", path, retired)
			}
		}
	}
}

func TestTaskHelpOmitsUnsupportedStubs(t *testing.T) {
	cmd := newTaskCmdWithStore(beadstore.NewFakeStore())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task --help error: %v\n%s", err, out.String())
	}

	help := out.String()
	for _, unsupported := range []string{
		"search",
		"import",
		"doctor",
		"tag",
		"meta",
	} {
		if strings.Contains(help, unsupported) {
			t.Fatalf("task --help lists unsupported stub %q:\n%s", unsupported, help)
		}
	}
	if !strings.Contains(help, "note") {
		t.Fatalf("task --help must keep implemented note command reachable:\n%s", help)
	}

	noteHelpCmd := newTaskCmdWithStore(beadstore.NewFakeStore())
	var noteOut bytes.Buffer
	noteHelpCmd.SetOut(&noteOut)
	noteHelpCmd.SetErr(&noteOut)
	noteHelpCmd.SetArgs([]string{"note", "--help"})
	if err := noteHelpCmd.Execute(); err != nil {
		t.Fatalf("task note --help error: %v\n%s", err, noteOut.String())
	}
	if strings.Contains(noteOut.String(), "list") {
		t.Fatalf("task note --help lists unsupported note list stub:\n%s", noteOut.String())
	}

	for _, args := range [][]string{
		{"search", "query"},
		{"import", "snapshot.json"},
		{"doctor"},
		{"tag", "add", "oro-1", "cli"},
		{"meta", "set", "oro-1", "key=value"},
		{"note", "list", "oro-1"},
	} {
		unsupportedCmd := newTaskCmdWithStore(beadstore.NewFakeStore())
		var unsupportedOut bytes.Buffer
		unsupportedCmd.SetOut(&unsupportedOut)
		unsupportedCmd.SetErr(&unsupportedOut)
		unsupportedCmd.SetArgs(args)
		err := unsupportedCmd.Execute()
		if err == nil {
			t.Fatalf("task %s unexpectedly succeeded", strings.Join(args, " "))
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("task %s returned non-Cobra unknown-command error:\nerr=%v\nout=%s", strings.Join(args, " "), err, unsupportedOut.String())
		}
		if strings.Contains(err.Error(), "not implemented yet") || strings.Contains(unsupportedOut.String(), "not implemented yet") {
			t.Fatalf("task %s returned stub error instead of Cobra unknown-command behavior:\nerr=%v\nout=%s", strings.Join(args, " "), err, unsupportedOut.String())
		}
	}
}

func TestTaskCommandHelpUsesTaskTerminology(t *testing.T) {
	cmd := newTaskCmdWithStore(nil)
	for _, args := range [][]string{
		{"--help"},
		{"ready", "--help"},
		{"list", "--help"},
		{"show", "--help"},
	} {
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("task %s help error: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		got := out.String()
		if strings.Contains(got, "bead") || strings.Contains(got, "Bead") {
			t.Fatalf("task %s help should be task-primary, got:\n%s", strings.Join(args, " "), got)
		}
		if !strings.Contains(got, "task") && !strings.Contains(got, "Task") {
			t.Fatalf("task %s help should mention task terminology, got:\n%s", strings.Join(args, " "), got)
		}
	}
}

func TestTaskListHelpMentionsTableAndJSON(t *testing.T) {
	cmd := newTaskCmdWithStore(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task list --help error: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "table") {
		t.Fatalf("task list help should mention 'table', got:\n%s", got)
	}
	if !strings.Contains(got, "--json") {
		t.Fatalf("task list help should mention '--json', got:\n%s", got)
	}
	if strings.Contains(got, "bead") || strings.Contains(got, "Bead") {
		t.Fatalf("task list help should not use bead terminology, got:\n%s", got)
	}
}

func TestTaskCommandRejectsMigrationAlias(t *testing.T) {
	cmd := newTaskCmdWithStore(beadstore.NewFakeStore())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"migrate-from-dolt"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("task migrate-from-dolt unexpectedly succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "migrate-from-dolt") {
		t.Fatalf("task migrate-from-dolt error = %v, want unavailable migration command named", err)
	}
}

func TestBeadCommandRemovedFromRoot(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root help error: %v", err)
	}
	if strings.Contains(out.String(), "\n  bead ") {
		t.Fatalf("root help should omit bead command:\n%s", out.String())
	}

	root = newRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bead", "status"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("oro bead status unexpectedly succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), "bead") {
		t.Fatalf("oro bead status error = %v, want unknown bead command", err)
	}

	root = newRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"task", "status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("oro task status error: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "open\t") {
		t.Fatalf("oro task status output missing status counts:\n%s", out.String())
	}
}

func TestTaskCommandAliasLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	execTask := func(args ...string) string {
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

	created := decodeBeadJSONObject(t, execTask(
		"create",
		"--id", "oro-task-e2e",
		"--title", "task alias test",
		"--type", "task",
		"--priority", "1",
		"--acceptance-criteria", "lifecycle passes",
		"--json",
	))
	if created["id"] != "oro-task-e2e" || created["status"] != "open" {
		t.Fatalf("created = %#v, want open oro-task-e2e", created)
	}

	shown := decodeBeadJSONObject(t, execTask("show", "oro-task-e2e", "--json"))
	if shown["title"] != "task alias test" || shown["acceptance_criteria"] != "lifecycle passes" {
		t.Fatalf("show = %#v, want round-tripped fields", shown)
	}

	updated := decodeBeadJSONObject(t, execTask(
		"update", "oro-task-e2e",
		"--status", "in_progress",
		"--priority", "0",
		"--json",
	))
	if updated["status"] != "in_progress" || updated["priority"] != float64(0) {
		t.Fatalf("update = %#v, want in_progress priority 0", updated)
	}

	closed := decodeBeadJSONObject(t, execTask("close", "oro-task-e2e", "--reason", "done", "--json"))
	if closed["status"] != "closed" || closed["close_reason"] != "done" {
		t.Fatalf("close = %#v, want closed with reason", closed)
	}
}

func TestTaskDeleteSoftDeletesHumanOutput(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-delete-cli", Title: "delete cli"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cmd := newTaskCmdWithStore(store)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"delete", "oro-delete-cli", "--reason", "cleanup"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task delete error: %v\n%s", err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "deleted oro-delete-cli" {
		t.Fatalf("task delete output = %q, want deleted oro-delete-cli", got)
	}

	cmd = newTaskCmdWithStore(store)
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "oro-delete-cli"})
	err = cmd.Execute()
	if err == nil {
		t.Fatalf("task show after delete unexpectedly succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("task show after delete error = %v, want not found", err)
	}
}

func TestTaskDeleteJSONAndRefusals(t *testing.T) {
	ctx := context.Background()

	newStore := func(t *testing.T) (*beadstore.SQLiteStore, func(query string, args ...any)) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "state.db")
		store, err := beadstore.OpenSQLiteStore(ctx, path)
		if err != nil {
			t.Fatalf("OpenSQLiteStore: %v", err)
		}
		db, err := dbutil.OpenDB(path)
		if err != nil {
			t.Fatalf("OpenDB raw: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		execRaw := func(query string, args ...any) {
			t.Helper()
			if _, err := db.ExecContext(ctx, query, args...); err != nil {
				t.Fatalf("exec raw %q: %v", query, err)
			}
		}
		return store, execRaw
	}
	execTask := func(store beadstore.Store, args ...string) (string, error) {
		cmd := newTaskCmdWithStore(store)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	decode := func(t *testing.T, out string) map[string]any {
		t.Helper()
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("unmarshal JSON %q: %v", out, err)
		}
		return got
	}

	t.Run("json success includes deleted and reason", func(t *testing.T) {
		store, _ := newStore(t)
		if _, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-json-delete", Title: "json delete"}); err != nil {
			t.Fatalf("Create: %v", err)
		}

		out, err := execTask(store, "delete", "oro-json-delete", "--reason", "cleanup", "--json")
		if err != nil {
			t.Fatalf("task delete --json error: %v\n%s", err, out)
		}
		got := decode(t, out)
		if got["id"] != "oro-json-delete" || got["deleted"] != true || got["reason"] != "cleanup" {
			t.Fatalf("delete JSON = %#v, want id/deleted/reason", got)
		}
	})

	for name, setup := range map[string]func(t *testing.T, store *beadstore.SQLiteStore, execRaw func(string, ...any)) string{
		"active assignment": func(t *testing.T, store *beadstore.SQLiteStore, execRaw func(string, ...any)) string {
			t.Helper()
			if _, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-active-delete", Title: "active delete"}); err != nil {
				t.Fatalf("Create active: %v", err)
			}
			execRaw(`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-active-delete', 'worker-1', '/tmp/active-delete', 'active')`)
			return "oro-active-delete"
		},
		"child bead": func(t *testing.T, store *beadstore.SQLiteStore, _ func(string, ...any)) string {
			t.Helper()
			if _, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-parent-delete-cli", Title: "parent delete"}); err != nil {
				t.Fatalf("Create parent: %v", err)
			}
			if _, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-child-delete-cli", Title: "child delete", ParentID: "oro-parent-delete-cli"}); err != nil {
				t.Fatalf("Create child: %v", err)
			}
			return "oro-parent-delete-cli"
		},
		"unknown id": func(_ *testing.T, _ *beadstore.SQLiteStore, _ func(string, ...any)) string {
			return "oro-missing-delete"
		},
		"already deleted": func(t *testing.T, store *beadstore.SQLiteStore, _ func(string, ...any)) string {
			t.Helper()
			if _, err := store.Create(ctx, beadstore.CreateParams{ID: "oro-double-delete", Title: "double delete"}); err != nil {
				t.Fatalf("Create double: %v", err)
			}
			if err := store.Delete(ctx, "oro-double-delete", "first"); err != nil {
				t.Fatalf("Delete first: %v", err)
			}
			return "oro-double-delete"
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, execRaw := newStore(t)
			id := setup(t, store, execRaw)
			out, err := execTask(store, "delete", id, "--json")
			if err != nil {
				t.Fatalf("task delete %s JSON error: %v\n%s", name, err, out)
			}
			got := decode(t, out)
			if got["ok"] != false || got["message"] == "" {
				t.Fatalf("delete refusal JSON = %#v, want ok=false with message", got)
			}
		})
	}
}

func TestTaskDeleteNoPremortemEndToEnd(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ORO_PROJECT", "task-delete-e2e")
	db, err := openStateDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := beadstore.NewSQLiteStore(db)

	execTask := func(args ...string) (string, error) {
		t.Helper()
		cmd := newTaskCmdWithStore(store)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	mustTask := func(args ...string) string {
		t.Helper()
		out, err := execTask(args...)
		if err != nil {
			t.Fatalf("task %s error: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	mustTask("create", "--id", "oro-e2e-epic", "--title", "e2e epic", "--type", "epic", "--acceptance-criteria", "epic ac")
	for i := range 6 {
		mustTask(
			"create",
			"--id", fmt.Sprintf("oro-e2e-child-%d", i),
			"--title", fmt.Sprintf("e2e child %d", i),
			"--type", "task",
			"--parent", "oro-e2e-epic",
			"--acceptance-criteria", "child ac",
		)
	}

	out, err := execTask("create", "--id", "oro-e2e-premortem", "--title", "blocked", "--type", "premortem")
	if err == nil {
		t.Fatalf("task create premortem unexpectedly succeeded:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "premortem") {
		t.Fatalf("task create premortem error = %v, want premortem mentioned", err)
	}

	beads := decodeBeadJSONArray(t, mustTask("list", "--json"))
	var taskChildren, premortemChildren int
	for _, bead := range beads {
		if bead["parent_id"] != "oro-e2e-epic" {
			continue
		}
		switch bead["type"] {
		case "task":
			taskChildren++
		case "premortem":
			premortemChildren++
		}
	}
	if taskChildren != 6 || premortemChildren != 0 {
		t.Fatalf("task-created children: tasks=%d premortem=%d, want tasks=6 premortem=0; beads=%#v", taskChildren, premortemChildren, beads)
	}

	mustTask("delete", "oro-e2e-child-0", "--reason", "cleanup")
	if _, err := execTask("show", "oro-e2e-child-0"); err == nil {
		t.Fatal("task show after delete unexpectedly succeeded")
	}

	err = executeWork(ctx, &workConfig{
		beadID:  "oro-e2e-child-1",
		timeout: 5 * time.Second,
		dryRun:  true,
	}, &workDeps{
		beadSrc:  store,
		repoRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("executeWork dry-run with eligible legacy gate = %v, want nil", err)
	}
}

func TestTaskCommandReadyListStatusAndDependencies(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	execTask := func(args ...string) string {
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

	execTask("create", "--id", "oro-task-parent", "--title", "parent", "--type", "epic")
	execTask("create", "--id", "oro-task-blocker", "--title", "blocker")
	execTask("create", "--id", "oro-task-blocked", "--title", "blocked", "--parent", "oro-task-parent", "--tag", "alias")

	added := decodeBeadJSONObject(t, execTask("dep", "add", "oro-task-blocked", "oro-task-blocker", "--type", "blocks", "--json"))
	deps, ok := added["dependencies"].([]any)
	if !ok || len(deps) != 1 {
		t.Fatalf("task dep add dependencies = %#v, want one dependency", added["dependencies"])
	}

	listedDeps := decodeDependencyJSONArray(t, execTask("dep", "list", "oro-task-blocked", "--json"))
	if len(listedDeps) != 1 || listedDeps[0]["depends_on_id"] != "oro-task-blocker" || listedDeps[0]["type"] != "blocks" {
		t.Fatalf("task dep list = %#v, want oro-task-blocked -> oro-task-blocker blocks", listedDeps)
	}

	ready := decodeBeadJSONArray(t, execTask("ready", "--json"))
	if beadJSONArrayHasID(ready, "oro-task-blocked") {
		t.Fatalf("task ready included blocked task: %#v", ready)
	}
	if !beadJSONArrayHasID(ready, "oro-task-blocker") {
		t.Fatalf("task ready omitted blocker task: %#v", ready)
	}

	openTasks := decodeBeadJSONArray(t, execTask("list", "--status=open", "--json"))
	if !beadJSONArrayHasID(openTasks, "oro-task-blocked") || !beadJSONArrayHasID(openTasks, "oro-task-blocker") {
		t.Fatalf("task list --status=open = %#v, want all open tasks including blocked", openTasks)
	}

	filtered := decodeBeadJSONArray(t, execTask("list", "--parent", "oro-task-parent", "--tag", "alias", "--json"))
	if len(filtered) != 1 || filtered[0]["id"] != "oro-task-blocked" {
		t.Fatalf("task list filtered = %#v, want oro-task-blocked", filtered)
	}

	status := decodeBeadJSONObject(t, execTask("status", "--json"))
	if status["open"] != float64(3) || status["in_progress"] != float64(0) || status["closed"] != float64(0) {
		t.Fatalf("task status = %#v, want three open tasks", status)
	}

	removed := decodeBeadJSONObject(t, execTask("dep", "rm", "oro-task-blocked", "oro-task-blocker", "--json"))
	removedDeps, ok := removed["dependencies"].([]any)
	if !ok || len(removedDeps) != 0 {
		t.Fatalf("task dep rm dependencies = %#v, want empty", removed["dependencies"])
	}

	ready = decodeBeadJSONArray(t, execTask("ready", "--json"))
	if !beadJSONArrayHasID(ready, "oro-task-blocked") {
		t.Fatalf("task ready omitted unblocked task after dep rm: %#v", ready)
	}
}

func TestTaskListJSONHydratesParentFromParentChildDependency(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	execTask := func(args ...string) string {
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

	execTask("create", "--id", "oro-parent-a", "--title", "parent a", "--type", "epic")
	execTask("create", "--id", "oro-parent-b", "--title", "parent b", "--type", "epic")
	execTask("create", "--id", "oro-legacy-child", "--title", "legacy child")
	execTask("create", "--id", "oro-explicit-child", "--title", "explicit child", "--parent", "oro-parent-b")
	execTask("create", "--id", "oro-blocks-only", "--title", "blocks only")

	execTask("dep", "add", "oro-legacy-child", "oro-parent-a", "--type", "parent-child")
	execTask("dep", "add", "oro-explicit-child", "oro-parent-a", "--type", "parent-child")
	execTask("dep", "add", "oro-blocks-only", "oro-parent-a", "--type", "blocks")

	listed := decodeBeadJSONArray(t, execTask("list", "--status=open", "--json"))
	byID := map[string]map[string]any{}
	for _, bead := range listed {
		id, _ := bead["id"].(string)
		byID[id] = bead
	}

	if got := byID["oro-legacy-child"]["parent_id"]; got != "oro-parent-a" {
		t.Fatalf("legacy child parent_id = %#v, want parent-child dependency parent oro-parent-a in %#v", got, byID["oro-legacy-child"])
	}
	if got := byID["oro-explicit-child"]["parent_id"]; got != "oro-parent-b" {
		t.Fatalf("explicit child parent_id = %#v, want explicit parent oro-parent-b to win in %#v", got, byID["oro-explicit-child"])
	}
	if got := byID["oro-blocks-only"]["parent_id"]; got != nil {
		t.Fatalf("blocks-only child parent_id = %#v, want nil because blocks deps are not parentage in %#v", got, byID["oro-blocks-only"])
	}
}

func TestTaskListDefaultIncludesInProgress(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	execTask := func(args ...string) string {
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

	execTask("create", "--id", "oro-task-progress", "--title", "progress", "--type", "task")
	execTask("update", "oro-task-progress", "--status", "in_progress")

	listed := decodeBeadJSONArray(t, execTask("list", "--json"))
	if !beadJSONArrayHasID(listed, "oro-task-progress") {
		t.Fatalf("task list omitted in-progress task: %#v", listed)
	}
}

func TestTaskListHumanOutputUsesReadableTable(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)

	beads := []protocol.Bead{
		{
			ID:        "oro-abc1",
			Status:    "open",
			Priority:  1,
			Type:      "task",
			UpdatedAt: recent,
			Title:     "normal title",
		},
		{
			ID:       "oro-abc2",
			Status:   "in_progress",
			Priority: 0,
			Type:     "bug",
			Title:    "title with\nnewline",
		},
	}

	var buf bytes.Buffer
	if err := writeBeadListHuman(&buf, beads, now); err != nil {
		t.Fatalf("writeBeadListHuman: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	// Header is first and contains all expected columns in declared order.
	if len(lines) < 1 {
		t.Fatal("no output")
	}
	header := lines[0]
	cols := []string{"ID", "STATUS", "PRI", "TYPE", "UPDATED", "TITLE"}
	for _, col := range cols {
		if !strings.Contains(header, col) {
			t.Fatalf("header missing column %q in: %q", col, header)
		}
	}
	for i := 1; i < len(cols); i++ {
		if strings.Index(header, cols[i-1]) >= strings.Index(header, cols[i]) {
			t.Fatalf("header column %q not before %q in: %q", cols[i-1], cols[i], header)
		}
	}

	// One header + one row per bead.
	if len(lines) != 1+len(beads) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), 1+len(beads), output)
	}

	// Both bead IDs appear in the output.
	for _, b := range beads {
		if !strings.Contains(output, b.ID) {
			t.Fatalf("output missing bead ID %q:\n%s", b.ID, output)
		}
	}

	// Embedded newlines in titles are replaced — total \n count equals line count.
	if strings.Count(output, "\n") != 1+len(beads) {
		t.Fatalf("title newlines not normalized; got %d newlines, want %d:\n%s",
			strings.Count(output, "\n"), 1+len(beads), output)
	}

	// Column alignment: STATUS value in each row starts at the same byte offset as the header.
	statusOff := strings.Index(header, "STATUS")
	for i, line := range lines[1:] {
		if len(line) < statusOff {
			t.Fatalf("row %d shorter than STATUS column offset %d: %q", i+1, statusOff, line)
		}
		if line[statusOff] == ' ' {
			t.Fatalf("row %d: STATUS column misaligned (space at offset %d): %q", i+1, statusOff, line)
		}
	}

	t.Run("empty_slice_has_header", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeBeadListHuman(&buf, []protocol.Bead{}, now); err != nil {
			t.Fatalf("writeBeadListHuman(empty): %v", err)
		}
		out := buf.String()
		outLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(outLines) < 1 || !strings.Contains(outLines[0], "ID") {
			t.Fatalf("empty slice: header missing: %q", out)
		}
	})

	t.Run("updated_label_no_timestamps", func(t *testing.T) {
		b := protocol.Bead{ID: "x"}
		if got := beadListUpdatedLabel(now, b); got != "-" {
			t.Fatalf("beadListUpdatedLabel with no timestamps = %q, want \"-\"", got)
		}
	})

	t.Run("updated_label_invalid_timestamp", func(t *testing.T) {
		b := protocol.Bead{ID: "x", UpdatedAt: "not-a-timestamp"}
		got := beadListUpdatedLabel(now, b)
		if got == "" {
			t.Fatal("beadListUpdatedLabel returned empty for invalid timestamp")
		}
	})

	t.Run("single_line_title", func(t *testing.T) {
		cases := []struct {
			input string
			want  string
		}{
			{"hello\nworld", "hello world"},
			{"a\r\nb", "a b"},
			{"no newline", "no newline"},
			{"multi\nline\ntitle", "multi line title"},
		}
		for _, tc := range cases {
			if got := singleLineListTitle(tc.input); got != tc.want {
				t.Fatalf("singleLineListTitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		}
	})
}

func TestTaskListDefaultsToTopUnfinished(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	execTask := func(args ...string) string {
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

	// 2 in_progress beads (priority 1)
	execTask("create", "--id", "oro-ip-00", "--title", "in-progress 0", "--priority", "1")
	execTask("update", "oro-ip-00", "--status", "in_progress")
	execTask("create", "--id", "oro-ip-01", "--title", "in-progress 1", "--priority", "1")
	execTask("update", "oro-ip-01", "--status", "in_progress")

	// 22 open/ready beads (priority 2) — exceeds the default cap of 20 when combined with in_progress
	for i := 0; i < 22; i++ {
		execTask("create",
			"--id", fmt.Sprintf("oro-open-%02d", i),
			"--title", fmt.Sprintf("open %d", i),
			"--priority", "2",
		)
	}

	// 1 blocked bead (has an active blocker dependency)
	execTask("create", "--id", "oro-blocker", "--title", "blocker", "--priority", "3")
	execTask("create", "--id", "oro-blocked", "--title", "blocked", "--priority", "3")
	execTask("dep", "add", "oro-blocked", "oro-blocker", "--type", "blocks")

	// 1 closed bead
	execTask("create", "--id", "oro-closed", "--title", "closed", "--priority", "3")
	execTask("close", "oro-closed", "--reason", "done")

	// 1 deferred bead
	execTask("create", "--id", "oro-deferred", "--title", "deferred", "--priority", "3")
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	execTask("defer", "oro-deferred", "--until", future)

	// Default list — no flags
	listed := decodeBeadJSONArray(t, execTask("list", "--json"))

	if len(listed) > 20 {
		t.Fatalf("task list returned %d beads, want at most 20", len(listed))
	}
	if len(listed) == 0 {
		t.Fatal("task list returned no beads")
	}

	// All statuses must be in_progress or open
	for _, bead := range listed {
		status, _ := bead["status"].(string)
		if status != "in_progress" && status != "open" {
			t.Fatalf("task list included bead with status %q, want only in_progress or open", status)
		}
	}

	// in_progress beads appear before open beads
	seenOpen := false
	for _, bead := range listed {
		status, _ := bead["status"].(string)
		if status == "open" {
			seenOpen = true
		}
		if seenOpen && status == "in_progress" {
			t.Fatalf("task list: in_progress bead appeared after open bead (ordering violated)")
		}
	}

	// Both in_progress beads are included
	if !beadJSONArrayHasID(listed, "oro-ip-00") || !beadJSONArrayHasID(listed, "oro-ip-01") {
		t.Fatalf("task list missing in_progress beads: %#v", listed)
	}

	// Blocked, closed, and deferred beads are excluded
	if beadJSONArrayHasID(listed, "oro-blocked") {
		t.Fatalf("task list included blocked bead")
	}
	if beadJSONArrayHasID(listed, "oro-closed") {
		t.Fatalf("task list included closed bead")
	}
	if beadJSONArrayHasID(listed, "oro-deferred") {
		t.Fatalf("task list included deferred bead")
	}

	// --limit=5 overrides the default cap
	limited := decodeBeadJSONArray(t, execTask("list", "--limit=5", "--json"))
	if len(limited) != 5 {
		t.Fatalf("task list --limit=5 returned %d beads, want 5", len(limited))
	}

	// --limit=0 means unlimited (returns more than the default 20)
	unlimited := decodeBeadJSONArray(t, execTask("list", "--limit=0", "--json"))
	if len(unlimited) <= 20 {
		t.Fatalf("task list --limit=0 returned %d beads, want more than 20 (unlimited)", len(unlimited))
	}

	// --status=open preserves existing full-export path (no implicit cap, includes blocked)
	openListed := decodeBeadJSONArray(t, execTask("list", "--status=open", "--json"))
	if len(openListed) <= 20 {
		t.Fatalf("task list --status=open returned %d beads, want more than 20 (no implicit cap)", len(openListed))
	}

	// Dedup: same bead appearing in both InProgress and Ready results in exactly one entry.
	t.Run("dedup", func(t *testing.T) {
		dupBead := protocol.Bead{
			ID:        "oro-dup",
			Title:     "dup bead",
			Status:    "in_progress",
			Priority:  1,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		base := beadstore.NewFakeStore()
		wrapper := &dupReadStore{Store: base, extra: dupBead}
		cmd := newTaskCmdWithStore(wrapper)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"list", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("task list error: %v\n%s", err, out.String())
		}
		got := decodeBeadJSONArray(t, out.String())
		count := 0
		for _, b := range got {
			if b["id"] == "oro-dup" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("dedup: bead %q appeared %d times in list, want 1", "oro-dup", count)
		}
	})
}

func TestTaskListHumanOutputPreservesJSONContract(t *testing.T) {
	ctx := context.Background()
	store, err := beadstore.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}

	execTask := func(args ...string) string {
		t.Helper()
		cmd := newTaskCmdWithStore(store)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("task %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}

	execTask("create", "--id", "oro-hl-1", "--title", "alpha task", "--priority", "0")
	execTask("update", "oro-hl-1", "--status", "in_progress")
	execTask("create", "--id", "oro-hl-2", "--title", "beta task", "--priority", "2")

	// Human output: must have a header row containing ID, STATUS, TITLE columns.
	humanOut := execTask("list")
	lines := strings.Split(strings.TrimSpace(humanOut), "\n")
	if len(lines) < 2 {
		t.Fatalf("task list (no --json) expected at least header + data rows, got:\n%s", humanOut)
	}
	header := strings.ToUpper(lines[0])
	if !strings.Contains(header, "ID") {
		t.Fatalf("task list header missing ID column, got: %s", lines[0])
	}
	if !strings.Contains(header, "STATUS") {
		t.Fatalf("task list header missing STATUS column, got: %s", lines[0])
	}
	if !strings.Contains(header, "TITLE") {
		t.Fatalf("task list header missing TITLE column, got: %s", lines[0])
	}
	if !strings.Contains(humanOut, "oro-hl-1") || !strings.Contains(humanOut, "oro-hl-2") {
		t.Fatalf("task list (no --json) missing expected task IDs:\n%s", humanOut)
	}

	// JSON output: must start with '[' (no header text), decode to an array with
	// id/title/status/priority/parent_id/type fields on every element.
	jsonOut := execTask("list", "--json")
	if trimmed := strings.TrimSpace(jsonOut); !strings.HasPrefix(trimmed, "[") {
		t.Fatalf("task list --json should start with '[', got:\n%s", jsonOut)
	}
	items := decodeBeadJSONArray(t, jsonOut)
	if len(items) == 0 {
		t.Fatal("task list --json returned empty array")
	}
	for _, item := range items {
		for _, field := range []string{"id", "title", "status", "priority", "parent_id", "type"} {
			if _, ok := item[field]; !ok {
				t.Fatalf("task list --json item missing field %q: %#v", field, item)
			}
		}
	}
}
