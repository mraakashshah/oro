package dispatcher //nolint:testpackage // white-box test pins the janitor orchestration boundary

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/janitor"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestJanitorSpawnFlow(t *testing.T) {
	assertJanitorSpawnSignature((*Dispatcher).spawnJanitor)

	t.Run("detectors feed one triage spawn and only triaged findings are filed", func(t *testing.T) {
		d, beads, worktrees, _, _, spawner := newTestDispatcher(t)
		source, scan := janitorSpawnFixture(t, `#!/usr/bin/env bash
printf '%s\n' '{"detector":"todo","file":"legacy.go","line":1,"title":"deterministic candidate","detail":"TODO remove legacy path"}'
printf '%s\n' 'skipped-detector-output'
`)
		d.repoRoot = source
		d.cfg.DefaultBranch = "main"
		beads.beads = []protocol.Bead{{ID: "oro-open", Title: "Existing cleanup task", Status: "open"}}
		worktrees.createFn = func(context.Context, string, string) (string, string, error) {
			return scan, "agent/janitor-scan", nil
		}
		spawner.verdict = `[{
			"severity":"important",
			"category":"todo",
			"title":"triaged legacy cleanup",
			"detail":"remove the candidate-backed legacy path",
			"evidence":[{"file":"legacy.go","line_start":1,"line_end":1,"quote":"TODO remove legacy path"}],
			"confidence":95,
			"sources":["todo"],
			"origin":"pre_existing"
		}]`

		d.spawnJanitor(context.Background(), nil)

		if got := spawner.SpawnCount(); got != 1 {
			t.Fatalf("janitor triage spawns = %d, want 1", got)
		}
		spawner.mu.Lock()
		spawn := spawner.spawns[0]
		spawner.mu.Unlock()
		for _, want := range []string{"deterministic candidate", "Existing cleanup task", "Finding JSON ONLY"} {
			if !strings.Contains(spawn.prompt, want) {
				t.Errorf("janitor prompt missing %q:\n%s", want, spawn.prompt)
			}
		}
		if spawn.workdir != scan {
			t.Errorf("janitor triage workdir = %q, want %q", spawn.workdir, scan)
		}

		beads.mu.Lock()
		created := append([]createCall(nil), beads.created...)
		journey := append([]beadstore.JourneyEvent(nil), beads.journeys["oro-new1"]...)
		beads.mu.Unlock()
		if got := countCreateCallsWithMetadata(created, cleanlinessRoleMetadataKey); got != 1 {
			t.Fatalf("janitor role creates = %d, want 1: %#v", got, created)
		}
		if got := countCreateCallsWithMetadata(created, janitorFindingMetadataKey); got != 1 {
			t.Fatalf("janitor finding creates = %d, want 1: %#v", got, created)
		}
		for _, call := range created {
			if call.metadata[janitorFindingMetadataKey] == "" {
				continue
			}
			if call.title != "triaged legacy cleanup" {
				t.Fatalf("filed finding title = %q, want triaged output", call.title)
			}
		}
		if !janitorJourneyHasSkipped(journey, "skipped-detector-output") {
			t.Fatalf("janitor journey = %#v, want skipped detector output", journey)
		}
	})

	t.Run("overlapping spawns serialize", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		var active atomic.Int32
		var maximum atomic.Int32
		hook := func(context.Context) {
			current := active.Add(1)
			for current > maximum.Load() {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			entered <- struct{}{}
			<-release
			active.Add(-1)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			d.spawnJanitor(context.Background(), hook)
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first janitor spawn did not enter")
		}
		go func() {
			defer wg.Done()
			d.spawnJanitor(context.Background(), hook)
		}()
		select {
		case <-entered:
			t.Fatal("second janitor spawn entered before the first completed")
		case <-time.After(150 * time.Millisecond):
		}
		close(release)
		wg.Wait()
		if got := maximum.Load(); got != 1 {
			t.Fatalf("maximum concurrent janitor spawns = %d, want 1", got)
		}
	})

	for _, stage := range []string{"worktree", "detector", "llm"} {
		t.Run(stage+" failure records the role journey and restores cadence", func(t *testing.T) {
			d, beads, worktrees, _, _, spawner := newTestDispatcher(t)
			d.cfg.JanitorInterval = 50
			d.cfg.AuditEnabled = true
			d.cfg.AuditEveryNJanitors = 5
			d.janitorRunsSinceAudit = 1

			script := `#!/usr/bin/env bash
printf '%s\n' '{"detector":"todo","file":"legacy.go","line":1,"title":"candidate","detail":"TODO remove legacy path"}'
`
			if stage == "detector" {
				script = "#!/usr/bin/env bash\necho detector-failed >&2\nexit 7\n"
			}
			source, scan := janitorSpawnFixture(t, script)
			d.repoRoot = source
			worktrees.createFn = func(context.Context, string, string) (string, string, error) {
				if stage == "worktree" {
					return "", "", errors.New("worktree unavailable")
				}
				return scan, "agent/janitor-scan", nil
			}
			if stage == "llm" {
				spawner.spawnErr = errors.New("triage unavailable")
			}

			d.spawnJanitor(context.Background(), nil)

			d.mu.Lock()
			merges := d.mergesSinceJanitor
			auditRuns := d.janitorRunsSinceAudit
			d.mu.Unlock()
			if merges != 50 || auditRuns != 0 {
				t.Fatalf("restored cadence = merges:%d audit:%d, want 50 and 0", merges, auditRuns)
			}
			if got := countCreatedJanitorFindings(beads); got != 0 {
				t.Fatalf("filed findings after %s failure = %d, want 0", stage, got)
			}
			beads.mu.Lock()
			journey := append([]beadstore.JourneyEvent(nil), beads.journeys["oro-new1"]...)
			beads.mu.Unlock()
			if !journeyHasEvent(journey, "note") {
				t.Fatalf("janitor journey after %s failure = %#v, want note", stage, journey)
			}
		})
	}
}

