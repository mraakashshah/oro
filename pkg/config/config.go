package config

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Config is Oro's project configuration. It remains an alias for AgentConfig
// while agent configuration is the only existing Load return type.
type Config = AgentConfig

// FactoryConfig configures the project factory lifecycle and quality gate.
type FactoryConfig struct {
	Lifecycle   FactoryLifecycleConfig `yaml:"lifecycle,omitempty"`
	QualityGate RemoteGateConfig       `yaml:"quality_gate,omitempty"`
}

// FactoryLifecycleConfig controls dispatcher lifecycle behavior.
type FactoryLifecycleConfig struct {
	AutoInstallAfterEpic bool   `yaml:"auto_install_after_epic,omitempty"`
	Supervisor           string `yaml:"supervisor,omitempty"`
	ManualIntegration    bool   `yaml:"manual_integration,omitempty"`
}

// RemoteGateMode selects the project quality-gate implementation.
type RemoteGateMode string

const (
	// RemoteGateModeLocal keeps quality-gate execution local to the project.
	RemoteGateModeLocal RemoteGateMode = "local"
	// RemoteGateModeGitHubPR delegates quality-gate execution to a GitHub pull request.
	RemoteGateModeGitHubPR RemoteGateMode = "github-pr"
)

// RemoteGateConfig contains local and GitHub PR quality-gate settings.
type RemoteGateConfig struct {
	Mode   RemoteGateMode         `yaml:"mode,omitempty"`
	GitHub GitHubRemoteGateConfig `yaml:"github,omitempty"`
	Local  LocalRemoteGateConfig  `yaml:"local,omitempty"`
}

// GitHubRemoteGateConfig configures the dispatcher-owned GitHub PR gate.
type GitHubRemoteGateConfig struct {
	Remote               string                     `yaml:"remote,omitempty"`
	Workflow             string                     `yaml:"workflow,omitempty"`
	AggregateCheck       string                     `yaml:"aggregate_check,omitempty"`
	MaxInFlight          int                        `yaml:"max_in_flight,omitempty"`
	PollMinInterval      time.Duration              `yaml:"poll_min_interval,omitempty"`
	PollMaxInterval      time.Duration              `yaml:"poll_max_interval,omitempty"`
	RunTimeout           time.Duration              `yaml:"run_timeout,omitempty"`
	OutageFallbackAfter  time.Duration              `yaml:"outage_fallback_after,omitempty"`
	CloseSupersededPRs   bool                       `yaml:"close_superseded_prs,omitempty"`
	CLI                  GitHubCLIConfig            `yaml:"cli,omitempty"`
	API                  GitHubAPIConfig            `yaml:"api,omitempty"`
	RuntimeIdentity      GitHubAppIdentityConfig    `yaml:"runtime_identity,omitempty"`
	PolicyReconciliation PolicyReconciliationConfig `yaml:"policy_reconciliation,omitempty"`
}

// GitHubCLIConfig specifies the managed GitHub CLI dependency.
type GitHubCLIConfig struct {
	Executable       string `yaml:"executable,omitempty"`
	InstallIfMissing bool   `yaml:"install_if_missing,omitempty"`
}

// GitHubAPIConfig specifies the GitHub API connection contract.
type GitHubAPIConfig struct {
	BaseURL     string `yaml:"base_url,omitempty"`
	CABundleRef string `yaml:"ca_bundle_ref,omitempty"`
	Proxy       string `yaml:"proxy,omitempty"`
	APIVersion  string `yaml:"api_version,omitempty"`
}

// GitHubAppIdentityConfig identifies a GitHub App credential reference.
type GitHubAppIdentityConfig struct {
	Type           string `yaml:"type,omitempty"`
	AppID          int64  `yaml:"app_id,omitempty"`
	InstallationID int64  `yaml:"installation_id,omitempty"`
	PrivateKeyRef  string `yaml:"private_key_ref,omitempty"`
}

// PolicyReconciliationConfig specifies the one policy owned by Oro.
type PolicyReconciliationConfig struct {
	Enabled             bool                    `yaml:"enabled,omitempty"`
	OwnedRulesetKey     string                  `yaml:"owned_ruleset_key,omitempty"`
	OwnedRulesetName    string                  `yaml:"owned_ruleset_name,omitempty"`
	DesiredTemplateHash string                  `yaml:"desired_template_hash,omitempty"`
	MaintenanceIdentity GitHubAppIdentityConfig `yaml:"maintenance_identity,omitempty"`
}

