package github

import (
	"context"
	"errors"
	"fmt"
)

type effectiveRuleResponse struct {
	ID             int64                        `json:"id"`
	Source         string                       `json:"source"`
	Version        string                       `json:"version"`
	Pattern        string                       `json:"pattern"`
	Enforcement    string                       `json:"enforcement"`
	Operations     []string                     `json:"operations"`
	BypassActors   []effectiveRuleBypassActor   `json:"bypass_actors"`
	RequiredChecks []effectiveRuleRequiredCheck `json:"required_status_checks"`
}

type effectiveRuleCollection struct {
	Items    []effectiveRuleResponse
	Evidence CollectionEvidence
}

func (c *Client) collectEffectiveRuleResponses(ctx context.Context, target string) (effectiveRuleCollection, error) {
	if err := ctx.Err(); err != nil {
		return effectiveRuleCollection{}, fmt.Errorf("collect effective rules context: %w", err)
	}
	if c.collection == nil {
		return effectiveRuleCollection{}, fmt.Errorf("%w: collection reader is required", ErrPolicyAmbiguous)
	}
	request, err := effectiveRuleCollectionRequest(c.repository, target, c.collectionLimits)
	if err != nil {
		return effectiveRuleCollection{}, err
	}

	items := make([]effectiveRuleResponse, 0)
	evidence, err := c.collection.CollectJSON(ctx, request, &items)
	if contextErr := ctx.Err(); contextErr != nil {
		return effectiveRuleCollection{}, fmt.Errorf("collect effective rules context: %w", contextErr)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return effectiveRuleCollection{}, fmt.Errorf("collect effective rules context: %w", err)
		}
		return effectiveRuleCollection{}, fmt.Errorf("%w: effective rule collection incomplete", ErrPolicyAmbiguous)
	}
	if hasDuplicateEffectiveRuleID(items) {
		return effectiveRuleCollection{}, fmt.Errorf("%w: duplicate effective rule ID", ErrPolicyAmbiguous)
	}
	if items == nil {
		items = make([]effectiveRuleResponse, 0)
	}
	return effectiveRuleCollection{Items: items, Evidence: evidence}, nil
}

func hasDuplicateEffectiveRuleID(items []effectiveRuleResponse) bool {
	ids := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if _, ok := ids[item.ID]; ok {
			return true
		}
		ids[item.ID] = struct{}{}
	}
	return false
}
