package dispatcher_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"oro/pkg/dispatcher"
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

	plan, err := dispatcher.BuildPresubmitPlan(context.Background(), dispatcher.RepoSnapshot{
		Root:              repoRoot,
		ChangedPackages:   []string{"./pkg/changed"},
		DependentPackages: []string{"./pkg/dependent"},
	}, dispatcher.PresubmitConfig{
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

	want := []dispatcher.PresubmitAction{
		{Name: "acceptance", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "format", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "hygiene", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "lint", ResourceClass: dispatcher.ResourceMemoryHeavy},
		{Name: "type", ResourceClass: dispatcher.ResourceMemoryHeavy},
		{Name: "build", ResourceClass: dispatcher.ResourceCPUHeavy},
		{Name: "vet", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "changed-package", ResourceClass: dispatcher.ResourceCPUHeavy},
		{Name: "dependent", ResourceClass: dispatcher.ResourceCPUHeavy},
		{Name: "shell", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "docs", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "python-format", ResourceClass: dispatcher.ResourceCPULight},
		{Name: "python-lint", ResourceClass: dispatcher.ResourceMemoryHeavy},
		{Name: "python-type", ResourceClass: dispatcher.ResourceMemoryHeavy},
		{Name: "python-changed-package", ResourceClass: dispatcher.ResourceCPUHeavy},
	}
	got := make([]dispatcher.PresubmitAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		got = append(got, dispatcher.PresubmitAction{Name: action.Name, ResourceClass: action.ResourceClass})
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

	_, err := dispatcher.BuildPresubmitPlan(context.Background(), dispatcher.RepoSnapshot{Root: repoRoot}, dispatcher.PresubmitConfig{
		AcceptanceCommand: "go test ./...",
		AvailableTools:    map[string]bool{"go": true},
	})
	if err == nil {
		t.Fatal("BuildPresubmitPlan succeeded without required Go formatting tools")
	}
}

func TestBuildPresubmitPlanUsesExplicitEmptyClassWithoutLanguage(t *testing.T) {
	plan, err := dispatcher.BuildPresubmitPlan(context.Background(), dispatcher.RepoSnapshot{Root: t.TempDir()}, dispatcher.PresubmitConfig{})
	if err != nil {
		t.Fatalf("BuildPresubmitPlan: %v", err)
	}
	if !reflect.DeepEqual(plan.Actions, []dispatcher.PresubmitAction{{Name: "empty", ResourceClass: dispatcher.ResourceEmpty}}) {
		t.Fatalf("empty plan actions = %#v", plan.Actions)
	}
}
