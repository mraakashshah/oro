package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/mg/data"
)

func TestMgFlagParsing(t *testing.T) {
	cmd := newMgCmd()

	// Verify the command name and description.
	if cmd.Use != "mg" {
		t.Fatalf("expected Use='mg', got %q", cmd.Use)
	}
	if cmd.Deprecated == "" {
		t.Fatal("expected mg command to be marked deprecated")
	}

	// Check all required flags exist.
	flags := []struct {
		name     string
		defValue string
	}{
		{"path", ""},
		{"block-types", ""},
		{"status", "false"},
	}
	for _, f := range flags {
		flag := cmd.Flag(f.name)
		if flag == nil {
			t.Fatalf("expected --%s flag to exist", f.name)
			return
		}
		if flag.DefValue != f.defValue {
			t.Fatalf("--%s default: expected %q, got %q", f.name, f.defValue, flag.DefValue)
		}
	}
}

func TestMgCmd_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "mg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'mg' command to be registered in root")
	}
}

// --- parseBlockingTypes ---

func TestParseBlockingTypes_EmptyUsesDefault(t *testing.T) {
	t.Setenv("MG_BLOCK_TYPES", "")
	got := parseBlockingTypes("")
	if len(got) != len(data.DefaultBlockingTypes) {
		t.Fatalf("expected default blocking types, got %v", got)
	}
	for k := range data.DefaultBlockingTypes {
		if !got[k] {
			t.Errorf("expected %q in default blocking types", k)
		}
	}
}

func TestParseBlockingTypes_FlagValue(t *testing.T) {
	got := parseBlockingTypes("foo,bar")
	if !got["foo"] || !got["bar"] {
		t.Fatalf("expected foo and bar, got %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 types, got %d", len(got))
	}
}

func TestParseBlockingTypes_WhitespaceTrimmed(t *testing.T) {
	got := parseBlockingTypes("  foo , bar  ")
	if !got["foo"] || !got["bar"] {
		t.Fatalf("expected foo and bar after trimming, got %v", got)
	}
}

func TestParseBlockingTypes_WhitespaceOnlyUsesDefault(t *testing.T) {
	t.Setenv("MG_BLOCK_TYPES", "")
	got := parseBlockingTypes(" , , ")
	if len(got) != len(data.DefaultBlockingTypes) {
		t.Fatalf("expected default blocking types for whitespace-only input, got %v", got)
	}
}

func TestParseBlockingTypes_EnvVar(t *testing.T) {
	t.Setenv("MG_BLOCK_TYPES", "depends-on,blocks")
	got := parseBlockingTypes("")
	if !got["depends-on"] || !got["blocks"] {
		t.Fatalf("expected env-var types, got %v", got)
	}
}

// --- findBeadsFile ---

func TestFindBeadsFile_Found(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findBeadsFile(dir)
	if got != jsonlPath {
		t.Fatalf("expected %q, got %q", jsonlPath, got)
	}
}

