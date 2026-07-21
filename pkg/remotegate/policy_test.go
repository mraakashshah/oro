package remotegate_test

import (
	"errors"
	"reflect"
	"testing"

	"oro/pkg/remotegate"
)

func TestValidateEffectivePolicyEvidence(t *testing.T) {
	t.Parallel()

	valid := remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
		{
			Source:         "repository",
			ID:             "42",
			Version:        "7",
			Pattern:        "main",
			Enforcement:    "active",
			Operations:     []string{"update", "delete"},
			BypassActors:   []string{"oro-integration"},
			RequiredChecks: []string{"quality-gate"},
		},
		{
			Source:         "organization",
			ID:             "99",
			Version:        "3",
			Pattern:        "epic/**",
			Enforcement:    "evaluate",
			Operations:     []string{"create", "update"},
			BypassActors:   []string{"oro-integration"},
			RequiredChecks: []string{"quality-gate", "security"},
		},
	}}

	if err := remotegate.ValidateEffectivePolicy(valid); err != nil {
		t.Fatalf("ValidateEffectivePolicy() error = %v", err)
	}
	if !reflect.DeepEqual(valid.Rules[0], (remotegate.ApplicableRule{
		Source:         "repository",
		ID:             "42",
		Version:        "7",
		Pattern:        "main",
		Enforcement:    "active",
		Operations:     []string{"update", "delete"},
		BypassActors:   []string{"oro-integration"},
		RequiredChecks: []string{"quality-gate"},
	})) {
		t.Fatal("repository rule fields changed")
	}
	if !reflect.DeepEqual(valid.Rules[1], (remotegate.ApplicableRule{
		Source:         "organization",
		ID:             "99",
		Version:        "3",
		Pattern:        "epic/**",
		Enforcement:    "evaluate",
		Operations:     []string{"create", "update"},
		BypassActors:   []string{"oro-integration"},
		RequiredChecks: []string{"quality-gate", "security"},
	})) {
		t.Fatal("organization rule fields changed")
	}

	for _, policy := range []remotegate.EffectivePolicy{
		{},
		{Rules: []remotegate.ApplicableRule{}},
	} {
		if err := remotegate.ValidateEffectivePolicy(policy); err != nil {
			t.Fatalf("ValidateEffectivePolicy(%+v) error = %v", policy, err)
		}
	}

	for _, tt := range []struct {
		name   string
		policy remotegate.EffectivePolicy
	}{
		{
			name: "duplicate source and ID",
			policy: remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
				{Source: "repository", ID: "42", Pattern: "main", Enforcement: "active", Operations: []string{"update"}},
				{Source: "repository", ID: "42", Pattern: "epic/**", Enforcement: "evaluate", Operations: []string{"create"}},
			}},
		},
		{
			name: "empty source",
			policy: remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
				{ID: "42", Pattern: "main", Enforcement: "active", Operations: []string{"update"}},
			}},
		},
		{
			name: "empty ID",
			policy: remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
				{Source: "repository", Pattern: "main", Enforcement: "active", Operations: []string{"update"}},
			}},
		},
		{
			name: "empty pattern",
			policy: remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
				{Source: "repository", ID: "42", Enforcement: "active", Operations: []string{"update"}},
			}},
		},
		{
			name: "unknown enforcement",
			policy: remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
				{Source: "repository", ID: "42", Pattern: "main", Enforcement: "unknown", Operations: []string{"update"}},
			}},
		},
		{
			name: "unknown operation",
			policy: remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
				{Source: "repository", ID: "42", Pattern: "main", Enforcement: "active", Operations: []string{"unknown"}},
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := remotegate.ValidateEffectivePolicy(tt.policy)
			if !errors.Is(err, remotegate.ErrInvalidPolicyEvidence) {
				t.Fatalf("ValidateEffectivePolicy() error = %v, want ErrInvalidPolicyEvidence", err)
			}
		})
	}
}
