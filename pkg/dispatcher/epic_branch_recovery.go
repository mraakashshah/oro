package dispatcher

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

const (
	epicBranchRecoveryTagName       = "epic-branch-recovery"
	epicBranchRecoveryMaxCandidates = 16
)

// ensureEpicBranchBlockRecovery returns the one active recovery child linked
// to a blocked branch generation, creating and CAS-linking it when necessary.
func (d *Dispatcher) ensureEpicBranchBlockRecovery(ctx context.Context, admission epicBranchAdmission) (*protocol.Bead, error) {
	if err := validateEpicBranchRecoveryAdmission(admission); err != nil {
		return nil, err
	}

	// The deterministic ID and the admission-row CAS make repair safe across
	// processes. This lock avoids redundant create attempts inside one process.
	d.mu.Lock()
	defer d.mu.Unlock()

	store := newEpicBranchAdmissionStore(d.db)
	for range epicBranchRecoveryMaxCandidates {
		child, found, err := d.findLinkedEpicBranchRecoveryChild(ctx, admission)
		if err != nil {
			return nil, err
		}
		if found {
			return child, nil
		}
		child, err = d.createOrReuseEpicBranchRecoveryChild(ctx, admission)
		if err != nil {
			return nil, err
		}
		if child == nil {
			return nil, fmt.Errorf("create or reuse epic branch recovery child: store returned nil bead")
		}
		next, linked, err := d.linkEpicBranchRecoveryCandidate(ctx, store, admission, child)
		if err != nil {
			return nil, err
		}
		if linked {
			return child, nil
		}
		admission = next
	}
	return nil, fmt.Errorf("ensure epic branch recovery for %s generation %d: candidate limit exceeded",
		admission.branch, admission.generation)
}

func (d *Dispatcher) linkEpicBranchRecoveryCandidate(
	ctx context.Context,
	store *epicBranchAdmissionStore,
	admission epicBranchAdmission,
	child *protocol.Bead,
) (epicBranchAdmission, bool, error) {
	linked, err := store.linkRecovery(ctx, admission.branch, admission.generation,
		admission.recoveryBeadID, child.ID, d.nowFunc())
	if err == nil {
		if linked.recoveryBeadID != child.ID {
			return epicBranchAdmission{}, false,
				fmt.Errorf("link epic branch recovery child: stored %q, want %q", linked.recoveryBeadID, child.ID)
		}
		if err := d.retireEpicBranchRecoveryPredecessors(ctx, linked, child); err != nil {
			return epicBranchAdmission{}, false, err
		}
		return linked, true, nil
	}
	next, superseded, err := d.reloadEpicBranchRecoveryLinkConflict(ctx, admission, err)
	if err != nil {
		return epicBranchAdmission{}, false, err
	}
	if !superseded {
		return next, false, nil
	}
	cleanupErr := d.retireEpicBranchRecovery(ctx, admission.epicID, child,
		"epic branch recovery admission generation advanced before link")
	return epicBranchAdmission{}, false, errors.Join(ErrEpicBranchAdmissionCAS, cleanupErr)
}

func (d *Dispatcher) findLinkedEpicBranchRecoveryChild(
	ctx context.Context,
	admission epicBranchAdmission,
) (*protocol.Bead, bool, error) {
	if admission.recoveryBeadID == "" {
		return nil, false, nil
	}
	child, err := d.beads.Show(ctx, admission.recoveryBeadID)
	if err != nil {
		return nil, false, fmt.Errorf("show linked epic branch recovery child: %w", err)
	}
	if child == nil || !isExactEpicBranchRecoveryChild(child, admission) {
		return nil, false, nil
	}
	if err := d.addEpicBranchRecoveryDependency(ctx, admission.epicID, child.ID); err != nil {
		return nil, false, err
	}
	if err := d.retireEpicBranchRecoveryPredecessors(ctx, admission, child); err != nil {
		return nil, false, err
	}
	return child, true, nil
}

func (d *Dispatcher) reloadEpicBranchRecoveryLinkConflict(
	ctx context.Context,
	admission epicBranchAdmission,
	linkErr error,
) (epicBranchAdmission, bool, error) {
	if !errors.Is(linkErr, ErrEpicBranchAdmissionCAS) {
		return epicBranchAdmission{}, false, linkErr
	}
	current, err := loadEpicBranchAdmission(ctx, d.db, admission.branch)
	if err != nil {
		return epicBranchAdmission{}, false, fmt.Errorf("reload epic branch recovery link conflict: %w", err)
	}
	if current.state != "blocked" || current.generation != admission.generation {
		return admission, true, nil
	}
	return current, false, nil
}

