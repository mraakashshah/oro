//nolint:testpackage // The regression covers the package-private parser contract.
package github

import (
	"errors"
	"slices"
	"testing"

	"oro/pkg/remotegate"
)

func TestParseFlatWorkflowTriggerDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		contents         string
		wantDispatch     bool
		wantBranches     []string
		wantBranchesSkip []string
		wantIneligible   bool
		wantDistinct     bool
	}{
		{
			name:         "literal workflow dispatch key",
			contents:     "on: workflow_dispatch\n",
			wantDispatch: true,
		},
		{
			name:             "flat event sequence",
			contents:         "on: [workflow_dispatch, pull_request, push]\n",
			wantDispatch:     true,
			wantBranches:     []string{},
			wantBranchesSkip: []string{},
			wantDistinct:     true,
		},
		{
			name:     "unrelated scalar event",
			contents: "on: push\n",
		},
		{
			name:           "malformed yaml",
			contents:       "on: [workflow_dispatch\n",
			wantIneligible: true,
		},
		{
			name: "malformed trailing document",
			contents: `on: workflow_dispatch
---
on: [pull_request
`,
			wantIneligible: true,
		},
		{
			name: "duplicate workflow trigger key",
			contents: `on: workflow_dispatch
on: push
`,
			wantIneligible: true,
		},
		{
			name: "duplicate unrelated workflow key",
			contents: `name: first
name: second
on: workflow_dispatch
`,
			wantIneligible: true,
		},
		{
			name: "boolean sequence entry",
			contents: `on:
  - true
`,
			wantIneligible: true,
		},
		{
			name: "numeric sequence entry",
			contents: `on:
  - 1
`,
			wantIneligible: true,
		},
		{
			name: "mapping sequence entry",
			contents: `on:
  - workflow_dispatch: {}
`,
			wantIneligible: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			contents := []byte(tt.contents)
			original := slices.Clone(contents)
			triggers, err := parseWorkflowTriggers(contents)
			if !slices.Equal(contents, original) {
				t.Fatal("parseWorkflowTriggers mutated its input")
			}
			if tt.wantIneligible {
				if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
					t.Fatalf("parseWorkflowTriggers() error = %v, want ErrWorkflowIneligible", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWorkflowTriggers() error = %v", err)
			}
			if triggers.WorkflowDispatch != tt.wantDispatch {
				t.Errorf("WorkflowDispatch = %t, want %t", triggers.WorkflowDispatch, tt.wantDispatch)
			}
			if !slices.Equal(triggers.PullRequestBranches, tt.wantBranches) || (triggers.PullRequestBranches == nil) != (tt.wantBranches == nil) {
				t.Errorf("PullRequestBranches = %#v, want %#v", triggers.PullRequestBranches, tt.wantBranches)
			}
			if !slices.Equal(triggers.PullRequestBranchesIgnore, tt.wantBranchesSkip) || (triggers.PullRequestBranchesIgnore == nil) != (tt.wantBranchesSkip == nil) {
				t.Errorf("PullRequestBranchesIgnore = %#v, want %#v", triggers.PullRequestBranchesIgnore, tt.wantBranchesSkip)
			}
			if tt.wantDistinct {
				if cap(triggers.PullRequestBranches) == 0 || cap(triggers.PullRequestBranchesIgnore) == 0 {
					t.Fatal("pull-request filter slices do not have independently testable backing storage")
				}
				branches := append(triggers.PullRequestBranches, "include")
				branchesIgnore := append(triggers.PullRequestBranchesIgnore, "ignore")
				if branches[0] != "include" || branchesIgnore[0] != "ignore" {
					t.Fatal("pull-request filter slices share backing storage")
				}
			}
		})
	}
}

func TestParseMappedWorkflowTriggerDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		contents         string
		wantDispatch     bool
		wantBranches     []string
		wantBranchesSkip []string
		wantIneligible   bool
	}{
		{
			name:             "null and empty mappings are unfiltered",
			contents:         "on:\n  workflow_dispatch:\n  pull_request: {}\n",
			wantDispatch:     true,
			wantBranches:     []string{},
			wantBranchesSkip: []string{},
		},
		{
			name:         "dispatch without pull request preserves nil filters",
			contents:     "on:\n  workflow_dispatch:\n",
			wantDispatch: true,
		},
		{
			name:     "unrelated event is ignored",
			contents: "on:\n  push:\n    branches: [main]\n",
		},
		{name: "non-string event key", contents: "on:\n  true: {}\n", wantIneligible: true},
		{name: "duplicate event key", contents: "on:\n  push: {}\n  push: {}\n", wantIneligible: true},
		{name: "dispatch scalar configuration", contents: "on:\n  workflow_dispatch: true\n", wantIneligible: true},
		{name: "pull request scalar configuration", contents: "on:\n  pull_request: false\n", wantIneligible: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			triggers, err := parseWorkflowTriggers([]byte(tt.contents))
			if tt.wantIneligible {
				if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
					t.Fatalf("parseWorkflowTriggers() error = %v, want ErrWorkflowIneligible", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWorkflowTriggers() error = %v", err)
			}
			if triggers.WorkflowDispatch != tt.wantDispatch || !slices.Equal(triggers.PullRequestBranches, tt.wantBranches) || (triggers.PullRequestBranches == nil) != (tt.wantBranches == nil) || !slices.Equal(triggers.PullRequestBranchesIgnore, tt.wantBranchesSkip) || (triggers.PullRequestBranchesIgnore == nil) != (tt.wantBranchesSkip == nil) {
				t.Fatalf("triggers = %#v", triggers)
			}
		})
	}
}
