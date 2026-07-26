//nolint:testpackage // The acceptance test must call the required unexported decoder seam.
package github

import (
	"reflect"
	"testing"

	"oro/pkg/remotegate"
)

func TestDecodeOrganizationEffectiveRule(t *testing.T) {
	raw := effectiveRuleResponse{
		ID:          202,
		Source:      "organization",
		Version:     "v9",
		Pattern:     "release/**",
		Enforcement: "active",
		Operations:  []string{"create", "update", "delete"},
		BypassActors: []effectiveRuleBypassActor{{
			ActorID:   7,
			ActorType: "Team",
		}},
		RequiredChecks: []effectiveRuleRequiredCheck{{
			Context: "oro-qg",
		}},
	}

	got, err := decodeEffectiveRule(raw)
	if err != nil {
		t.Fatalf("decodeEffectiveRule() error = %v", err)
	}
	want := remotegate.ApplicableRule{
		Source:         "organization",
		ID:             "202",
		Version:        "v9",
		Pattern:        "release/**",
		Enforcement:    "active",
		Operations:     []string{"create", "update", "delete"},
		BypassActors:   []string{"Team:7"},
		RequiredChecks: []string{"oro-qg"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeEffectiveRule() = %+v, want %+v", got, want)
	}

	raw.Operations[0] = "delete"
	raw.BypassActors[0] = effectiveRuleBypassActor{ActorID: 99, ActorType: "User"}
	raw.RequiredChecks[0] = effectiveRuleRequiredCheck{Context: "changed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded rule aliases raw input: got %+v, want %+v", got, want)
	}
}
