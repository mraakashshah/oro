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

func TestJanitorPersistenceFailureRestoresCadence(t *testing.T) {
	tests := []struct {
		name           string
		appendErrEvent string
		createErr      bool
		wantMerges     uint64
		wantFiled      int
	}{
		{name: "finding journey", appendErrEvent: "janitor_finding", wantMerges: 7},
		{name: "finding create", createErr: true, wantMerges: 7},
		{name: "cycle journey", appendErrEvent: "janitor_cycle", wantMerges: 7, wantFiled: 1},
		{name: "success", wantFiled: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, store := janitorPersistenceFixture(t)
			faults := &faultingJanitorStore{
				fakeBeadStore:  store,
				appendErrEvent: tc.appendErrEvent,
			}
			if tc.createErr {
				faults.findingCreateErr = errors.New("finding create unavailable")
			}
			d.beads = faults

			d.spawnJanitor(context.Background(), nil)

			assertJanitorPersistenceOutcome(t, d, store, tc.wantMerges, tc.wantFiled)
		})
	}
}

func janitorPersistenceFixture(t *testing.T) (*Dispatcher, *fakeBeadStore) {
	t.Helper()
	d, store, worktrees, _, _, spawner := newTestDispatcher(t)
	d.cfg.JanitorInterval = 7
	source, scan := janitorSpawnFixture(t, `#!/usr/bin/env bash
printf '%s\n' '{"detector":"todo","file":"legacy.go","line":1,"title":"candidate","detail":"TODO remove legacy path"}'
`)
	d.repoRoot = source
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
	return d, store
}

func assertJanitorPersistenceOutcome(
	t *testing.T,
	d *Dispatcher,
	store *fakeBeadStore,
	wantMerges uint64,
	wantFiled int,
) {
	t.Helper()
	d.mu.Lock()
	merges := d.mergesSinceJanitor
	d.mu.Unlock()
	if merges != wantMerges {
		t.Fatalf("mergesSinceJanitor = %d, want %d", merges, wantMerges)
	}
	if got := countCreatedJanitorFindings(store); got != wantFiled {
		t.Fatalf("durably filed janitor findings = %d, want %d", got, wantFiled)
	}

	store.mu.Lock()
	journey := append([]beadstore.JourneyEvent(nil), store.journeys["oro-new1"]...)
	store.mu.Unlock()
	if wantMerges > 0 {
		if got := eventCount(t, d.db, "janitor_scan_failed"); got != 1 {
			t.Fatalf("janitor_scan_failed events = %d, want 1", got)
		}
		if !journeyHasEvent(journey, "note") {
			t.Fatalf("janitor failure journey = %#v, want failure note", journey)
		}
	}
	if cycle, ok := janitorCycleFiledCount(t, journey); ok && cycle != wantFiled {
		t.Fatalf("janitor cycle filed = %d, want successful creates %d", cycle, wantFiled)
	}
}

func janitorCycleFiledCount(t *testing.T, events []beadstore.JourneyEvent) (int, bool) {
	t.Helper()
	for _, event := range events {
		if event.Actor != janitorRoleActor || event.Event != "janitor_cycle" {
			continue
		}
		var payload struct {
			Filed int `json:"filed"`
		}
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			t.Fatalf("parse janitor cycle journey: %v", err)
		}
		return payload.Filed, true
	}
	return 0, false
}

type faultingJanitorStore struct {
	*fakeBeadStore
	appendErrEvent   string
	findingCreateErr error
}

func (s *faultingJanitorStore) Create(
	ctx context.Context,
	params beadstore.CreateParams,
) (*protocol.Bead, error) {
	if params.Metadata[janitorFindingMetadataKey] != "" && s.findingCreateErr != nil {
		return nil, s.findingCreateErr
	}
	return s.fakeBeadStore.Create(ctx, params)
}

func (s *faultingJanitorStore) AppendJourney(
	ctx context.Context,
	beadID string,
	event beadstore.JourneyEvent,
) error {
	if event.Event == s.appendErrEvent {
		return errors.New("journey persistence unavailable")
	}
	return s.fakeBeadStore.AppendJourney(ctx, beadID, event)
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

func TestJanitorRejectsModelStatus(t *testing.T) {
	worktree := t.TempDir()
	writeJanitorEvidenceFixture(t, worktree, "code.go", "package fixture\n// TODO remove legacy path\n")
	candidates := []janitor.Candidate{{Detector: "todo", File: "code.go", Line: 2}}

	tests := []struct {
		name   string
		mutate func(*ops.Finding)
	}{
		{name: "status", mutate: func(f *ops.Finding) { f.Status = "wont-fix" }},
		{name: "history", mutate: func(f *ops.Finding) {
			f.History = []ops.FindingHistoryEntry{{Status: "wont-fix", Actor: "model"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finding := ops.Finding{
				Severity: ops.SevImportant, Category: "todo", Title: "remove legacy path",
				Detail: "remove the candidate-backed legacy path", Confidence: 95,
				Evidence: []ops.Evidence{{File: "code.go", LineStart: 2, LineEnd: 2, Quote: "TODO remove legacy path"}},
				Sources:  []string{"todo"}, Origin: "pre_existing",
			}
			tt.mutate(&finding)
			raw, err := json.Marshal([]ops.Finding{finding})
			if err != nil {
				t.Fatalf("marshal finding: %v", err)
			}
			if got, err := parseJanitorTriage(string(raw), candidates, worktree); err == nil {
				t.Fatalf("parseJanitorTriage() = %#v, nil; want model lifecycle state rejected", got)
			}
		})
	}
}

func TestJanitorTriageFallbackEvidence(t *testing.T) {
	worktree := t.TempDir()
	writeJanitorEvidenceFixture(t, worktree, "pyproject.toml", "[project]\nname = 'fixture'\n")
	writeJanitorEvidenceFixture(t, worktree, "unused.py", "def live(): pass\ndef unused(): pass\n")
	binDir := t.TempDir()
	writeJanitorEvidenceFixture(t, binDir, "vulture", "#!/bin/sh\nprintf '%s\\n' \"unused.py:2: unused function 'unused' (60% confidence)\"\n")
	if err := os.Chmod(filepath.Join(binDir, "vulture"), 0o700); err != nil {
		t.Fatalf("make vulture executable: %v", err)
	}
	t.Setenv("PATH", binDir)

	candidates, _, _, err := janitor.RunBuiltins(t.Context(), worktree, "", janitor.WithDirectExecutionForTest())
	if err != nil {
		t.Fatalf("run fallback detectors: %v", err)
	}
	finding := ops.Finding{
		Severity: ops.SevMinor, Category: "dead-code", Title: "remove unused function",
		Detail: "remove the candidate-backed unused function", Confidence: 90,
		Evidence: []ops.Evidence{{File: "unused.py", LineStart: 2, LineEnd: 2, Quote: "def unused"}},
		Sources:  []string{"vulture"}, Origin: "pre_existing",
	}
	raw, err := json.Marshal([]ops.Finding{finding})
	if err != nil {
		t.Fatalf("marshal fallback finding: %v", err)
	}
	findings, err := parseJanitorTriage(string(raw), candidates, worktree)
	if err != nil {
		t.Fatalf("validate fallback evidence: %v (candidates: %#v)", err, candidates)
	}
	if len(findings) != 1 || findings[0].ID == "" {
		t.Fatalf("validated findings = %#v, want one assigned finding", findings)
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
