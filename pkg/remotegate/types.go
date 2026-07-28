package remotegate

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidRequest indicates that a remote-gate request has no complete,
// self-consistent identity.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
var ErrInvalidRequest = errors.New("invalid remote gate request")

// ErrDeterministic indicates a provider failure that cannot succeed without a
// changed candidate, target, policy, or request.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
var ErrDeterministic = errors.New("deterministic remote gate failure")

// ErrTransient indicates a provider failure that may succeed on a bounded
// retry without changing the requested remote-gate operation.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
var ErrTransient = errors.New("transient remote gate failure")

// ErrAuth indicates an unavailable, expired, or incorrectly scoped provider
// identity.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
var ErrAuth = errors.New("remote gate authentication failure")

// ErrConfig indicates an unsupported or inconsistent remote-gate provider
// configuration.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
var ErrConfig = errors.New("remote gate configuration failure")

// ErrAmbiguous indicates that a provider side effect may have completed but
// cannot be proven from the available observation.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
var ErrAmbiguous = errors.New("ambiguous remote gate result")

// Repository identifies a provider repository without exposing provider
// transport models.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Repository struct {
	Host  string
	Owner string
	Name  string
}

// Target identifies the exact remote ref and commit a candidate is evaluated
// against.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Target struct {
	Repository Repository
	Ref        string
	SHA        string
}

// Candidate identifies the exact proposed source ref, commit, and tree.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Candidate struct {
	Repository Repository
	Ref        string
	SHA        string
	TreeSHA    string
}

// Change identifies the remote review object associated with a candidate and
// its target.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Change struct {
	ID        string
	URL       string
	Candidate Candidate
	Target    Target
	Draft     bool
}

// Evidence binds a remote gate result to one change, candidate, target, and
// tested tree.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Evidence struct {
	ID            string
	Change        Change
	CandidateSHA  string
	Target        Target
	TestedTreeSHA string
	PolicyHash    string
}

// Lease authorizes one exact provider mutation without exposing provider
// transport lease types.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Lease struct {
	Owner          string
	Generation     int64
	ExpectedSHA    string
	ExpectedAbsent bool
}

// EphemeralTarget records a dispatcher-owned target that may be advanced or
// retired only by its owning generation.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type EphemeralTarget struct {
	Target     Target
	Owner      string
	Generation int64
}

// RemoteChange records the provider-visible change with its immutable
// dispatcher ownership identity.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type RemoteChange struct {
	Change     Change
	Owner      string
	Generation int64
}

// CheckEvidence identifies one provider-neutral check attached to a run.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type CheckEvidence struct {
	ID         string
	Name       string
	Conclusion string
}

// PageEvidence proves that a paginated provider collection was complete.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type PageEvidence struct {
	Number   int
	Complete bool
}

// RunEvidence binds workflow, run, check, policy, and page evidence to a
// provider-neutral gate observation.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type RunEvidence struct {
	Change               Change
	CandidateSHA         string
	Target               Target
	TestedTreeSHA        string
	Workflow             WorkflowEvidence
	RunID                string
	PolicyHash           string
	Checks               []CheckEvidence
	Pages                []PageEvidence
	ExpectedPages        []int
	ExpectedWorkflowPath string
	ExpectedWorkflowRef  string
	ExpectedRunID        string
	ExpectedCheckIDs     []string
}

// Policy is the provider-neutral effective policy evidence for a target.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Policy = EffectivePolicy

// Audit identifies an immutable remote-gate audit campaign and its exact
// candidate and target state.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Audit struct {
	ID         string
	Repository Repository
	Candidate  Candidate
	Target     Target
}

// Capabilities describes the provider features and policy evidence accepted
// during preflight.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type Capabilities struct {
	Repository        Repository
	Policy            Policy
	PolicyHash        string
	SupportsSquashCAS bool
	Git               GitTransportCapabilities
}

// PreflightRequest requests provider capability and target-policy evidence.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type PreflightRequest struct {
	Repository Repository
	Target     Target
}

// PublishRequest requests publication of one exact candidate against a target.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type PublishRequest struct {
	Candidate Candidate
	Target    Target
}

// EnsureEphemeralTargetRequest creates or adopts an owned ephemeral target
// using an expected-absent or exact-SHA lease.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type EnsureEphemeralTargetRequest struct {
	ProjectID  string
	EpicID     string
	Target     Target
	SeedSHA    string
	Owner      string
	Generation int64
	Lease      Lease
}

// DeleteEphemeralTargetRequest retires an owned ephemeral target at its exact
// final SHA and generation.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type DeleteEphemeralTargetRequest struct {
	Target   EphemeralTarget
	FinalSHA string
	Retired  bool
	Lease    Lease
}

