package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrPolicyAmbiguous indicates that a target rule request cannot be resolved
// to one unambiguous repository branch collection.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
var ErrPolicyAmbiguous = errors.New("ambiguous policy")

// CollectionLimits bounds a JSON collection operation.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
type CollectionLimits struct {
	MaxPages int
	MaxItems int
	MaxBytes int
}

// CollectionRequest describes one bounded GitHub JSON collection request.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
type CollectionRequest struct {
	Path     string
	MaxPages int
	MaxItems int
	MaxBytes int
}

// CollectionEvidence reports the bounded collection result.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
type CollectionEvidence struct {
	PageCount int
	ItemCount int
}

// CollectionReader is the read-only API seam for bounded JSON collection.
//
//oro:testonly — production GitHub policy collection is wired by subsequent remote-gate tasks.
type CollectionReader interface {
	CollectJSON(context.Context, CollectionRequest, any) (CollectionEvidence, error)
}

func effectiveRuleCollectionRequest(repository, target string, limits CollectionLimits) (CollectionRequest, error) {
	if !isRepository(repository) || strings.TrimSpace(target) == "" || limits.MaxPages <= 0 || limits.MaxItems <= 0 || limits.MaxBytes <= 0 {
		return CollectionRequest{}, fmt.Errorf("%w: invalid rule collection request", ErrPolicyAmbiguous)
	}
	return CollectionRequest{
		Path:     "/repos/" + repository + "/rules/branches/" + url.PathEscape(target),
		MaxPages: limits.MaxPages,
		MaxItems: limits.MaxItems,
		MaxBytes: limits.MaxBytes,
	}, nil
}

func isRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && strings.TrimSpace(repository) == repository
}
