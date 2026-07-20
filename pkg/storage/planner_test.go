package storage_test

import (
	"testing"

	"oro/pkg/storage"
)

func TestPlannerPreservesUncertainCandidates(t *testing.T) {
	t.Parallel()

	snapshot := storage.Snapshot{
		CatalogHealthy: true,
		Candidates: []storage.Candidate{
			{Path: "/tmp/oro-subprocess/safe", Scope: storage.ScopeRuntime, Allowlisted: true, Owned: true},
			{Scope: storage.ScopeRuntime, Allowlisted: true, Owned: true},
			{Path: "/tmp/oro-subprocess/unknown", Scope: storage.ScopeRuntime, Owned: true},
			{Path: "/tmp/oro-subprocess/foreign", Scope: storage.ScopeRuntime, Allowlisted: true},
			{Path: "/tmp/oro-subprocess/live", Scope: storage.ScopeRuntime, Allowlisted: true, Owned: true, LeaseActive: true},
			{Path: "/tmp/oro-subprocess/other", Scope: storage.ScopeWorktrees, Allowlisted: true, Owned: true},
		},
	}
	policy := storage.StoragePolicy{DeletionAuthorized: true}

	plan := storage.PlanCleanup(snapshot, policy, storage.ScopeRuntime)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/safe", storage.Delete, "")
	assertPlanDecision(t, plan, "", storage.Preserve, storage.PreserveUnknown)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/unknown", storage.Preserve, storage.PreserveUnknown)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/foreign", storage.Preserve, storage.PreserveOwnershipUncertain)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/live", storage.Preserve, storage.PreserveActive)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/other", storage.Preserve, storage.PreserveOutOfScope)

	noAuthority := storage.PlanCleanup(snapshot, storage.StoragePolicy{}, storage.ScopeRuntime)
	assertPlanDecision(t, noAuthority, "/tmp/oro-subprocess/safe", storage.Preserve, storage.PreserveNoAuthority)

	corrupt := storage.PlanCleanup(storage.Snapshot{Candidates: snapshot.Candidates}, policy, storage.ScopeRuntime)
	for _, candidate := range snapshot.Candidates {
		assertPlanDecision(t, corrupt, candidate.Path, storage.Preserve, storage.PreserveCatalogCorrupt)
	}
}

func assertPlanDecision(t *testing.T, plan storage.Plan, path string, action storage.ActionType, reason storage.PreserveReason) {
	t.Helper()
	for _, decision := range plan.Decisions {
		if decision.Candidate.Path != path {
			continue
		}
		if decision.Action != action {
			t.Errorf("decision for %q action = %q, want %q", path, decision.Action, action)
		}
		if decision.PreserveReason != reason {
			t.Errorf("decision for %q preserve reason = %q, want %q", path, decision.PreserveReason, reason)
		}
		return
	}
	t.Errorf("missing decision for %q", path)
}