// LocalRemoteGateConfig controls the local fallback quality gate.
type LocalRemoteGateConfig struct {
	Profile    string `yaml:"profile,omitempty"`
	MaxActions int    `yaml:"max_actions,omitempty"`
}

func defaultFactoryConfig() FactoryConfig {
	return FactoryConfig{QualityGate: RemoteGateConfig{Mode: RemoteGateModeLocal}}
}

// ValidateRemoteGateConfig rejects incomplete or incompatible explicit remote
// quality-gate configuration. An omitted mode uses the safe local default.
func ValidateRemoteGateConfig(cfg RemoteGateConfig) error {
	if cfg.Mode == "" || cfg.Mode == RemoteGateModeLocal {
		return nil
	}
	if cfg.Mode != RemoteGateModeGitHubPR {
		return fmt.Errorf("invalid remote gate mode %q", cfg.Mode)
	}

	github := cfg.GitHub
	var invalid []string
	requireRemoteGateField(&invalid, "remote", github.Remote)
	requireRemoteGateField(&invalid, "workflow", github.Workflow)
	requireRemoteGateField(&invalid, "aggregate_check", github.AggregateCheck)
	requireRemoteGateField(&invalid, "cli.executable", github.CLI.Executable)
	requireRemoteGateField(&invalid, "api.base_url", github.API.BaseURL)
	validateGitHubAppIdentity(&invalid, "runtime_identity", github.RuntimeIdentity)
	validatePolicyReconciliation(&invalid, github.PolicyReconciliation)
	validateRemoteGateTiming(&invalid, github)
	if len(invalid) != 0 {
		return fmt.Errorf("invalid github-pr remote gate config: %s", strings.Join(invalid, "; "))
	}
	return nil
}

func requireRemoteGateField(invalid *[]string, name, value string) {
	if strings.TrimSpace(value) == "" {
		*invalid = append(*invalid, name+" is required")
	}
}

func validateGitHubAppIdentity(invalid *[]string, name string, identity GitHubAppIdentityConfig) {
	if identity.Type != "github-app" {
		*invalid = append(*invalid, name+".type must be github-app")
	}
	if identity.AppID <= 0 {
		*invalid = append(*invalid, name+".app_id is required")
	}
	if identity.InstallationID <= 0 {
		*invalid = append(*invalid, name+".installation_id is required")
	}
	if !validKeychainPrivateKeyReference(identity.PrivateKeyRef) {
		*invalid = append(*invalid, name+".private_key_ref must be a nonempty keychain reference")
	}
}

func validKeychainPrivateKeyReference(ref string) bool {
	const scheme = "keychain:"
	if !strings.HasPrefix(ref, scheme) || len(ref) == len(scheme) {
		return false
	}
	for _, char := range ref[len(scheme):] {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validatePolicyReconciliation(invalid *[]string, policy PolicyReconciliationConfig) {
	if !policy.Enabled {
		*invalid = append(*invalid, "policy_reconciliation.enabled must be true")
	}
	requireRemoteGateField(invalid, "policy_reconciliation.owned_ruleset_key", policy.OwnedRulesetKey)
	requireRemoteGateField(invalid, "policy_reconciliation.owned_ruleset_name", policy.OwnedRulesetName)
	requireRemoteGateField(invalid, "policy_reconciliation.desired_template_hash", policy.DesiredTemplateHash)
	validateGitHubAppIdentity(invalid, "policy_reconciliation.maintenance_identity", policy.MaintenanceIdentity)
}

func validateRemoteGateTiming(invalid *[]string, github GitHubRemoteGateConfig) {
	if github.MaxInFlight <= 0 {
		*invalid = append(*invalid, "max_in_flight must be positive")
	}
	if github.PollMinInterval <= 0 {
		*invalid = append(*invalid, "poll_min_interval must be positive")
	}
	if github.PollMaxInterval < github.PollMinInterval {
		*invalid = append(*invalid, "poll_max_interval must be at least poll_min_interval")
	}
	if github.RunTimeout <= 0 {
		*invalid = append(*invalid, "run_timeout must be positive")
	}
	if github.OutageFallbackAfter <= 0 {
		*invalid = append(*invalid, "outage_fallback_after must be positive")
	}
}
