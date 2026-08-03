package github //nolint:testpackage // Exercises the package-private attested runner fixture.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
	"oro/pkg/remotegate"
)

func TestGitHubChangeLifecycle(t *testing.T) {
	t.Parallel()

	fixture := newChangeLifecycleFixture(t)
	repository := remotegate.Repository{Host: "github.example", Owner: "acme", Name: "oro"}
	candidate := remotegate.Candidate{
		Repository: repository,
		Ref:        "refs/heads/agent/oro-83st",
		SHA:        strings.Repeat("1", 40),
		TreeSHA:    strings.Repeat("2", 40),
	}
	target := remotegate.Target{
		Repository: repository,
		Ref:        "refs/heads/main",
		SHA:        strings.Repeat("3", 40),
	}
	change := remotegate.Change{ID: "7", Candidate: candidate, Target: target, Draft: true}
	owned := remotegate.RemoteChange{Change: change, Owner: "worker-1", Generation: 1}
	lease := remotegate.Lease{Owner: owned.Owner, Generation: owned.Generation, ExpectedSHA: candidate.SHA}

	client, err := NewClient(Config{
		Repository:     repository,
		RequiredChecks: []string{"quality-gate"},
		Limits:         CollectionLimits{MaxPages: 3, MaxItems: 10, MaxBytes: 4096},
	}, fixture.runner, &fixture.git, fixture.credentials)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	published, err := client.Publish(context.Background(), remotegate.PublishRequest{Candidate: candidate, Target: target, Lease: remotegate.RefLease{ExpectedAbsent: true}})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if published.Candidate != candidate || published.RemoteRef != candidate.Ref || len(fixture.git.requests) != 1 {
		t.Fatalf("Publish() = %#v, pushes = %#v", published, fixture.git.requests)
	}

	ensured, err := client.EnsureChange(context.Background(), remotegate.EnsureChangeRequest{Change: owned, Lease: lease})
	if err != nil {
		t.Fatalf("EnsureChange(lost response) error = %v", err)
	}
	if !ensured.Change.Draft || ensured.Change.URL != "https://github.example/acme/oro/pull/7" {
		t.Fatalf("EnsureChange() = %#v, want exact draft PR", ensured)
	}
	if _, err := client.EnsureChange(context.Background(), remotegate.EnsureChangeRequest{Change: ensured, Lease: lease}); err != nil {
		t.Fatalf("EnsureChange(idempotent) error = %v", err)
	}
	if got := fixture.countExactCalls("POST /repos/acme/oro/pulls"); got != 1 {
		t.Fatalf("create pull request calls = %d, want 1", got)
	}

	observation, err := client.Observe(context.Background(), remotegate.ObserveGateRequest{
		Change: ensured.Change, Candidate: candidate, Target: target,
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if !observation.Terminal || !observation.Passed || observation.Evidence.CandidateSHA != candidate.SHA ||
		observation.Evidence.Target != target || observation.Evidence.TestedTreeSHA != candidate.TreeSHA {
		t.Fatalf("Observe() = %#v, want exact passing checks", observation)
	}

	if err := os.WriteFile(fixture.mismatch, []byte("mismatch"), 0o600); err != nil {
		t.Fatalf("write mismatch marker: %v", err)
	}
	mismatch, err := client.Observe(context.Background(), remotegate.ObserveGateRequest{
		Change: ensured.Change, Candidate: candidate, Target: target,
	})
	if err != nil {
		t.Fatalf("Observe(mismatch) error = %v", err)
	}
	if !mismatch.Terminal || mismatch.Passed {
		t.Fatalf("Observe(mismatch) = %#v, want normalized terminal recovery", mismatch)
	}
	if err := os.Remove(fixture.mismatch); err != nil {
		t.Fatalf("remove mismatch marker: %v", err)
	}

	ready, err := client.SetChangeReady(context.Background(), remotegate.ChangeReadyRequest{
		Change: ensured, Evidence: observation.Evidence, Lease: lease,
	})
	if err != nil {
		t.Fatalf("SetChangeReady() error = %v", err)
	}
	if ready.Change.Draft {
		t.Fatalf("SetChangeReady() = %#v, want non-draft", ready)
	}
	if _, err := client.SetChangeReady(context.Background(), remotegate.ChangeReadyRequest{
		Change: ready, Evidence: readyEvidence(observation.Evidence, ready.Change), Lease: lease,
	}); err != nil {
		t.Fatalf("SetChangeReady(idempotent) error = %v", err)
	}
	if got := fixture.countExactCalls("POST /graphql"); got != 1 {
		t.Fatalf("ready calls = %d, want 1", got)
	}
	graphqlBody, err := os.ReadFile(fixture.graphqlBody)
	if err != nil {
		t.Fatalf("read GraphQL ready body: %v", err)
	}
	for _, want := range []string{"markPullRequestReadyForReview", `"pullRequestId":"PR_node_7"`} {
		if !strings.Contains(string(graphqlBody), want) {
			t.Errorf("GraphQL ready body = %s, want %q", graphqlBody, want)
		}
	}

	cancel := remotegate.CancelGateRequest{Change: ready, Reason: "superseded", Lease: lease}
	if err := client.Cancel(context.Background(), cancel); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if err := client.Cancel(context.Background(), cancel); err != nil {
		t.Fatalf("Cancel(idempotent) error = %v", err)
	}
	if got := fixture.countCalls("PATCH"); got != 1 {
		t.Fatalf("close calls = %d, want 1", got)
	}

	reconcile := reconciliationRequest(ready, readyEvidence(observation.Evidence, ready.Change), lease)
	if err := client.Reconcile(context.Background(), reconcile); err != nil {
		t.Fatalf("Reconcile(closed) error = %v", err)
	}
}

func TestNewClientRejectsCredentialScopeMismatchBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		clientCred func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider
		gitCred    func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider
	}{
		{name: "zero provider", clientCred: func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider {
			return remotegate.RuntimeCredentialProvider{}
		}},
		{name: "foreign repository", clientCred: func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider {
			target := lifecycleCredentialTarget()
			target.Name = "other"
			return runtimeProviderForTarget(target)
		}},
		{name: "wrong app and installation", clientCred: func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider {
			target := lifecycleCredentialTarget()
			target.Identity.AppID++
			target.Identity.InstallationID++
			return runtimeProviderForTarget(target)
		}},
		{name: "split API provider", clientCred: func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider {
			return runtimeProviderForTarget(lifecycleCredentialTarget())
		}},
		{name: "split Git provider", gitCred: func(remotegate.RuntimeCredentialProvider) remotegate.RuntimeCredentialProvider {
			return runtimeProviderForTarget(lifecycleCredentialTarget())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newChangeLifecycleFixture(t)
			clientCredentials := fixture.credentials
			gitCredentials := fixture.credentials
			if test.clientCred != nil {
				clientCredentials = test.clientCred(fixture.credentials)
			}
			if test.gitCred != nil {
				gitCredentials = test.gitCred(fixture.credentials)
			}
			fixture.git.credentials = gitCredentials
			_, err := NewClient(Config{
				Repository:     remotegate.Repository{Host: "github.example", Owner: "acme", Name: "oro"},
				RequiredChecks: []string{"quality-gate"},
			}, fixture.runner, &fixture.git, clientCredentials)
			if !errors.Is(err, remotegate.ErrConfig) {
				t.Fatalf("NewClient() error = %v, want ErrConfig", err)
			}
			if len(fixture.git.requests) != 0 || fixture.countCalls("") != 0 {
				t.Fatalf("constructor mismatch performed mutation: pushes=%d calls=%d", len(fixture.git.requests), fixture.countCalls(""))
			}
		})
	}
}

