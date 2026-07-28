package remotegate_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/remotegate"
	"oro/pkg/remotegate/github"
)

type providerAdapterContract struct {
	Client github.Client
}

func TestRemoteGateLifecycleContract(t *testing.T) {
	identity := remotegate.Repository{Host: "code.example", Owner: "oro", Name: "oro"}
	target := remotegate.Target{Repository: identity, Ref: "main", SHA: "base"}
	candidate := remotegate.Candidate{Repository: identity, Ref: "refs/oro/candidate/1", SHA: "candidate", TreeSHA: "tree"}
	change := remotegate.Change{ID: "change-1", Candidate: candidate, Target: target}
	evidence := remotegate.Evidence{ID: "evidence-1", Change: change, CandidateSHA: candidate.SHA, Target: target, TestedTreeSHA: candidate.TreeSHA, PolicyHash: "policy"}
	prepared := remotegate.PreparedSquash{AttemptKey: "attempt-1", Change: change, Candidate: candidate, Target: target, Evidence: evidence, SHA: "squash", ParentSHA: target.SHA, TreeSHA: candidate.TreeSHA, LocalRef: "refs/oro/integrations/attempt-1"}

	assertClientSignature(t)
	assertTransportNeutral(t, contractTypes())
	if !isTransportNeutral(reflect.TypeOf((*context.Context)(nil)).Elem()) {
		t.Fatal("context.Context must remain transport-neutral")
	}
	if !isTransportNeutral(reflect.TypeOf((*error)(nil)).Elem()) {
		t.Fatal("error must remain transport-neutral")
	}
	if isTransportNeutral(reflect.TypeOf(providerAdapterContract{})) {
		t.Fatal("concrete provider adapter must not be transport-neutral")
	}
	for _, class := range []error{
		remotegate.ErrInvalidRequest,
		remotegate.ErrDeterministic,
		remotegate.ErrTransient,
		remotegate.ErrAuth,
		remotegate.ErrConfig,
		remotegate.ErrAmbiguous,
		remotegate.ErrInvalidPolicyEvidence,
		remotegate.ErrWorkflowIneligible,
	} {
		if class == nil {
			t.Fatal("remote-gate error class is nil")
		}
		assertTransportNeutral(t, []reflect.Type{reflect.TypeOf(class)})
	}

	valid := map[string]any{
		"preflight": remotegate.PreflightRequest{Repository: identity, Target: target},
		"publish":   remotegate.PublishRequest{Candidate: candidate, Target: target},
		"observe":   remotegate.ObserveGateRequest{Change: change, Candidate: candidate, Target: target},
		"prepare":   remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: evidence},
		"prepared":  prepared,
	}
	for name, request := range valid {
		if err := remotegate.ValidateRequest(request); err != nil {
			t.Errorf("ValidateRequest(%s) = %v", name, err)
		}
	}

	mismatchedRepository := candidate
	mismatchedRepository.Repository.Name = "other"
	mismatchedChange := change
	mismatchedChange.Candidate.TreeSHA = "other-tree"
	mismatchedEvidenceCandidate := evidence
	mismatchedEvidenceCandidate.CandidateSHA = "other-candidate"
	mismatchedEvidenceChange := evidence
	mismatchedEvidenceChange.Change.ID = "other-change"
	mismatchedEvidenceTarget := evidence
	mismatchedEvidenceTarget.Target.SHA = "other-base"
	mismatchedEvidenceTree := evidence
	mismatchedEvidenceTree.TestedTreeSHA = "other-tree"
	emptyEvidencePolicyHash := evidence
	emptyEvidencePolicyHash.PolicyHash = ""
	mismatchedParent := prepared
	mismatchedParent.ParentSHA = "other-base"
	mismatchedTree := prepared
	mismatchedTree.TreeSHA = "other-tree"

	for name, request := range map[string]any{
		"nil":                   nil,
		"incomplete preflight":  remotegate.PreflightRequest{},
		"incomplete publish":    remotegate.PublishRequest{},
		"incomplete observe":    remotegate.ObserveGateRequest{},
		"incomplete prepare":    remotegate.PrepareSquashRequest{},
		"incomplete prepared":   remotegate.PreparedSquash{},
		"candidate repository":  remotegate.PublishRequest{Candidate: mismatchedRepository, Target: target},
		"preflight repository":  remotegate.PreflightRequest{Repository: identity, Target: remotegate.Target{Repository: mismatchedRepository.Repository, Ref: target.Ref, SHA: target.SHA}},
		"change candidate":      remotegate.ObserveGateRequest{Change: mismatchedChange, Candidate: candidate, Target: target},
		"change target":         remotegate.ObserveGateRequest{Change: change, Candidate: candidate, Target: remotegate.Target{Repository: identity, Ref: "release", SHA: target.SHA}},
		"evidence candidate":    remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceCandidate},
		"evidence change":       remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceChange},
		"evidence target":       remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceTarget},
		"evidence tree":         remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceTree},
		"empty evidence policy": remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: emptyEvidencePolicyHash},
		"prepared parent":       mismatchedParent,
		"prepared tree":         mismatchedTree,
	} {
		if err := remotegate.ValidateRequest(request); !errors.Is(err, remotegate.ErrInvalidRequest) {
			t.Errorf("ValidateRequest(%s) error = %v, want ErrInvalidRequest", name, err)
		}
	}

	owned := remotegate.RemoteChange{Change: remotegate.Change{ID: "change-1", Candidate: candidate, Target: target, Draft: true}, Owner: "worker-1", Generation: 7}
	lease := remotegate.Lease{Owner: "worker-1", Generation: 7, ExpectedSHA: candidate.SHA}
	readyEvidence := remotegate.Evidence{ID: "evidence-1", Change: owned.Change, CandidateSHA: candidate.SHA, Target: target, TestedTreeSHA: candidate.TreeSHA, PolicyHash: "policy"}
	if err := remotegate.ValidateRequest(remotegate.ChangeReadyRequest{Change: owned, Evidence: readyEvidence, Lease: lease}); err != nil {
		t.Fatalf("draft ready rejected: %v", err)
	}
	if err := remotegate.ValidateRequest(remotegate.PrepareSquashRequest{Change: owned.Change, Candidate: candidate, Target: target, Evidence: readyEvidence}); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("draft integration error = %v, want ErrInvalidRequest", err)
	}

	ephemeral := remotegate.EphemeralTarget{
		ProjectID: "project-1", EpicID: "epic-1",
		Target: remotegate.Target{Repository: identity, Ref: "refs/heads/epic/1", SHA: "seed-sha"},
		Owner:  "worker-1", Generation: 7,
	}
	create := remotegate.ReconcileChangeRequest{
		EphemeralTarget: ephemeral, SeedSHA: ephemeral.Target.SHA,
		AttemptedOperation: "create_ephemeral_target", AttemptID: "attempt-create",
		ObservedOperation: "create_ephemeral_target", ObservedAttemptID: "attempt-create", ObservedOutcome: "accepted",
		Lease: remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedAbsent: true},
	}
	delete := remotegate.ReconcileChangeRequest{
		EphemeralTarget: ephemeral, FinalSHA: ephemeral.Target.SHA,
		AttemptedOperation: "delete_ephemeral_target", AttemptID: "attempt-delete",
		ObservedOperation: "delete_ephemeral_target", ObservedAttemptID: "attempt-delete", ObservedOutcome: "accepted",
		Lease: remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedSHA: ephemeral.Target.SHA},
	}
	for name, request := range map[string]remotegate.ReconcileChangeRequest{"create": create, "delete": delete} {
		if err := remotegate.ValidateRequest(request); err != nil {
			t.Errorf("valid %s reconciliation rejected: %v", name, err)
		}
	}

	workflow := remotegate.WorkflowEvidence{Path: ".github/workflows/quality.yml", State: "active", Ref: "refs/heads/main", WorkflowDispatch: true, PullRequestTargets: []string{"main"}}
	run := remotegate.RunEvidence{
		Change: owned.Change, CandidateSHA: candidate.SHA, Target: target, TestedTreeSHA: candidate.TreeSHA,
		Workflow: workflow, RunID: "run-1", PolicyHash: readyEvidence.PolicyHash,
		Checks: []remotegate.CheckEvidence{{ID: "check-1", Name: "quality", Conclusion: "success"}},
		Pages:  []remotegate.PageEvidence{{Number: 1, Complete: true}, {Number: 2, Complete: true}}, ExpectedPages: []int{1, 2},
		ExpectedWorkflowPath: workflow.Path, ExpectedWorkflowRef: workflow.Ref, ExpectedRunID: "run-1", ExpectedCheckIDs: []string{"check-1"},
	}
	pr := remotegate.ReconcileChangeRequest{
		Change: owned, Evidence: readyEvidence, Run: run,
		AttemptedOperation: "set_ready", AttemptID: "attempt-ready", ObservedOperation: "set_ready", ObservedAttemptID: "attempt-ready", ObservedOutcome: "accepted", Lease: lease,
	}
	if err := remotegate.ValidateRequest(pr); err != nil {
		t.Fatalf("valid PR reconciliation rejected: %v", err)
	}
	ensureEphemeral := remotegate.EnsureEphemeralTargetRequest{
		ProjectID: "project-1", EpicID: "epic-1", Target: ephemeral.Target,
		SeedSHA: ephemeral.Target.SHA, Owner: ephemeral.Owner, Generation: ephemeral.Generation,
		Lease: remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedAbsent: true},
	}
	deleteEphemeral := remotegate.DeleteEphemeralTargetRequest{
		Target: ephemeral, FinalSHA: ephemeral.Target.SHA, Retired: true,
		Lease: remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedSHA: ephemeral.Target.SHA},
	}
	ensureChange := remotegate.EnsureChangeRequest{Change: owned, Lease: lease}
	ready := remotegate.ChangeReadyRequest{Change: owned, Evidence: readyEvidence, Lease: lease}
	cancel := remotegate.CancelGateRequest{Change: owned, Reason: "operator requested", Lease: lease}
	mutations := map[string]any{
		"EnsureEphemeralTarget":  ensureEphemeral,
		"DeleteEphemeralTarget":  deleteEphemeral,
		"EnsureChange":           ensureChange,
		"ChangeReady":            ready,
		"CancelGateRequest":      cancel,
		"ReconcileChangeRequest": pr,
	}
	for name, request := range mutations {
		if err := remotegate.ValidateRequest(request); err != nil {
			t.Fatalf("valid %s rejected: %v", name, err)
		}
	}

	reject := func(name string, request remotegate.ReconcileChangeRequest) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if err := remotegate.ValidateRequest(request); !errors.Is(err, remotegate.ErrInvalidRequest) {
				t.Fatalf("ValidateRequest() = %v, want ErrInvalidRequest", err)
			}
		})
	}
	wrongEnsureEphemeral := ensureEphemeral
	wrongEnsureEphemeral.Lease.ExpectedAbsent = false
	wrongEnsureEphemeral.Lease.ExpectedSHA = "wrong-sha"
	if err := remotegate.ValidateRequest(wrongEnsureEphemeral); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("EnsureEphemeralTarget wrong lease error = %v, want ErrInvalidRequest", err)
	}
	wrongDeleteEphemeral := deleteEphemeral
	wrongDeleteEphemeral.Lease.ExpectedSHA = "wrong-sha"
	if err := remotegate.ValidateRequest(wrongDeleteEphemeral); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("DeleteEphemeralTarget wrong lease error = %v, want ErrInvalidRequest", err)
	}
	wrongEnsureChange := ensureChange
	wrongEnsureChange.Lease.ExpectedSHA = "wrong-sha"
	if err := remotegate.ValidateRequest(wrongEnsureChange); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("EnsureChange wrong lease error = %v, want ErrInvalidRequest", err)
	}
	wrongReady := ready
	wrongReady.Lease.ExpectedSHA = "wrong-sha"
	if err := remotegate.ValidateRequest(wrongReady); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("ChangeReady wrong lease error = %v, want ErrInvalidRequest", err)
	}
	wrongCancel := cancel
	wrongCancel.Lease.ExpectedSHA = "wrong-sha"
	if err := remotegate.ValidateRequest(wrongCancel); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("CancelGateRequest wrong lease error = %v, want ErrInvalidRequest", err)
	}
	wrongPRLease := pr
	wrongPRLease.Lease.ExpectedSHA = "wrong-sha"
	if err := remotegate.ValidateRequest(wrongPRLease); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("ReconcileChangeRequest wrong lease error = %v, want ErrInvalidRequest", err)
	}

	missingIdentity := create
	missingIdentity.EphemeralTarget.Target.Ref = ""
	reject("missing ephemeral target ref", missingIdentity)
	missingTargetSHA := create
	missingTargetSHA.EphemeralTarget.Target.SHA = ""
	reject("missing ephemeral target SHA", missingTargetSHA)
	missingTargetRepository := create
	missingTargetRepository.EphemeralTarget.Target.Repository = remotegate.Repository{}
	reject("missing ephemeral target repository", missingTargetRepository)
	deleteExpectedAbsent := delete
	deleteExpectedAbsent.Lease.ExpectedAbsent = true
	deleteExpectedAbsent.Lease.ExpectedSHA = ""
	reject("delete reconciliation expected absent", deleteExpectedAbsent)
	populatedChange := create
	populatedChange.Change = owned
	reject("populated remote change for early operation", populatedChange)
	wrongSeed := create
	wrongSeed.SeedSHA = "wrong-seed"
	reject("wrong seed", wrongSeed)
	wrongFinal := delete
	wrongFinal.FinalSHA = "wrong-final"
	reject("wrong final", wrongFinal)
	wrongCreateLease := create
	wrongCreateLease.Lease.ExpectedAbsent = false
	wrongCreateLease.Lease.ExpectedSHA = "seed-sha"
	reject("wrong create lease", wrongCreateLease)
	wrongDeleteLease := delete
	wrongDeleteLease.Lease.ExpectedSHA = "wrong-final"
	reject("wrong delete lease", wrongDeleteLease)
	alternate := pr
	alternate.Run.Workflow.Path = "other.yml"
	alternate.Run.Workflow.Ref = "refs/heads/release"
	alternate.Run.RunID = "run-2"
	alternate.Run.Checks = []remotegate.CheckEvidence{{ID: "check-2", Name: "other", Conclusion: "success"}}
	alternate.Run.Pages = []remotegate.PageEvidence{{Number: 3, Complete: true}}
	reject("alternate observed run identity", alternate)
	unknownOutcome := pr
	unknownOutcome.ObservedOutcome = "mystery"
	reject("unknown outcome", unknownOutcome)
	mismatchedOperation := pr
	mismatchedOperation.ObservedOperation = "cancel"
	reject("mismatched operation", mismatchedOperation)
	mismatchedAttempt := pr
	mismatchedAttempt.ObservedAttemptID = "attempt-other"
	reject("mismatched attempt", mismatchedAttempt)
}