// EnsureChangeRequest creates or adopts the exact provider change for an
// owned candidate and target.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type EnsureChangeRequest struct {
	Change RemoteChange
	Lease  Lease
}

// ChangeReadyRequest records exact evidence before a non-draft change is
// marked ready for provider evaluation.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type ChangeReadyRequest struct {
	Change   RemoteChange
	Evidence Evidence
	Lease    Lease
}

// CancelGateRequest cancels only the owned change identity represented by its
// exact lease.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type CancelGateRequest struct {
	Change RemoteChange
	Reason string
	Lease  Lease
}

// ReconcileChangeRequest observes an owned change after lost responses before
// any retry can be attempted.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type ReconcileChangeRequest struct {
	Change             RemoteChange
	Evidence           Evidence
	Run                RunEvidence
	AttemptedOperation string
	AttemptID          string
	ObservedOperation  string
	ObservedAttemptID  string
	ObservedOutcome    string
	Lease              Lease
}

// PublishedCandidate records the provider-visible identity after publication.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type PublishedCandidate struct {
	Candidate Candidate
	RemoteRef string
}

// ObserveGateRequest requests the current remote-gate state for one exact
// change, candidate, and target.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type ObserveGateRequest struct {
	Change    Change
	Candidate Candidate
	Target    Target
}

// RemoteGateObservation contains a normalized remote change and any exact
// gate evidence currently observed for it.
//
//nolint:revive // Contract name is fixed by the dispatcher boundary.
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type RemoteGateObservation struct {
	Change   Change
	Evidence Evidence
	Terminal bool
	Passed   bool
}

// PrepareSquashRequest supplies the exact reviewed and tested state from
// which the provider constructs one deterministic squash commit.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type PrepareSquashRequest struct {
	Change         Change
	Candidate      Candidate
	Target         Target
	Evidence       Evidence
	CommitMessage  string
	CommitMetadata string
}

// PreparedSquash is the adapter-created commit identity that must be durably
// acknowledged before IntegrateSquashCAS may mutate the target.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type PreparedSquash struct {
	AttemptKey string
	Change     Change
	Candidate  Candidate
	Target     Target
	Evidence   Evidence
	SHA        string
	ParentSHA  string
	TreeSHA    string
	LocalRef   string
}

// MergeResult reports the observed result of a squash CAS attempt.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type MergeResult struct {
	Target        Target
	IntegratedSHA string
	Integrated    bool
	Ambiguous     bool
}

// RemoteGateClient is the provider-neutral remote quality-gate boundary.
//
//nolint:revive // Contract name is fixed by the dispatcher boundary.
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
type RemoteGateClient interface {
	Preflight(context.Context, PreflightRequest) (Capabilities, error)
	EnsureEphemeralTarget(context.Context, EnsureEphemeralTargetRequest) (EphemeralTarget, error)
	DeleteEphemeralTarget(context.Context, DeleteEphemeralTargetRequest) error
	Publish(context.Context, PublishRequest) (PublishedCandidate, error)
	EnsureChange(context.Context, EnsureChangeRequest) (RemoteChange, error)
	Observe(context.Context, ObserveGateRequest) (RemoteGateObservation, error)
	SetChangeReady(context.Context, ChangeReadyRequest) (RemoteChange, error)
	PrepareSquash(context.Context, PrepareSquashRequest) (PreparedSquash, error)
	IntegrateSquashCAS(context.Context, PreparedSquash) (MergeResult, error)
	Cancel(context.Context, CancelGateRequest) error
	Reconcile(context.Context, ReconcileChangeRequest) error
}

// ValidateRequest rejects nil, unknown, or incomplete remote-gate identities.
//
//oro:testonly — remote-gate orchestration is wired by subsequent tasks.
func ValidateRequest(request any) error {
	switch typed := request.(type) {
	case PreflightRequest:
		return validatePreflightRequest(typed)
	case PublishRequest:
		return validatePublishRequest(typed)
	case EnsureEphemeralTargetRequest:
		return validateEnsureEphemeralTargetRequest(typed)
	case DeleteEphemeralTargetRequest:
		return validateDeleteEphemeralTargetRequest(typed)
	case EnsureChangeRequest:
		return validateEnsureChangeRequest(typed)
	case ObserveGateRequest:
		return validateObserveGateRequest(typed)
	case ChangeReadyRequest:
		return validateChangeReadyRequest(typed)
	case PrepareSquashRequest:
		return validatePrepareSquashRequest(typed)
	case PreparedSquash:
		return validatePreparedSquash(typed)
	case CancelGateRequest:
		return validateCancelGateRequest(typed)
	case ReconcileChangeRequest:
		return validateReconcileChangeRequest(typed)
	default:
		return invalidRequest("unsupported request type")
	}
}

