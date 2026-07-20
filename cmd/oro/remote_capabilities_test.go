package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/config"
)

func TestRemoteCapabilitiesAttestation(t *testing.T) {
	binDir := t.TempDir()
	execDir := filepath.Join(binDir, "git-exec")
	if err := os.Mkdir(execDir, 0o750); err != nil {
		t.Fatalf("make git exec directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-remote-https"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$1 $2 $3" in
  "remote get-url origin") printf '%s\n' 'https://github.example/acme/oro.git' ;;
  "--version  ") printf '%s\n' 'git version 2.47.0' ;;
  "--exec-path  ") printf '%s\n' "$FAKE_GIT_EXEC_PATH" ;;
  "config --get-all credential.helper") printf '%s\n' 'osxkeychain' ;;
  *) exit 1 ;;
esac
`)
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "gh"), `#!/bin/sh
case "$*" in
  "--version") printf '%s\n' 'gh version 2.63.0 (2025-01-01)' ;;
  "api --hostname github.example repos/acme/oro") printf '%s\n' '{"full_name":"acme/oro","permissions":{"push":true}}' ;;
  "api --hostname github.example rate_limit") printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999},"actions_runner_registration":{"limit":1000,"remaining":999}}}' ;;
  "api --hostname github.example repos/acme/oro/actions/workflows/ci.yml") printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active","matrix_entries_limit":256}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", binDir)
	t.Setenv("FAKE_GIT_EXEC_PATH", execDir)

	caps, err := AttestRemoteCapabilities(context.Background(), config.RemoteGateConfig{
		Mode: config.RemoteGateModeGitHubPR,
		GitHub: config.GitHubRemoteGateConfig{
			Remote:      "origin",
			Workflow:    "ci.yml",
			MaxInFlight: 4,
			CLI:         config.GitHubCLIConfig{Executable: "gh"},
			API:         config.GitHubAPIConfig{BaseURL: "https://github.example/api/v3"},
		},
	})
	if err != nil {
		t.Fatalf("AttestRemoteCapabilities() error = %v", err)
	}
	if caps.Host != "github.example" || caps.Repository != "acme/oro" || caps.Workflow != "ci.yml" || !caps.Permission.Push {
		t.Fatalf("repository capability = %+v, want host, repository, workflow, and push permission", caps)
	}
	wantGHPath, err := filepath.EvalSymlinks(filepath.Join(binDir, "gh"))
	if err != nil {
		t.Fatalf("canonicalize fake gh path: %v", err)
	}
	if caps.GitHubCLI.Path != wantGHPath || caps.GitHubCLI.Version != "2.63.0" || caps.GitHubCLI.Provenance == "" || caps.GitHubCLI.Hash == "" {
		t.Fatalf("GitHub CLI evidence = %+v, want path, version, provenance, and hash", caps.GitHubCLI)
	}
	if len(caps.Git.CredentialHelpers) != 1 || caps.Git.CredentialHelpers[0] != "osxkeychain" || caps.Git.RemoteHTTPSHelper.Hash == "" {
		t.Fatalf("Git capability = %+v, want credential helper identity and helper hash", caps.Git)
	}
	if caps.APILimits.Core.Limit != 5000 || caps.APILimits.ActionsRunnerRegistration.Remaining != 999 || caps.MatrixBound != 256 {
		t.Fatalf("API and matrix capability = %+v, want decoded limits and bound", caps)
	}

	evidencePath := filepath.Join(t.TempDir(), "remote-capabilities.json")
	if err := PersistRemoteCapabilities(evidencePath, caps); err != nil {
		t.Fatalf("PersistRemoteCapabilities() error = %v", err)
	}
	if err := VerifyRemoteCapabilities(context.Background(), config.RemoteGateConfig{
		Mode: config.RemoteGateModeGitHubPR,
		GitHub: config.GitHubRemoteGateConfig{
			Remote:      "origin",
			Workflow:    "ci.yml",
			MaxInFlight: 4,
			CLI:         config.GitHubCLIConfig{Executable: "gh"},
			API:         config.GitHubAPIConfig{BaseURL: "https://github.example/api/v3"},
		},
	}, evidencePath); err != nil {
		t.Fatalf("VerifyRemoteCapabilities() error = %v", err)
	}
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("make project config directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(projectRoot, ".oro", "config.yaml"), remoteCapabilityConfigYAML)
	if err := persistSetupRemoteCapabilities(context.Background(), projectRoot); err != nil {
		t.Fatalf("persistSetupRemoteCapabilities() error = %v", err)
	}
	if _, err := os.Stat(remoteCapabilityEvidencePath(projectRoot)); err != nil {
		t.Fatalf("setup did not persist capability evidence: %v", err)
	}
	if err := verifyStartupRemoteCapabilities(context.Background(), projectRoot); err != nil {
		t.Fatalf("verifyStartupRemoteCapabilities() error = %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "gh"), "#!/bin/sh\nexit 1\n")
	if err := VerifyRemoteCapabilities(context.Background(), config.RemoteGateConfig{
		Mode: config.RemoteGateModeGitHubPR,
		GitHub: config.GitHubRemoteGateConfig{
			Remote:      "origin",
			Workflow:    "ci.yml",
			MaxInFlight: 4,
			CLI:         config.GitHubCLIConfig{Executable: "gh"},
			API:         config.GitHubAPIConfig{BaseURL: "https://github.example/api/v3"},
		},
	}, evidencePath); err == nil {
		t.Fatal("VerifyRemoteCapabilities() accepted changed gh executable")
	}
	if err := verifyStartupRemoteCapabilities(context.Background(), projectRoot); err == nil {
		t.Fatal("startup capability preflight accepted changed gh executable")
	}
}

const remoteCapabilityConfigYAML = `factory:
  quality_gate:
    mode: github-pr
    github:
      remote: origin
      workflow: ci.yml
      aggregate_check: aggregate
      max_in_flight: 4
      poll_min_interval: 1s
      poll_max_interval: 2s
      run_timeout: 1m
      outage_fallback_after: 1m
      cli:
        executable: gh
      api:
        base_url: https://github.example/api/v3
      runtime_identity:
        type: github-app
        app_id: 1
        installation_id: 2
        private_key_ref: runtime-key
      policy_reconciliation:
        enabled: true
        owned_ruleset_key: oro
        owned_ruleset_name: Oro
        desired_template_hash: abc
        maintenance_identity:
          type: github-app
          app_id: 3
          installation_id: 4
          private_key_ref: maintenance-key
`

func writeRemoteCapabilityFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o750); err != nil { //nolint:gosec // test fixture executable
		t.Fatalf("write fixture %s: %v", path, err)
	}
}
