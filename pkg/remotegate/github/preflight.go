// Package github contains the read-only GitHub workflow preflight boundary.
package github

import (
	"context"
	"errors"
	"fmt"
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

// Client performs read-only workflow preflight operations through api.
type Client struct {
	api APIReader
}

func (c *Client) fetchWorkflowMetadata(ctx context.Context, repository, workflow string) (path, state string, err error) {
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

type workflowRegistration struct { //nolint:unused // registration shape is defined for the preflight implementation.
	DefaultBranch string
	Path          string
	State         string
	Contents      []byte
}

func (c *Client) fetchDefaultBranch(ctx context.Context, repository string) (string, error) {
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