func validatePreflightRequest(request PreflightRequest) error {
	if err := validateRepository(request.Repository); err != nil {
		return err
	}
	if request.Target.Repository != request.Repository {
		return invalidRequest("target repository does not match preflight repository")
	}
	return validateTarget(request.Target)
}

func validatePublishRequest(request PublishRequest) error {
	if err := validateCandidate(request.Candidate); err != nil {
		return err
	}
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if request.Candidate.Repository != request.Target.Repository {
		return invalidRequest("candidate repository does not match target repository")
	}
	return nil
}

func validateEnsureEphemeralTargetRequest(request EnsureEphemeralTargetRequest) error {
	if strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.EpicID) == "" || strings.TrimSpace(request.SeedSHA) == "" {
		return invalidRequest("ephemeral target identity is incomplete")
	}
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if request.Target.SHA != request.SeedSHA {
		return invalidRequest("ephemeral target seed does not match target SHA")
	}
	return validateMutationLease(request.Owner, request.Generation, request.Lease, request.SeedSHA, true)
}

func validateDeleteEphemeralTargetRequest(request DeleteEphemeralTargetRequest) error {
	if !request.Retired || strings.TrimSpace(request.FinalSHA) == "" {
		return invalidRequest("ephemeral target is not durably retired")
	}
	if err := validateEphemeralTarget(request.Target); err != nil {
		return err
	}
	if request.Target.Target.SHA != request.FinalSHA {
		return invalidRequest("ephemeral target final SHA does not match target")
	}
	return validateMutationLease(request.Target.Owner, request.Target.Generation, request.Lease, request.FinalSHA, false)
}

func validateEnsureChangeRequest(request EnsureChangeRequest) error {
	if err := validateRemoteChange(request.Change); err != nil {
		return err
	}
	return validateMutationLease(request.Change.Owner, request.Change.Generation, request.Lease, request.Change.Change.Candidate.SHA, false)
}

func validateObserveGateRequest(request ObserveGateRequest) error {
	if err := validateChange(request.Change); err != nil {
		return err
	}
	if err := validatePublishRequest(PublishRequest{Candidate: request.Candidate, Target: request.Target}); err != nil {
		return err
	}
	if request.Change.Candidate != request.Candidate || request.Change.Target != request.Target {
		return invalidRequest("change identity does not match observed candidate and target")
	}
	return nil
}

func validatePrepareSquashRequest(request PrepareSquashRequest) error {
	if err := validateObserveGateRequest(ObserveGateRequest{Change: request.Change, Candidate: request.Candidate, Target: request.Target}); err != nil {
		return err
	}
	if err := validateEvidence(request.Evidence); err != nil {
		return err
	}
	if request.Change.Draft {
		return invalidRequest("draft change cannot integrate")
	}
	if request.Evidence.Change != request.Change || request.Evidence.CandidateSHA != request.Candidate.SHA || request.Evidence.Target != request.Target || request.Evidence.TestedTreeSHA != request.Candidate.TreeSHA {
		return invalidRequest("evidence does not match squash identity")
	}
	return nil
}

func validateChangeReadyRequest(request ChangeReadyRequest) error {
	if err := validateRemoteChange(request.Change); err != nil {
		return err
	}
	if err := validateEvidence(request.Evidence); err != nil {
		return err
	}
	if request.Evidence.Change != request.Change.Change {
		return invalidRequest("ready evidence does not match change")
	}
	return validateMutationLease(request.Change.Owner, request.Change.Generation, request.Lease, request.Change.Change.Candidate.SHA, false)
}

func validateCancelGateRequest(request CancelGateRequest) error {
	if err := validateRemoteChange(request.Change); err != nil {
		return err
	}
	if strings.TrimSpace(request.Reason) == "" {
		return invalidRequest("cancellation reason is required")
	}
	return validateMutationLease(request.Change.Owner, request.Change.Generation, request.Lease, request.Change.Change.Candidate.SHA, false)
}

