//nolint:testpackage // The collection acceptance test verifies Client's unexported read-only boundary.
package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type incompleteEffectiveRuleCollectionFixture struct {
	items []effectiveRuleResponse
	err   error
}

var _ CollectionReader = (*incompleteEffectiveRuleCollectionFixture)(nil)

func (f *incompleteEffectiveRuleCollectionFixture) CollectJSON(_ context.Context, _ CollectionRequest, dst any) (CollectionEvidence, error) {
	if f.err != nil {
		return CollectionEvidence{}, f.err
	}
	*(dst.(*[]effectiveRuleResponse)) = append([]effectiveRuleResponse(nil), f.items...)
	return CollectionEvidence{PageCount: 1, ItemCount: len(f.items)}, nil
}

func TestRejectIncompleteEffectiveRulePages(t *testing.T) {
	const rawRulePrefix = "raw-rule:"

	tests := []struct {
		name    string
		fixture incompleteEffectiveRuleCollectionFixture
	}{
		{name: "page two read failure", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " page two read failed")}},
		{name: "repeated page token", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " repeated page token")}},
		{name: "next link on evil host", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " next link https://evil.example/page/2")}},
		{name: "duplicate stable ID", fixture: incompleteEffectiveRuleCollectionFixture{items: []effectiveRuleResponse{{ID: 101}, {ID: 101}}}},
		{name: "repository identity mismatch", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " repository identity other/oro")}},
		{name: "max pages exhausted", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " MaxPages=1 exhausted")}},
		{name: "max items exhausted", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " MaxItems=1 exhausted")}},
		{name: "max bytes exhausted", fixture: incompleteEffectiveRuleCollectionFixture{err: errors.New(rawRulePrefix + " MaxBytes=32 exhausted")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := tt.fixture
			client := Client{
				repository:       "acme/oro",
				collection:       &fixture,
				collectionLimits: CollectionLimits{MaxPages: 1, MaxItems: 1, MaxBytes: 32},
			}

			got, err := client.collectEffectiveRuleResponses(context.Background(), "main")
			if !errors.Is(err, ErrPolicyAmbiguous) {
				t.Fatalf("collectEffectiveRuleResponses() error = %v, want ErrPolicyAmbiguous", err)
			}
			if got.Items != nil || got.Evidence != (CollectionEvidence{}) {
				t.Fatalf("collection = %+v, want zero collection", got)
			}
			if strings.Contains(err.Error(), rawRulePrefix) {
				t.Fatalf("error = %q, must not expose raw-rule prefix", err)
			}
		})
	}
}
