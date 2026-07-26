// Package github parses GitHub workflow trigger declarations.
package github

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"

	"gopkg.in/yaml.v3"

	"oro/pkg/remotegate"
)

type workflowTriggers struct {
	WorkflowDispatch          bool
	PullRequestBranches       []string
	PullRequestBranchesIgnore []string
}

func parseWorkflowTriggers(contents []byte) (workflowTriggers, error) {
	document, err := decodeWorkflowDocument(contents)
	if err != nil {
		return workflowTriggers{}, err
	}

	on := workflowOnNode(document)
	if on == nil {
		return workflowTriggers{}, nil
	}

	return parseFlatWorkflowTriggers(on)
}

func decodeWorkflowDocument(contents []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return &document, nil
		}
		return nil, fmt.Errorf("%w: decode workflow YAML: %w", remotegate.ErrWorkflowIneligible, err)
	}

	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("%w: decode workflow YAML: %w", remotegate.ErrWorkflowIneligible, err)
		}
		return nil, fmt.Errorf("%w: workflow YAML must contain one document", remotegate.ErrWorkflowIneligible)
	}
	if err := validateUniqueMappingKeys(&document); err != nil {
		return nil, err
	}

	return &document, nil
}

func validateUniqueMappingKeys(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			for otherIndex := index + 2; otherIndex < len(node.Content); otherIndex += 2 {
				otherKey := node.Content[otherIndex]
				if key.Kind == otherKey.Kind && key.Value == otherKey.Value {
					return fmt.Errorf("%w: workflow YAML contains duplicate mapping key %q", remotegate.ErrWorkflowIneligible, otherKey.Value)
				}
			}
		}
	}

	for _, child := range node.Content {
		if err := validateUniqueMappingKeys(child); err != nil {
			return err
		}
	}

	return nil
}

func workflowOnNode(document *yaml.Node) *yaml.Node {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil
	}

	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil
	}

	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind == yaml.ScalarNode && key.Tag == "!!str" && key.Value == "on" {
			return mapping.Content[index+1]
		}
	}

	return nil
}

func parseFlatWorkflowTriggers(on *yaml.Node) (workflowTriggers, error) {
	switch on.Kind {
	case yaml.ScalarNode:
		return parseWorkflowEvents([]*yaml.Node{on})
	case yaml.SequenceNode:
		return parseWorkflowEvents(on.Content)
	case yaml.MappingNode:
		return parseMappedWorkflowTriggers(on)
	default:
		return workflowTriggers{}, fmt.Errorf("%w: workflow on declaration must be a scalar or sequence", remotegate.ErrWorkflowIneligible)
	}
}

func parseMappedWorkflowTriggers(on *yaml.Node) (workflowTriggers, error) {
	var triggers workflowTriggers
	for index := 0; index+1 < len(on.Content); index += 2 {
		key, value := on.Content[index], on.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return workflowTriggers{}, fmt.Errorf("%w: workflow event key must be a string", remotegate.ErrWorkflowIneligible)
		}
		if err := applyMappedWorkflowEvent(&triggers, key.Value, value); err != nil {
			return workflowTriggers{}, err
		}
	}
	return triggers, nil
}

func applyMappedWorkflowEvent(triggers *workflowTriggers, event string, configuration *yaml.Node) error {
	if event != "workflow_dispatch" && event != "pull_request" {
		return nil
	}
	if !isUnfilteredWorkflowEvent(configuration) {
		return fmt.Errorf("%w: %s configuration must be a mapping or null", remotegate.ErrWorkflowIneligible, event)
	}
	if event == "workflow_dispatch" {
		triggers.WorkflowDispatch = true
		return nil
	}

	triggers.PullRequestBranches = make([]string, 0, 1)
	triggers.PullRequestBranchesIgnore = make([]string, 0, 1)
	if configuration.Kind != yaml.MappingNode {
		return nil
	}
	branches, branchesIgnore, err := parsePullRequestFilters(configuration)
	if err != nil {
		return err
	}
	triggers.PullRequestBranches = branches
	triggers.PullRequestBranchesIgnore = branchesIgnore
	return nil
}

func parsePullRequestFilters(configuration *yaml.Node) (branches, branchesIgnore []string, err error) {
	branches = make([]string, 0, 1)
	branchesIgnore = make([]string, 0, 1)
	for index := 0; index+1 < len(configuration.Content); index += 2 {
		key, value := configuration.Content[index], configuration.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, nil, fmt.Errorf("%w: pull_request filter key must be a string", remotegate.ErrWorkflowIneligible)
		}
		switch key.Value {
		case "branches":
			branches, err = parseBranchPatterns(value)
		case "branches-ignore":
			branchesIgnore, err = parseBranchPatterns(value)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if len(branches) > 0 && len(branchesIgnore) > 0 {
		return nil, nil, fmt.Errorf("%w: pull_request branches filters are ambiguous", remotegate.ErrWorkflowIneligible)
	}
	return branches, branchesIgnore, nil
}

func parseBranchPatterns(node *yaml.Node) ([]string, error) {
	nodes := node.Content
	if node.Kind == yaml.ScalarNode {
		nodes = []*yaml.Node{node}
	} else if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: branch filter must be a string or sequence", remotegate.ErrWorkflowIneligible)
	}
	patterns := make([]string, 0, len(nodes))
	for _, pattern := range nodes {
		if pattern.Kind != yaml.ScalarNode || pattern.Tag != "!!str" || pattern.Value == "" {
			return nil, fmt.Errorf("%w: branch filter must contain strings", remotegate.ErrWorkflowIneligible)
		}
		patterns = append(patterns, pattern.Value)
	}
	return patterns, nil
}

func isUnfilteredWorkflowEvent(value *yaml.Node) bool {
	return value.Kind == yaml.MappingNode || (value.Kind == yaml.ScalarNode && value.Tag == "!!null")
}

func parseWorkflowEvents(events []*yaml.Node) (workflowTriggers, error) {
	var triggers workflowTriggers
	for _, event := range events {
		if event.Kind != yaml.ScalarNode || event.Tag != "!!str" {
			return workflowTriggers{}, fmt.Errorf("%w: workflow event must be a string", remotegate.ErrWorkflowIneligible)
		}

		switch event.Value {
		case "workflow_dispatch":
			triggers.WorkflowDispatch = true
		case "pull_request":
			triggers.PullRequestBranches = make([]string, 0, 1)
			triggers.PullRequestBranchesIgnore = make([]string, 0, 1)
		}
	}

	return triggers, nil
}

func (triggers workflowTriggers) eligibleTargets(targets []string) []string {
	if triggers.PullRequestBranches == nil {
		return nil
	}
	eligible := make([]string, 0, len(targets))
	for _, target := range targets {
		if targetEligible(target, triggers.PullRequestBranches, triggers.PullRequestBranchesIgnore) {
			eligible = append(eligible, target)
		}
	}
	return eligible
}

func targetEligible(target string, branches, branchesIgnore []string) bool {
	if len(branches) > 0 && !matchesAnyBranchPattern(target, branches) {
		return false
	}
	return !matchesAnyBranchPattern(target, branchesIgnore)
}

func matchesAnyBranchPattern(target string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := path.Match(pattern, target)
		if err == nil && matched {
			return true
		}
	}
	return false
}
