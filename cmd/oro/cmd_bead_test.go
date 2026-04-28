package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

func TestRootCommandIncludesBead(t *testing.T) {
	root := newRootCmd()

	for _, cmd := range root.Commands() {
		if cmd.Name() == "bead" {
			return
		}
	}

	t.Fatal("root command did not register bead subcommand")
}

func TestBeadCommandHelpExposesSubcommands(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"bead", "--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected help error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"create",
		"update",
		"close",
		"list",
		"show",
		"ready",
		"blocked",
		"closed",
		"dep",
		"export",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bead help missing %q:\n%s", want, out)
		}
	}
}

func TestBeadDepCommandHelpExposesSubcommands(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"bead", "dep", "--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected dep help error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"add", "rm", "list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bead dep help missing %q:\n%s", want, out)
		}
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
		protocol.Bead{ID: "oro-closed", Title: "Closed", Status: "closed", Priority: 0, Type: "task", ClosedAt: "2026-04-28T00:00:00Z"},
	)
	out := executeBeadCommand(t, store, "list", "--parent", "oro-parent", "--tag", "cli", "--json")

	got := decodeBeadJSONArray(t, out)
	if len(got) != 1 {
		t.Fatalf("list count = %d, want 1 in:\n%s", len(got), out)
	}
	if got[0]["id"] != "oro-list1" || got[0]["parent_id"] != "oro-parent" {
		t.Fatalf("list JSON = %#v, want oro-native filtered bead in:\n%s", got[0], out)
	}
	if _, ok := got[0]["issue_type"]; ok {
		t.Fatalf("legacy issue_type key present in oro-native JSON:\n%s", out)
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

func TestBeadExportJSONEmitsOroNativeArray(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:       "oro-export",
		Title:    "Export",
		Status:   "open",
		Priority: 1,
		Type:     "task",
		Epic:     "oro-parent",
	})

	got := decodeBeadJSONArray(t, executeBeadCommand(t, store, "export", "--json"))
	if len(got) != 1 {
		t.Fatalf("export count = %d, want 1", len(got))
	}
	if got[0]["id"] != "oro-export" || got[0]["parent_id"] != "oro-parent" {
		t.Fatalf("export JSON = %#v, want oro-native exported bead", got[0])
	}
	if _, ok := got[0]["issue_type"]; ok {
		t.Fatalf("legacy issue_type key present in oro-native JSON")
	}
}

func TestBeadDepJSONEmitsUnsupportedErrorObject(t *testing.T) {
	store := beadstore.NewFakeStore()
	for _, args := range [][]string{
		{"dep", "add", "oro-a", "oro-b", "--json"},
		{"dep", "rm", "oro-a", "oro-b", "--json"},
		{"dep", "list", "oro-a", "--json"},
	} {
		out := executeBeadCommand(t, store, args...)
		got := decodeBeadJSONObject(t, out)
		if got["ok"] != false {
			t.Fatalf("%s ok = %#v, want false in:\n%s", strings.Join(args, " "), got["ok"], out)
		}
		if got["error"] != "unsupported" {
			t.Fatalf("%s error = %#v, want unsupported in:\n%s", strings.Join(args, " "), got["error"], out)
		}
		message, ok := got["message"].(string)
		if !ok || !strings.Contains(message, "not implemented yet") {
			t.Fatalf("%s message = %#v, want not implemented yet in:\n%s", strings.Join(args, " "), got["message"], out)
		}
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
