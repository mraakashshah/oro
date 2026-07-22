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
	}{
		{
			name:         "literal workflow dispatch key",
			contents:     "on: workflow_dispatch\n",
			wantDispatch: true,
		},
		{
			name: "flat event sequence",
			contents: `on:
  - workflow_dispatch
  - pull_request
  - push
`,
			wantDispatch:     true,
			wantBranches:     []string{},
			wantBranchesSkip: []string{},
		},
		{
			name:           "malformed yaml",
			contents:       "on: [workflow_dispatch\n",
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
		})
	}
}