func readyEvidence(evidence remotegate.Evidence, change remotegate.Change) remotegate.Evidence {
	evidence.Change = change
	return evidence
}

func reconciliationRequest(change remotegate.RemoteChange, evidence remotegate.Evidence, lease remotegate.Lease) remotegate.ReconcileChangeRequest {
	workflow := remotegate.WorkflowEvidence{
		Path: ".github/workflows/quality.yml", State: "active", Ref: "refs/heads/main",
		WorkflowDispatch: true, PullRequestTargets: []string{"main"},
	}
	run := remotegate.RunEvidence{
		Change: change.Change, CandidateSHA: change.Change.Candidate.SHA, Target: change.Change.Target,
		TestedTreeSHA: change.Change.Candidate.TreeSHA, Workflow: workflow, RunID: "run-1", PolicyHash: evidence.PolicyHash,
		Checks: []remotegate.CheckEvidence{{ID: "check-1", Name: "quality-gate", Conclusion: "success"}},
		Pages:  []remotegate.PageEvidence{{Number: 1, Complete: true}}, ExpectedPages: []int{1},
		ExpectedWorkflowPath: workflow.Path, ExpectedWorkflowRef: workflow.Ref,
		ExpectedRunID: "run-1", ExpectedCheckIDs: []string{"check-1"},
	}
	return remotegate.ReconcileChangeRequest{
		Change: change, Evidence: evidence, Run: run,
		AttemptedOperation: "cancel", AttemptID: "attempt-1",
		ObservedOperation: "cancel", ObservedAttemptID: "attempt-1", ObservedOutcome: "accepted",
		Lease: lease,
	}
}

type recordingGitTransport struct {
	credentials remotegate.RuntimeCredentialProvider
	requests    []remotegate.GitPushRequest
}

func (transport *recordingGitTransport) Push(_ context.Context, request remotegate.GitPushRequest) error {
	transport.requests = append(transport.requests, request)
	return nil
}

func (transport *recordingGitTransport) RuntimeCredentialProvider() remotegate.RuntimeCredentialProvider {
	return transport.credentials
}

