package workproposal_test

import (
	"testing"
	"time"

	"oro/pkg/workproposal"
)

func TestEvidenceIdentityValidation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 21, 18, 0, 0, 0, time.UTC)
	live := workproposal.AssignmentSnapshot{
		Project:      "oro",
		AssignmentID: 42,
		WorkerID:     "worker-a",
		BeadID:       "bead-a",
		Worktree:     "/repo/.worktrees/bead-a",
		Branch:       "agent/bead-a",
		HEAD:         "abc123",
		Command:      []string{"go", "test", "./pkg/workproposal"},
	}
	manifest := workproposal.EvidenceManifest{
		Project:      live.Project,
		AssignmentID: live.AssignmentID,
		WorkerID:     live.WorkerID,
		BeadID:       live.BeadID,
		Worktree:     live.Worktree,
		Branch:       live.Branch,
		HEAD:         live.HEAD,
		Command:      append([]string(nil), live.Command...),
		StartedAt:    now.Add(-time.Minute),
		CompletedAt:  now.Add(-time.Second),
		Terminal:     true,
	}

	if err := workproposal.ValidateEvidence(manifest, live, now); err != nil {
		t.Fatalf("ValidateEvidence(valid) error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*workproposal.EvidenceManifest)
	}{
		{name: "project", mutate: func(m *workproposal.EvidenceManifest) { m.Project = "foreign" }},
		{name: "assignment", mutate: func(m *workproposal.EvidenceManifest) { m.AssignmentID++ }},
		{name: "worker", mutate: func(m *workproposal.EvidenceManifest) { m.WorkerID = "worker-b" }},
		{name: "bead", mutate: func(m *workproposal.EvidenceManifest) { m.BeadID = "bead-b" }},
		{name: "worktree", mutate: func(m *workproposal.EvidenceManifest) { m.Worktree = "/repo/.worktrees/foreign" }},
		{name: "branch", mutate: func(m *workproposal.EvidenceManifest) { m.Branch = "agent/foreign" }},
		{name: "head", mutate: func(m *workproposal.EvidenceManifest) { m.HEAD = "def456" }},
		{name: "command", mutate: func(m *workproposal.EvidenceManifest) { m.Command = []string{"go", "test", "./..."} }},
		{name: "future timestamp", mutate: func(m *workproposal.EvidenceManifest) { m.CompletedAt = now.Add(time.Second) }},
		{name: "expired terminal output", mutate: func(m *workproposal.EvidenceManifest) { m.CompletedAt = now.Add(-21 * time.Minute) }},
		{name: "nonterminal output", mutate: func(m *workproposal.EvidenceManifest) { m.Terminal = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			candidate.Command = append([]string(nil), manifest.Command...)
			test.mutate(&candidate)
			if err := workproposal.ValidateEvidence(candidate, live, now); err == nil {
				t.Fatal("ValidateEvidence() error = nil, want rejection")
			}
		})
	}
}
