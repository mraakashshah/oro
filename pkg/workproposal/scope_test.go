package workproposal

import (
	"strings"
	"testing"
)

func TestNormalizeScopeV1Equivalence(t *testing.T) {
	t.Parallel()

	first, err := NormalizeScopeV1(ScopeInput{
		Project:         " Oro ",
		Kind:            ScopeKindPrerequisite,
		Package:         " pkg/dispatcher ",
		Component:       " Assignment ",
		ExternalSubject: "",
		Invariant:       "assignment identity is durable",
		Paths:           []string{"pkg/dispatcher/../dispatcher/worker_pool.go"},
		ReviewerProse:   "A first explanation from the reviewer.",
		Fingerprint:     "same-fingerprint",
	})
	if err != nil {
		t.Fatalf("NormalizeScopeV1(first) error = %v", err)
	}

	second, err := NormalizeScopeV1(ScopeInput{
		Project:       "oro",
		Kind:          ScopeKindPrerequisite,
		Package:       "pkg/dispatcher",
		Component:     "assignment",
		Invariant:     "assignment identity is durable",
		Paths:         []string{"pkg/dispatcher/worker_pool.go"},
		ReviewerProse: "Completely different advisory prose.",
		Fingerprint:   "same-fingerprint",
	})
	if err != nil {
		t.Fatalf("NormalizeScopeV1(second) error = %v", err)
	}
	if first != second {
		t.Fatalf("equivalent scopes produced different keys:\nfirst:  %s\nsecond: %s", first, second)
	}
	if !strings.HasPrefix(string(first), "scope-v1:") {
		t.Fatalf("key %q does not include the V1 version prefix", first)
	}

	distinctInvariant, err := NormalizeScopeV1(ScopeInput{
		Project:     "oro",
		Kind:        ScopeKindPrerequisite,
		Package:     "pkg/dispatcher",
		Component:   "assignment",
		Invariant:   "assignment capability is durable",
		Paths:       []string{"pkg/dispatcher/worker_pool.go"},
		Fingerprint: "same-fingerprint",
	})
	if err != nil {
		t.Fatalf("NormalizeScopeV1(distinct invariant) error = %v", err)
	}
	if first == distinctInvariant {
		t.Fatal("distinct invariants collapsed despite an equal fingerprint")
	}

	for _, input := range []ScopeInput{
		{Project: "oro", Kind: ScopeKindPrerequisite, Invariant: "x", Paths: []string{"../outside.go"}},
		{Project: "oro", Kind: "unknown", Invariant: "x"},
		{Project: "oro", Kind: ScopeKindPrerequisite, Invariant: "x", Fields: map[string]string{"unknown": "value"}},
	} {
		if _, err := NormalizeScopeV1(input); err == nil {
			t.Fatalf("NormalizeScopeV1(%+v) error = nil, want rejection", input)
		}
	}
}
