//nolint:testpackage // scheduler coverage must exercise dispatcher-private coordination seams.
package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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