func (d *Dispatcher) createOrReuseEpicBranchRecoveryChild(ctx context.Context, admission epicBranchAdmission) (*protocol.Bead, error) {
	predecessor := admission.recoveryBeadID
	for range epicBranchRecoveryMaxCandidates {
		candidateID := epicBranchRecoveryBeadID(admission, predecessor)
		candidateAdmission := admission
		candidateAdmission.recoveryBeadID = candidateID
		child, available, err := d.loadOrCreateEpicBranchRecoveryCandidate(ctx, candidateAdmission, predecessor)
		if err != nil {
			return nil, err
		}
		if !available {
			predecessor = candidateID
			continue
		}
		if child == nil {
			return nil, fmt.Errorf("load or create epic branch recovery candidate: store returned nil bead")
		}
		if err := d.addEpicBranchRecoveryDependency(ctx, admission.epicID, child.ID); err != nil {
			return nil, err
		}
		return child, nil
	}
	return nil, fmt.Errorf("create epic branch recovery for %s generation %d: candidate limit exceeded",
		admission.branch, admission.generation)
}

func (d *Dispatcher) loadOrCreateEpicBranchRecoveryCandidate(
	ctx context.Context,
	admission epicBranchAdmission,
	predecessor string,
) (*protocol.Bead, bool, error) {
	child, err := d.beads.Show(ctx, admission.recoveryBeadID)
	if err != nil {
		return nil, false, fmt.Errorf("show epic branch recovery candidate: %w", err)
	}
	if child != nil {
		return child, isExactEpicBranchRecoveryChild(child, admission), nil
	}
	child, createErr := d.beads.Create(ctx, epicBranchRecoveryCreateParams(ctx, d, admission, predecessor))
	if createErr == nil {
		if child == nil || !isExactEpicBranchRecoveryChild(child, admission) {
			return nil, false, fmt.Errorf("epic branch recovery store returned non-canonical child %s", admission.recoveryBeadID)
		}
		return child, true, nil
	}
	child, showErr := d.beads.Show(ctx, admission.recoveryBeadID)
	if showErr != nil {
		return nil, false, fmt.Errorf("create then show epic branch recovery child: %w", errors.Join(createErr, showErr))
	}
	if !isExactEpicBranchRecoveryChild(child, admission) {
		return nil, false, fmt.Errorf("create epic branch recovery child %s: %w", admission.recoveryBeadID, createErr)
	}
	return child, true, nil
}

func validateEpicBranchRecoveryAdmission(admission epicBranchAdmission) error {
	if admission.state != "blocked" || admission.branch == "" || admission.epicID == "" ||
		admission.targetBranch == "" || admission.generation <= 0 || admission.blockerKind == "" {
		return fmt.Errorf("ensure epic branch recovery: invalid blocked admission for %q generation %d",
			admission.branch, admission.generation)
	}
	return nil
}

func epicBranchRecoveryBeadID(admission epicBranchAdmission, predecessor string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		admission.branch,
		strconv.FormatInt(admission.generation, 10),
		predecessor,
	}, "\x00")))
	return fmt.Sprintf("oro-ebr-%x", digest[:8])
}

func epicBranchRecoveryCreateParams(
	ctx context.Context,
	d *Dispatcher,
	admission epicBranchAdmission,
	predecessor string,
) beadstore.CreateParams {
	return beadstore.CreateParams{
		ID:                 admission.recoveryBeadID,
		Title:              epicBranchRecoveryTitle(admission),
		Type:               "task",
		Priority:           0,
		Description:        epicBranchRecoveryDescription(admission),
		ParentID:           admission.epicID,
		AcceptanceCriteria: epicBranchRecoveryAcceptance(admission),
		Tags:               []string{epicBranchRecoveryTagName},
		Metadata: map[string]string{
			"epic_branch_recovery":             "true",
			"epic_branch_recovery_branch":      admission.branch,
			"epic_branch_recovery_generation":  strconv.FormatInt(admission.generation, 10),
			"epic_branch_recovery_blocker":     admission.blockerKind,
			"epic_branch_recovery_predecessor": predecessor,
		},
		Tier: parentTierForCreate(ctx, d.beads, admission.epicID),
	}
}

func epicBranchRecoveryTitle(admission epicBranchAdmission) string {
	return fmt.Sprintf("Recover blocked epic branch %s (generation %d)", admission.branch, admission.generation)
}

