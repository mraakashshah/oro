// Package workproposal defines durable work-proposal identities.
package workproposal

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ScopeKind identifies the controller-owned class of work a scope describes.
type ScopeKind string

const (
	// ScopeKindTaskLocal identifies a deterministic repair to the source task.
	ScopeKindTaskLocal ScopeKind = "task_local"
	// ScopeKindPrerequisite identifies work that must complete before the source task.
	ScopeKindPrerequisite ScopeKind = "prerequisite"
	// ScopeKindSystemic identifies a project-wide issue.
	ScopeKindSystemic ScopeKind = "systemic"
	// ScopeKindExternal identifies work owned outside the repository.
	ScopeKindExternal ScopeKind = "external"
)

// ScopeInput is the structured, controller-validated identity of a proposal.
// ReviewerProse and Fingerprint are evidence metadata, not scope identity.
type ScopeInput struct {
	Project         string
	Kind            ScopeKind
	Package         string
	Component       string
	ExternalSubject string
	Invariant       string
	Paths           []string
	ReviewerProse   string
	Fingerprint     string
	Fields          map[string]string
}

// ScopeKeyV1 is a versioned serialized materialization identity.
type ScopeKeyV1 string

type canonicalScopeV1 struct {
	Project         string    `json:"project"`
	Kind            ScopeKind `json:"kind"`
	Package         string    `json:"package,omitempty"`
	Component       string    `json:"component,omitempty"`
	ExternalSubject string    `json:"external_subject,omitempty"`
	Invariant       string    `json:"invariant"`
	Paths           []string  `json:"paths,omitempty"`
}

// NormalizeScopeV1 validates and serializes a canonical V1 scope key.
//
//oro:testonly
func NormalizeScopeV1(input ScopeInput) (ScopeKeyV1, error) {
	if len(input.Fields) != 0 {
		return "", fmt.Errorf("scope contains unknown fields")
	}

	project := normalizeIdentifier(input.Project)
	if project == "" {
		return "", fmt.Errorf("scope project is required")
	}

	kind, err := normalizeScopeKind(input.Kind)
	if err != nil {
		return "", err
	}

	invariant := strings.TrimSpace(input.Invariant)
	if invariant == "" {
		return "", fmt.Errorf("scope invariant is required")
	}

	paths, err := normalizePaths(input.Paths)
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(canonicalScopeV1{
		Project:         project,
		Kind:            kind,
		Package:         normalizeIdentifier(input.Package),
		Component:       normalizeIdentifier(input.Component),
		ExternalSubject: strings.TrimSpace(input.ExternalSubject),
		Invariant:       invariant,
		Paths:           paths,
	})
	if err != nil {
		return "", fmt.Errorf("serialize canonical scope: %w", err)
	}

	return ScopeKeyV1("scope-v1:" + string(payload)), nil
}

func normalizeScopeKind(kind ScopeKind) (ScopeKind, error) {
	normalized := ScopeKind(normalizeIdentifier(string(kind)))
	switch normalized {
	case ScopeKindTaskLocal, ScopeKindPrerequisite, ScopeKindSystemic, ScopeKindExternal:
		return normalized, nil
	default:
		return "", fmt.Errorf("unknown scope kind %q", kind)
	}
}

func normalizeIdentifier(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	unique := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path, err := normalizeRepositoryPath(rawPath)
		if err != nil {
			return nil, err
		}
		unique[path] = struct{}{}
	}

	normalized := make([]string, 0, len(unique))
	for path := range unique {
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)

	return normalized, nil
}

func normalizeRepositoryPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("scope path is required")
	}

	slashed := filepath.ToSlash(trimmed)
	if filepath.IsAbs(trimmed) || strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("scope path %q must be repository-relative", rawPath)
	}

	cleaned := filepath.ToSlash(filepath.Clean(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("scope path %q escapes the repository", rawPath)
	}

	return cleaned, nil
}