func validateReconcileChangeRequest(request ReconcileChangeRequest) error {
	if err := validateRemoteChange(request.Change); err != nil {
		return err
	}
	if !isKnownAttemptedOperation(request.AttemptedOperation) || strings.TrimSpace(request.AttemptID) == "" || request.ObservedOperation != request.AttemptedOperation || request.ObservedAttemptID != request.AttemptID {
		return invalidRequest("reconciliation observation does not match attempted operation")
	}
	if !isObservedOutcome(request.AttemptedOperation, request.ObservedOutcome) {
		return invalidRequest("reconciliation outcome is not valid for operation")
	}
	if isEphemeralTargetOperation(request.AttemptedOperation) {
		return validateMutationLease(request.Change.Owner, request.Change.Generation, request.Lease, request.Change.Change.Candidate.SHA, false)
	}
	if err := validateEvidence(request.Evidence); err != nil {
		return err
	}
	if request.Evidence.Change != request.Change.Change {
		return invalidRequest("reconciliation evidence does not match change")
	}
	if err := validateRunEvidence(request.Run); err != nil {
		return err
	}
	if request.Run.PolicyHash != request.Evidence.PolicyHash {
		return invalidRequest("reconciliation policy does not match evidence")
	}
	if request.Run.Change != request.Change.Change || request.Run.CandidateSHA != request.Evidence.CandidateSHA || request.Run.Target != request.Evidence.Target || request.Run.TestedTreeSHA != request.Evidence.TestedTreeSHA {
		return invalidRequest("reconciliation run identity does not match evidence")
	}
	return validateMutationLease(request.Change.Owner, request.Change.Generation, request.Lease, request.Change.Change.Candidate.SHA, false)
}

func validatePreparedSquash(prepared PreparedSquash) error {
	if strings.TrimSpace(prepared.AttemptKey) == "" || strings.TrimSpace(prepared.SHA) == "" || strings.TrimSpace(prepared.ParentSHA) == "" || strings.TrimSpace(prepared.TreeSHA) == "" || strings.TrimSpace(prepared.LocalRef) == "" {
		return invalidRequest("prepared squash identity is incomplete")
	}
	if err := validatePrepareSquashRequest(PrepareSquashRequest{Change: prepared.Change, Candidate: prepared.Candidate, Target: prepared.Target, Evidence: prepared.Evidence}); err != nil {
		return err
	}
	if prepared.ParentSHA != prepared.Target.SHA || prepared.TreeSHA != prepared.Candidate.TreeSHA {
		return invalidRequest("prepared squash parent or tree does not match tested state")
	}
	return nil
}

func validateChange(change Change) error {
	if strings.TrimSpace(change.ID) == "" {
		return invalidRequest("change ID is required")
	}
	return validatePublishRequest(PublishRequest{Candidate: change.Candidate, Target: change.Target})
}

func validateEvidence(evidence Evidence) error {
	if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.CandidateSHA) == "" || strings.TrimSpace(evidence.TestedTreeSHA) == "" || strings.TrimSpace(evidence.PolicyHash) == "" {
		return invalidRequest("evidence identity is incomplete")
	}
	if err := validateChange(evidence.Change); err != nil {
		return err
	}
	if err := validateTarget(evidence.Target); err != nil {
		return err
	}
	if evidence.CandidateSHA != evidence.Change.Candidate.SHA || evidence.TestedTreeSHA != evidence.Change.Candidate.TreeSHA || evidence.Target != evidence.Change.Target {
		return invalidRequest("evidence identity does not match change")
	}
	return nil
}

func validateEphemeralTarget(target EphemeralTarget) error {
	if err := validateTarget(target.Target); err != nil {
		return err
	}
	if strings.TrimSpace(target.Owner) == "" || target.Generation <= 0 {
		return invalidRequest("ephemeral target ownership is incomplete")
	}
	return nil
}

func validateRemoteChange(change RemoteChange) error {
	if err := validateChange(change.Change); err != nil {
		return err
	}
	if strings.TrimSpace(change.Owner) == "" || change.Generation <= 0 {
		return invalidRequest("remote change ownership is incomplete")
	}
	return nil
}

func validateOwnedLease(owner string, generation int64, lease Lease) error {
	if strings.TrimSpace(owner) == "" || generation <= 0 || lease.Owner != owner || lease.Generation != generation {
		return invalidRequest("lease is foreign or unowned")
	}
	if lease.ExpectedAbsent == (strings.TrimSpace(lease.ExpectedSHA) != "") {
		return invalidRequest("lease must expect absent or exact SHA")
	}
	return nil
}

func validateMutationLease(owner string, generation int64, lease Lease, expectedSHA string, allowAbsent bool) error {
	if err := validateOwnedLease(owner, generation, lease); err != nil {
		return err
	}
	if lease.ExpectedAbsent {
		if allowAbsent {
			return nil
		}
		return invalidRequest("mutation lease cannot expect absent")
	}
	if lease.ExpectedSHA != expectedSHA {
		return invalidRequest("mutation lease SHA does not match request")
	}
	return nil
}

