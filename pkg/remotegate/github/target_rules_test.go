//nolint:testpackage // The acceptance test verifies Client's unexported policy boundary.
package github

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"testing"

	"oro/pkg/remotegate"
)

type targetRuleCollectionFixture struct {
	itemsByTarget map[string][]effectiveRuleResponse
	requests      []CollectionRequest
}

var _ CollectionReader = (*targetRuleCollectionFixture)(nil)

func (f *targetRuleCollectionFixture) CollectJSON(_ context.Context, request CollectionRequest, dst any) (CollectionEvidence, error) {
	f.requests = append(f.requests, request)
	for target, items := range f.itemsByTarget {
		if request.Path == "/repos/acme/oro/rules/branches/"+url.PathEscape(target) {
			*(dst.(*[]effectiveRuleResponse)) = slices.Clone(items)
			return CollectionEvidence{PageCount: 1, ItemCount: len(items)}, nil
		}
	}
	return CollectionEvidence{}, ErrPolicyAmbiguous
}

func TestEffectiveTargetRuleCollection(t *testing.T) {
	baseFixtures := map[string][]effectiveRuleResponse{
		"main": {{
			ID: 101, Source: "repository", Version: "v1", Pattern: "main", Enforcement: "active",
			Operations: []string{"update"}, BypassActors: []effectiveRuleBypassActor{{ActorID: 42, ActorType: "App"}},
			RequiredChecks: []effectiveRuleRequiredCheck{{Context: "build"}},
		}},
		"release/1": {{
			ID: 202, Source: "organization", Version: "v2", Pattern: "release/**", Enforcement: "evaluate",
			Operations: []string{"create", "update"}, BypassActors: []effectiveRuleBypassActor{{ActorID: 7, ActorType: "Team"}},
			RequiredChecks: []effectiveRuleRequiredCheck{{Context: "security"}},
		}},
		"epic/demo": {{
			ID: 303, Source: "repository", Version: "v3", Pattern: "epic/**", Enforcement: "active",
			Operations: []string{"update", "delete"}, BypassActors: []effectiveRuleBypassActor{{ActorID: 99, ActorType: "User"}},
			RequiredChecks: []effectiveRuleRequiredCheck{{Context: "integration"}},
		}},
	}
	want := remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
		{Source: "repository", ID: "101", Version: "v1", Pattern: "main", Enforcement: "active", Operations: []string{"update"}, BypassActors: []string{"App:42"}, RequiredChecks: []string{"build"}},
		{Source: "organization", ID: "202", Version: "v2", Pattern: "release/**", Enforcement: "evaluate", Operations: []string{"create", "update"}, BypassActors: []string{"Team:7"}, RequiredChecks: []string{"security"}},
		{Source: "repository", ID: "303", Version: "v3", Pattern: "epic/**", Enforcement: "active", Operations: []string{"update", "delete"}, BypassActors: []string{"User:99"}, RequiredChecks: []string{"integration"}},
	}}

	fixtures := targetRuleCollectionFixture{itemsByTarget: baseFixtures}
	client := Client{repository: "acme/oro", collection: &fixtures, collectionLimits: CollectionLimits{MaxPages: 3, MaxItems: 50, MaxBytes: 4096}}
	got, err := client.effectiveTargetPolicy(context.Background(), []string{"main", "release/1", "epic/demo"})
	if err != nil {
		t.Fatalf("effectiveTargetPolicy() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effectiveTargetPolicy() = %+v, want %+v", got, want)
	}

	hash, err := remotegate.CanonicalPolicyHash(got)
	if err != nil {
		t.Fatalf("CanonicalPolicyHash() error = %v", err)
	}
	reversedFixtures := targetRuleCollectionFixture{itemsByTarget: map[string][]effectiveRuleResponse{
		"main":      slices.Clone(baseFixtures["main"]),
		"release/1": slices.Clone(baseFixtures["release/1"]),
		"epic/demo": slices.Clone(baseFixtures["epic/demo"]),
	}}
	reversedClient := Client{repository: "acme/oro", collection: &reversedFixtures, collectionLimits: CollectionLimits{MaxPages: 3, MaxItems: 50, MaxBytes: 4096}}
	reversed, err := reversedClient.effectiveTargetPolicy(context.Background(), []string{"epic/demo", "release/1", "main"})
	if err != nil {
		t.Fatalf("effectiveTargetPolicy() with reversed targets error = %v", err)
	}
	reversedHash, err := remotegate.CanonicalPolicyHash(reversed)
	if err != nil {
		t.Fatalf("CanonicalPolicyHash() with reversed targets error = %v", err)
	}
	if hash != reversedHash {
		t.Fatalf("CanonicalPolicyHash() = %q, reversed hash = %q", hash, reversedHash)
	}
}

func TestEffectiveTargetPolicyRejectsAmbiguousEvidence(t *testing.T) {
	valid := effectiveRuleResponse{
		ID: 101, Source: "repository", Version: "v1", Pattern: "main", Enforcement: "active", Operations: []string{"update"}, Target: "main",
	}

	for _, tt := range []struct {
		name    string
		targets []string
		items   map[string][]effectiveRuleResponse
		cancel  bool
	}{
		{name: "target identity mismatch", targets: []string{"main"}, items: map[string][]effectiveRuleResponse{"main": {{Target: "other", ID: 101, Source: "repository", Version: "v1", Pattern: "main", Enforcement: "active", Operations: []string{"update"}}}}},
		{name: "conflicting duplicate source and ID", targets: []string{"main", "release/1"}, items: map[string][]effectiveRuleResponse{"main": {valid}, "release/1": {{Target: "release/1", ID: 101, Source: "repository", Version: "v2", Pattern: "release/**", Enforcement: "active", Operations: []string{"update"}}}}},
		{name: "empty requested target", targets: []string{""}, items: map[string][]effectiveRuleResponse{}},
		{name: "canceled context", targets: []string{"main"}, items: map[string][]effectiveRuleResponse{"main": {valid}}, cancel: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			fixture := targetRuleCollectionFixture{itemsByTarget: tt.items}
			client := Client{repository: "acme/oro", collection: &fixture, collectionLimits: CollectionLimits{MaxPages: 3, MaxItems: 50, MaxBytes: 4096}}
			got, err := client.effectiveTargetPolicy(ctx, tt.targets)
			if tt.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("effectiveTargetPolicy() error = %v, want context.Canceled", err)
				}
			} else if !errors.Is(err, ErrPolicyAmbiguous) {
				t.Fatalf("effectiveTargetPolicy() error = %v, want ErrPolicyAmbiguous", err)
			}
			if !reflect.DeepEqual(got, remotegate.EffectivePolicy{}) {
				t.Fatalf("effectiveTargetPolicy() = %+v, want zero policy", got)
			}
		})
	}
}
