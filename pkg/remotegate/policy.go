// Package remotegate defines provider-neutral remote quality-gate evidence.
package remotegate

import (
	"errors"
	"fmt"
	"strings"
)

// EffectivePolicy is the complete effective policy for a target ref.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
type EffectivePolicy struct {
	Rules []ApplicableRule
}

// ApplicableRule describes one repository or organization rule that applies
// to a concrete target ref.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
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
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
var ErrInvalidPolicyEvidence = errors.New("invalid policy evidence")

// ValidateEffectivePolicy verifies that policy evidence is complete and
// unambiguous. An empty policy is valid evidence that no rules apply.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
func ValidateEffectivePolicy(policy EffectivePolicy) error {
	seen := make(map[string]struct{}, len(policy.Rules))
	for _, rule := range policy.Rules {
		if err := validateApplicableRule(rule, seen); err != nil {
			return err
		}
	}
	return nil
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
