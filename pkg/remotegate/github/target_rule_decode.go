// Package github adapts GitHub remote policy evidence to provider-neutral types.
package github

import (
	"fmt"
	"strconv"
	"strings"

	"oro/pkg/remotegate"
)

type effectiveRuleBypassActor struct {
	ActorID   int64  `json:"actor_id"`
	ActorType string `json:"actor_type"`
}

type effectiveRuleRequiredCheck struct {
	Context string `json:"context"`
}

func decodeEffectiveRule(raw effectiveRuleResponse) (remotegate.ApplicableRule, error) {
	if err := validateEffectiveRuleResponse(raw); err != nil {
		return remotegate.ApplicableRule{}, err
	}

	rule := remotegate.ApplicableRule{
		Source:         raw.Source,
		ID:             strconv.FormatInt(raw.ID, 10),
		Version:        raw.Version,
		Pattern:        raw.Pattern,
		Enforcement:    raw.Enforcement,
		Operations:     append([]string(nil), raw.Operations...),
		BypassActors:   decodeBypassActors(raw.BypassActors),
		RequiredChecks: decodeRequiredChecks(raw.RequiredChecks),
	}
	if err := remotegate.ValidateEffectivePolicy(remotegate.EffectivePolicy{Rules: []remotegate.ApplicableRule{rule}}); err != nil {
		return remotegate.ApplicableRule{}, ambiguousEffectiveRuleError()
	}
	return rule, nil
}

func validateEffectiveRuleResponse(raw effectiveRuleResponse) error {
	if raw.ID <= 0 || strings.TrimSpace(raw.Version) == "" {
		return ambiguousEffectiveRuleError()
	}
	if !isEffectiveRuleSource(raw.Source) {
		return ambiguousEffectiveRuleError()
	}
	return nil
}

func ambiguousEffectiveRuleError() error {
	return fmt.Errorf("%w: invalid effective rule", ErrPolicyAmbiguous)
}

func isEffectiveRuleSource(source string) bool {
	switch source {
	case "repository", "organization":
		return true
	default:
		return false
	}
}

func decodeBypassActors(raw []effectiveRuleBypassActor) []string {
	actors := make([]string, len(raw))
	for index, actor := range raw {
		actors[index] = fmt.Sprintf("%s:%d", actor.ActorType, actor.ActorID)
	}
	return actors
}

func decodeRequiredChecks(raw []effectiveRuleRequiredCheck) []string {
	checks := make([]string, len(raw))
	for index, check := range raw {
		checks[index] = check.Context
	}
	return checks
}