func TestRemoteGateOperationObservationIdentity(t *testing.T) {
	identity := remotegate.Repository{Host: "code.example", Owner: "oro", Name: "oro"}
	target := remotegate.Target{Repository: identity, Ref: "main", SHA: "base-sha"}
	candidate := remotegate.Candidate{Repository: identity, Ref: "refs/oro/candidate/1", SHA: "candidate-sha", TreeSHA: "tree-sha"}
	change := remotegate.Change{ID: "change-1", Candidate: candidate, Target: target, Draft: true}
	evidence := remotegate.Evidence{ID: "evidence-1", Change: change, CandidateSHA: candidate.SHA, Target: target, TestedTreeSHA: candidate.TreeSHA, PolicyHash: "policy-hash"}
	owned := remotegate.RemoteChange{Change: change, Owner: "worker-1", Generation: 7}
	lease := remotegate.Lease{Owner: "worker-1", Generation: 7, ExpectedSHA: candidate.SHA}

	if err := remotegate.ValidateRequest(remotegate.ChangeReadyRequest{Change: owned, Evidence: evidence, Lease: lease}); err != nil {
		t.Fatalf("draft ready rejected: %v", err)
	}
	if err := remotegate.ValidateRequest(remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: evidence}); !errors.Is(err, remotegate.ErrInvalidRequest) {
		t.Fatalf("draft squash error = %v, want ErrInvalidRequest", err)
	}

	ephemeral := remotegate.EphemeralTarget{ProjectID: "project-1", EpicID: "epic-1", Target: remotegate.Target{Repository: identity, Ref: "refs/heads/epic/1", SHA: "seed-sha"}, Owner: "worker-1", Generation: 7}
	mutationRequests := map[string]any{
		"create ephemeral": remotegate.EnsureEphemeralTargetRequest{ProjectID: "project-1", EpicID: "epic-1", Target: ephemeral.Target, SeedSHA: ephemeral.Target.SHA, Owner: ephemeral.Owner, Generation: ephemeral.Generation, Lease: remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedAbsent: true}},
		"delete ephemeral": remotegate.DeleteEphemeralTargetRequest{Target: ephemeral, FinalSHA: ephemeral.Target.SHA, Retired: true, Lease: remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedSHA: ephemeral.Target.SHA}},
		"ensure change":    remotegate.EnsureChangeRequest{Change: owned, Lease: lease},
		"ready":            remotegate.ChangeReadyRequest{Change: owned, Evidence: evidence, Lease: lease},
		"cancel":           remotegate.CancelGateRequest{Change: owned, Reason: "operator requested", Lease: lease},
		"reconcile":        remotegate.ReconcileChangeRequest{Change: owned, Evidence: evidence, Lease: lease},
	}
	for name, request := range mutationRequests {
		t.Run(name+" wrong lease", func(t *testing.T) {
			mutated := replaceExpectedSHA(request, "wrong-sha")
			if err := remotegate.ValidateRequest(mutated); !errors.Is(err, remotegate.ErrInvalidRequest) {
				t.Fatalf("wrong lease error = %v, want ErrInvalidRequest", err)
			}
		})
	}

	workflow := remotegate.WorkflowEvidence{Path: ".github/workflows/quality.yml", State: "active", Ref: "refs/heads/main", WorkflowDispatch: true, PullRequestTargets: []string{"main"}}
	run := remotegate.RunEvidence{
		Change: change, CandidateSHA: candidate.SHA, Target: target, TestedTreeSHA: candidate.TreeSHA,
		Workflow: workflow, RunID: "run-1", PolicyHash: evidence.PolicyHash,
		Checks: []remotegate.CheckEvidence{{ID: "check-1", Name: "quality", Conclusion: "success"}},
		Pages:  []remotegate.PageEvidence{{Number: 1, Complete: true}, {Number: 2, Complete: true}}, ExpectedPages: []int{1, 2},
		ExpectedWorkflowPath: workflow.Path, ExpectedWorkflowRef: workflow.Ref, ExpectedRunID: "run-1", ExpectedCheckIDs: []string{"check-1"},
	}
	valid := remotegate.ReconcileChangeRequest{Change: owned, Evidence: evidence, Run: run, AttemptedOperation: "set_ready", AttemptID: "attempt-1", ObservedOperation: "set_ready", ObservedAttemptID: "attempt-1", ObservedOutcome: "accepted", Lease: lease}
	if err := remotegate.ValidateRequest(valid); err != nil {
		t.Fatalf("valid reconciliation rejected: %v", err)
	}
	for name, operation := range map[string]string{
		"create ephemeral target": "create_ephemeral_target",
		"delete ephemeral target": "delete_ephemeral_target",
	} {
		t.Run(name+" without gate evidence", func(t *testing.T) {
			request := remotegate.ReconcileChangeRequest{
				AttemptedOperation: operation, AttemptID: "attempt-1",
				ObservedOperation: operation, ObservedAttemptID: "attempt-1", ObservedOutcome: "accepted", EphemeralTarget: ephemeral,
			}
			if operation == "create_ephemeral_target" {
				request.SeedSHA = ephemeral.Target.SHA
				request.Lease = remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedAbsent: true}
			} else {
				request.FinalSHA = ephemeral.Target.SHA
				request.Lease = remotegate.Lease{Owner: ephemeral.Owner, Generation: ephemeral.Generation, ExpectedSHA: ephemeral.Target.SHA}
			}
			if err := remotegate.ValidateRequest(request); err != nil {
				t.Fatalf("ephemeral reconciliation rejected without gate evidence: %v", err)
			}
		})
	}

	invalid := map[string]remotegate.ReconcileChangeRequest{}
	for name, mutate := range map[string]func(*remotegate.ReconcileChangeRequest){
		"workflow path": func(r *remotegate.ReconcileChangeRequest) { r.Run.Workflow.Path = "other.yml" },
		"workflow ref":  func(r *remotegate.ReconcileChangeRequest) { r.Run.Workflow.Ref = "refs/heads/release" },
		"run ID":        func(r *remotegate.ReconcileChangeRequest) { r.Run.RunID = "run-2" },
		"check ID":      func(r *remotegate.ReconcileChangeRequest) { r.Run.Checks[0].ID = "check-2" },
		"terminal pages": func(r *remotegate.ReconcileChangeRequest) {
			r.Run.Pages = []remotegate.PageEvidence{{Number: 1, Complete: true}, {Number: 3, Complete: true}}
		},
		"operation":       func(r *remotegate.ReconcileChangeRequest) { r.ObservedOperation = "cancel" },
		"attempt":         func(r *remotegate.ReconcileChangeRequest) { r.ObservedAttemptID = "attempt-2" },
		"unknown outcome": func(r *remotegate.ReconcileChangeRequest) { r.ObservedOutcome = "mystery" },
		"wrong lease":     func(r *remotegate.ReconcileChangeRequest) { r.Lease.ExpectedSHA = "other-sha" },
	} {
		request := valid
		request.Run.Checks = append([]remotegate.CheckEvidence(nil), valid.Run.Checks...)
		request.Run.Pages = append([]remotegate.PageEvidence(nil), valid.Run.Pages...)
		mutate(&request)
		invalid[name] = request
	}
	for name, request := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := remotegate.ValidateRequest(request); !errors.Is(err, remotegate.ErrInvalidRequest) {
				t.Fatalf("ValidateRequest() = %v, want ErrInvalidRequest", err)
			}
		})
	}

	for name, operation := range map[string]string{
		"create": "create_ephemeral_target", "delete": "delete_ephemeral_target", "ensure": "ensure_change", "ready": "set_ready", "cancel": "cancel",
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			request.AttemptedOperation, request.ObservedOperation = operation, operation
			request.AttemptID, request.ObservedAttemptID = "attempt-1", "attempt-1"
			request.ObservedOutcome = "accepted"
			if operation == "create_ephemeral_target" || operation == "delete_ephemeral_target" {
				return
			}
			if err := remotegate.ValidateRequest(request); err != nil {
				t.Fatalf("operation-specific observation rejected: %v", err)
			}
		})
	}
}

