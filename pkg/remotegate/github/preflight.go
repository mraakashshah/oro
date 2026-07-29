// Package github contains the read-only GitHub workflow preflight boundary.
package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"oro/pkg/remotegate"
)

// APIReader is the narrow GitHub API surface available to workflow preflight.
// It intentionally contains only operations that read repository metadata and
// workflow contents.
type APIReader interface {
	GetJSON(ctx context.Context, path string, dst any) error
	GetContent(ctx context.Context, path string, ref string) ([]byte, error)
}

// PreflightClient performs read-only workflow preflight operations through api.
type PreflightClient struct {
	api              APIReader
	repository       string
	collection       CollectionReader
	collectionLimits CollectionLimits
}

func (c *PreflightClient) fetchWorkflowMetadata(ctx context.Context, repository, workflow string) (path, state string, err error) {
	if strings.TrimSpace(repository) == "" || strings.TrimSpace(workflow) == "" {
		return "", "", remotegate.ErrWorkflowIneligible
	}
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, err)
	}

	var response struct {
		Path  string `json:"path"`
		State string `json:"state"`
	}
	requestPath := "repos/" + repository + "/actions/workflows/" + workflow
	readErr := c.api.GetJSON(ctx, requestPath, &response)
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, err)
	}
	if readErr != nil {
		return "", "", fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, readErr)
	}
	wantPath := ".github/workflows/" + workflow
	if response.Path != wantPath || response.State != "active" {
		return "", "", remotegate.ErrWorkflowIneligible
	}
	return response.Path, response.State, nil
}

// PreflightRequest identifies the repository workflow and target branches to
// inspect.
type PreflightRequest struct {
	Repository string
	Workflow   string
	Targets    []string
}

// PreflightEvidence is the complete read-only workflow and policy evidence
// collected for a startup target.
type PreflightEvidence struct {
	Workflow remotegate.WorkflowEvidence
	Policy   remotegate.EffectivePolicy
	Hash     string
}

// NewPreflightClient constructs a read-only GitHub preflight client.
func NewPreflightClient(api APIReader, repository string, collection CollectionReader, limits CollectionLimits) *PreflightClient {
	return &PreflightClient{
		api:              api,
		repository:       repository,
		collection:       collection,
		collectionLimits: limits,
	}
}

// Preflight verifies workflow eligibility and effective policy for every
// requested target without mutating GitHub state.
func (c *PreflightClient) Preflight(ctx context.Context, req PreflightRequest) (PreflightEvidence, error) {
	if err := ctx.Err(); err != nil {
		return PreflightEvidence{}, fmt.Errorf("preflight context: %w", err)
	}
	workflow, err := c.inspectWorkflow(ctx, req)
	if err != nil {
		return PreflightEvidence{}, fmt.Errorf("inspect workflow: %w", err)
	}
	policy, err := c.effectiveTargetPolicy(ctx, req.Targets)
	if err != nil {
		return PreflightEvidence{}, fmt.Errorf("inspect effective policy: %w", err)
	}
	hash, err := remotegate.CanonicalPolicyHash(policy)
	if err != nil {
		return PreflightEvidence{}, fmt.Errorf("hash effective policy: %w", err)
	}
	return PreflightEvidence{Workflow: workflow, Policy: policy, Hash: hash}, nil
}

type workflowRegistration struct {
	DefaultBranch string
	Path          string
	State         string
	Contents      []byte
}

func (c *PreflightClient) fetchWorkflowRegistration(ctx context.Context, req PreflightRequest) (workflowRegistration, error) {
	defaultBranch, err := c.fetchDefaultBranch(ctx, req.Repository)
	if err != nil {
		return workflowRegistration{}, err
	}
	path, state, err := c.fetchWorkflowMetadata(ctx, req.Repository, req.Workflow)
	if err != nil {
		return workflowRegistration{}, err
	}
	contents, readErr := c.api.GetContent(ctx, path, defaultBranch)
	if err := ctx.Err(); err != nil {
		return workflowRegistration{}, fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, err)
	}
	if readErr != nil {
		return workflowRegistration{}, fmt.Errorf("%w: read workflow contents: %w", remotegate.ErrWorkflowIneligible, readErr)
	}
	if len(contents) == 0 {
		return workflowRegistration{}, fmt.Errorf("%w: workflow contents are empty", remotegate.ErrWorkflowIneligible)
	}
	return workflowRegistration{
		DefaultBranch: defaultBranch,
		Path:          path,
		State:         state,
		Contents:      contents,
	}, nil
}

func (c *PreflightClient) inspectWorkflow(ctx context.Context, req PreflightRequest) (remotegate.WorkflowEvidence, error) {
	registration, err := c.fetchWorkflowRegistration(ctx, req)
	if err != nil {
		return remotegate.WorkflowEvidence{}, err
	}
	triggers, err := parseWorkflowTriggers(registration.Contents)
	if err != nil {
		return remotegate.WorkflowEvidence{}, err
	}
	eligibleTargets := triggers.eligibleTargets(req.Targets)
	if !slices.Equal(eligibleTargets, req.Targets) {
		return remotegate.WorkflowEvidence{}, fmt.Errorf("%w: pull_request does not cover every required target", remotegate.ErrWorkflowIneligible)
	}
	evidence := remotegate.WorkflowEvidence{
		Path:               registration.Path,
		State:              registration.State,
		Ref:                registration.DefaultBranch,
		WorkflowDispatch:   triggers.WorkflowDispatch,
		PullRequestTargets: eligibleTargets,
	}
	if err := remotegate.ValidateWorkflowEvidence(evidence); err != nil {
		return remotegate.WorkflowEvidence{}, fmt.Errorf("validate workflow evidence: %w", err)
	}
	return evidence, nil
}

func (c *PreflightClient) fetchDefaultBranch(ctx context.Context, repository string) (string, error) {
	if strings.TrimSpace(repository) == "" {
		return "", fmt.Errorf("%w: repository is required", remotegate.ErrWorkflowIneligible)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, err)
	}
	var response struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.api.GetJSON(ctx, "repos/"+repository, &response); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, err)
		}
		return "", fmt.Errorf("%w: read repository metadata: %w", remotegate.ErrWorkflowIneligible, err)
	}
	if response.FullName != repository {
		return "", fmt.Errorf("%w: repository identity %q does not match %q", remotegate.ErrWorkflowIneligible, response.FullName, repository)
	}
	if strings.TrimSpace(response.DefaultBranch) == "" {
		return "", fmt.Errorf("%w: repository default branch is empty", remotegate.ErrWorkflowIneligible)
	}
	return response.DefaultBranch, nil
}