func epicBranchRecoveryDescription(admission epicBranchAdmission) string {
	return fmt.Sprintf("Epic branch %s is blocked by %s while targeting %s. Branch SHA: %s. Target SHA: %s. Evidence: %s",
		admission.branch, admission.blockerKind, admission.targetBranch,
		admission.branchSHA, admission.targetSHA, admission.details)
}

func epicBranchRecoveryAcceptance(admission epicBranchAdmission) string {
	return strings.Join([]string{
		fmt.Sprintf("Test: recover %s generation %d from %s", admission.branch, admission.generation, admission.blockerKind),
		fmt.Sprintf("Cmd: git merge-base --is-ancestor %s %s && go test ./pkg/dispatcher -run '^TestEpicBranchBlockCreatesOneCrashSafeCanonicalRecoveryChild$'", admission.targetBranch, admission.branch),
		fmt.Sprintf("Assert: %s is safe relative to %s and its durable generation-%d blocker can be explicitly resolved.", admission.branch, admission.targetBranch, admission.generation),
		"Read: pkg/dispatcher/epic_branch_recovery.go, pkg/dispatcher/epic_branch_admission.go",
	}, " | ")
}

func isExactEpicBranchRecoveryChild(child *protocol.Bead, admission epicBranchAdmission) bool {
	if child == nil || admission.recoveryBeadID == "" {
		return false
	}
	gotIdentity := [7]string{child.ID, child.Status, child.Epic, child.Type, child.Title, child.Description, child.AcceptanceCriteria}
	wantIdentity := [7]string{
		admission.recoveryBeadID, child.Status, admission.epicID, "task",
		epicBranchRecoveryTitle(admission), epicBranchRecoveryDescription(admission), epicBranchRecoveryAcceptance(admission),
	}
	if gotIdentity != wantIdentity || child.Priority != 0 || !slices.Contains(child.Tags, epicBranchRecoveryTagName) {
		return false
	}
	if child.Status != "open" && child.Status != "in_progress" {
		return false
	}
	if !isEpicBranchRecoveryChainMember(child, admission) {
		return false
	}
	predecessor, ok := child.Metadata["epic_branch_recovery_predecessor"].(string)
	return ok && child.ID == epicBranchRecoveryBeadID(admission, predecessor)
}

func recoveryMetadataEquals(metadata map[string]any, key, want string) bool {
	if metadata == nil {
		return false
	}
	got, ok := metadata[key].(string)
	return ok && got == want
}

func isEpicBranchRecoveryChainMember(child *protocol.Bead, admission epicBranchAdmission) bool {
	if child == nil || child.Epic != admission.epicID || child.Type != "task" || child.Priority != 0 ||
		!strings.HasPrefix(child.ID, "oro-ebr-") {
		return false
	}
	expectedMetadata := map[string]string{
		"epic_branch_recovery":            "true",
		"epic_branch_recovery_branch":     admission.branch,
		"epic_branch_recovery_generation": strconv.FormatInt(admission.generation, 10),
		"epic_branch_recovery_blocker":    admission.blockerKind,
	}
	for key, want := range expectedMetadata {
		if !recoveryMetadataEquals(child.Metadata, key, want) {
			return false
		}
	}
	return true
}

func (d *Dispatcher) addEpicBranchRecoveryDependency(ctx context.Context, epicID, childID string) error {
	store, ok := d.beads.(dependencyStore)
	if !ok {
		return fmt.Errorf("bead store does not support dependencies")
	}
	if err := store.AddDependency(ctx, epicID, childID, "blocks"); err != nil {
		return fmt.Errorf("add epic branch recovery dependency: %w", err)
	}
	return nil
}

func (d *Dispatcher) retireEpicBranchRecoveryPredecessors(
	ctx context.Context,
	admission epicBranchAdmission,
	child *protocol.Bead,
) error {
	predecessor := epicBranchRecoveryPredecessor(child)
	seen := make(map[string]bool)
	for predecessor != "" {
		if seen[predecessor] {
			return fmt.Errorf("retire epic branch recovery predecessors: cycle at %s", predecessor)
		}
		seen[predecessor] = true
		candidate, err := d.beads.Show(ctx, predecessor)
		if err != nil {
			return fmt.Errorf("show epic branch recovery predecessor %s: %w", predecessor, err)
		}
		if candidate == nil {
			return nil
		}
		if !isEpicBranchRecoveryChainMember(candidate, admission) {
			return fmt.Errorf("retire epic branch recovery predecessor %s: ownership metadata mismatch", predecessor)
		}
		next := epicBranchRecoveryPredecessor(candidate)
		if err := d.retireEpicBranchRecovery(ctx, admission.epicID, candidate,
			"superseded by canonical epic branch recovery "+child.ID); err != nil {
			return err
		}
		if candidate.ID != epicBranchRecoveryBeadID(admission, next) {
			return nil
		}
		predecessor = next
	}
	return nil
}

