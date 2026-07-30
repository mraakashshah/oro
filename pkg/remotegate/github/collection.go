package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrIncompleteCollection indicates that a GitHub collection could not be
// proven complete and must not be used by a policy decision.
var ErrIncompleteCollection = errors.New("incomplete GitHub collection")

// Identified is an item with a stable GitHub collection identity.
type Identified interface {
	ID() string
}

// CompleteCollection is a fully validated collection and its stable evidence.
type CompleteCollection[T Identified] struct {
	Items    []T
	Evidence CollectionEvidence
}

// Collect reads every GitHub pagination page before returning a normalized,
// bounded collection. Any malformed or ambiguous result returns no collection.
//
//oro:testonly — production endpoint readers are wired by subsequent remote-gate tasks.
func Collect[T Identified](ctx context.Context, runner *GHRunner, request CollectionRequest) (CompleteCollection[T], error) {
	if err := validateCollectionRequest(request); err != nil {
		return CompleteCollection[T]{}, err
	}
	if err := ctx.Err(); err != nil {
		return CompleteCollection[T]{}, fmt.Errorf("collect GitHub collection: %w", err)
	}
	output, err := runner.Run(ctx, APIRequest{Method: "GET", Path: request.Path, Paginate: true, Slurp: true})
	if err != nil {
		return CompleteCollection[T]{}, incompleteCollectionError(err)
	}
	if err := ctx.Err(); err != nil {
		return CompleteCollection[T]{}, fmt.Errorf("collect GitHub collection: %w", err)
	}
	if len(output) > request.MaxBytes {
		return CompleteCollection[T]{}, incompleteCollectionError(errors.New("byte bound exceeded"))
	}

	var pages []json.RawMessage
	if err := json.Unmarshal(output, &pages); err != nil || pages == nil || len(pages) == 0 || len(pages) >= request.MaxPages {
		return CompleteCollection[T]{}, incompleteCollectionError(errors.New("invalid or exhausted page sequence"))
	}
	items, err := decodeCollectionPages[T](pages, request.MaxItems)
	if err != nil {
		return CompleteCollection[T]{}, incompleteCollectionError(err)
	}
	return CompleteCollection[T]{Items: items, Evidence: CollectionEvidence{PageCount: len(pages), ItemCount: len(items)}}, nil
}

func validateCollectionRequest(request CollectionRequest) error {
	if strings.TrimSpace(request.Path) == "" || !strings.HasPrefix(request.Path, "/") || request.MaxPages <= 1 || request.MaxItems <= 0 || request.MaxBytes <= 0 {
		return incompleteCollectionError(errors.New("invalid request"))
	}
	return nil
}

func decodeCollectionPages[T Identified](pages []json.RawMessage, maxItems int) ([]T, error) {
	items := make([]T, 0)
	seen := make(map[string]struct{})
	for _, page := range pages {
		rawItems, err := normalizeCollectionPage(page)
		if err != nil {
			return nil, err
		}
		if len(items)+len(rawItems) >= maxItems {
			return nil, errors.New("item bound exhausted")
		}
		for _, raw := range rawItems {
			var item T
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, fmt.Errorf("decode item: %w", err)
			}
			id := strings.TrimSpace(item.ID())
			if id == "" {
				return nil, errors.New("item stable ID is absent")
			}
			if _, found := seen[id]; found {
				return nil, errors.New("duplicate item stable ID")
			}
			seen[id] = struct{}{}
			items = append(items, item)
		}
	}
	return items, nil
}

func normalizeCollectionPage(page json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(page, &items); err == nil && items != nil {
		return items, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(page, &object); err != nil {
		return nil, fmt.Errorf("decode page: %w", err)
	}
	for _, key := range []string{"items", "check_runs", "workflow_runs", "jobs", "artifacts", "rulesets", "history"} {
		rawItems, found := object[key]
		if !found {
			continue
		}
		if err := json.Unmarshal(rawItems, &items); err != nil || items == nil {
			return nil, errors.New("collection page items are malformed")
		}
		return items, nil
	}
	return nil, errors.New("collection page shape is unsupported")
}

func incompleteCollectionError(err error) error {
	return fmt.Errorf("collect GitHub collection: %w", errors.Join(ErrIncompleteCollection, err))
}
