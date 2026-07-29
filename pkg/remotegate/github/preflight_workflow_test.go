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

type workflowRegistrationReader struct {
	repositoryResponse string
	workflowResponse   string
	contents           []byte
	jsonErr            error
	contentErr         error
	cancelAfter        string
	cancel             context.CancelFunc
	calls              []string
}

func (r *workflowRegistrationReader) GetJSON(_ context.Context, path string, dst any) error {
	r.calls = append(r.calls, "GET "+path)
	if r.jsonErr != nil {
		return r.jsonErr
	}
	var response string
	switch path {
	case "repos/acme/oro":
		response = r.repositoryResponse
	case "repos/acme/oro/actions/workflows/ci.yml":
		response = r.workflowResponse
	default:
		return fmt.Errorf("unexpected JSON path %q", path)
	}
	if err := json.Unmarshal([]byte(response), dst); err != nil {
		return err
	}
	if r.cancelAfter == path {
		r.cancel()
	}
	return nil
}

func (r *workflowRegistrationReader) GetContent(_ context.Context, path, ref string) ([]byte, error) {
	r.calls = append(r.calls, "CONTENT "+path+"@"+ref)
	if r.contentErr != nil {
		return nil, r.contentErr
	}
	if r.cancelAfter == "content" {
		r.cancel()
	}
	return r.contents, nil
}

func TestFetchDefaultBranchWorkflowRegistration(t *testing.T) {
	t.Parallel()
	const (
		repository = "acme/oro"
		workflow   = "ci.yml"
		path       = ".github/workflows/ci.yml"
	)
	contents := []byte("name: CI\n")
	reader := &workflowRegistrationReader{
		repositoryResponse: `{"full_name":"acme/oro","default_branch":"main"}`,
		workflowResponse:   `{"path":".github/workflows/ci.yml","state":"active"}`,
		contents:           contents,
	}
	client := Client{api: reader}

	registration, err := client.fetchWorkflowRegistration(context.Background(), PreflightRequest{
		Repository: repository,
		Workflow:   workflow,
	})
	if err != nil {
		t.Fatalf("fetchWorkflowRegistration() error = %v", err)
	}
	want := workflowRegistration{
		DefaultBranch: "main",
		Path:          path,
		State:         "active",
		Contents:      contents,
	}
	if !reflect.DeepEqual(registration, want) {
		t.Fatalf("registration = %#v, want %#v", registration, want)
	}
	if !reflect.DeepEqual(reader.calls, []string{
		"GET repos/acme/oro",
		"GET repos/acme/oro/actions/workflows/ci.yml",
		"CONTENT .github/workflows/ci.yml@main",
	}) {
		t.Fatalf("calls = %v", reader.calls)
	}

	for _, tc := range []struct {
		name        string
		reader      workflowRegistrationReader
		cancelAfter string
		wantCause   error
	}{
		{name: "empty contents", reader: workflowRegistrationReader{repositoryResponse: reader.repositoryResponse, workflowResponse: reader.workflowResponse}},
		{name: "workflow reader failure", reader: workflowRegistrationReader{repositoryResponse: reader.repositoryResponse, jsonErr: errors.New("reader failed")}},
		{name: "content reader failure", reader: workflowRegistrationReader{repositoryResponse: reader.repositoryResponse, workflowResponse: reader.workflowResponse, contentErr: errors.New("content failed")}},
		{name: "cancelled after workflow read", reader: workflowRegistrationReader{repositoryResponse: reader.repositoryResponse, workflowResponse: reader.workflowResponse}, cancelAfter: "repos/acme/oro/actions/workflows/ci.yml", wantCause: context.Canceled},
		{name: "cancelled after content read", reader: workflowRegistrationReader{repositoryResponse: reader.repositoryResponse, workflowResponse: reader.workflowResponse, contents: contents}, cancelAfter: "content", wantCause: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := tc.reader
			r.cancelAfter = tc.cancelAfter
			r.cancel = cancel
			got, err := (&Client{api: &r}).fetchWorkflowRegistration(ctx, PreflightRequest{Repository: repository, Workflow: workflow})
			if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
				t.Fatalf("error = %v, want ErrWorkflowIneligible", err)
			}
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, tc.wantCause)
			}
			if !reflect.DeepEqual(got, workflowRegistration{}) {
				t.Fatalf("registration = %#v, want zero value", got)
			}
		})
	}
}

type workflowEligibilityReader struct {
	repositoryResponse string
	workflowResponse   string
	contents           []byte
	jsonErrPath        string
	jsonErr            error
	contentErr         error
	mutations          int
}

func (r *workflowEligibilityReader) GetJSON(_ context.Context, path string, dst any) error {
	if path == r.jsonErrPath {
		return r.jsonErr
	}
	var response string
	switch path {
	case "repos/acme/oro":
		response = r.repositoryResponse
	case "repos/acme/oro/actions/workflows/ci.yml":
		response = r.workflowResponse
	default:
		return fmt.Errorf("unexpected JSON path %q", path)
	}
	return json.Unmarshal([]byte(response), dst)
}

func (r *workflowEligibilityReader) GetContent(context.Context, string, string) ([]byte, error) {
	return r.contents, r.contentErr
}