func epicBranchRecoveryPredecessor(child *protocol.Bead) string {
	if child == nil || child.Metadata == nil {
		return ""
	}
	predecessor, _ := child.Metadata["epic_branch_recovery_predecessor"].(string)
	return predecessor
}

func (d *Dispatcher) retireEpicBranchRecovery(
	ctx context.Context,
	epicID string,
	child *protocol.Bead,
	reason string,
) error {
	if child == nil {
		return nil
	}
	var errs []error
	if child.Status == "open" || child.Status == "in_progress" {
		if err := d.CloseBead(ctx, child.ID, reason); err != nil {
			errs = append(errs, fmt.Errorf("close superseded epic branch recovery %s: %w", child.ID, err))
		}
	}
	store, ok := d.beads.(dependencyRemovalStore)
	if !ok {
		errs = append(errs, fmt.Errorf("remove superseded epic branch recovery dependency %s: store does not support removal", child.ID))
	} else if err := store.RemoveDependency(ctx, epicID, child.ID); err != nil {
		errs = append(errs, fmt.Errorf("remove superseded epic branch recovery dependency %s: %w", child.ID, err))
	}
	return errors.Join(errs...)
}

func (d *Dispatcher) repairBlockedEpicBranchRecoveries(ctx context.Context) error {
	admissions, err := newEpicBranchAdmissionStore(d.db).blocked(ctx)
	if err != nil {
		return err
	}
	for i := range admissions {
		if _, err := d.ensureEpicBranchBlockRecovery(ctx, admissions[i]); err != nil {
			return fmt.Errorf("repair blocked epic branch %s: %w", admissions[i].branch, err)
		}
	}
	return nil
}

func (d *Dispatcher) filterBlockedEpicBranchReady(ctx context.Context, beads []protocol.Bead) ([]protocol.Bead, error) {
	if d.db == nil || len(beads) == 0 {
		return beads, nil
	}
	store := newEpicBranchAdmissionStore(d.db)
	exists, err := store.schemaExists(ctx)
	if err != nil {
		return nil, fmt.Errorf("filter blocked epic branch ready beads: %w", err)
	}
	if !exists {
		return beads, nil
	}
	admissions, err := store.blocked(ctx)
	if err != nil {
		return nil, fmt.Errorf("filter blocked epic branch ready beads: %w", err)
	}
	if len(admissions) == 0 {
		return beads, nil
	}
	blockedRecoveries := make(map[string]string, len(admissions))
	for i := range admissions {
		blockedRecoveries[admissions[i].epicID] = admissions[i].recoveryBeadID
	}
	filtered := make([]protocol.Bead, 0, len(beads))
	parentCache := make(map[string]string)
	for i := range beads {
		blocked, err := d.hasBlockingEpicAncestor(ctx, beads[i].ID, beads[i].Epic, blockedRecoveries, parentCache)
		if err != nil {
			return nil, err
		}
		if !blocked {
			filtered = append(filtered, beads[i])
		}
	}
	return filtered, nil
}

func (d *Dispatcher) hasBlockingEpicAncestor(
	ctx context.Context,
	beadID string,
	parentID string,
	blockedRecoveries map[string]string,
	parentCache map[string]string,
) (bool, error) {
	seen := make(map[string]bool)
	for parentID != "" {
		if recoveryID, blocked := blockedRecoveries[parentID]; blocked && recoveryID != beadID {
			return true, nil
		}
		if seen[parentID] {
			return false, fmt.Errorf("filter blocked epic branch ready beads: parent cycle at %s", parentID)
		}
		seen[parentID] = true
		if cached, ok := parentCache[parentID]; ok {
			parentID = cached
			continue
		}
		parent, err := d.beads.Show(ctx, parentID)
		if err != nil {
			return false, fmt.Errorf("filter blocked epic branch ready bead parent %s: %w", parentID, err)
		}
		if parent == nil {
			return false, fmt.Errorf("filter blocked epic branch ready bead: parent %s not found", parentID)
		}
		parentCache[parentID] = parent.Epic
		parentID = parent.Epic
	}
	return false, nil
}
