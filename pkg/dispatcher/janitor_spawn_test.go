package dispatcher //nolint:testpackage // white-box test pins the janitor orchestration boundary

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/beadstore"
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
