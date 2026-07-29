// Package remotegate defines provider-neutral remote quality-gate evidence.
package remotegate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// EffectivePolicy is the complete effective policy for a target ref.
type EffectivePolicy struct {
	Rules []ApplicableRule
}

// ApplicableRule describes one repository or organization rule that applies
// to a concrete target ref.
type ApplicableRule struct {
	Source         string
	ID             string
	Version        string
	Pattern        string
	Enforcement    string
	Operations     []string
	BypassActors   []string
	RequiredChecks []string
}

// ErrInvalidPolicyEvidence indicates incomplete or unsupported policy evidence.
var ErrInvalidPolicyEvidence = errors.New("invalid policy evidence")

// ValidateEffectivePolicy verifies that policy evidence is complete and
// unambiguous. An empty policy is valid evidence that no rules apply.
func ValidateEffectivePolicy(policy EffectivePolicy) error {
	seen := make(map[string]struct{}, len(policy.Rules))
	for _, rule := range policy.Rules {
		if err := validateApplicableRule(rule, seen); err != nil {
			return err
		}
	}
	return nil
}

// CanonicalPolicyHash returns the stable SHA-256 identity of validated policy
// evidence. The hash is independent of caller-owned rule and value ordering.
func CanonicalPolicyHash(policy EffectivePolicy) (string, error) {
	if err := ValidateEffectivePolicy(policy); err != nil {
		return "", err
	}

	canonical := make([]canonicalApplicableRule, len(policy.Rules))
	for index, rule := range policy.Rules {
		canonical[index] = canonicalizeRule(rule)
	}
	sort.Slice(canonical, func(left, right int) bool {
		return canonical[left].Source+"\x00"+canonical[left].ID < canonical[right].Source+"\x00"+canonical[right].ID
	})

	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical policy: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

type canonicalApplicableRule struct {
	Source         string   `json:"source"`
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	Pattern        string   `json:"pattern"`
	Enforcement    string   `json:"enforcement"`
	Operations     []string `json:"operations"`
	BypassActors   []string `json:"bypass_actors"`
	RequiredChecks []string `json:"required_checks"`
}

func canonicalizeRule(rule ApplicableRule) canonicalApplicableRule {
	canonical := canonicalApplicableRule{
		Source:         rule.Source,
		ID:             rule.ID,
		Version:        rule.Version,
		Pattern:        rule.Pattern,
		Enforcement:    rule.Enforcement,
		Operations:     append([]string(nil), rule.Operations...),
		BypassActors:   append([]string(nil), rule.BypassActors...),
		RequiredChecks: append([]string(nil), rule.RequiredChecks...),
	}
	sort.Strings(canonical.Operations)
	sort.Strings(canonical.BypassActors)
	sort.Strings(canonical.RequiredChecks)
	return canonical
}

func validateApplicableRule(rule ApplicableRule, seen map[string]struct{}) error {
	if strings.TrimSpace(rule.Source) == "" {
		return invalidPolicyEvidence("rule source is required")
	}
	if strings.TrimSpace(rule.ID) == "" {
		return invalidPolicyEvidence("rule ID is required")
	}
	if strings.TrimSpace(rule.Pattern) == "" {
		return invalidPolicyEvidence("rule pattern is required")
	}
	if !isKnownEnforcement(rule.Enforcement) {
		return invalidPolicyEvidence("unknown enforcement %q", rule.Enforcement)
	}
	for _, operation := range rule.Operations {
		if !isKnownOperation(operation) {
			return invalidPolicyEvidence("unknown operation %q", operation)
		}
	}

	identity := rule.Source + "\x00" + rule.ID
	if _, duplicate := seen[identity]; duplicate {
		return invalidPolicyEvidence("duplicate rule identity %q/%q", rule.Source, rule.ID)
	}
	seen[identity] = struct{}{}
	return nil
}

func isKnownEnforcement(enforcement string) bool {
	switch enforcement {
	case "active", "evaluate", "disabled":
		return true
	default:
		return false
	}
}

func isKnownOperation(operation string) bool {
	switch operation {
	case "create", "update", "delete":
		return true
	default:
		return false
	}
}

func invalidPolicyEvidence(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidPolicyEvidence}, args...)...)
}
