//nolint:testpackage // The acceptance test must call the required unexported decoder seam.
package github

import (
	"errors"
	"reflect"
	"testing"

	"oro/pkg/remotegate"
)

func TestDecodeEffectiveRuleRejectsAmbiguity(t *testing.T) {
	valid := effectiveRuleResponse{
		ID:          101,
		Source:      "repository",
		Version:     "v1",
		Pattern:     "main",
		Enforcement: "active",
		Operations:  []string{"update"},
	}

	for _, tt := range []struct {
		name   string
		mutate func(*effectiveRuleResponse)
	}{
		{name: "missing source", mutate: func(rule *effectiveRuleResponse) { rule.Source = "" }},
		{name: "missing ID", mutate: func(rule *effectiveRuleResponse) { rule.ID = 0 }},
		{name: "missing version", mutate: func(rule *effectiveRuleResponse) { rule.Version = "" }},
		{name: "missing matched pattern", mutate: func(rule *effectiveRuleResponse) { rule.Pattern = "" }},
		{name: "unknown enforcement", mutate: func(rule *effectiveRuleResponse) { rule.Enforcement = "unknown" }},
		{name: "unknown operation", mutate: func(rule *effectiveRuleResponse) { rule.Operations = []string{"merge"} }},
		{name: "repository and organization ownership", mutate: func(rule *effectiveRuleResponse) { rule.Source = "repository,organization" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := valid
			tt.mutate(&raw)
			got, err := decodeEffectiveRule(raw)
			if !errors.Is(err, ErrPolicyAmbiguous) {
				t.Fatalf("decodeEffectiveRule() error = %v, want ErrPolicyAmbiguous", err)
			}
			if !reflect.DeepEqual(got, remotegate.ApplicableRule{}) {
				t.Fatalf("decodeEffectiveRule() = %+v, want zero ApplicableRule", got)
			}
		})
	}
}