func TestFindBeadsFile_WalksUp(t *testing.T) {
	parent := t.TempDir()
	beadsDir := filepath.Join(parent, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(parent, "sub", "dir")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	got := findBeadsFile(child)
	if got != jsonlPath {
		t.Fatalf("expected %q walking up, got %q", jsonlPath, got)
	}
}

func TestFindBeadsFile_NotFound(t *testing.T) {
	dir := t.TempDir()
	got := findBeadsFile(dir)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// --- findBeadsDir ---

func TestFindBeadsDir_Found(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := findBeadsDir(dir)
	if got != dir {
		t.Fatalf("expected %q, got %q", dir, got)
	}
}

func TestFindBeadsDir_WalksUp(t *testing.T) {
	parent := t.TempDir()
	beadsDir := filepath.Join(parent, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(parent, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	got := findBeadsDir(child)
	if got != parent {
		t.Fatalf("expected %q walking up, got %q", parent, got)
	}
}

func TestFindBeadsDir_NotFound(t *testing.T) {
	dir := t.TempDir()
	got := findBeadsDir(dir)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// --- bdOnPath ---

func TestBdOnPath_ReturnsBoolean(t *testing.T) {
	// Just verify it returns a valid bool without panicking.
	_ = bdOnPath()
}

func TestBdOnPath_FalseWhenNotOnPath(t *testing.T) {
	t.Setenv("PATH", "")
	if bdOnPath() {
		t.Fatal("expected bdOnPath to return false when PATH is empty")
	}
}

// --- resolveSource ---

func TestResolveSource_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, ".beads", "issues.jsonl")
	got := resolveSource(dir, explicit)
	if got.Mode != data.SourceJSONL {
		t.Fatalf("expected SourceJSONL, got %v", got.Mode)
	}
	if got.Path != explicit {
		t.Fatalf("expected path %q, got %q", explicit, got.Path)
	}
	if !got.Explicit {
		t.Fatal("expected Explicit=true")
	}
}

func TestResolveSource_BeadsDirOnPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(`{"id":"stale-jsonl","title":"stale","status":"open","priority":1,"type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", t.TempDir())
	store, err := beadstore.OpenSQLiteStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := store.Create(context.Background(), beadstore.CreateParams{
		ID:       "mg-store",
		Title:    "store issue",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Setenv("PATH", "")
	got := resolveSource(dir, "")
	if got.Mode != data.SourceCLI {
		t.Fatalf("expected SourceCLI, got %v", got.Mode)
	}
	if got.ProjectDir != dir {
		t.Fatalf("expected ProjectDir %q, got %q", dir, got.ProjectDir)
	}
	if got.Store == nil {
		t.Fatalf("expected store-backed source when bd is absent")
	}
	issues, err := data.FetchActiveIssues(got.Store)
	if err != nil {
		t.Fatalf("FetchActiveIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "mg-store" {
		t.Fatalf("issues = %+v, want mg-store from state db", issues)
	}
}

func TestResolveSource_BeadsFileNoBd(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove bd from PATH so bdOnPath returns false.
	t.Setenv("PATH", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("ORO_HOME", t.TempDir())
	got := resolveSource(dir, "")
	if got.Mode != data.SourceJSONL {
		t.Fatalf("expected SourceJSONL, got %v", got.Mode)
	}
	if got.Path != jsonlPath {
		t.Fatalf("expected path %q, got %q", jsonlPath, got.Path)
	}
}

func TestResolveSource_Nothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("ORO_PROJECT", "")
	got := resolveSource(dir, "")
	if got.Mode != data.SourceJSONL || got.Path != "" {
		t.Fatalf("expected empty SourceJSONL, got %+v", got)
	}
}

func TestResolveSource_UsesOROProjectScopedStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "env-proj")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("PATH", "")
	dbPath := filepath.Join(oroHome, "projects", "env-proj", "state.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := beadstore.OpenSQLiteStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := store.Create(context.Background(), beadstore.CreateParams{
		ID:       "mg-env",
		Title:    "env project issue",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := resolveSource(dir, "")
	if got.Mode != data.SourceCLI || got.Store == nil {
		t.Fatalf("source = %+v, want store-backed SourceCLI", got)
	}
	issues, err := data.FetchActiveIssues(got.Store)
	if err != nil {
		t.Fatalf("FetchActiveIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "mg-env" {
		t.Fatalf("issues = %+v, want mg-env from env-scoped db", issues)
	}
}

func TestResolveSource_StealthProjectStore(t *testing.T) {
	dir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("PATH", "")
	hash, err := projectHash(dir)
	if err != nil {
		t.Fatalf("projectHash: %v", err)
	}
	stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)
	if err := os.MkdirAll(stealthDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "config.yaml"), []byte("mode: stealth\nproject: stealth\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := beadstore.OpenSQLiteStore(context.Background(), filepath.Join(stealthDir, "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := store.Create(context.Background(), beadstore.CreateParams{
		ID:       "mg-stealth",
		Title:    "stealth issue",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := resolveSource(dir, "")
	if got.Mode != data.SourceCLI || got.Store == nil {
		t.Fatalf("source = %+v, want stealth store-backed SourceCLI", got)
	}
	issues, err := data.FetchActiveIssues(got.Store)
	if err != nil {
		t.Fatalf("FetchActiveIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "mg-stealth" {
		t.Fatalf("issues = %+v, want mg-stealth from stealth db", issues)
	}
}

func TestResolveSource_ReportsStoreOpenErrorWithoutJSONLFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORO_DB_PATH", filepath.Join(t.TempDir(), "missing", "state.db"))
	t.Setenv("ORO_PROJECT", "")

	got := resolveSource(dir, "")
	if got.Err == nil {
		t.Fatalf("expected store open error, got %+v", got)
	}
	if _, err := loadInitialIssues(got); err == nil {
		t.Fatal("loadInitialIssues succeeded, want store open error")
	}
}

func TestMgCmd_ReportsStoreOpenError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORO_DB_PATH", filepath.Join(t.TempDir(), "missing", "state.db"))
	t.Setenv("ORO_PROJECT", "")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	cmd := newMgCmd()
	cmd.SetArgs([]string{"--status"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("Execute succeeded, want store open error")
	}
	if !strings.Contains(err.Error(), "open mg bead store") {
		t.Fatalf("error = %v, want store open context", err)
	}
}

func TestResolveSource_GlobalStoreFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("PATH", "")
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(`{"id":"stale-jsonl","title":"stale","status":"open","priority":1,"type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := beadstore.OpenSQLiteStore(context.Background(), filepath.Join(oroHome, "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := store.Create(context.Background(), beadstore.CreateParams{
		ID:       "mg-global",
		Title:    "global issue",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := resolveSource(dir, "")
	if got.Mode != data.SourceCLI || got.Store == nil {
		t.Fatalf("source = %+v, want global store-backed SourceCLI", got)
	}
	issues, err := data.FetchActiveIssues(got.Store)
	if err != nil {
		t.Fatalf("FetchActiveIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "mg-global" {
		t.Fatalf("issues = %+v, want mg-global from global db", issues)
	}
}

// --- loadInitialIssues ---

func TestLoadInitialIssues_JSONL_Success(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "issues*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	line := `{"id":"test-001","title":"Test","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}` + "\n"
	if _, err := fmt.Fprint(f, line); err != nil {
		t.Fatal(err)
	}
	f.Close()

	source := data.Source{Mode: data.SourceJSONL, Path: f.Name()}
	issues, err := loadInitialIssues(source)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

func TestLoadInitialIssues_JSONL_FileError(t *testing.T) {
	source := data.Source{Mode: data.SourceJSONL, Path: "/nonexistent/path/issues.jsonl"}
	_, err := loadInitialIssues(source)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadInitialIssues_JSONL_SkippedLines(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "issues*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	good := `{"id":"test-001","title":"Test","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}` + "\n"
	bad := "not-json\n"
	if _, err := fmt.Fprint(f, good+bad); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Capture stderr to verify the warning is printed.
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stderr = w

	source := data.Source{Mode: data.SourceJSONL, Path: f.Name()}
	issues, loadErr := loadInitialIssues(source)

	w.Close()
	os.Stderr = old
	io.ReadAll(r) //nolint:errcheck
	r.Close()

	if loadErr != nil {
		t.Fatalf("unexpected error: %v", loadErr)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (bad line skipped), got %d", len(issues))
	}
}

func TestLoadInitialIssues_CLI_Error(t *testing.T) {
	t.Setenv("PATH", "")
	source := data.Source{Mode: data.SourceCLI, ProjectDir: t.TempDir()}
	_, err := loadInitialIssues(source)
	if err == nil {
		t.Fatal("expected error when bd not on PATH")
	}
}

// --- newMgCmd RunE integration ---

func TestMgCmd_StatusMode(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "issues*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	line := `{"id":"test-001","title":"Test","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}` + "\n"
	if _, err := fmt.Fprint(f, line); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Suppress stdout for the status line output.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w

	cmd := newMgCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--path", f.Name(), "--status"})
	execErr := cmd.Execute()

	w.Close()
	os.Stdout = old
	io.ReadAll(r) //nolint:errcheck
	r.Close()

	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
}

func TestMgCmd_LoadError(t *testing.T) {
	cmd := newMgCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--path", "/nonexistent/path/issues.jsonl"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent issues file")
	}
}

func TestMgCmd_NoSourceError(t *testing.T) {
	t.Setenv("PATH", "")
	t.Chdir(t.TempDir())

	cmd := newMgCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no source found")
	}
}
