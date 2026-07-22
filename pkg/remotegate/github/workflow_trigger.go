// Package github parses GitHub workflow trigger declarations.
package github

import (
	"bytes"
	"errors"
	"fmt"
	"io"

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
	default:
		return workflowTriggers{}, fmt.Errorf("%w: workflow on declaration must be a scalar or sequence", remotegate.ErrWorkflowIneligible)
	}
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
