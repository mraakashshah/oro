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
