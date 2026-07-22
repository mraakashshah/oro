package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
	"oro/pkg/remotegate"

	"github.com/spf13/cobra"
)

func TestRemoteCapabilitiesPersistTargetPolicyEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capabilities.json")
	capabilities := Capabilities{
		DefaultBranch: "main",
		WorkflowEvidence: WorkflowEvidence{
			Path:             ".github/workflows/remote-gate.yml",
			State:            "active",
			Ref:              "main",
			WorkflowDispatch: true,
		},
		ApplicableRules: []remotegate.ApplicableRule{{
			Source:         "repository",
			ID:             "protect-main",
			Version:        "1",
			Pattern:        "main",
			Enforcement:    "active",
			Operations:     []string{"create", "update"},
			BypassActors:   []string{"release-bot"},
			RequiredChecks: []string{"unit", "lint"},
		}},
		EffectivePolicyHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	if err := PersistRemoteCapabilities(path, capabilities); err != nil {
		t.Fatalf("PersistRemoteCapabilities() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted capabilities: %v", err)
	}
	var persisted Capabilities
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode persisted capabilities: %v", err)
	}
	if persisted.DefaultBranch != "main" || persisted.WorkflowEvidence != capabilities.WorkflowEvidence {
		t.Fatalf("persisted target evidence = %+v, want %+v", persisted, capabilities)
	}
	if !reflect.DeepEqual(persisted.ApplicableRules, capabilities.ApplicableRules) {
		t.Fatalf("persisted applicable rules = %+v, want %+v", persisted.ApplicableRules, capabilities.ApplicableRules)
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(persisted.EffectivePolicyHash) {
		t.Fatalf("persisted EffectivePolicyHash = %q, want 64-character lowercase SHA-256 hex", persisted.EffectivePolicyHash)
	}

	for _, mutation := range capabilityTargetPolicyMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			current := cloneTargetPolicyCapabilities(capabilities)
			mutation.mutate(&current)
			if !remoteCapabilitiesDrifted(capabilities, current) {
				t.Fatal("remoteCapabilitiesDrifted() = false, want true")
			}
		})
	}

	t.Run("canonical empty applicable rules", func(t *testing.T) {
		emptyPath := filepath.Join(t.TempDir(), "empty-capabilities.json")
		empty := Capabilities{}
		if err := PersistRemoteCapabilities(emptyPath, empty); err != nil {
			t.Fatalf("PersistRemoteCapabilities() error = %v", err)
		}
		data, err := os.ReadFile(emptyPath)
		if err != nil {
			t.Fatalf("read persisted empty capabilities: %v", err)
		}
		var raw struct {
			ApplicableRules json.RawMessage `json:"applicable_rules"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode persisted empty capabilities: %v", err)
		}
		if string(raw.ApplicableRules) != "[]" {
			t.Fatalf("persisted ApplicableRules = %s, want []", raw.ApplicableRules)
		}
		if remoteCapabilitiesDrifted(Capabilities{}, Capabilities{ApplicableRules: []remotegate.ApplicableRule{}}) {
			t.Fatal("nil and empty ApplicableRules caused capability drift")
		}
	})
}

func cloneTargetPolicyCapabilities(capabilities Capabilities) Capabilities {
	cloned := capabilities
	cloned.ApplicableRules = append([]remotegate.ApplicableRule(nil), capabilities.ApplicableRules...)
	cloned.ApplicableRules[0].Operations = append([]string(nil), capabilities.ApplicableRules[0].Operations...)
	cloned.ApplicableRules[0].BypassActors = append([]string(nil), capabilities.ApplicableRules[0].BypassActors...)
	cloned.ApplicableRules[0].RequiredChecks = append([]string(nil), capabilities.ApplicableRules[0].RequiredChecks...)
	return cloned
}

func capabilityTargetPolicyMutations() []struct {
	name   string
	mutate func(*Capabilities)
} {
	return []struct {
		name   string
		mutate func(*Capabilities)
	}{
		{name: "default branch", mutate: func(capabilities *Capabilities) { capabilities.DefaultBranch = "release" }},
		{name: "workflow path", mutate: func(capabilities *Capabilities) { capabilities.WorkflowEvidence.Path = "other.yml" }},
		{name: "workflow state", mutate: func(capabilities *Capabilities) { capabilities.WorkflowEvidence.State = "disabled" }},
		{name: "workflow ref", mutate: func(capabilities *Capabilities) { capabilities.WorkflowEvidence.Ref = "release" }},
		{name: "workflow dispatch", mutate: func(capabilities *Capabilities) { capabilities.WorkflowEvidence.WorkflowDispatch = false }},
		{name: "rule source", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].Source = "organization" }},
		{name: "rule ID", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].ID = "other" }},
		{name: "rule version", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].Version = "2" }},
		{name: "rule pattern", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].Pattern = "release/**" }},
		{name: "rule enforcement", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].Enforcement = "disabled" }},
		{name: "rule operations", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].Operations[0] = "delete" }},
		{name: "rule bypass actors", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].BypassActors[0] = "other-bot" }},
		{name: "rule required checks", mutate: func(capabilities *Capabilities) { capabilities.ApplicableRules[0].RequiredChecks[0] = "integration" }},
		{name: "effective policy hash", mutate: func(capabilities *Capabilities) { capabilities.EffectivePolicyHash = strings.Repeat("f", 64) }},
	}
}

func TestSetupPersistsRemoteCapabilitiesFromExistingConfig(t *testing.T) {
	projectRoot := t.TempDir()
	oroHome := t.TempDir()
	binDir := t.TempDir()
	execDir := filepath.Join(binDir, "git-exec")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("make project config directory: %v", err)
	}
	if err := os.Mkdir(execDir, 0o750); err != nil {
		t.Fatalf("make git exec directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(projectRoot, ".oro", "config.yaml"), remoteCapabilityConfigYAML)
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-remote-https"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-credential-osxkeychain"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$1 $2 $3" in
  "remote get-url origin") printf '%s\n' 'https://github.com/acme/oro.git' ;;
  "--version  ") printf '%s\n' 'git version 2.47.0' ;;
  "--exec-path  ") printf '%s\n' "${0%/*}/git-exec" ;;
  "config --get-all credential.helper") printf '%s\n' 'osxkeychain' ;;
  *) exit 1 ;;
esac
`)
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "gh"), `#!/bin/sh
case "$*" in
  "--version") printf '%s\n' 'gh version 2.63.0 (2025-01-01)' ;;
  "api --hostname github.com repos/acme/oro") printf '%s\n' '{"full_name":"acme/oro","permissions":{"push":true}}' ;;
  "api --hostname github.com rate_limit") printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999},"actions_runner_registration":{"limit":1000,"remaining":999}}}' ;;
  "api --hostname github.com repos/acme/oro/actions/workflows/ci.yml") printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active"}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("PATH", binDir)

	var output bytes.Buffer
	if err := executeBootstrap(&output, "remote-project", setupOptions{projectRoot: projectRoot}); err != nil {
		t.Fatalf("executeBootstrap() error = %v", err)
	}
	if _, err := os.Stat(remoteCapabilityEvidencePath(projectRoot)); err != nil {
		t.Fatalf("setup did not persist remote capability evidence: %v", err)
	}
	cfg, err := config.Load(filepath.Join(projectRoot, ".oro", "config.yaml"))
	if err != nil {
		t.Fatalf("load config after setup: %v", err)
	}
	if cfg.Factory.QualityGate.Mode != config.RemoteGateModeGitHubPR {
		t.Fatalf("quality gate mode after setup = %q, want %q", cfg.Factory.QualityGate.Mode, config.RemoteGateModeGitHubPR)
	}
	if err := executeBootstrap(&output, "remote-project", setupOptions{projectRoot: projectRoot, force: true}); err != nil {
		t.Fatalf("executeBootstrap(force) error = %v", err)
	}
	cfg, err = config.Load(filepath.Join(projectRoot, ".oro", "config.yaml"))
	if err != nil {
		t.Fatalf("load config after forced setup: %v", err)
	}
	if cfg.Factory.QualityGate.Mode != config.RemoteGateModeGitHubPR {
		t.Fatalf("quality gate mode after forced setup = %q, want %q", cfg.Factory.QualityGate.Mode, config.RemoteGateModeGitHubPR)
	}
}

func TestRemoteCapabilitiesAcceptCanonicalGitHubConfig(t *testing.T) {
	binDir := t.TempDir()
	execDir := filepath.Join(binDir, "git-exec")
	if err := os.Mkdir(execDir, 0o750); err != nil {
		t.Fatalf("make git exec directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-remote-https"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-credential-osxkeychain"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$1 $2 $3" in
  "remote get-url origin") printf '%s\n' 'git@github.com:acme/oro.git' ;;
  "--version  ") printf '%s\n' 'git version 2.47.0' ;;
  "--exec-path  ") printf '%s\n' "${0%/*}/git-exec" ;;
  "config --get-all credential.helper") exit 1 ;;
  "rev-parse --git-dir ") printf '%s\n' '.git' ;;
  *) exit 1 ;;
esac
`)
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "claude"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "tmux"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "gh"), `#!/bin/sh
case "$*" in
  "--version") printf '%s\n' 'gh version 2.63.0 (2025-01-01)' ;;
  "api --hostname github.com repos/acme/oro") printf '%s\n' '{"full_name":"acme/oro","permissions":{"push":true}}' ;;
  "api --hostname github.com rate_limit") printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999},"actions_runner_registration":{"limit":1000,"remaining":999}}}' ;;
  "api --hostname github.com repos/acme/oro/actions/workflows/ci.yml") printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active"}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", binDir)

	caps, err := AttestRemoteCapabilities(context.Background(), config.RemoteGateConfig{
		Mode: config.RemoteGateModeGitHubPR,
		GitHub: config.GitHubRemoteGateConfig{
			Remote:   "origin",
			Workflow: "ci.yml",
			CLI:      config.GitHubCLIConfig{Executable: "managed"},
			API:      config.GitHubAPIConfig{BaseURL: "https://api.github.com"},
		},
	})
	if err != nil {
		t.Fatalf("AttestRemoteCapabilities() error = %v", err)
	}
	if caps.Host != "github.com" || caps.MatrixBound != 256 {
		t.Fatalf("capabilities = %+v, want github.com with matrix bound 256", caps)
	}
}

func TestRemoteCapabilitiesRequireActionsAPILimit(t *testing.T) {
	ghPath := filepath.Join(t.TempDir(), "gh")
	writeRemoteCapabilityFixture(t, ghPath, `#!/bin/sh
printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999}}}'
`)

	if _, err := fetchAPILimits(context.Background(), ghPath, "github.com"); err == nil {
		t.Fatal("fetchAPILimits() accepted a missing actions runner registration limit")
	}
}

func TestRemoteCapabilitiesRejectUnknownMatrixBound(t *testing.T) {
	ghPath := filepath.Join(t.TempDir(), "gh")
	writeRemoteCapabilityFixture(t, ghPath, `#!/bin/sh
printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active"}'
`)

	if _, err := fetchMatrixBound(context.Background(), ghPath, "github.example", "acme/oro", "ci.yml"); err == nil {
		t.Fatal("fetchMatrixBound() accepted an unknown custom-host provider bound")
	}
}

func TestCapabilityCommandUsesMinimalEnvironment(t *testing.T) {
	commandPath := filepath.Join(t.TempDir(), "print-env")
	writeRemoteCapabilityFixture(t, commandPath, "#!/bin/sh\n/usr/bin/env\n")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/poison/path")
	t.Setenv("GH_TOKEN", "ambient-token")
	t.Setenv("GIT_EXEC_PATH", "/poison/git-exec")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/poison/loader.dylib")

	out, err := runCapabilityCommand(context.Background(), commandPath)
	if err != nil {
		t.Fatalf("runCapabilityCommand() error = %v", err)
	}
	environment := string(out)
	for _, forbidden := range []string{"PATH=", "GH_TOKEN=", "GIT_EXEC_PATH=", "DYLD_INSERT_LIBRARIES="} {
		if strings.Contains(environment, forbidden) {
			t.Fatalf("capability command inherited %s in environment:\n%s", forbidden, environment)
		}
	}
	if !strings.Contains(environment, "HOME=") {
		t.Fatalf("capability command environment omitted HOME:\n%s", environment)
	}
}

func TestRemoteCapabilityDriftIgnoresRemainingQuota(t *testing.T) {
	persisted := Capabilities{APILimits: APILimits{
		Core:                      RateLimit{Limit: 5000, Remaining: 4999},
		ActionsRunnerRegistration: RateLimit{Limit: 1000, Remaining: 999},
	}}
	current := persisted
	current.APILimits.Core.Remaining = 4900
	current.APILimits.ActionsRunnerRegistration.Remaining = 900

	if remoteCapabilitiesDrifted(persisted, current) {
		t.Fatal("volatile API quota remaining values caused capability drift")
	}
}

func TestStartRejectsRemoteCapabilityDriftBeforeLaunch(t *testing.T) {
	projectRoot := t.TempDir()
	binDir := t.TempDir()
	execDir := filepath.Join(binDir, "git-exec")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("make project config directory: %v", err)
	}
	if err := os.Mkdir(execDir, 0o750); err != nil {
		t.Fatalf("make git exec directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(projectRoot, ".oro", "config.yaml"), canonicalRemoteCapabilityConfigYAML)
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-remote-https"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$1 $2 $3" in
  "remote get-url origin") printf '%s\n' 'git@github.com:acme/oro.git' ;;
  "--version  ") printf '%s\n' 'git version 2.47.0' ;;
  "--exec-path  ") printf '%s\n' "${0%/*}/git-exec" ;;
  "config --get-all credential.helper") printf '%s\n' '' ;;
  "rev-parse --git-dir ") printf '%s\n' '.git' ;;
  *) exit 1 ;;
esac
`)
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "claude"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "tmux"), "#!/bin/sh\nexit 0\n")
	ghPath := filepath.Join(binDir, "gh")
	writeRemoteCapabilityFixture(t, ghPath, `#!/bin/sh
case "$*" in
  "--version") printf '%s\n' 'gh version 2.63.0 (2025-01-01)' ;;
  "api --hostname github.com repos/acme/oro") printf '%s\n' '{"full_name":"acme/oro","permissions":{"push":true}}' ;;
  "api --hostname github.com rate_limit") printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999},"actions_runner_registration":{"limit":1000,"remaining":999}}}' ;;
  "api --hostname github.com repos/acme/oro/actions/workflows/ci.yml") printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active"}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", binDir)
	cfg, err := config.Load(filepath.Join(projectRoot, ".oro", "config.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	caps, err := AttestRemoteCapabilities(context.Background(), cfg.Factory.QualityGate)
	if err != nil {
		t.Fatalf("initial attestation: %v", err)
	}
	if err := PersistRemoteCapabilities(remoteCapabilityEvidencePath(projectRoot), caps); err != nil {
		t.Fatalf("persist initial attestation: %v", err)
	}
	writeRemoteCapabilityFixture(t, ghPath, "#!/bin/sh\nexit 1\n")

	env := hermeticOroEnv(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(env.OroHome, "hooks"), 0o750); err != nil {
		t.Fatalf("make Oro hooks directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(env.OroHome, "hooks", "oro-search-hook"), "#!/bin/sh\nexit 0\n")
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	t.Setenv(daemonSkipPreflightEnv, "1")
	if !shouldSkipDaemonPreflight(true) {
		t.Fatal("daemon preflight bypass fixture is not active")
	}
	launched := false
	previousRunDaemonOnly := runDaemonOnlyFn
	runDaemonOnlyFn = func(_ *cobra.Command, _ string, _, _ int, _, _, _ time.Duration, _ bool, _ string, _ bool, _ bool, _ string, _ cleanlinessStartConfig) error {
		launched = true
		return nil
	}
	t.Cleanup(func() { runDaemonOnlyFn = previousRunDaemonOnly })

	cmd := newStartCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("daemon-only", "true"); err != nil {
		t.Fatalf("set daemon-only flag: %v", err)
	}
	if err := cmd.Flags().Set("workers", "0"); err != nil {
		t.Fatalf("set workers flag: %v", err)
	}
	withChdir(t, projectRoot, func() {
		err = cmd.RunE(cmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "remote capability startup preflight") {
		t.Fatalf("start error = %v, want remote capability startup preflight rejection", err)
	}
	if launched {
		t.Fatal("start launched dispatcher after remote capability drift")
	}
	if _, statErr := os.Stat(env.PIDPath); !os.IsNotExist(statErr) {
		t.Fatalf("PID path was mutated before drift rejection: %v", statErr)
	}
}

func TestDispatcherStartChecksRemoteCapabilitiesBeforePreflight(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("make project config directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(projectRoot, ".oro", "config.yaml"), `factory:
  quality_gate:
    mode: github-pr
`)
	t.Setenv("PATH", t.TempDir())
	hermeticOroEnv(t, t.TempDir())

	cmd := newDispatcherStartCmd()
	cmd.SetContext(context.Background())
	var err error
	withChdir(t, projectRoot, func() {
		err = cmd.RunE(cmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "invalid github-pr remote gate config") {
		t.Fatalf("dispatcher start error = %v, want remote config rejection before preflight", err)
	}
}

func TestRemoteCapabilitiesAttestation(t *testing.T) {
	binDir := t.TempDir()
	execDir := filepath.Join(binDir, "git-exec")
	if err := os.Mkdir(execDir, 0o750); err != nil {
		t.Fatalf("make git exec directory: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-remote-https"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(execDir, "git-credential-osxkeychain"), "#!/bin/sh\nexit 0\n")
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$1 $2 $3" in
  "remote get-url origin") printf '%s\n' 'https://github.com/acme/oro.git' ;;
  "--version  ") printf '%s\n' 'git version 2.47.0' ;;
  "--exec-path  ") printf '%s\n' "${0%/*}/git-exec" ;;
  "config --get-all credential.helper") printf '%s\n' 'osxkeychain' ;;
  *) exit 1 ;;
esac
`)
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "gh"), `#!/bin/sh
case "$*" in
  "--version") printf '%s\n' 'gh version 2.63.0 (2025-01-01)' ;;
  "api --hostname github.com repos/acme/oro") printf '%s\n' '{"full_name":"acme/oro","permissions":{"push":true}}' ;;
  "api --hostname github.com rate_limit") printf '%s\n' '{"resources":{"core":{"limit":5000,"remaining":4999},"actions_runner_registration":{"limit":1000,"remaining":999}}}' ;;
  "api --hostname github.com repos/acme/oro/actions/workflows/ci.yml") printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active"}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", binDir)

	caps, err := AttestRemoteCapabilities(context.Background(), config.RemoteGateConfig{
		Mode: config.RemoteGateModeGitHubPR,
		GitHub: config.GitHubRemoteGateConfig{
			Remote:      "origin",
			Workflow:    "ci.yml",
			MaxInFlight: 4,
			CLI:         config.GitHubCLIConfig{Executable: "gh"},
			API:         config.GitHubAPIConfig{BaseURL: "https://api.github.com"},
		},
	})
	if err != nil {
		t.Fatalf("AttestRemoteCapabilities() error = %v", err)
	}
	if caps.Host != "github.com" || caps.Repository != "acme/oro" || caps.Workflow != "ci.yml" || !caps.Permission.Push {
		t.Fatalf("repository capability = %+v, want host, repository, workflow, and push permission", caps)
	}
	wantGHPath, err := filepath.EvalSymlinks(filepath.Join(binDir, "gh"))
	if err != nil {
		t.Fatalf("canonicalize fake gh path: %v", err)
	}
	if caps.GitHubCLI.Path != wantGHPath || caps.GitHubCLI.Version != "2.63.0" || caps.GitHubCLI.Provenance == "" || caps.GitHubCLI.Hash == "" {
		t.Fatalf("GitHub CLI evidence = %+v, want path, version, provenance, and hash", caps.GitHubCLI)
	}
	helperEvidence, err := json.Marshal(caps.Git.CredentialHelpers)
	if err != nil {
		t.Fatalf("encode credential helper evidence: %v", err)
	}
	if len(caps.Git.CredentialHelpers) != 1 || !strings.Contains(string(helperEvidence), `"hash"`) || caps.Git.RemoteHTTPSHelper.Hash == "" {
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
			API:         config.GitHubAPIConfig{BaseURL: "https://api.github.com"},
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
			API:         config.GitHubAPIConfig{BaseURL: "https://api.github.com"},
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
        base_url: https://api.github.com
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

const canonicalRemoteCapabilityConfigYAML = `factory:
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
        executable: managed
      api:
        base_url: https://api.github.com
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
