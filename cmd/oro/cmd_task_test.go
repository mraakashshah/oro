package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
)

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

func TestTaskCommandSubcommandParity(t *testing.T) {
	beadCmd := newBeadCmdWithStore(nil)
	taskCmd := newTaskCmdWithStore(nil)

	beadSubs := map[string]bool{}
	for _, sub := range beadCmd.Commands() {
		beadSubs[sub.Name()] = true
	}

	taskSubs := map[string]bool{}
	for _, sub := range taskCmd.Commands() {
		taskSubs[sub.Name()] = true
	}

	if taskSubs["migrate-from-dolt"] {
		t.Fatal("task command must not expose migrate-from-dolt")
	}

	if !beadSubs["migrate-from-dolt"] {
		t.Fatal("bead command must retain migrate-from-dolt")
	}

	for name := range beadSubs {
		if name == "migrate-from-dolt" {
			continue
		}
		if !taskSubs[name] {
			t.Fatalf("task command missing subcommand %q that bead has", name)
		}
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
