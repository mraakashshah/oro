package github_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
	"oro/pkg/remotegate"
	github "oro/pkg/remotegate/github"
)

func TestAttestedGHRunner(t *testing.T) {
	assertConstructionIsProductionWired(t)

	helper, marker := testHelperEvidence(t)
	provider := testRuntimeCredentialProvider()
	runner, err := github.NewGHRunner(helper, provider, github.GHRunnerConfig{Host: "github.example"})
	if err != nil {
		t.Fatalf("NewGHRunner() error = %v", err)
	}

	requestType := reflect.TypeOf(github.APIRequest{})
	if _, found := requestType.FieldByName("BearerToken"); found {
		t.Fatal("APIRequest must not expose a bearer token field")
	}

	t.Setenv("GH_TOKEN", "ambient-gh-token")
	t.Setenv("GITHUB_TOKEN", "ambient-github-token")
	t.Setenv("GIT_CONFIG_GLOBAL", "/ambient/gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/ambient/gitconfig-system")
	result, err := runner.Run(context.Background(), github.APIRequest{Method: "GET", Path: "/repos/oro/oro"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, forbidden := range []string{"ambient-gh-token", "ambient-github-token", "/ambient/gitconfig", "secret-runtime-token"} {
		if strings.Contains(string(result), forbidden) {
			t.Errorf("helper result leaked %q: %s", forbidden, result)
		}
	}
	if !strings.Contains(string(result), `"path":"/repos/oro/oro"`) {
		t.Errorf("helper result = %s, want API path", result)
	}
	raw, err := runner.Run(context.Background(), github.APIRequest{
		Method:  "GET",
		Path:    "/repos/oro/oro/contents/.github/workflows/ci.yml",
		Headers: []string{"Accept: application/vnd.github.raw+json"},
		Raw:     true,
	})
	if err != nil {
		t.Fatalf("Run(raw) error = %v", err)
	}
	if string(raw) != "on: workflow_dispatch" {
		t.Fatalf("Run(raw) = %q, want raw workflow contents", raw)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove successful-run marker: %v", err)
	}

	t.Run("credential failures do not spawn", func(t *testing.T) {
		credentialError := errors.New("credential-source-secret")
		failedProvider := testRuntimeCredentialProviderWithError(credentialError)
		for name, provider := range map[string]remotegate.RuntimeCredentialProvider{
			"absent provider": {},
			"provider error":  failedProvider,
		} {
			t.Run(name, func(t *testing.T) {
				unavailable, err := github.NewGHRunner(helper, provider, github.GHRunnerConfig{})
				if err != nil {
					t.Fatalf("NewGHRunner() error = %v", err)
				}
				_, err = unavailable.Run(context.Background(), github.APIRequest{Method: "GET", Path: "/user"})
				if !errors.Is(err, remotegate.ErrCredentialInvalid) {
					t.Fatalf("Run() error = %v, want wrapped ErrCredentialInvalid", err)
				}
				if strings.Contains(err.Error(), "credential-source-secret") {
					t.Errorf("Run() error leaked credential detail: %v", err)
				}
				if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("credential failure started gh helper: stat marker error = %v", statErr)
				}
			})
		}
	})

	t.Run("invalid evidence does not spawn", func(t *testing.T) {
		for name, evidence := range map[string]github.AttestedCLI{
			"relative executable": {Path: "gh", Hash: helper.Hash},
			"digest mismatch":     {Path: helper.Path, Hash: strings.Repeat("0", len(helper.Hash))},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := github.NewGHRunner(evidence, provider, github.GHRunnerConfig{})
				if !errors.Is(err, github.ErrInvalidAttestation) {
					t.Fatalf("NewGHRunner() error = %v, want wrapped ErrInvalidAttestation", err)
				}
				if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("invalid evidence started gh helper: stat marker error = %v", statErr)
				}
			})
		}
	})
}

func assertConstructionIsProductionWired(t *testing.T) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate gh runner test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "gh_runner.go"))
	if err != nil {
		t.Fatalf("read gh runner source: %v", err)
	}
	if strings.Contains(string(source), "//oro:testonly") {
		t.Fatal("gh runner must not retain a test-only suppression after production wiring")
	}
}

func testHelperEvidence(t *testing.T) (github.AttestedCLI, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	marker := filepath.Join(dir, "started")
	script := `#!/bin/sh
printf started > '` + marker + `'
case "$GH_TOKEN:$GITHUB_TOKEN:$GIT_CONFIG_GLOBAL:$GIT_CONFIG_SYSTEM" in
  *ambient-gh-token*|*ambient-github-token*|*/ambient/*) exit 2 ;;
esac
case "$*" in
  *"--header Accept: application/vnd.github.raw+json"*) printf 'on: workflow_dispatch'; exit 0 ;;
esac
last=""
for arg do last="$arg"; done
printf '{"path":"%s"}' "$last"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write GitHub CLI helper: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonical GitHub CLI helper: %v", err)
	}
	path = resolved
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper: %v", err)
	}
	digest := sha256.Sum256(contents)
	return github.AttestedCLI{Path: path, Hash: hex.EncodeToString(digest[:])}, marker
}

func testRuntimeCredentialProvider() remotegate.RuntimeCredentialProvider {
	target := remotegate.CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{Type: "github-app", AppID: 1, InstallationID: 2, PrivateKeyRef: "keychain:oro/test"},
		Host:     "github.example",
		Owner:    "oro",
		Name:     "oro",
	}
	return remotegate.NewRuntimeCredentialProvider(target, testCredentialSource{target: target})
}

func testRuntimeCredentialProviderWithError(err error) remotegate.RuntimeCredentialProvider {
	target := remotegate.CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{Type: "github-app", AppID: 1, InstallationID: 2, PrivateKeyRef: "keychain:oro/test"},
		Host:     "github.example",
		Owner:    "oro",
		Name:     "oro",
	}
	return remotegate.NewRuntimeCredentialProvider(target, testCredentialSource{target: target, err: err})
}

type testCredentialSource struct {
	target remotegate.CredentialTarget
	err    error
}

func (source testCredentialSource) Resolve(_ context.Context, request remotegate.CredentialRequest) (remotegate.Credential, error) {
	if source.err != nil {
		return remotegate.Credential{}, source.err
	}
	return remotegate.Credential{
		Token:          "secret-runtime-token",
		Role:           request.Role,
		AppID:          source.target.Identity.AppID,
		InstallationID: source.target.Identity.InstallationID,
		Host:           source.target.Host,
		Owner:          source.target.Owner,
		Name:           source.target.Name,
		Permissions:    request.Permissions,
		ExpiresAt:      time.Now().Add(time.Hour),
	}, nil
}
