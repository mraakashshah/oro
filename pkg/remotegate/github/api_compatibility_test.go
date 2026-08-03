package github_test

import (
	"testing"

	"oro/pkg/remotegate"
	"oro/pkg/remotegate/github"
)

type preflightConstructor func(github.APIReader, string, github.CollectionReader, github.CollectionLimits) *github.Client

type changeConstructor func(github.Config, *github.GHRunner, github.GitTransport, remotegate.RuntimeCredentialProvider) (*github.ChangeClient, error)

func TestCurrentMainPreflightConstructorCompatibility(t *testing.T) {
	t.Parallel()
	var constructor preflightConstructor = github.NewClient
	if client := constructor(nil, "acme/oro", nil, github.CollectionLimits{}); client == nil {
		t.Fatal("NewClient() returned a nil preflight client")
	}
}

func TestChangeAdapterConstructorContract(t *testing.T) {
	t.Parallel()
	var constructor changeConstructor = github.NewChangeClient
	if constructor == nil {
		t.Fatal("NewChangeClient constructor is nil")
	}
}
