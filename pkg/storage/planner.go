package storage

import "strings"

// Scope identifies a cleanup domain selected by a caller.
type Scope string

const (
	// ScopeAll plans eligible candidates from every cleanup domain.
	ScopeAll Scope = "all"
	// ScopeRuntime plans worktree runtime namespaces.
	ScopeRuntime Scope = "runtime"
	// ScopeWorktrees plans managed worktrees and related metadata.
	ScopeWorktrees Scope = "worktrees"
	// ScopeOroHome plans explicitly allowlisted Oro home artifacts.
	ScopeOroHome Scope = "oro-home"
	// ScopeDevTools plans trusted developer-tool cache maintenance.
	ScopeDevTools Scope = "dev-tools"
)

// Snapshot is the read-only cleanup input collected by an impure edge.
type Snapshot struct {
	CatalogHealthy bool
	Candidates     []Candidate
}

// Candidate is one observed cleanup target and its safety proof.
type Candidate struct {
	Path        string
	Scope       Scope
	Allowlisted bool
	Owned       bool
	LeaseActive bool
}

// StoragePolicy controls whether the caller explicitly authorizes deletion.
// Planning remains dry-run only; execution must separately revalidate every
// delete decision before changing the filesystem.
type StoragePolicy struct {
	DeletionAuthorized bool
}

// ActionType identifies a planned cleanup outcome.
type ActionType string

const (
	// Delete marks a candidate for a later revalidated deletion attempt.
	Delete ActionType = "delete"
	// Preserve records that a candidate must not be deleted.
	Preserve ActionType = "preserve"
)

// PreserveReason is a stable explanation for a preservation decision.
type PreserveReason string

const (
	// PreserveCatalogCorrupt prevents deletion without a trustworthy catalog.
	PreserveCatalogCorrupt PreserveReason = "catalog_corrupt"
	// PreserveUnknown prevents deletion of targets outside the explicit allowlist.
	PreserveUnknown PreserveReason = "unknown_path"
	// PreserveOwnershipUncertain prevents deletion without ownership proof.
	PreserveOwnershipUncertain PreserveReason = "ownership_uncertain"
	// PreserveActive prevents deletion while a target has an active lease.
	PreserveActive PreserveReason = "active_lease"
	// PreserveOutOfScope prevents a selected scope from affecting other domains.
	PreserveOutOfScope PreserveReason = "out_of_scope"
	// PreserveNoAuthority prevents deletion without explicit caller authority.
	PreserveNoAuthority PreserveReason = "authority_not_granted"
)

// Decision records one candidate's planned action and preservation evidence.
type Decision struct {
	Candidate      Candidate
	Action         ActionType
	PreserveReason PreserveReason
}

// Plan is a deterministic, dry-run-only list of cleanup decisions.
type Plan struct {
	Decisions []Decision
}

// PlanCleanup returns a preservation-first cleanup plan. Only allowlisted,
// Oro-owned, unleased candidates in the requested scope become delete actions.
func PlanCleanup(snapshot Snapshot, policy StoragePolicy, scope Scope) Plan {
	decisions := make([]Decision, 0, len(snapshot.Candidates))
	for _, candidate := range snapshot.Candidates {
		decisions = append(decisions, planCandidate(snapshot.CatalogHealthy, policy, scope, candidate))
	}
	return Plan{Decisions: decisions}
}

func planCandidate(catalogHealthy bool, policy StoragePolicy, scope Scope, candidate Candidate) Decision {
	decision := Decision{Candidate: candidate, Action: Preserve}
	switch {
	case !catalogHealthy:
		decision.PreserveReason = PreserveCatalogCorrupt
	case strings.TrimSpace(candidate.Path) == "":
		decision.PreserveReason = PreserveUnknown
	case !scopeIncludes(scope, candidate.Scope):
		decision.PreserveReason = PreserveOutOfScope
	case !candidate.Allowlisted:
		decision.PreserveReason = PreserveUnknown
	case !candidate.Owned:
		decision.PreserveReason = PreserveOwnershipUncertain
	case candidate.LeaseActive:
		decision.PreserveReason = PreserveActive
	case !policy.DeletionAuthorized:
		decision.PreserveReason = PreserveNoAuthority
	default:
		decision.Action = Delete
	}
	return decision
}

func scopeIncludes(selected, candidate Scope) bool {
	return selected == ScopeAll || selected == candidate
}