type changeLifecycleFixture struct {
	runner      *GHRunner
	credentials remotegate.RuntimeCredentialProvider
	git         recordingGitTransport
	calls       string
	graphqlBody string
	mismatch    string
}

func newChangeLifecycleFixture(t *testing.T) changeLifecycleFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	calls := filepath.Join(dir, "calls")
	created := filepath.Join(dir, "created")
	ready := filepath.Join(dir, "ready")
	closed := filepath.Join(dir, "closed")
	graphqlBody := filepath.Join(dir, "graphql-body")
	readyLost := filepath.Join(dir, "ready-lost")
	mismatch := filepath.Join(dir, "mismatch")
	script := fmt.Sprintf(`#!/bin/sh
method=
last=
previous=
for argument do
  if [ "$previous" = "--method" ]; then method="$argument"; fi
  previous="$argument"
  last="$argument"
done
printf '%%s %%s\n' "$method" "$last" >> %q
emit_pr() {
  draft=true
  state=open
  head_sha=%s
  [ -f %q ] && draft=false
  [ -f %q ] && state=closed
  [ -f %q ] && head_sha=%s
  printf '{"number":7,"node_id":"PR_node_7","html_url":"https://github.example/acme/oro/pull/7","state":"%%s","draft":%%s,"head":{"ref":"agent/oro-83st","sha":"%%s","repo":{"full_name":"acme/oro"}},"base":{"ref":"main","sha":"%s","repo":{"full_name":"acme/oro"}}}' "$state" "$draft" "$head_sha"
}
case "$last" in
  *"/check-runs") printf '[{"check_runs":[{"node_id":"check-1","name":"quality-gate","status":"completed","conclusion":"success"}]}]' ;;
  "/graphql")
	cat > %q
	touch %q
	if [ ! -f %q ]; then touch %q; exit 1; fi
	printf '{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_node_7","isDraft":false}}}}'
	;;
  *"/pulls?"*) if [ -f %q ]; then printf '['; emit_pr; printf ']'; else printf '[]'; fi ;;
  *"/pulls")
    touch %q
    emit_pr
    if [ ! -f %q ]; then touch %q; exit 1; fi
    ;;
  *"/pulls/7")
    if [ "$method" = "PATCH" ]; then touch %q; fi
    emit_pr
    ;;
  *) exit 2 ;;
esac
`, calls, strings.Repeat("1", 40), ready, closed, mismatch, strings.Repeat("9", 40),
		strings.Repeat("3", 40), graphqlBody, ready, readyLost, readyLost, created, created, filepath.Join(dir, "lost"), filepath.Join(dir, "lost"), closed)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write lifecycle gh fixture: %v", err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize lifecycle gh fixture: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lifecycle gh fixture: %v", err)
	}
	digest := sha256.Sum256(contents)
	credentials := lifecycleCredentials()
	runner, err := NewGHRunner(
		AttestedCLI{Path: path, Hash: hex.EncodeToString(digest[:])},
		credentials,
		GHRunnerConfig{Host: "github.example"},
	)
	if err != nil {
		t.Fatalf("NewGHRunner() error = %v", err)
	}
	return changeLifecycleFixture{runner: runner, credentials: credentials, git: recordingGitTransport{credentials: credentials}, calls: calls, graphqlBody: graphqlBody, mismatch: mismatch}
}

func (fixture changeLifecycleFixture) countCalls(fragment string) int {
	contents, err := os.ReadFile(fixture.calls)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.Contains(line, fragment) {
			count++
		}
	}
	return count
}

func (fixture changeLifecycleFixture) countExactCalls(want string) int {
	contents, err := os.ReadFile(fixture.calls)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(contents), "\n") {
		if line == want {
			count++
		}
	}
	return count
}

type lifecycleCredentialSource struct {
	target remotegate.CredentialTarget
}

func (source lifecycleCredentialSource) Resolve(_ context.Context, request remotegate.CredentialRequest) (remotegate.Credential, error) {
	return remotegate.Credential{
		Token: "runtime-token", Role: request.Role,
		AppID: source.target.Identity.AppID, InstallationID: source.target.Identity.InstallationID,
		Host: source.target.Host, Owner: source.target.Owner, Name: source.target.Name,
		Permissions: request.Permissions, ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func lifecycleCredentials() remotegate.RuntimeCredentialProvider {
	return runtimeProviderForTarget(lifecycleCredentialTarget())
}

func lifecycleCredentialTarget() remotegate.CredentialTarget {
	return remotegate.CredentialTarget{
		Identity: config.GitHubAppIdentityConfig{
			Type: "github-app", AppID: 1, InstallationID: 2, PrivateKeyRef: "keychain:oro/test",
		},
		Host: "github.example", Owner: "acme", Name: "oro",
	}

}

func runtimeProviderForTarget(target remotegate.CredentialTarget) remotegate.RuntimeCredentialProvider {
	return remotegate.NewRuntimeCredentialProvider(target, lifecycleCredentialSource{target: target})
}
