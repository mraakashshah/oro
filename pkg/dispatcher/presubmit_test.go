//nolint:testpackage // scheduler coverage must exercise dispatcher-private coordination seams.
package dispatcher

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
)

func TestPresubmitActionManifest(t *testing.T) {
	repoRoot := t.TempDir()
	for path, content := range map[string]string{
		"go.mod":                     "module example.com/presubmit\n\ngo 1.26\n",
		"requirements.txt":           "pytest\n",
		"scripts/check.sh":           "#!/bin/sh\nexit 0\n",
		"docs/guide.md":              "# Guide\n",
		"pkg/changed/changed.go":     "package changed\n",
		"pkg/dependent/dependent.go": "package dependent\n",
	} {
		fullPath := filepath.Join(repoRoot, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}

	plan, err := BuildPresubmitPlan(context.Background(), RepoSnapshot{
		Root:              repoRoot,
		ChangedPackages:   []string{"./pkg/changed"},
		DependentPackages: []string{"./pkg/dependent"},
	}, PresubmitConfig{
		AcceptanceCommand: "go test ./pkg/changed -run '^TestAcceptance$'",
		AvailableTools: map[string]bool{
			"gofumpt":       true,
			"goimports":     true,
			"golangci-lint": true,
			"nilaway":       true,
			"go":            true,
			"ruff":          true,
			"pylint":        true,
			"pyright":       true,
			"pytest":        true,
			"shfmt":         true,
			"shellcheck":    true,
			"markdownlint":  true,
			"yamllint":      true,
			"biome":         true,
		},
	})
	if err != nil {
		t.Fatalf("BuildPresubmitPlan: %v", err)
	}

	want := []PresubmitAction{
		{Name: "acceptance", ResourceClass: ResourceCPULight},
		{Name: "format", ResourceClass: ResourceCPULight},
		{Name: "hygiene", ResourceClass: ResourceCPULight},
		{Name: "lint", ResourceClass: ResourceMemoryHeavy},
		{Name: "type", ResourceClass: ResourceMemoryHeavy},
		{Name: "build", ResourceClass: ResourceCPUHeavy},
		{Name: "vet", ResourceClass: ResourceCPULight},
		{Name: "changed-package", ResourceClass: ResourceCPUHeavy},
		{Name: "dependent", ResourceClass: ResourceCPUHeavy},
		{Name: "shell", ResourceClass: ResourceCPULight},
		{Name: "docs", ResourceClass: ResourceCPULight},
		{Name: "python-format", ResourceClass: ResourceCPULight},
		{Name: "python-lint", ResourceClass: ResourceMemoryHeavy},
		{Name: "python-type", ResourceClass: ResourceMemoryHeavy},
		{Name: "python-changed-package", ResourceClass: ResourceCPUHeavy},
	}
	got := make([]PresubmitAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		got = append(got, PresubmitAction{Name: action.Name, ResourceClass: action.ResourceClass})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest actions = %#v, want %#v", got, want)
	}
}

func TestBuildPresubmitPlanFailsForMissingTool(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/presubmit\n\ngo 1.26\n"), 0o600); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}

	_, err := BuildPresubmitPlan(context.Background(), RepoSnapshot{Root: repoRoot}, PresubmitConfig{
		AcceptanceCommand: "go test ./...",
		AvailableTools:    map[string]bool{"go": true},
	})
	if err == nil {
		t.Fatal("BuildPresubmitPlan succeeded without required Go formatting tools")
	}
}

func TestBuildPresubmitPlanUsesExplicitEmptyClassWithoutLanguage(t *testing.T) {
	plan, err := BuildPresubmitPlan(context.Background(), RepoSnapshot{Root: t.TempDir()}, PresubmitConfig{})
	if err != nil {
		t.Fatalf("BuildPresubmitPlan: %v", err)
	}
	if !reflect.DeepEqual(plan.Actions, []PresubmitAction{{Name: "empty", ResourceClass: ResourceEmpty}}) {
		t.Fatalf("empty plan actions = %#v", plan.Actions)
	}
}

func TestPresubmitActionScheduler(t *testing.T) {
	started := make(chan string, 8)
	release := map[string]chan struct{}{
		"light-one": make(chan struct{}),
		"light-two": make(chan struct{}),
		"heavy-one": make(chan struct{}),
		"heavy-two": make(chan struct{}),
	}
	d := &Dispatcher{
		presubmitCandidates: make(chan presubmitCandidate, 5),
		presubmitSemaphore: NewQGSemaphore(map[ResourceClass]int{
			ResourceCPULight:    2,
			ResourceCPUHeavy:    1,
			ResourceMemoryHeavy: 1,
			ResourceEmpty:       0,
		}),
		presubmitActionRunner: func(ctx context.Context, action PresubmitAction) error {
			started <- action.Name
			if wait, ok := release[action.Name]; ok {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-wait:
				}
			}
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runPresubmitScheduler(ctx)

	done := make([]chan struct{}, 0, 5)
	submit := func(action PresubmitAction) {
		candidateDone := make(chan struct{})
		done = append(done, candidateDone)
		d.presubmitCandidates <- presubmitCandidate{actions: []PresubmitAction{action}, done: candidateDone}
	}
	submit(PresubmitAction{Name: "light-one", ResourceClass: ResourceCPULight})
	submit(PresubmitAction{Name: "light-two", ResourceClass: ResourceCPULight})
	submit(PresubmitAction{Name: "heavy-one", ResourceClass: ResourceCPUHeavy})

	waitForPresubmitStarts(t, started, map[string]bool{
		"light-one": true,
		"light-two": true,
		"heavy-one": true,
	})
	submit(PresubmitAction{Name: "heavy-two", ResourceClass: ResourceCPUHeavy})
	submit(PresubmitAction{Name: "queued", ResourceClass: ResourceEmpty})
	select {
	case action := <-started:
		t.Fatalf("started %q before capacity was released", action)
	case <-time.After(50 * time.Millisecond):
	}

	close(release["light-one"])
	close(release["light-two"])
	close(release["heavy-one"])
	waitForPresubmitStarts(t, started, map[string]bool{"heavy-two": true})
	close(release["heavy-two"])

	cancel()
	for _, candidateDone := range done {
		select {
		case <-candidateDone:
		case <-time.After(time.Second):
			t.Fatal("presubmit action did not release its scheduler permit on cancellation")
		}
	}
}

func TestQGSemaphoreWrapsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewQGSemaphore(map[ResourceClass]int{ResourceCPULight: 0}).Acquire(ctx, ResourceCPULight)
	if err == nil || err.Error() != "acquire presubmit capacity: context canceled" {
		t.Fatalf("Acquire cancellation error = %v", err)
	}
}

func TestPresubmitEvidenceIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "presubmit-evidence.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(ctx, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	actions := []PresubmitAction{
		{Name: "acceptance", Command: "go test ./pkg/dispatcher -run TestAcceptance", ResourceClass: ResourceCPULight},
		{Name: "lint", Command: "golangci-lint run ./pkg/dispatcher", ResourceClass: ResourceMemoryHeavy},
	}
	const (
		candidateSHA = "candidate-abc123"
		baseSHA      = "base-def456"
		profile      = "go-v1"
		toolHash     = "tools-789"
		startedAt    = "2026-07-20T12:00:00.123456789Z"
		completedAt  = "2026-07-20T12:00:01.987654321Z"
	)

	type evidenceCase struct {
		name       string
		missing    bool
		mutateLast func(*PresubmitResult)
		wantPass   bool
	}
	cases := []evidenceCase{
		{name: "complete exact plan", wantPass: true},
		{name: "missing action", missing: true},
		{name: "stale candidate", mutateLast: func(result *PresubmitResult) { result.CandidateSHA = "stale-candidate" }},
		{name: "stale base", mutateLast: func(result *PresubmitResult) { result.BaseSHA = "stale-base" }},
		{name: "stale command", mutateLast: func(result *PresubmitResult) { result.Command += " --config stale" }},
		{name: "stale profile", mutateLast: func(result *PresubmitResult) { result.Profile = "go-v0" }},
		{name: "stale tool inventory", mutateLast: func(result *PresubmitResult) { result.ToolHash = "tools-old" }},
		{name: "stale resource class", mutateLast: func(result *PresubmitResult) { result.ResourceClass = ResourceCPUHeavy }},
		{name: "skipped action", mutateLast: func(result *PresubmitResult) { result.Outcome = "skipped" }},
		{name: "cancelled action", mutateLast: func(result *PresubmitResult) { result.Outcome = "cancelled" }},
		{name: "failed action", mutateLast: func(result *PresubmitResult) { result.Outcome = "failed" }},
	}

	for caseIndex, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := RemoteGateCandidate{
				Key:          "oro-kw5r:" + testCase.name,
				BeadID:       "oro-kw5r",
				AssignmentID: int64(caseIndex + 1),
				CandidateSHA: candidateSHA,
				BaseSHA:      baseSHA,
				TargetBranch: "epic/oro-10jk",
				AdoptionRef:  "refs/oro/adopted/oro/oro-kw5r/" + testCase.name,
			}
			gate, err := store.AdoptCandidate(ctx, candidate)
			if err != nil {
				t.Fatalf("AdoptCandidate: %v", err)
			}
			plan := PresubmitEvidencePlan{
				GateID:       gate.ID,
				CandidateSHA: candidateSHA,
				BaseSHA:      baseSHA,
				Profile:      profile,
				ToolHash:     toolHash,
				Actions:      actions,
			}
			results := make([]PresubmitResult, 0, len(actions))
			for _, action := range actions {
				results = append(results, PresubmitResult{
					GateID:        gate.ID,
					ActionName:    action.Name,
					CandidateSHA:  candidateSHA,
					BaseSHA:       baseSHA,
					Command:       action.Command,
					Profile:       profile,
					ToolHash:      toolHash,
					StartedAt:     startedAt,
					CompletedAt:   completedAt,
					Outcome:       "passed",
					Logs:          strings.Repeat("x", maxPresubmitLogBytes+128),
					ResourceClass: action.ResourceClass,
				})
			}
			if testCase.missing {
				results = results[:len(results)-1]
			} else if testCase.mutateLast != nil {
				testCase.mutateLast(&results[len(results)-1])
			}
			for _, result := range results {
				if err := store.RecordPresubmitResult(ctx, result); err != nil {
					t.Fatalf("RecordPresubmitResult(%s): %v", result.ActionName, err)
				}
			}

			if testCase.wantPass {
				duplicate := results[0]
				duplicate.StartedAt = "2026-07-20T13:00:00Z"
				duplicate.CompletedAt = "2026-07-20T13:00:01Z"
				duplicate.Outcome = "cancelled"
				duplicate.Logs = "duplicate must not overwrite completion"
				if err := store.RecordPresubmitResult(ctx, duplicate); err != nil {
					t.Fatalf("duplicate RecordPresubmitResult: %v", err)
				}
				assertPersistedPresubmitEvidence(ctx, t, db, gate.ID, startedAt, completedAt)
			}

			passed, err := store.PresubmitPlanPassed(ctx, plan)
			if err != nil {
				t.Fatalf("PresubmitPlanPassed: %v", err)
			}
			if passed != testCase.wantPass {
				t.Fatalf("PresubmitPlanPassed = %t, want %t", passed, testCase.wantPass)
			}
		})
	}

	t.Run("rejects invalid timestamps", func(t *testing.T) {
		invalid := PresubmitResult{
			GateID:        1,
			ActionName:    "acceptance",
			CandidateSHA:  candidateSHA,
			BaseSHA:       baseSHA,
			Command:       actions[0].Command,
			Profile:       profile,
			ToolHash:      toolHash,
			StartedAt:     "not-a-timestamp",
			CompletedAt:   completedAt,
			Outcome:       "passed",
			ResourceClass: actions[0].ResourceClass,
		}
		if err := store.RecordPresubmitResult(ctx, invalid); err == nil {
			t.Fatal("RecordPresubmitResult accepted an invalid start timestamp")
		}
		invalid.StartedAt = completedAt
		invalid.CompletedAt = startedAt
		if err := store.RecordPresubmitResult(ctx, invalid); err == nil {
			t.Fatal("RecordPresubmitResult accepted completion before start")
		}
	})
}

