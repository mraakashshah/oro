package github_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
	"oro/pkg/remotegate"
	github "oro/pkg/remotegate/github"
)

type collectionItem struct {
	StableID string `json:"id"`
	Kind     string `json:"kind"`
}

func (item collectionItem) ID() string { return item.StableID }

func TestCompleteCollection(t *testing.T) {
	request := github.CollectionRequest{Path: "/repos/acme/oro/pulls", MaxPages: 3, MaxItems: 40, MaxBytes: 4096}

	t.Run("normalizes uneven pages", func(t *testing.T) {
		first := []collectionItem{{StableID: "pr-0", Kind: "pull"}}
		second := make([]collectionItem, 30)
		kinds := []string{"check", "run", "job", "artifact", "ruleset", "history"}
		for index := range second {
			second[index] = collectionItem{StableID: fmt.Sprintf("item-%d", index+1), Kind: kinds[index%len(kinds)]}
		}
		runner := collectionTestRunner(t, collectionPagesJSON(t, first, second))
		collection, err := github.Collect[collectionItem](context.Background(), runner, request)
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		if collection.Evidence != (github.CollectionEvidence{PageCount: 2, ItemCount: 31}) {
			t.Fatalf("Collect() evidence = %+v, want %+v", collection.Evidence, github.CollectionEvidence{PageCount: 2, ItemCount: 31})
		}
		ids := collectionIDs(collection)
		if len(ids) != 31 || ids[0] != "pr-0" || ids[30] != "item-30" {
			t.Fatalf("Collect() IDs = %q, want all 31 IDs in page order", strings.Join(ids, ","))
		}
		seenKinds := make(map[string]bool)
		for _, item := range collection.Items {
			seenKinds[item.Kind] = true
		}
		for _, kind := range append([]string{"pull"}, kinds...) {
			if !seenKinds[kind] {
				t.Errorf("Collect() omitted %s result", kind)
			}
		}
	})

	t.Run("normalizes GitHub collection shapes", func(t *testing.T) {
		for name, output := range map[string]string{
			"pull requests": `[ [{"id":"pull"}] ]`,
			"checks":        `[{"check_runs":[{"id":"check"}]}]`,
			"runs":          `[{"workflow_runs":[{"id":"run"}]}]`,
			"jobs":          `[{"jobs":[{"id":"job"}]}]`,
			"artifacts":     `[{"artifacts":[{"id":"artifact"}]}]`,
			"rulesets":      `[{"rulesets":[{"id":"ruleset"}]}]`,
			"history":       `[{"history":[{"id":"history"}]}]`,
		} {
			t.Run(name, func(t *testing.T) {
				collection, err := github.Collect[collectionItem](context.Background(), collectionTestRunner(t, output), request)
				if err != nil {
					t.Fatalf("Collect() error = %v", err)
				}
				if collection.Evidence != (github.CollectionEvidence{PageCount: 1, ItemCount: 1}) {
					t.Fatalf("Collect() evidence = %+v, want one normalized item", collection.Evidence)
				}
			})
		}
	})

	for name, output := range map[string]string{
		"malformed shape": `{"id":"not-a-page"}`,
		"duplicate ID":    `[[{"id":"duplicate"}],[{"id":"duplicate"}]]`,
		"exhausted bound": `[[{"id":"one"}],[{"id":"two"}],[{"id":"three"}]]`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := collectionTestRunner(t, output)
			collection, err := github.Collect[collectionItem](context.Background(), runner, request)
			if !errors.Is(err, github.ErrIncompleteCollection) {
				t.Fatalf("Collect() error = %v, want ErrIncompleteCollection", err)
			}
			if collection.Items != nil {
				t.Fatalf("Collect() items = %+v, want no collection", collection.Items)
			}
		})
	}

	for _, name := range []string{"later-page failure", "cycle", "foreign host", "repeated token"} {
		t.Run(name, func(t *testing.T) {
			runner := collectionTestRunnerWithStatus(t, `[[{"id":"partial"}]]`, 1)
			collection, err := github.Collect[collectionItem](context.Background(), runner, request)
			if !errors.Is(err, github.ErrIncompleteCollection) {
				t.Fatalf("Collect() error = %v, want ErrIncompleteCollection", err)
			}
			if collection.Items != nil {
				t.Fatalf("Collect() items = %+v, want no collection", collection.Items)
			}
		})
	}
}

func collectionIDs[T github.Identified](collection github.CompleteCollection[T]) []string {
	ids := make([]string, len(collection.Items))
	for index, item := range collection.Items {
		ids[index] = item.ID()
	}
	return ids
}

func collectionPagesJSON(t *testing.T, pages ...any) string {
	t.Helper()
	output, err := json.Marshal(pages)
	if err != nil {
		t.Fatalf("marshal collection pages: %v", err)
	}
	return string(output)
}

func collectionTestRunner(t *testing.T, output string) *github.GHRunner {
	t.Helper()
	return collectionTestRunnerWithStatus(t, output, 0)
}

func collectionTestRunnerWithStatus(t *testing.T, output string, status int) *github.GHRunner {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'--paginate'*'--slurp'*) printf '%%s' '%s'; exit %d ;;\n  *) exit 9 ;;\nesac\n", output, status)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write gh helper: %v", err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonical GitHub CLI helper: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read gh helper: %v", err)
	}
	digest := sha256.Sum256(contents)
	target := remotegate.CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{Type: "github-app", AppID: 1, InstallationID: 2, PrivateKeyRef: "keychain:oro/test"},
		Host:     "github.example",
		Owner:    "acme",
		Name:     "oro",
	}
	provider := remotegate.NewRuntimeCredentialProvider(target, collectionCredentialSource{target: target})
	runner, err := github.NewGHRunner(github.AttestedCLI{Path: path, Hash: hex.EncodeToString(digest[:])}, provider, github.GHRunnerConfig{Host: target.Host})
	if err != nil {
		t.Fatalf("NewGHRunner() error = %v", err)
	}
	return runner
}

type collectionCredentialSource struct{ target remotegate.CredentialTarget }

func (source collectionCredentialSource) Resolve(_ context.Context, request remotegate.CredentialRequest) (remotegate.Credential, error) {
	return remotegate.Credential{Token: "test-token", Role: request.Role, AppID: source.target.Identity.AppID, InstallationID: source.target.Identity.InstallationID, Host: source.target.Host, Owner: source.target.Owner, Name: source.target.Name, Permissions: request.Permissions, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
