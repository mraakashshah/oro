//nolint:testpackage // The boundary acceptance test must inspect Client.api.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/remotegate"
)

type readOnlyWorkflowAPI struct{}

func (readOnlyWorkflowAPI) GetJSON(context.Context, string, any) error { return nil }

func (readOnlyWorkflowAPI) GetContent(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func TestReadOnlyWorkflowPreflightBoundary(t *testing.T) {
	var reader APIReader = readOnlyWorkflowAPI{}
	client := Client{api: reader}
	if client.api != reader {
		t.Fatal("Client did not retain the read-only API reader")
	}
	typeOfReader := reflect.TypeOf((*APIReader)(nil)).Elem()
	if got := typeOfReader.NumMethod(); got != 2 {
		t.Fatalf("APIReader exposes %d methods, want exactly 2", got)
	}
	for _, name := range []string{"GetJSON", "GetContent"} {
		if _, ok := typeOfReader.MethodByName(name); !ok {
			t.Fatalf("APIReader is missing %s", name)
		}
	}
	for _, forbidden := range []string{"Create", "Update", "Delete", "Do", "Request"} {
		if _, ok := typeOfReader.MethodByName(forbidden); ok {
			t.Fatalf("APIReader exposes mutation-capable method %s", forbidden)
		}
	}
}

type workflowReader struct {
	path            string
	err             error
	body            string
	cancelAfterRead context.CancelFunc
	reads           int
}

func (r *workflowReader) GetJSON(_ context.Context, path string, dst any) error {
	r.reads++
	r.path = path
	if r.err != nil {
		return r.err
	}
	err := json.Unmarshal([]byte(r.body), dst)
	if r.cancelAfterRead != nil {
		r.cancelAfterRead()
	}
	return err
}

func (r *workflowReader) GetContent(context.Context, string, string) ([]byte, error) { return nil, nil }

func TestFetchActiveWorkflowMetadata(t *testing.T) {
	readerFailure := errors.New("reader failure")
	tests := []struct {
		name             string
		repository       string
		workflow         string
		body             string
		readerErr        error
		cancelBeforeRead bool
		cancelAfterRead  bool
		wantPath         string
		wantState        string
		wantRead         int
		wantErr          bool
		wantCause        error
	}{
		{
			name:       "active",
			repository: "acme/oro",
			workflow:   "ci.yml",
			body:       `{"path":".github/workflows/ci.yml","state":"active"}`,
			wantPath:   ".github/workflows/ci.yml",
			wantState:  "active",
			wantRead:   1,
		},
		{name: "absent", repository: "acme/oro", workflow: "ci.yml", readerErr: errors.New("404 not found"), wantRead: 1, wantErr: true},
		{name: "hidden", repository: "acme/oro", workflow: "ci.yml", readerErr: errors.New("404 hidden"), wantRead: 1, wantErr: true},
		{name: "inactive", repository: "acme/oro", workflow: "ci.yml", body: `{"path":".github/workflows/ci.yml","state":"disabled"}`, wantRead: 1, wantErr: true},
		{name: "path mismatch", repository: "acme/oro", workflow: "ci.yml", body: `{"path":".github/workflows/other.yml","state":"active"}`, wantRead: 1, wantErr: true},
		{name: "reader failure", repository: "acme/oro", workflow: "ci.yml", readerErr: readerFailure, wantRead: 1, wantErr: true, wantCause: readerFailure},
		{name: "malformed response", repository: "acme/oro", workflow: "ci.yml", body: `{`, wantRead: 1, wantErr: true},
		{name: "empty repository", workflow: "ci.yml", wantErr: true},
		{name: "empty workflow", repository: "acme/oro", wantErr: true},
		{name: "canceled before read", repository: "acme/oro", workflow: "ci.yml", cancelBeforeRead: true, wantErr: true, wantCause: context.Canceled},
		{name: "canceled during read", repository: "acme/oro", workflow: "ci.yml", body: `{"path":".github/workflows/ci.yml","state":"active"}`, cancelAfterRead: true, wantRead: 1, wantErr: true, wantCause: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelBeforeRead {
				cancel()
			}
			r := &workflowReader{body: tt.body, err: tt.readerErr}
			if tt.cancelAfterRead {
				r.cancelAfterRead = cancel
			}
			client := Client{api: r}
			path, state, err := client.fetchWorkflowMetadata(ctx, tt.repository, tt.workflow)
			if tt.wantRead == 1 && r.path != "repos/acme/oro/actions/workflows/ci.yml" {
				t.Fatalf("request path = %q", r.path)
			}
			if r.reads != tt.wantRead {
				t.Fatalf("reads = %d, want %d", r.reads, tt.wantRead)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("fetch metadata: %v", err)
				}
				if path != tt.wantPath || state != tt.wantState {
					t.Fatalf("metadata = %q, %q, want %q, %q", path, state, tt.wantPath, tt.wantState)
				}
				return
			}
			if err == nil {
				t.Fatal("fetch metadata succeeded, want error")
			}
			if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
				t.Fatalf("error %v is not ineligible", err)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("error %v does not preserve %v", err, tt.wantCause)
			}
			if path != "" || state != "" {
				t.Fatalf("metadata = %q, %q on error", path, state)
			}
		})
	}
}