func TestPresubmitPostRebaseInvalidation(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "post-rebase-presubmit.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(ctx, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	actions := []PresubmitAction{
		{Name: "acceptance", Command: "go test ./pkg/dispatcher -run TestAcceptance", ResourceClass: ResourceCPULight},
		{Name: "lint", Command: "golangci-lint run ./pkg/dispatcher", ResourceClass: ResourceMemoryHeavy},
	}
	initial := RemoteGateCandidate{
		Key:          "oro-4vh0:initial",
		BeadID:       "oro-4vh0",
		AssignmentID: 41,
		CandidateSHA: "candidate-before-rebase",
		BaseSHA:      "base-before-rebase",
		TargetBranch: "main",
		AdoptionRef:  "refs/oro/adopted/oro/oro-4vh0/41",
	}
	initialGate, err := store.AdoptCandidate(ctx, initial)
	if err != nil {
		t.Fatalf("AdoptCandidate(initial): %v", err)
	}
	initialPlan := PresubmitEvidencePlan{
		GateID:       initialGate.ID,
		CandidateSHA: initial.CandidateSHA,
		BaseSHA:      initial.BaseSHA,
		Profile:      "go-v1",
		ToolHash:     "tools-v1",
		Actions:      actions,
	}
	for _, action := range actions {
		if err := store.RecordPresubmitResult(ctx, PresubmitResult{
			GateID:        initialGate.ID,
			ActionName:    action.Name,
			CandidateSHA:  initial.CandidateSHA,
			BaseSHA:       initial.BaseSHA,
			Command:       action.Command,
			Profile:       initialPlan.Profile,
			ToolHash:      initialPlan.ToolHash,
			StartedAt:     "2026-07-20T12:00:00Z",
			CompletedAt:   "2026-07-20T12:00:01Z",
			Outcome:       "passed",
			ResourceClass: action.ResourceClass,
		}); err != nil {
			t.Fatalf("RecordPresubmitResult(%s): %v", action.Name, err)
		}
	}
	if passed, err := store.PresubmitPlanPassed(ctx, initialPlan); err != nil || !passed {
		t.Fatalf("initial PresubmitPlanPassed = %t, %v; want true, nil", passed, err)
	}

	postRebase := initial
	postRebase.Key = "oro-4vh0:post-rebase"
	postRebase.CandidateSHA = "candidate-after-rebase"
	postRebase.BaseSHA = "base-after-rebase"
	postRebase.AdoptionRef = "refs/oro/adopted/oro/oro-4vh0/41-rebased"
	postRebaseGate, err := store.AdoptCandidate(ctx, postRebase)
	if err != nil {
		t.Fatalf("AdoptCandidate(post-rebase): %v", err)
	}
	postRebasePlan := PresubmitEvidencePlan{
		GateID:       postRebaseGate.ID,
		CandidateSHA: postRebase.CandidateSHA,
		BaseSHA:      postRebase.BaseSHA,
		Profile:      initialPlan.Profile,
		ToolHash:     initialPlan.ToolHash,
		Actions:      actions,
	}

	if passed, err := store.PresubmitPlanPassed(ctx, postRebasePlan); err != nil || passed {
		t.Fatalf("stale pre-rebase evidence admitted post-rebase plan = %t, %v; want false, nil", passed, err)
	}

	for _, action := range actions[:1] {
		if err := store.RecordPresubmitResult(ctx, PresubmitResult{
			GateID:        postRebaseGate.ID,
			ActionName:    action.Name,
			CandidateSHA:  postRebase.CandidateSHA,
			BaseSHA:       postRebase.BaseSHA,
			Command:       action.Command,
			Profile:       postRebasePlan.Profile,
			ToolHash:      postRebasePlan.ToolHash,
			StartedAt:     "2026-07-20T12:01:00Z",
			CompletedAt:   "2026-07-20T12:01:01Z",
			Outcome:       "passed",
			ResourceClass: action.ResourceClass,
		}); err != nil {
			t.Fatalf("RecordPresubmitResult(%s): %v", action.Name, err)
		}
	}
	if passed, err := store.PresubmitPlanPassed(ctx, postRebasePlan); err != nil || passed {
		t.Fatalf("partial post-rebase evidence admitted plan = %t, %v; want false, nil", passed, err)
	}

	for _, action := range actions[1:] {
		if err := store.RecordPresubmitResult(ctx, PresubmitResult{
			GateID:        postRebaseGate.ID,
			ActionName:    action.Name,
			CandidateSHA:  postRebase.CandidateSHA,
			BaseSHA:       postRebase.BaseSHA,
			Command:       action.Command,
			Profile:       postRebasePlan.Profile,
			ToolHash:      postRebasePlan.ToolHash,
			StartedAt:     "2026-07-20T12:01:00Z",
			CompletedAt:   "2026-07-20T12:01:01Z",
			Outcome:       "passed",
			ResourceClass: action.ResourceClass,
		}); err != nil {
			t.Fatalf("RecordPresubmitResult(%s): %v", action.Name, err)
		}
	}
	if passed, err := store.PresubmitPlanPassed(ctx, postRebasePlan); err != nil || !passed {
		t.Fatalf("complete post-rebase evidence admitted plan = %t, %v; want true, nil", passed, err)
	}
}

