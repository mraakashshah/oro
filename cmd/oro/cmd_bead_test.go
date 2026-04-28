package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

func TestCmdBeadCreateShowUpdateCloseRoundTripThroughBinary(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "oro")
	dbPath := filepath.Join(tmpDir, "state.db")
	oroHome := filepath.Join(tmpDir, "oro-home")

	build := exec.Command("go", "build", "-o", binPath, ".")
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
		"bead", "create",
		"--id", "oro-e2e1",
		"--title", "Binary bead",
		"--type", "task",
		"--priority", "2",
		"--description", "created through the oro binary",
		"--acceptance-criteria", "create show update close",
		"--tag", "cli",
		"--json",
	))
	if created["id"] != "oro-e2e1" || created["status"] != "open" {
		t.Fatalf("created bead = %#v, want open oro-e2e1", created)
	}

	shown := decodeBeadJSONObject(t, run("bead", "show", "oro-e2e1", "--json"))
	if shown["title"] != "Binary bead" || shown["acceptance_criteria"] != "create show update close" {
		t.Fatalf("shown bead did not round-trip create fields: %#v", shown)
	}

	updated := decodeBeadJSONObject(t, run(
		"bead", "update", "oro-e2e1",
		"--status", "in_progress",
		"--priority", "0",
		"--type", "bug",
		"--owner", "worker",
		"--acceptance", "updated acceptance",
		"--notes", "updated by e2e",
		"--json",
	))
	if updated["status"] != "in_progress" || updated["priority"] != float64(0) || updated["type"] != "bug" || updated["owner"] != "worker" {
		t.Fatalf("updated bead did not round-trip update fields: %#v", updated)
	}
	if updated["acceptance_criteria"] != "updated acceptance" || updated["notes"] != "updated by e2e" {
		t.Fatalf("updated bead did not round-trip text fields: %#v", updated)
	}

	closed := decodeBeadJSONObject(t, run("bead", "close", "oro-e2e1", "--reason", "verified", "--json"))
	if closed["status"] != "closed" || closed["close_reason"] != "verified" {
		t.Fatalf("closed bead did not round-trip close fields: %#v", closed)
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
