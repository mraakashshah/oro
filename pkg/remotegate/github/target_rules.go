package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"oro/pkg/remotegate"
)

func (c *Client) effectiveTargetPolicy(ctx context.Context, targets []string) (remotegate.EffectivePolicy, error) {
	if err := ctx.Err(); err != nil {
		return remotegate.EffectivePolicy{}, fmt.Errorf("collect effective target policy context: %w", err)
	}

	policy := remotegate.EffectivePolicy{Rules: make([]remotegate.ApplicableRule, 0)}
	seenTargets := make(map[string]struct{}, len(targets))
	seenRules := make(map[string]struct{})
	for _, target := range targets {
		if strings.TrimSpace(target) == "" {
			return remotegate.EffectivePolicy{}, ambiguousTargetPolicyError()
		}
		if _, duplicate := seenTargets[target]; duplicate {
			return remotegate.EffectivePolicy{}, ambiguousTargetPolicyError()
		}
		seenTargets[target] = struct{}{}

		rules, err := c.collectTargetRules(ctx, target, seenRules)
		if err != nil {
			return zeroEffectiveTargetPolicyError(err)
		}
		policy.Rules = append(policy.Rules, rules...)
	}
	if err := remotegate.ValidateEffectivePolicy(policy); err != nil {
		return remotegate.EffectivePolicy{}, ambiguousTargetPolicyError()
	}
	return policy, nil
}

func (c *Client) collectTargetRules(ctx context.Context, target string, seenRules map[string]struct{}) ([]remotegate.ApplicableRule, error) {
	collection, err := c.collectEffectiveRuleResponses(ctx, target)
	if err != nil {
		return nil, err
	}
	rules := make([]remotegate.ApplicableRule, 0, len(collection.Items))
	for _, raw := range collection.Items {
		if raw.Target != "" && raw.Target != target {
			return nil, ambiguousTargetPolicyError()
		}
		rule, decodeErr := decodeEffectiveRule(raw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		identity := rule.Source + "\x00" + rule.ID
		if _, duplicate := seenRules[identity]; duplicate {
			return nil, ambiguousTargetPolicyError()
		}
		seenRules[identity] = struct{}{}
		rules = append(rules, rule)
	}
	return rules, nil
}

func zeroEffectiveTargetPolicyError(err error) (remotegate.EffectivePolicy, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return remotegate.EffectivePolicy{}, fmt.Errorf("collect effective target policy context: %w", err)
	}
	return remotegate.EffectivePolicy{}, ambiguousTargetPolicyError()
}

func ambiguousTargetPolicyError() error {
	return fmt.Errorf("%w: effective target policy is ambiguous", ErrPolicyAmbiguous)
}