func TestPreflightWorkflowEligibility(t *testing.T) {
	t.Parallel()

	const (
		repositoryResponse = `{"full_name":"acme/oro","default_branch":"main"}`
		workflowResponse   = `{"path":".github/workflows/ci.yml","state":"active"}`
	)
	request := PreflightRequest{
		Repository: "acme/oro",
		Workflow:   "ci.yml",
		Targets:    []string{"main", "release/1", "epic/demo"},
	}
	validContents := []byte("on:\n  workflow_dispatch:\n  pull_request:\n")
	want := remotegate.WorkflowEvidence{
		Path:               ".github/workflows/ci.yml",
		State:              "active",
		Ref:                "main",
		WorkflowDispatch:   true,
		PullRequestTargets: []string{"main", "release/1", "epic/demo"},
	}

	reader := &workflowEligibilityReader{
		repositoryResponse: repositoryResponse,
		workflowResponse:   workflowResponse,
		contents:           validContents,
	}
	got, err := (&Client{api: reader}).inspectWorkflow(context.Background(), request)
	if err != nil {
		t.Fatalf("inspectWorkflow() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inspectWorkflow() = %#v, want %#v", got, want)
	}
	if reader.mutations != 0 {
		t.Fatalf("mutation calls = %d, want 0", reader.mutations)
	}

	tests := []struct {
		name               string
		repositoryResponse string
		workflowResponse   string
		contents           []byte
		jsonErrPath        string
		jsonErr            error
		contentErr         error
		cancel             bool
		wantCause          error
	}{
		{name: "absent", repositoryResponse: repositoryResponse, jsonErrPath: "repos/acme/oro/actions/workflows/ci.yml", jsonErr: errors.New("404 not found")},
		{name: "hidden", repositoryResponse: repositoryResponse, jsonErrPath: "repos/acme/oro/actions/workflows/ci.yml", jsonErr: errors.New("404 hidden")},
		{name: "disabled", repositoryResponse: repositoryResponse, workflowResponse: `{"path":".github/workflows/ci.yml","state":"disabled"}`, contents: validContents},
		{name: "ambiguous state", repositoryResponse: repositoryResponse, workflowResponse: `{"path":".github/workflows/ci.yml","state":""}`, contents: validContents},
		{name: "default branch failure", jsonErrPath: "repos/acme/oro", jsonErr: errors.New("metadata unavailable")},
		{name: "missing workflow dispatch", repositoryResponse: repositoryResponse, workflowResponse: workflowResponse, contents: []byte("on: pull_request\n")},
		{name: "missing pull request", repositoryResponse: repositoryResponse, workflowResponse: workflowResponse, contents: []byte("on: workflow_dispatch\n")},
		{name: "base filtered", repositoryResponse: repositoryResponse, workflowResponse: workflowResponse, contents: []byte("on:\n  workflow_dispatch:\n  pull_request:\n    branches: [main]\n")},
		{name: "content failure", repositoryResponse: repositoryResponse, workflowResponse: workflowResponse, contentErr: errors.New("content unavailable")},
		{name: "cancelled", repositoryResponse: repositoryResponse, workflowResponse: workflowResponse, contents: validContents, cancel: true, wantCause: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r := &workflowEligibilityReader{
				repositoryResponse: tt.repositoryResponse,
				workflowResponse:   tt.workflowResponse,
				contents:           tt.contents,
				jsonErrPath:        tt.jsonErrPath,
				jsonErr:            tt.jsonErr,
				contentErr:         tt.contentErr,
			}
			evidence, err := (&Client{api: r}).inspectWorkflow(ctx, request)
			if !errors.Is(err, remotegate.ErrWorkflowIneligible) {
				t.Fatalf("inspectWorkflow() error = %v, want ErrWorkflowIneligible", err)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("inspectWorkflow() error = %v, want cause %v", err, tt.wantCause)
			}
			if !reflect.DeepEqual(evidence, remotegate.WorkflowEvidence{}) {
				t.Fatalf("inspectWorkflow() evidence = %#v, want zero value", evidence)
			}
			if r.mutations != 0 {
				t.Fatalf("mutation calls = %d, want 0", r.mutations)
			}
		})
	}
}

func TestClientPreflightCollectsWorkflowAndPolicy(t *testing.T) {
	reader := &workflowEligibilityReader{
		repositoryResponse: `{"full_name":"acme/oro","default_branch":"main"}`,
		workflowResponse:   `{"path":".github/workflows/ci.yml","state":"active"}`,
		contents:           []byte("on:\n  workflow_dispatch:\n  pull_request:\n"),
	}
	collection := &targetRuleCollectionFixture{itemsByTarget: map[string][]effectiveRuleResponse{"main": {{
		ID:          1,
		Source:      "repository",
		Version:     "1",
		Pattern:     "main",
		Enforcement: "active",
		Operations:  []string{"update"},
	}}}}
	client := NewClient(reader, "acme/oro", collection, CollectionLimits{MaxPages: 1, MaxItems: 2, MaxBytes: 1024})
	evidence, err := client.Preflight(context.Background(), PreflightRequest{
		Repository: "acme/oro",
		Workflow:   "ci.yml",
		Targets:    []string{"main"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if evidence.Workflow.Ref != "main" || evidence.Hash == "" || len(evidence.Policy.Rules) != 1 {
		t.Fatalf("Preflight() evidence = %+v, want workflow and policy evidence", evidence)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Preflight(ctx, PreflightRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Preflight() canceled error = %v, want context.Canceled", err)
	}
}