func TestJanitorTriageEvidenceValidation(t *testing.T) {
	worktree := t.TempDir()
	writeJanitorEvidenceFixture(t, worktree, "code.go", "package fixture\n// TODO remove legacy path\n")
	writeJanitorEvidenceFixture(t, worktree, "assets/orphan.svg", "<svg></svg>\n")
	candidates := []janitor.Candidate{
		{Detector: "todo", File: "code.go", Line: 2},
		{Detector: "orphan-files", File: "assets/orphan.svg"},
	}
	base := ops.Finding{
		Severity: ops.SevImportant, Category: "todo", Title: "remove legacy path",
		Detail: "remove the candidate-backed legacy path", Confidence: 95,
		Evidence: []ops.Evidence{{File: "code.go", LineStart: 2, LineEnd: 2, Quote: "TODO remove legacy path"}},
		Sources:  []string{"todo"}, Origin: "pre_existing",
	}

	tests := []struct {
		name    string
		mutate  func(*ops.Finding)
		wantErr bool
	}{
		{name: "matching candidate"},
		{name: "wrong file", mutate: func(f *ops.Finding) { f.Evidence[0].File = "other.go" }, wantErr: true},
		{name: "wrong line", mutate: func(f *ops.Finding) { f.Evidence[0].LineStart, f.Evidence[0].LineEnd = 1, 1 }, wantErr: true},
		{name: "wrong quote", mutate: func(f *ops.Finding) { f.Evidence[0].Quote = "invented quote" }, wantErr: true},
		{name: "wrong source", mutate: func(f *ops.Finding) { f.Sources = []string{"orphan-files"} }, wantErr: true},
		{name: "path traversal", mutate: func(f *ops.Finding) { f.Evidence[0].File = "../code.go" }, wantErr: true},
		{name: "absolute path", mutate: func(f *ops.Finding) { f.Evidence[0].File = filepath.Join(worktree, "code.go") }, wantErr: true},
		{name: "matching file-only candidate", mutate: func(f *ops.Finding) {
			f.Evidence = []ops.Evidence{{File: "assets/orphan.svg"}}
			f.Sources = []string{"orphan-files"}
		}},
		{name: "file-only candidate with line", mutate: func(f *ops.Finding) {
			f.Evidence = []ops.Evidence{{File: "assets/orphan.svg", LineStart: 1, LineEnd: 1, Quote: "<svg>"}}
			f.Sources = []string{"orphan-files"}
		}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finding := base
			finding.Evidence = append([]ops.Evidence(nil), base.Evidence...)
			finding.Sources = append([]string(nil), base.Sources...)
			if tt.mutate != nil {
				tt.mutate(&finding)
			}
			raw, err := json.Marshal([]ops.Finding{finding})
			if err != nil {
				t.Fatalf("marshal finding: %v", err)
			}
			got, err := parseJanitorTriage(string(raw), candidates, worktree)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseJanitorTriage() = %#v, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJanitorTriage() error = %v", err)
			}
			if len(got) != 1 || got[0].ID == "" {
				t.Fatalf("parseJanitorTriage() = %#v, want one finding with ID", got)
			}
		})
	}
}

func writeJanitorEvidenceFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write evidence fixture: %v", err)
	}
}

func countCreateCallsWithMetadata(calls []createCall, key string) int {
	count := 0
	for _, call := range calls {
		if call.metadata[key] != "" {
			count++
		}
	}
	return count
}

func countCreatedJanitorFindings(beads *fakeBeadStore) int {
	beads.mu.Lock()
	defer beads.mu.Unlock()
	return countCreateCallsWithMetadata(beads.created, janitorFindingMetadataKey)
}

func journeyHasEvent(events []beadstore.JourneyEvent, name string) bool {
	for _, event := range events {
		if event.Actor == janitorRoleActor && event.Event == name {
			return true
		}
	}
	return false
}

func janitorSpawnFixture(t *testing.T, script string) (source, scan string) {
	t.Helper()
	source = t.TempDir()
	scriptPath := filepath.Join(source, janitorDetectScriptPath)
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o750); err != nil {
		t.Fatalf("create detector directory: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o750); err != nil {
		t.Fatalf("write detector script: %v", err)
	}
	scan = t.TempDir()
	if err := os.WriteFile(filepath.Join(scan, "legacy.go"), []byte("// TODO remove legacy path\n"), 0o600); err != nil {
		t.Fatalf("write scan candidate: %v", err)
	}
	return source, scan
}

func assertJanitorSpawnSignature(_ func(*Dispatcher, context.Context, func(context.Context))) {}
