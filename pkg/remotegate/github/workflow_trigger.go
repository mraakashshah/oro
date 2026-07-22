// Package github parses GitHub workflow trigger declarations.
package github

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"oro/pkg/remotegate"
)

type workflowTriggers struct {
	WorkflowDispatch          bool
	PullRequestBranches       []string
	PullRequestBranchesIgnore []string
}

func parseWorkflowTriggers(contents []byte) (workflowTriggers, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return workflowTriggers{}, fmt.Errorf("%w: decode workflow YAML: %w", remotegate.ErrWorkflowIneligible, err)
	}

	on, found := workflowOnNode(&document)
	if !found {
		return workflowTriggers{}, nil
	}

	return parseFlatWorkflowTriggers(on)
}

func workflowOnNode(document *yaml.Node) (*yaml.Node, bool) {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, false
	}

	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, false
	}

	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind == yaml.ScalarNode && key.Tag == "!!str" && key.Value == "on" {
			return mapping.Content[index+1], true
		}
	}

	return nil, false
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
