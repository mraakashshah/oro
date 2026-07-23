package remotegate_test

import (
	"errors"
	"reflect"
	"regexp"
	"testing"

	"oro/pkg/remotegate"
)

func TestCanonicalEffectivePolicyHash(t *testing.T) {
	t.Parallel()

	policy := canonicalHashPolicy()
	reversed := remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
		policy.Rules[1],
		policy.Rules[0],
	}}
	original := clonePolicy(policy)

	hash, err := remotegate.CanonicalPolicyHash(policy)
	if err != nil {
		t.Fatalf("CanonicalPolicyHash() error = %v", err)
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
		t.Fatalf("CanonicalPolicyHash() = %q, want lowercase SHA-256 hex", hash)
	}
	reversedHash, err := remotegate.CanonicalPolicyHash(reversed)
	if err != nil {
		t.Fatalf("CanonicalPolicyHash() reversed error = %v", err)
	}
	if reversedHash != hash {
		t.Fatalf("CanonicalPolicyHash() reversed = %q, want %q", reversedHash, hash)
	}
	if !reflect.DeepEqual(policy, original) {
		t.Fatalf("CanonicalPolicyHash() changed caller-owned policy: got %+v, want %+v", policy, original)
	}

	for _, mutation := range []struct {
		name   string
		mutate func(*remotegate.ApplicableRule)
	}{
		{name: "source", mutate: func(rule *remotegate.ApplicableRule) { rule.Source = "organization" }},
		{name: "ID", mutate: func(rule *remotegate.ApplicableRule) { rule.ID = "other" }},
		{name: "version", mutate: func(rule *remotegate.ApplicableRule) { rule.Version = "8" }},
		{name: "pattern", mutate: func(rule *remotegate.ApplicableRule) { rule.Pattern = "release/**" }},
		{name: "enforcement", mutate: func(rule *remotegate.ApplicableRule) { rule.Enforcement = "evaluate" }},
		{name: "operations", mutate: func(rule *remotegate.ApplicableRule) { rule.Operations = []string{"create"} }},
		{name: "bypass actors", mutate: func(rule *remotegate.ApplicableRule) { rule.BypassActors = []string{"release-bot"} }},
		{name: "required checks", mutate: func(rule *remotegate.ApplicableRule) { rule.RequiredChecks = []string{"security"} }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := clonePolicy(policy)
			mutation.mutate(&changed.Rules[0])

			changedHash, err := remotegate.CanonicalPolicyHash(changed)
			if err != nil {
				t.Fatalf("CanonicalPolicyHash() error = %v", err)
			}
			if changedHash == hash {
				t.Fatalf("CanonicalPolicyHash() = %q after %s change, want a different hash", changedHash, mutation.name)
			}
		})
	}

	duplicate := clonePolicy(policy)
	duplicate.Rules = append(duplicate.Rules, cloneRule(policy.Rules[0]))
	if _, err := remotegate.CanonicalPolicyHash(duplicate); !errors.Is(err, remotegate.ErrInvalidPolicyEvidence) {
		t.Fatalf("CanonicalPolicyHash() duplicate error = %v, want ErrInvalidPolicyEvidence", err)
	}
}

func canonicalHashPolicy() remotegate.EffectivePolicy {
	return remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{
		{
			Source:         "repository",
			ID:             "42",
			Version:        "7",
			Pattern:        "main",
			Enforcement:    "active",
			Operations:     []string{"update", "delete"},
			BypassActors:   []string{"oro-integration"},
			RequiredChecks: []string{"quality-gate", "security"},
		},
		{
			Source:         "organization",
			ID:             "99",
			Version:        "3",
			Pattern:        "epic/**",
			Enforcement:    "evaluate",
			Operations:     []string{"create", "update"},
			BypassActors:   []string{"release-bot"},
			RequiredChecks: []string{"security"},
		},
	}}
}

func clonePolicy(policy remotegate.EffectivePolicy) remotegate.EffectivePolicy {
	cloned := remotegate.EffectivePolicy{Rules: make([]remotegate.ApplicableRule, len(policy.Rules))}
	for index, rule := range policy.Rules {
		cloned.Rules[index] = cloneRule(rule)
	}
	return cloned
}

func cloneRule(rule remotegate.ApplicableRule) remotegate.ApplicableRule {
	rule.Operations = append([]string(nil), rule.Operations...)
	rule.BypassActors = append([]string(nil), rule.BypassActors...)
	rule.RequiredChecks = append([]string(nil), rule.RequiredChecks...)
	return rule
}

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