func assertPersistedPresubmitEvidence(
	ctx context.Context,
	t *testing.T,
	db *sql.DB,
	gateID int64,
	wantStartedAt, wantCompletedAt string,
) {
	t.Helper()
	var (
		rowCount               int
		startedAt, completedAt string
		outcome                string
		logBytes               int
	)
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_gate_presubmit_results WHERE gate_id = ?`, gateID).Scan(&rowCount); err != nil {
		t.Fatalf("count presubmit evidence: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("presubmit evidence rows = %d, want 2 after duplicate completion", rowCount)
	}
	if err := db.QueryRowContext(ctx, `
SELECT started_at, completed_at, outcome, length(logs)
FROM remote_gate_presubmit_results
WHERE gate_id = ? AND action_name = 'acceptance'`, gateID).Scan(&startedAt, &completedAt, &outcome, &logBytes); err != nil {
		t.Fatalf("load persisted presubmit evidence: %v", err)
	}
	if startedAt != wantStartedAt || completedAt != wantCompletedAt {
		t.Fatalf("persisted timestamps = %q..%q, want %q..%q", startedAt, completedAt, wantStartedAt, wantCompletedAt)
	}
	if outcome != "passed" {
		t.Fatalf("persisted duplicate outcome = %q, want first completion passed", outcome)
	}
	if logBytes != maxPresubmitLogBytes {
		t.Fatalf("persisted logs = %d bytes, want bounded prefix of %d", logBytes, maxPresubmitLogBytes)
	}
}

func waitForPresubmitStarts(t *testing.T, started <-chan string, want map[string]bool) {
	t.Helper()
	timeout := time.After(time.Second)
	for len(want) > 0 {
		select {
		case got := <-started:
			if !want[got] {
				t.Fatalf("started unexpected action %q", got)
			}
			delete(want, got)
		case <-timeout:
			t.Fatalf("timed out waiting for actions %v", want)
		}
	}
}