type defaultBranchReader struct {
	response any
	err      error
	paths    []string
	decoded  bool
}

func (r *defaultBranchReader) GetJSON(_ context.Context, path string, dst any) error {
	r.paths = append(r.paths, path)
	if r.err != nil {
		return r.err
	}
	response, ok := r.response.(struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	})
	if !ok {
		return fmt.Errorf("malformed response")
	}
	r.decoded = true
	*(dst.(*struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	})) = response
	return nil
}

func (*defaultBranchReader) GetContent(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func TestFetchRepositoryDefaultBranch(t *testing.T) {
	t.Parallel()
	response := struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}{FullName: "acme/oro", DefaultBranch: "main"}
	reader := &defaultBranchReader{response: response}
	client := Client{api: reader}

	branch, err := client.fetchDefaultBranch(context.Background(), "acme/oro")
	if err != nil || branch != "main" {
		t.Fatalf("fetchDefaultBranch() = %q, %v; want main, nil", branch, err)
	}
	if !reflect.DeepEqual(reader.paths, []string{"repos/acme/oro"}) {
		t.Fatalf("reads = %v, want one repository read", reader.paths)
	}

	cases := []struct {
		name     string
		response any
		err      error
		repo     string
	}{
		{name: "identity", response: struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		}{FullName: "other/oro", DefaultBranch: "main"}, repo: "acme/oro"},
		{name: "blank", response: response, repo: "acme/oro"},
		{name: "reader", err: errors.New("reader failed"), repo: "acme/oro"},
		{name: "malformed", response: "bad", repo: "acme/oro"},
		{name: "empty repository", repo: ""},
		{name: "cancelled", response: response, repo: "acme/oro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "blank" {
				tc.response = struct {
					FullName      string `json:"full_name"`
					DefaultBranch string `json:"default_branch"`
				}{FullName: "acme/oro"}
			}
			ctx := context.Background()
			if tc.name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r := &defaultBranchReader{response: tc.response, err: tc.err}
			client := Client{api: r}
			_, err := client.fetchDefaultBranch(ctx, tc.repo)
			if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
				t.Fatalf("error %v does not satisfy ErrWorkflowIneligible", err)
			}
			if tc.name == "identity" && !r.decoded {
				t.Fatal("identity response was not decoded")
			}
			if tc.name == "empty repository" && len(r.paths) != 0 {
				t.Fatalf("empty repository performed reads: %v", r.paths)
			}
			if tc.name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error %v does not preserve cancellation", err)
			}
			if strings.TrimSpace(tc.repo) == "" && len(r.paths) != 0 {
				t.Fatalf("blank repository performed reads: %v", r.paths)
			}
		})
	}
}
