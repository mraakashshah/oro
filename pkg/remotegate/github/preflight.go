// Package github contains the read-only GitHub workflow preflight boundary.
package github

import "context"

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
