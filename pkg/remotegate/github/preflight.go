// Package github contains the read-only GitHub workflow preflight boundary.
package github

import (
	"context"
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
	if err := c.api.GetJSON(ctx, requestPath, &response); err != nil {
		return "", "", fmt.Errorf("%w: %w", remotegate.ErrWorkflowIneligible, err)
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
