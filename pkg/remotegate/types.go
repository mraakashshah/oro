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
	Publish(context.Context, PublishRequest) (PublishedCandidate, error)
	Observe(context.Context, ObserveGateRequest) (RemoteGateObservation, error)
	PrepareSquash(context.Context, PrepareSquashRequest) (PreparedSquash, error)
	IntegrateSquashCAS(context.Context, PreparedSquash) (MergeResult, error)
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
	case ObserveGateRequest:
		return validateObserveGateRequest(typed)
	case PrepareSquashRequest:
		return validatePrepareSquashRequest(typed)
	case PreparedSquash:
		return validatePreparedSquash(typed)
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
	if request.Evidence.Change != request.Change || request.Evidence.CandidateSHA != request.Candidate.SHA || request.Evidence.Target != request.Target || request.Evidence.TestedTreeSHA != request.Candidate.TreeSHA {
		return invalidRequest("evidence does not match squash identity")
	}
	return nil
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
	return validateTarget(evidence.Target)
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
