package github

import (
	"context"
	"fmt"
)

type effectiveRuleResponse struct {
	ID int64 `json:"id"`
}

type effectiveRuleCollection struct {
	Items    []effectiveRuleResponse
	Evidence CollectionEvidence
}

func (c *Client) collectEffectiveRuleResponses(ctx context.Context, target string) (effectiveRuleCollection, error) {
	if err := ctx.Err(); err != nil {
		return effectiveRuleCollection{}, fmt.Errorf("%w: %w", ErrPolicyAmbiguous, err)
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
		return effectiveRuleCollection{}, fmt.Errorf("%w: %w", ErrPolicyAmbiguous, contextErr)
	}
	if err != nil {
		return effectiveRuleCollection{}, fmt.Errorf("%w: collect effective rules: %w", ErrPolicyAmbiguous, err)
	}
	if items == nil {
		items = make([]effectiveRuleResponse, 0)
	}
	return effectiveRuleCollection{Items: items, Evidence: evidence}, nil
}
