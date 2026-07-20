package storage

import "testing"

func TestPlannerPreservesUncertainCandidates(t *testing.T) {
	t.Parallel()

	snapshot := Snapshot{
		CatalogHealthy: true,
		Candidates: []Candidate{
			{Path: "/tmp/oro-subprocess/safe", Scope: ScopeRuntime, Allowlisted: true, Owned: true},
			{Scope: ScopeRuntime, Allowlisted: true, Owned: true},
			{Path: "/tmp/oro-subprocess/unknown", Scope: ScopeRuntime, Owned: true},
			{Path: "/tmp/oro-subprocess/foreign", Scope: ScopeRuntime, Allowlisted: true},
			{Path: "/tmp/oro-subprocess/live", Scope: ScopeRuntime, Allowlisted: true, Owned: true, LeaseActive: true},
			{Path: "/tmp/oro-subprocess/other", Scope: ScopeWorktrees, Allowlisted: true, Owned: true},
		},
	}
	policy := StoragePolicy{DeletionAuthorized: true}

	plan := PlanCleanup(snapshot, policy, ScopeRuntime)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/safe", Delete, "")
	assertPlanDecision(t, plan, "", Preserve, PreserveUnknown)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/unknown", Preserve, PreserveUnknown)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/foreign", Preserve, PreserveOwnershipUncertain)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/live", Preserve, PreserveActive)
	assertPlanDecision(t, plan, "/tmp/oro-subprocess/other", Preserve, PreserveOutOfScope)

	noAuthority := PlanCleanup(snapshot, StoragePolicy{}, ScopeRuntime)
	assertPlanDecision(t, noAuthority, "/tmp/oro-subprocess/safe", Preserve, PreserveNoAuthority)

	corrupt := PlanCleanup(Snapshot{Candidates: snapshot.Candidates}, policy, ScopeRuntime)
	for _, candidate := range snapshot.Candidates {
		assertPlanDecision(t, corrupt, candidate.Path, Preserve, PreserveCatalogCorrupt)
	}
}

func assertPlanDecision(t *testing.T, plan Plan, path string, action ActionType, reason PreserveReason) {
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
