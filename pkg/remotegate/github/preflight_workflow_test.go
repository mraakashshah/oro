//nolint:testpackage // The boundary acceptance test must inspect Client.api.
package github

import (
	"context"
	"reflect"
	"testing"
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
