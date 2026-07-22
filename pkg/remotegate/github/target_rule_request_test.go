//nolint:testpackage // The boundary acceptance test must inspect the unexported request builder.
package github

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEffectiveRuleCollectionRequest(t *testing.T) {
	typeOfReader := reflect.TypeOf((*CollectionReader)(nil)).Elem()
	if typeOfReader.NumMethod() != 1 {
		t.Fatalf("collection reader exposes %d methods, want 1", typeOfReader.NumMethod())
	}
	if _, ok := typeOfReader.MethodByName("CollectJSON"); !ok {
		t.Fatal("collection reader is missing CollectJSON")
	}

	limits := CollectionLimits{MaxPages: 3, MaxItems: 50, MaxBytes: 4096}
	original := limits
	request, err := effectiveRuleCollectionRequest("acme/oro", "release/1", limits)
	if err != nil {
		t.Fatalf("effectiveRuleCollectionRequest() error = %v", err)
	}
	want := CollectionRequest{Path: "/repos/acme/oro/rules/branches/release%2F1", MaxPages: 3, MaxItems: 50, MaxBytes: 4096}
	if request != want {
		t.Fatalf("request = %+v, want %+v", request, want)
	}
	if limits != original {
		t.Fatalf("limits changed: got %+v, want %+v", limits, original)
	}

	for _, test := range []struct {
		name, repository, target string
		limits                   CollectionLimits
	}{
		{name: "repository owner only", repository: "acme", target: "release/1", limits: limits},
		{name: "repository name only", repository: "/oro", target: "release/1", limits: limits},
		{name: "repository extra path", repository: "acme/oro/extra", target: "release/1", limits: limits},
		{name: "empty target", repository: "acme/oro", limits: limits},
		{name: "zero pages", repository: "acme/oro", target: "release/1", limits: CollectionLimits{MaxItems: 1, MaxBytes: 1}},
		{name: "negative pages", repository: "acme/oro", target: "release/1", limits: CollectionLimits{MaxPages: -1, MaxItems: 1, MaxBytes: 1}},
		{name: "zero items", repository: "acme/oro", target: "release/1", limits: CollectionLimits{MaxPages: 1, MaxBytes: 1}},
		{name: "negative items", repository: "acme/oro", target: "release/1", limits: CollectionLimits{MaxPages: 1, MaxItems: -1, MaxBytes: 1}},
		{name: "zero bytes", repository: "acme/oro", target: "release/1", limits: CollectionLimits{MaxPages: 1, MaxItems: 1}},
		{name: "negative bytes", repository: "acme/oro", target: "release/1", limits: CollectionLimits{MaxPages: 1, MaxItems: 1, MaxBytes: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := effectiveRuleCollectionRequest(test.repository, test.target, test.limits)
			if !errors.Is(err, ErrPolicyAmbiguous) {
				t.Fatalf("error = %v, want ErrPolicyAmbiguous", err)
			}
			if got != (CollectionRequest{}) {
				t.Fatalf("request = %+v, want zero request", got)
			}
		})
	}
}

type fakeCollectionReader struct{}

var _ CollectionReader = fakeCollectionReader{}

func (fakeCollectionReader) CollectJSON(context.Context, CollectionRequest, any) (CollectionEvidence, error) {
	return CollectionEvidence{}, nil
}