func replaceExpectedSHA(request any, sha string) any {
	switch typed := request.(type) {
	case remotegate.EnsureEphemeralTargetRequest:
		typed.Lease.ExpectedAbsent = false
		typed.Lease.ExpectedSHA = sha
		return typed
	case remotegate.DeleteEphemeralTargetRequest:
		typed.Lease.ExpectedSHA = sha
		return typed
	case remotegate.EnsureChangeRequest:
		typed.Lease.ExpectedSHA = sha
		return typed
	case remotegate.ChangeReadyRequest:
		typed.Lease.ExpectedSHA = sha
		return typed
	case remotegate.CancelGateRequest:
		typed.Lease.ExpectedSHA = sha
		return typed
	case remotegate.ReconcileChangeRequest:
		typed.Lease.ExpectedSHA = sha
		return typed
	default:
		panic("unsupported mutation request")
	}
}

func assertClientSignature(t *testing.T) {
	t.Helper()
	clientType := reflect.TypeOf((*remotegate.RemoteGateClient)(nil)).Elem()
	wantMethods := map[string]reflect.Type{
		"Preflight":             reflect.TypeOf((func(context.Context, remotegate.PreflightRequest) (remotegate.Capabilities, error))(nil)),
		"EnsureEphemeralTarget": reflect.TypeOf((func(context.Context, remotegate.EnsureEphemeralTargetRequest) (remotegate.EphemeralTarget, error))(nil)),
		"DeleteEphemeralTarget": reflect.TypeOf((func(context.Context, remotegate.DeleteEphemeralTargetRequest) error)(nil)),
		"Publish":               reflect.TypeOf((func(context.Context, remotegate.PublishRequest) (remotegate.PublishedCandidate, error))(nil)),
		"EnsureChange":          reflect.TypeOf((func(context.Context, remotegate.EnsureChangeRequest) (remotegate.RemoteChange, error))(nil)),
		"Observe":               reflect.TypeOf((func(context.Context, remotegate.ObserveGateRequest) (remotegate.RemoteGateObservation, error))(nil)),
		"SetChangeReady":        reflect.TypeOf((func(context.Context, remotegate.ChangeReadyRequest) (remotegate.RemoteChange, error))(nil)),
		"PrepareSquash":         reflect.TypeOf((func(context.Context, remotegate.PrepareSquashRequest) (remotegate.PreparedSquash, error))(nil)),
		"IntegrateSquashCAS":    reflect.TypeOf((func(context.Context, remotegate.PreparedSquash) (remotegate.MergeResult, error))(nil)),
		"Cancel":                reflect.TypeOf((func(context.Context, remotegate.CancelGateRequest) error)(nil)),
		"Reconcile":             reflect.TypeOf((func(context.Context, remotegate.ReconcileChangeRequest) error)(nil)),
	}
	if clientType.NumMethod() != len(wantMethods) {
		t.Fatalf("RemoteGateClient method count = %d, want %d", clientType.NumMethod(), len(wantMethods))
	}
	for name, want := range wantMethods {
		method, ok := clientType.MethodByName(name)
		if !ok {
			t.Errorf("RemoteGateClient missing %s", name)
			continue
		}
		if method.Type != want {
			t.Errorf("RemoteGateClient.%s type = %v, want %v", name, method.Type, want)
		}
	}
}

func contractTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(remotegate.Repository{}),
		reflect.TypeOf(remotegate.Candidate{}),
		reflect.TypeOf(remotegate.Target{}),
		reflect.TypeOf(remotegate.Change{}),
		reflect.TypeOf(remotegate.Evidence{}),
		reflect.TypeOf(remotegate.PublishedCandidate{}),
		reflect.TypeOf(remotegate.RemoteGateObservation{}),
		reflect.TypeOf(remotegate.PreflightRequest{}),
		reflect.TypeOf(remotegate.PublishRequest{}),
		reflect.TypeOf(remotegate.ObserveGateRequest{}),
		reflect.TypeOf(remotegate.PrepareSquashRequest{}),
		reflect.TypeOf(remotegate.PreparedSquash{}),
		reflect.TypeOf(remotegate.MergeResult{}),
		reflect.TypeOf(remotegate.Policy{}),
		reflect.TypeOf(remotegate.Audit{}),
		reflect.TypeOf(remotegate.Capabilities{}),
		reflect.TypeOf(remotegate.WorkflowEvidence{}),
		reflect.TypeOf(remotegate.Lease{}),
		reflect.TypeOf(remotegate.EphemeralTarget{}),
		reflect.TypeOf(remotegate.RemoteChange{}),
		reflect.TypeOf(remotegate.CheckEvidence{}),
		reflect.TypeOf(remotegate.PageEvidence{}),
		reflect.TypeOf(remotegate.RunEvidence{}),
		reflect.TypeOf(remotegate.EnsureEphemeralTargetRequest{}),
		reflect.TypeOf(remotegate.DeleteEphemeralTargetRequest{}),
		reflect.TypeOf(remotegate.EnsureChangeRequest{}),
		reflect.TypeOf(remotegate.ChangeReadyRequest{}),
		reflect.TypeOf(remotegate.CancelGateRequest{}),
		reflect.TypeOf(remotegate.ReconcileChangeRequest{}),
		reflect.TypeOf((*remotegate.RemoteGateClient)(nil)).Elem(),
	}
}

