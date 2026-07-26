//nolint:testpackage // The acceptance test must call the required unexported decoder seam.
package github

import (
	"reflect"
	"testing"

	"oro/pkg/remotegate"
)

func TestDecodeRepositoryEffectiveRule(t *testing.T) {
	raw := effectiveRuleResponse{
		ID:          101,
		Source:      "repository",
		Version:     "v7",
		Pattern:     "main",
		Enforcement: "active",
		Operations:  []string{"update"},
		BypassActors: []effectiveRuleBypassActor{{
			ActorID:   42,
			ActorType: "App",
		}},
		RequiredChecks: []effectiveRuleRequiredCheck{{Context: "oro-portable-qg"}},
	}

	got, err := decodeEffectiveRule(raw)
	if err != nil {
		t.Fatalf("decodeEffectiveRule() error = %v", err)
	}
	want := remotegate.ApplicableRule{
		Source:         "repository",
		ID:             "101",
		Version:        "v7",
		Pattern:        "main",
		Enforcement:    "active",
		Operations:     []string{"update"},
		BypassActors:   []string{"App:42"},
		RequiredChecks: []string{"oro-portable-qg"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeEffectiveRule() = %+v, want %+v", got, want)
	}
}
