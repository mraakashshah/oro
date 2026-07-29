package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
)

func TestRemoteGateConfigModes(t *testing.T) {
	t.Run("local remains the default", func(t *testing.T) {
		path := writeRemoteGateConfig(t, "project: example\n")

		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Factory.QualityGate.Mode != config.RemoteGateModeLocal {
			t.Errorf("quality gate mode = %q, want %q", cfg.Factory.QualityGate.Mode, config.RemoteGateModeLocal)
		}
	})

	t.Run("github PR parses typed configuration", func(t *testing.T) {
		path := writeRemoteGateConfig(t, `factory:
  lifecycle:
    auto_install_after_epic: true
    supervisor: managed-monitor
  quality_gate:
    mode: github-pr
    github:
      remote: origin
      workflow: ci.yml
      aggregate_check: oro-portable-qg
      max_in_flight: 3
      poll_min_interval: 5s
      poll_max_interval: 60s
      run_timeout: 35m
      outage_fallback_after: 15m
      close_superseded_prs: true
      cli:
        executable: managed
        install_if_missing: true
      api:
        base_url: https://api.github.com
        ca_bundle_ref: system
        proxy: none
        api_version: "2022-11-28"
      runtime_identity:
        type: github-app
        app_id: 123456
        installation_id: 789012
        private_key_ref: keychain:oro/github-app
      policy_reconciliation:
        enabled: true
        owned_ruleset_key: oro-target-policy
        owned_ruleset_name: oro:project-identity:target-policy
        desired_template_hash: sha256:template
        maintenance_identity:
          type: github-app
          app_id: 654321
          installation_id: 789012
          private_key_ref: keychain:oro/github-maintenance-app
    local:
      profile: memory-safe
      max_actions: 6
`)

		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.Factory.Lifecycle.AutoInstallAfterEpic || cfg.Factory.Lifecycle.Supervisor != "managed-monitor" {
			t.Errorf("lifecycle = %+v, want typed lifecycle settings", cfg.Factory.Lifecycle)
		}
		github := cfg.Factory.QualityGate.GitHub
		if cfg.Factory.QualityGate.Mode != config.RemoteGateModeGitHubPR || github.Remote != "origin" || github.Workflow != "ci.yml" || github.AggregateCheck != "oro-portable-qg" {
			t.Errorf("github remote gate = %+v, want mode, remote, workflow, and aggregate check", github)
		}
		if github.CLI.Executable != "managed" || !github.CLI.InstallIfMissing || github.API.BaseURL != "https://api.github.com" || github.API.APIVersion != "2022-11-28" {
			t.Errorf("github cli/api = %+v / %+v, want typed values", github.CLI, github.API)
		}
		if github.RuntimeIdentity.Type != "github-app" || github.RuntimeIdentity.AppID != 123456 || github.RuntimeIdentity.PrivateKeyRef != "keychain:oro/github-app" {
			t.Errorf("runtime identity = %+v, want typed identity", github.RuntimeIdentity)
		}
		if !github.PolicyReconciliation.Enabled || github.PolicyReconciliation.MaintenanceIdentity.AppID != 654321 {
			t.Errorf("policy reconciliation = %+v, want typed policy identity", github.PolicyReconciliation)
		}
		if github.PollMinInterval != 5*time.Second || github.PollMaxInterval != time.Minute || github.RunTimeout != 35*time.Minute || github.OutageFallbackAfter != 15*time.Minute {
			t.Errorf("polling/fallback = %+v, want configured durations", github)
		}
		if cfg.Factory.QualityGate.Local.Profile != "memory-safe" || cfg.Factory.QualityGate.Local.MaxActions != 6 {
			t.Errorf("local fallback = %+v, want typed local fallback", cfg.Factory.QualityGate.Local)
		}
	})

	for _, tt := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "github PR cannot use manual integration",
			content: `factory:
  lifecycle:
    manual_integration: true
  quality_gate:
    mode: github-pr
`,
			want: "manual integration",
		},
		{
			name: "github PR needs remote workflow and aggregate check",
			content: `factory:
  quality_gate:
    mode: github-pr
`,
			want: "remote",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(writeRemoteGateConfig(t, tt.content))
			if err == nil {
				t.Fatal("Load returned nil error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestKeychainPrivateKeyReference(t *testing.T) {
	valid := func(ref string) config.RemoteGateConfig {
		identity := config.GitHubAppIdentityConfig{Type: "github-app", AppID: 1, InstallationID: 2, PrivateKeyRef: ref}
		return config.RemoteGateConfig{
			Mode: config.RemoteGateModeGitHubPR,
			GitHub: config.GitHubRemoteGateConfig{
				Remote: "origin", Workflow: "ci.yml", AggregateCheck: "qg", MaxInFlight: 1,
				PollMinInterval: time.Second, PollMaxInterval: time.Second, RunTimeout: time.Minute,
				OutageFallbackAfter: time.Minute, CLI: config.GitHubCLIConfig{Executable: "gh"},
				API: config.GitHubAPIConfig{BaseURL: "https://api.github.com"}, RuntimeIdentity: identity,
				PolicyReconciliation: config.PolicyReconciliationConfig{Enabled: true, OwnedRulesetKey: "key", OwnedRulesetName: "name", DesiredTemplateHash: "hash", MaintenanceIdentity: identity},
			},
		}
	}

	for _, ref := range []string{"keychain:team:oro", "keychain:oro/github-app"} {
		if err := config.ValidateRemoteGateConfig(valid(ref)); err != nil {
			t.Errorf("reference %q rejected: %v", ref, err)
		}
	}
	for _, ref := range []string{"keychain:", "/tmp/key", "$ORO_KEY", " keychain:oro", "keychain:oro\n"} {
		runtimeErr := config.ValidateRemoteGateConfig(valid("keychain:runtime"))
		if runtimeErr != nil {
			t.Fatalf("valid runtime identity: %v", runtimeErr)
		}
		runtime := valid("keychain:runtime")
		runtime.GitHub.RuntimeIdentity.PrivateKeyRef = ref
		wantRuntime := "invalid github-pr remote gate config: runtime_identity.private_key_ref must be a nonempty keychain reference"
		if err := config.ValidateRemoteGateConfig(runtime); err == nil || err.Error() != wantRuntime {
			t.Errorf("runtime reference %q error = %v, want %q", ref, err, wantRuntime)
		}

		maintenance := valid("keychain:runtime")
		maintenance.GitHub.PolicyReconciliation.MaintenanceIdentity.PrivateKeyRef = ref
		wantMaintenance := "invalid github-pr remote gate config: policy_reconciliation.maintenance_identity.private_key_ref must be a nonempty keychain reference"
		if err := config.ValidateRemoteGateConfig(maintenance); err == nil || err.Error() != wantMaintenance {
			t.Errorf("maintenance reference %q error = %v, want %q", ref, err, wantMaintenance)
		}
	}
}

func writeRemoteGateConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
