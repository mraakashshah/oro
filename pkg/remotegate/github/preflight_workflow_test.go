//nolint:testpackage // The boundary acceptance test must inspect Client.api.
package github

import (
	"context"
	"errors"
	"reflect"
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
	path  string
	err   error
	body  any
	reads int
}

func (r *workflowReader) GetJSON(_ context.Context, path string, dst any) error {
	r.reads++
	r.path = path
	if r.err != nil {
		return r.err
	}
	*(dst.(*struct {
		Path  string `json:"path"`
		State string `json:"state"`
	})) = *(r.body.(*struct {
		Path  string `json:"path"`
		State string `json:"state"`
	}))
	return nil
}

func (r *workflowReader) GetContent(context.Context, string, string) ([]byte, error) { return nil, nil }

func TestFetchActiveWorkflowMetadata(t *testing.T) {
	active := struct {
		Path  string `json:"path"`
		State string `json:"state"`
	}{Path: ".github/workflows/ci.yml", State: "active"}
	tests := []struct {
		name, repository, workflow string
		body                       any
		err                        error
		wantPath, wantState        string
		wantRead                   int
	}{
		{name: "active", repository: "acme/oro", workflow: "ci.yml", body: &active, wantPath: active.Path, wantState: active.State, wantRead: 1},
		{name: "missing", repository: "acme/oro", workflow: "ci.yml", err: errors.New("404"), wantRead: 1},
		{name: "inactive", repository: "acme/oro", workflow: "ci.yml", body: &struct {
			Path  string `json:"path"`
			State string `json:"state"`
		}{active.Path, "disabled"}, wantRead: 1},
		{name: "empty repository", workflow: "ci.yml", wantRead: 0},
		{name: "empty workflow", repository: "acme/oro", wantRead: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &workflowReader{body: tt.body, err: tt.err}
			client := Client{api: r}
			path, state, err := client.fetchWorkflowMetadata(context.Background(), tt.repository, tt.workflow)
			if tt.wantRead == 1 && r.path != "repos/acme/oro/actions/workflows/ci.yml" {
				t.Fatalf("path = %q", r.path)
			}
			if r.reads != tt.wantRead {
				t.Fatalf("reads = %d, want %d", r.reads, tt.wantRead)
			}
			if tt.wantRead == 1 && err == nil && (path != tt.wantPath || state != tt.wantState) {
				t.Fatalf("metadata = %q, %q", path, state)
			}
			if tt.wantRead == 0 && err == nil {
				t.Fatal("expected error")
			}
			if err != nil && !errors.Is(err, remotegate.ErrWorkflowIneligible) {
				t.Fatalf("error %v is not ineligible", err)
			}
		})
	}
}