func validateRunEvidence(evidence RunEvidence) error {
	if err := validateChange(evidence.Change); err != nil {
		return invalidRequest("run change identity is invalid")
	}
	if evidence.CandidateSHA != evidence.Change.Candidate.SHA || evidence.Target != evidence.Change.Target || evidence.TestedTreeSHA != evidence.Change.Candidate.TreeSHA {
		return invalidRequest("run identity does not match change")
	}
	if err := ValidateWorkflowEvidence(evidence.Workflow); err != nil {
		return invalidRequest("workflow evidence is invalid")
	}
	if strings.TrimSpace(evidence.RunID) == "" || strings.TrimSpace(evidence.PolicyHash) == "" || len(evidence.Checks) == 0 || len(evidence.Pages) == 0 || len(evidence.ExpectedPages) == 0 || strings.TrimSpace(evidence.ExpectedWorkflowPath) == "" || strings.TrimSpace(evidence.ExpectedWorkflowRef) == "" || strings.TrimSpace(evidence.ExpectedRunID) == "" || len(evidence.ExpectedCheckIDs) == 0 {
		return invalidRequest("run evidence is incomplete")
	}
	if evidence.Workflow.Path != evidence.ExpectedWorkflowPath || evidence.Workflow.Ref != evidence.ExpectedWorkflowRef || evidence.RunID != evidence.ExpectedRunID {
		return invalidRequest("run evidence identity does not match expected gate")
	}
	if !sameStringSet(checkIDs(evidence.Checks), evidence.ExpectedCheckIDs) || !sameIntSet(pageNumbers(evidence.Pages), evidence.ExpectedPages) {
		return invalidRequest("run evidence collection does not match expected gate")
	}
	for _, check := range evidence.Checks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Conclusion) == "" {
			return invalidRequest("check evidence is incomplete")
		}
	}
	for _, page := range evidence.Pages {
		if page.Number <= 0 || !page.Complete {
			return invalidRequest("page evidence is incomplete")
		}
	}
	return nil
}

func checkIDs(checks []CheckEvidence) []string {
	ids := make([]string, 0, len(checks))
	for _, check := range checks {
		ids = append(ids, check.ID)
	}
	return ids
}

func pageNumbers(pages []PageEvidence) []int {
	numbers := make([]int, 0, len(pages))
	for _, page := range pages {
		numbers = append(numbers, page.Number)
	}
	return numbers
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	if len(seen) != len(right) {
		return false
	}
	return true
}

func sameIntSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[int]struct{}, len(left))
	for _, value := range left {
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	if len(seen) != len(right) {
		return false
	}
	return true
}

func isKnownAttemptedOperation(operation string) bool {
	switch strings.TrimSpace(operation) {
	case "create_ephemeral_target", "delete_ephemeral_target", "ensure_change", "set_ready", "cancel":
		return true
	default:
		return false
	}
}

func isEphemeralTargetOperation(operation string) bool {
	return operation == "create_ephemeral_target" || operation == "delete_ephemeral_target"
}

func isObservedOutcome(operation, outcome string) bool {
	switch operation {
	case "create_ephemeral_target", "ensure_change":
		return outcome == "accepted" || outcome == "adopted" || outcome == "already_exists"
	case "delete_ephemeral_target":
		return outcome == "accepted" || outcome == "not_found" || outcome == "already_deleted"
	case "set_ready":
		return outcome == "accepted" || outcome == "already_ready"
	case "cancel":
		return outcome == "accepted" || outcome == "already_cancelled"
	default:
		return false
	}
}

func validateCandidate(candidate Candidate) error {
	if err := validateRepository(candidate.Repository); err != nil {
		return err
	}
	if strings.TrimSpace(candidate.Ref) == "" || strings.TrimSpace(candidate.SHA) == "" || strings.TrimSpace(candidate.TreeSHA) == "" {
		return invalidRequest("candidate identity is incomplete")
	}
	return nil
}

func validateTarget(target Target) error {
	if err := validateRepository(target.Repository); err != nil {
		return err
	}
	if strings.TrimSpace(target.Ref) == "" || strings.TrimSpace(target.SHA) == "" {
		return invalidRequest("target identity is incomplete")
	}
	return nil
}

func validateRepository(repository Repository) error {
	if strings.TrimSpace(repository.Host) == "" || strings.TrimSpace(repository.Owner) == "" || strings.TrimSpace(repository.Name) == "" {
		return invalidRequest("repository identity is incomplete")
	}
	return nil
}

func invalidRequest(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, message)
}