func assertTransportNeutral(t *testing.T, roots []reflect.Type) {
	t.Helper()
	for _, root := range roots {
		if !isTransportNeutral(root) {
			t.Errorf("contract type %s exposes a provider transport package", root)
		}
	}
}

func isTransportNeutral(typ reflect.Type) bool {
	return !hasProviderTransport(typ, make(map[reflect.Type]struct{}))
}

func hasProviderTransport(typ reflect.Type, seen map[reflect.Type]struct{}) bool {
	if typ == nil {
		return false
	}
	if _, ok := seen[typ]; ok {
		return false
	}
	seen[typ] = struct{}{}
	if packagePathIsProviderTransport(typ.PkgPath()) {
		return true
	}
	switch typ.Kind() {
	case reflect.Array, reflect.Pointer, reflect.Slice:
		return hasProviderTransport(typ.Elem(), seen)
	case reflect.Map:
		return hasProviderTransport(typ.Key(), seen) || hasProviderTransport(typ.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < typ.NumField(); index++ {
			if hasProviderTransport(typ.Field(index).Type, seen) {
				return true
			}
		}
	case reflect.Interface:
		for index := 0; index < typ.NumMethod(); index++ {
			method := typ.Method(index).Type
			for input := 0; input < method.NumIn(); input++ {
				if hasProviderTransport(method.In(input), seen) {
					return true
				}
			}
			for output := 0; output < method.NumOut(); output++ {
				if hasProviderTransport(method.Out(output), seen) {
					return true
				}
			}
		}
	}
	return false
}

func packagePathIsProviderTransport(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), "oro/pkg/remotegate/")
}
