package dispatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResourceClass identifies the scheduler capacity required by a presubmit action.
//
//oro:testonly
type ResourceClass string

const (
	// ResourceEmpty identifies a repository with no supported language profile.
	//
	//oro:testonly
	ResourceEmpty ResourceClass = "empty"
	// ResourceCPULight is for short, independently parallelizable checks.
	//
	//oro:testonly
	ResourceCPULight ResourceClass = "cpu_light"
	// ResourceCPUHeavy is for compilation and test execution.
	//
	//oro:testonly
	ResourceCPUHeavy ResourceClass = "cpu_heavy"
	// ResourceMemoryHeavy is for whole-program static analysis.
	//
	//oro:testonly
	ResourceMemoryHeavy ResourceClass = "memory_heavy"
)

// RepoSnapshot is the immutable repository state used to construct a presubmit plan.
//
//oro:testonly
type RepoSnapshot struct {
	Root              string
	ChangedPackages   []string
	DependentPackages []string
}

// PresubmitConfig supplies the caller-probed tool inventory and acceptance command.
// A non-empty language profile requires a complete AvailableTools inventory; the
// planner deliberately does not treat an unavailable tool as an optional action.
//
//oro:testonly
type PresubmitConfig struct {
	AcceptanceCommand string
	AvailableTools    map[string]bool
}

// PresubmitAction is one independently schedulable local validation action.
//
//oro:testonly
type PresubmitAction struct {
	Name          string
	Command       string
	ResourceClass ResourceClass
	RequiredTools []string
}

// PresubmitPlan is a deterministic manifest. It contains no timeout because
// elapsed time is an observation for the scheduler, never a passing policy.
//
//oro:testonly
type PresubmitPlan struct {
	Actions []PresubmitAction
}

// BuildPresubmitPlan constructs the local presubmit manifest for a repository.
// It does not execute actions. Missing required commands or tools fail while
// constructing the plan so an incomplete manifest cannot be admitted.
//
//oro:testonly
func BuildPresubmitPlan(_ context.Context, repo RepoSnapshot, cfg PresubmitConfig) (PresubmitPlan, error) {
	if strings.TrimSpace(repo.Root) == "" {
		return PresubmitPlan{}, fmt.Errorf("presubmit repository root is required")
	}

	goProject := fileExists(filepath.Join(repo.Root, "go.mod"))
	pythonProject := fileExists(filepath.Join(repo.Root, "pyproject.toml")) ||
		fileExists(filepath.Join(repo.Root, "setup.py")) || fileExists(filepath.Join(repo.Root, "requirements.txt"))
	if !goProject && !pythonProject {
		return PresubmitPlan{Actions: []PresubmitAction{{Name: "empty", ResourceClass: ResourceEmpty}}}, nil
	}

	actions := []PresubmitAction{
		{
			Name:          "acceptance",
			Command:       strings.TrimSpace(cfg.AcceptanceCommand),
			ResourceClass: ResourceCPULight,
		},
	}
	if actions[0].Command == "" {
		return PresubmitPlan{}, fmt.Errorf("presubmit action acceptance is required")
	}

	if goProject {
		actions = append(actions, PresubmitAction{Name: "format", Command: "gofumpt -l . && goimports -l .", ResourceClass: ResourceCPULight, RequiredTools: []string{"gofumpt", "goimports"}})
	}
	actions = append(actions, PresubmitAction{
		Name:          "hygiene",
		Command:       "make stage-assets && git diff --exit-code",
		ResourceClass: ResourceCPULight,
	})

	if goProject {
		actions = append(actions,
			PresubmitAction{Name: "lint", Command: "golangci-lint run ./cmd/... ./internal/... ./pkg/...", ResourceClass: ResourceMemoryHeavy, RequiredTools: []string{"golangci-lint"}},
			PresubmitAction{Name: "type", Command: "nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/...", ResourceClass: ResourceMemoryHeavy, RequiredTools: []string{"nilaway"}},
			PresubmitAction{Name: "build", Command: "go build -buildvcs=false ./cmd/... ./internal/... ./pkg/...", ResourceClass: ResourceCPUHeavy, RequiredTools: []string{"go"}},
			PresubmitAction{Name: "vet", Command: "go vet ./cmd/... ./internal/... ./pkg/...", ResourceClass: ResourceCPULight, RequiredTools: []string{"go"}},
			PresubmitAction{Name: "changed-package", Command: goTestCommand(repo.ChangedPackages), ResourceClass: ResourceCPUHeavy, RequiredTools: []string{"go"}},
			PresubmitAction{Name: "dependent", Command: goTestCommand(repo.DependentPackages), ResourceClass: ResourceCPUHeavy, RequiredTools: []string{"go"}},
		)
	}
	if hasFilesWithSuffix(repo.Root, ".sh") {
		actions = append(actions, PresubmitAction{Name: "shell", Command: "shfmt -d . && shellcheck --severity=info $(git ls-files '*.sh')", ResourceClass: ResourceCPULight, RequiredTools: []string{"shfmt", "shellcheck"}})
	}
	if hasDocumentation(repo.Root) {
		actions = append(actions, PresubmitAction{Name: "docs", Command: "markdownlint-cli2 'docs/**/*.md' '*.md' && yamllint . && biome check --files-ignore-unknown=true docs/ .github/ '*.json'", ResourceClass: ResourceCPULight, RequiredTools: []string{"markdownlint", "yamllint", "biome"}})
	}
	if pythonProject {
		actions = append(actions,
			PresubmitAction{Name: "python-format", Command: "ruff format --check .", ResourceClass: ResourceCPULight, RequiredTools: []string{"ruff"}},
			PresubmitAction{Name: "python-lint", Command: "ruff check . && pylint tests", ResourceClass: ResourceMemoryHeavy, RequiredTools: []string{"ruff", "pylint"}},
			PresubmitAction{Name: "python-type", Command: "pyright .", ResourceClass: ResourceMemoryHeavy, RequiredTools: []string{"pyright"}},
			PresubmitAction{Name: "python-changed-package", Command: "pytest", ResourceClass: ResourceCPUHeavy, RequiredTools: []string{"pytest"}},
		)
	}
	if err := validatePresubmitActions(actions, cfg.AvailableTools); err != nil {
		return PresubmitPlan{}, err
	}
	return PresubmitPlan{Actions: actions}, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func goTestCommand(packages []string) string {
	if len(packages) == 0 {
		return "go test ./..."
	}
	return "go test " + strings.Join(packages, " ")
}

func hasFilesWithSuffix(root, suffix string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func hasDocumentation(root string) bool {
	return fileExists(filepath.Join(root, "README.md")) || hasFilesWithSuffix(filepath.Join(root, "docs"), ".md")
}

func validatePresubmitActions(actions []PresubmitAction, availableTools map[string]bool) error {
	for _, action := range actions {
		if strings.TrimSpace(action.Command) == "" && action.Name != "empty" {
			return fmt.Errorf("presubmit action %q has no command", action.Name)
		}
		for _, tool := range action.RequiredTools {
			if !availableTools[tool] {
				return fmt.Errorf("presubmit action %q requires unavailable tool %q", action.Name, tool)
			}
		}
	}
	return nil
}
