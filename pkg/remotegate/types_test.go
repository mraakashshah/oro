package remotegate_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/remotegate"
)

func TestRemoteGateContracts(t *testing.T) {
	identity := remotegate.Repository{Host: "code.example", Owner: "oro", Name: "oro"}
	target := remotegate.Target{Repository: identity, Ref: "main", SHA: "base"}
	candidate := remotegate.Candidate{Repository: identity, Ref: "refs/oro/candidate/1", SHA: "candidate", TreeSHA: "tree"}
	change := remotegate.Change{ID: "change-1", Candidate: candidate, Target: target}
	evidence := remotegate.Evidence{ID: "evidence-1", Change: change, CandidateSHA: candidate.SHA, Target: target, TestedTreeSHA: candidate.TreeSHA}
	prepared := remotegate.PreparedSquash{AttemptKey: "attempt-1", Change: change, Candidate: candidate, Target: target, Evidence: evidence, SHA: "squash", ParentSHA: target.SHA, TreeSHA: candidate.TreeSHA, LocalRef: "refs/oro/integrations/attempt-1"}

	assertClientSignature(t)
	assertTransportNeutral(t, contractTypes())
	for _, class := range []error{
		remotegate.ErrInvalidRequest,
		remotegate.ErrDeterministic,
		remotegate.ErrTransient,
		remotegate.ErrAuth,
		remotegate.ErrConfig,
		remotegate.ErrAmbiguous,
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
	mismatchedEvidenceTarget := evidence
	mismatchedEvidenceTarget.Target.SHA = "other-base"
	mismatchedEvidenceTree := evidence
	mismatchedEvidenceTree.TestedTreeSHA = "other-tree"
	mismatchedParent := prepared
	mismatchedParent.ParentSHA = "other-base"
	mismatchedTree := prepared
	mismatchedTree.TreeSHA = "other-tree"

	for name, request := range map[string]any{
		"nil":                  nil,
		"incomplete preflight": remotegate.PreflightRequest{},
		"incomplete publish":   remotegate.PublishRequest{},
		"incomplete observe":   remotegate.ObserveGateRequest{},
		"incomplete prepare":   remotegate.PrepareSquashRequest{},
		"incomplete prepared":  remotegate.PreparedSquash{},
		"candidate repository": remotegate.PublishRequest{Candidate: mismatchedRepository, Target: target},
		"preflight repository": remotegate.PreflightRequest{Repository: identity, Target: remotegate.Target{Repository: mismatchedRepository.Repository, Ref: target.Ref, SHA: target.SHA}},
		"change candidate":     remotegate.ObserveGateRequest{Change: mismatchedChange, Candidate: candidate, Target: target},
		"change target":        remotegate.ObserveGateRequest{Change: change, Candidate: candidate, Target: remotegate.Target{Repository: identity, Ref: "release", SHA: target.SHA}},
		"evidence candidate":   remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceCandidate},
		"evidence target":      remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceTarget},
		"evidence tree":        remotegate.PrepareSquashRequest{Change: change, Candidate: candidate, Target: target, Evidence: mismatchedEvidenceTree},
		"prepared parent":      mismatchedParent,
		"prepared tree":        mismatchedTree,
	} {
		if err := remotegate.ValidateRequest(request); !errors.Is(err, remotegate.ErrInvalidRequest) {
			t.Errorf("ValidateRequest(%s) error = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func assertClientSignature(t *testing.T) {
	t.Helper()
	clientType := reflect.TypeOf((*remotegate.RemoteGateClient)(nil)).Elem()
	wantMethods := map[string]reflect.Type{
		"Preflight":          reflect.TypeOf((func(context.Context, remotegate.PreflightRequest) (remotegate.Capabilities, error))(nil)),
		"Publish":            reflect.TypeOf((func(context.Context, remotegate.PublishRequest) (remotegate.PublishedCandidate, error))(nil)),
		"Observe":            reflect.TypeOf((func(context.Context, remotegate.ObserveGateRequest) (remotegate.RemoteGateObservation, error))(nil)),
		"PrepareSquash":      reflect.TypeOf((func(context.Context, remotegate.PrepareSquashRequest) (remotegate.PreparedSquash, error))(nil)),
		"IntegrateSquashCAS": reflect.TypeOf((func(context.Context, remotegate.PreparedSquash) (remotegate.MergeResult, error))(nil)),
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
		reflect.TypeOf((*remotegate.RemoteGateClient)(nil)).Elem(),
	}
}

func assertTransportNeutral(t *testing.T, roots []reflect.Type) {
	t.Helper()
	seen := make(map[reflect.Type]struct{})
	for _, root := range roots {
		walkContractType(t, root, seen)
	}
}

func walkContractType(t *testing.T, typ reflect.Type, seen map[reflect.Type]struct{}) {
	t.Helper()
	if typ == nil {
		return
	}
	if _, ok := seen[typ]; ok {
		return
	}
	seen[typ] = struct{}{}
	if packagePathIsProviderTransport(typ.PkgPath()) {
		t.Errorf("contract type %s exposes provider transport package %q", typ, typ.PkgPath())
	}
	switch typ.Kind() {
	case reflect.Array, reflect.Pointer, reflect.Slice:
		walkContractType(t, typ.Elem(), seen)
	case reflect.Map:
		walkContractType(t, typ.Key(), seen)
		walkContractType(t, typ.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < typ.NumField(); index++ {
			walkContractType(t, typ.Field(index).Type, seen)
		}
	case reflect.Interface:
		for index := 0; index < typ.NumMethod(); index++ {
			method := typ.Method(index).Type
			for input := 0; input < method.NumIn(); input++ {
				walkContractType(t, method.In(input), seen)
			}
			for output := 0; output < method.NumOut(); output++ {
				walkContractType(t, method.Out(output), seen)
			}
		}
	}
}

func packagePathIsProviderTransport(path string) bool {
	parts := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool {
		return r == '/' || r == '.' || r == '-'
	})
	for _, part := range parts {
		if part == "github" || part == "gh" {
			return true
		}
	}
	return false
}
