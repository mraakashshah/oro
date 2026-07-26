package remotegate

import (
	"errors"
	"fmt"
	"strings"
)

// ErrWorkflowIneligible indicates that a workflow cannot be used for a remote
// workflow capability preflight.
var ErrWorkflowIneligible = errors.New("workflow ineligible")

// WorkflowEvidence records the workflow registration and the pull-request
// target refs proven eligible by preflight.
type WorkflowEvidence struct {
	Path               string
	State              string
	Ref                string
	WorkflowDispatch   bool
	PullRequestTargets []string
}

// ValidateWorkflowEvidence rejects incomplete or ambiguous workflow evidence.
func ValidateWorkflowEvidence(evidence WorkflowEvidence) error {
	if !strings.HasPrefix(evidence.Path, ".github/workflows/") {
		return fmt.Errorf("%w: invalid workflow path", ErrWorkflowIneligible)
	}
	if evidence.State != "active" {
		return fmt.Errorf("%w: workflow is not active", ErrWorkflowIneligible)
	}
	if strings.TrimSpace(evidence.Ref) == "" {
		return fmt.Errorf("%w: workflow ref is required", ErrWorkflowIneligible)
	}
	if !evidence.WorkflowDispatch {
		return fmt.Errorf("%w: workflow_dispatch is required", ErrWorkflowIneligible)
	}
	if len(evidence.PullRequestTargets) == 0 {
		return fmt.Errorf("%w: pull_request targets are required", ErrWorkflowIneligible)
	}

	seen := make(map[string]struct{}, len(evidence.PullRequestTargets))
	for _, target := range evidence.PullRequestTargets {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("%w: pull_request target is empty", ErrWorkflowIneligible)
		}
		if _, duplicate := seen[target]; duplicate {
			return fmt.Errorf("%w: duplicate pull_request target %q", ErrWorkflowIneligible, target)
		}
		seen[target] = struct{}{}
	}
	return nil
}
