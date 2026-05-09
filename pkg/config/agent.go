package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"oro/pkg/protocol"

	"gopkg.in/yaml.v3"
)

// AgentConfig holds the agent runtime configuration: per-tier CLI settings,
// API-only model keys, and per-role overrides.
type AgentConfig struct {
	Tiers     map[protocol.Tier]TierConfig `yaml:"tiers,omitempty"`
	APIModels map[string]string            `yaml:"api_models,omitempty"`
	Roles     map[string]RoleConfig        `yaml:"roles,omitempty"`
	Transport TransportConfig              `yaml:"transport,omitempty"`
}

// TierConfig specifies the runtime and model for a provider-neutral routing tier.
// Used by CLI-spawn roles (transport: cli).
type TierConfig struct {
	Runtime string `yaml:"runtime"`
	Model   string `yaml:"model"`
}

// RoleConfig specifies the routing configuration for a named role.
// CLI roles (transport: cli) resolve via Tier or explicit Runtime+Model.
// API roles (transport: api) resolve via Provider and APIModel.
type RoleConfig struct {
	Tier      protocol.Tier `yaml:"tier,omitempty"`
	Transport string        `yaml:"transport"`
	Runtime   string        `yaml:"runtime,omitempty"`
	Model     string        `yaml:"model,omitempty"`
	Provider  string        `yaml:"provider,omitempty"`
	APIModel  string        `yaml:"api_model,omitempty"`
}

// TransportConfig holds global transport-level settings (reserved for future use).
type TransportConfig struct{}

// configFile is the top-level YAML document wrapper used only for parsing.
type configFile struct {
	Agent *AgentConfig `yaml:"agent"`
}

func defaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		Tiers: map[protocol.Tier]TierConfig{
			protocol.TierFast:       {Runtime: "claude", Model: "claude-haiku-4-5-20251001"},
			protocol.TierBalanced:   {Runtime: "claude", Model: "claude-sonnet-4-6"},
			protocol.TierDeep:       {Runtime: "claude", Model: "claude-opus-4-7"},
			protocol.TierBackground: {Runtime: "claude", Model: "claude-haiku-4-5-20251001"},
		},
		APIModels: map[string]string{
			"anthropic_fast": "claude-haiku-4-5-20251001",
		},
		Roles: map[string]RoleConfig{
			"worker":              {Tier: protocol.TierBalanced, Transport: "cli"},
			"worker_escalation":   {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_review":          {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_merge":           {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_diagnosis":       {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_epic_fix":        {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_write_ac":        {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_escalation":      {Tier: protocol.TierBalanced, Transport: "cli"},
			"ops_decompose":       {Tier: protocol.TierDeep, Transport: "cli"},
			"ops_dream":           {Tier: protocol.TierBackground, Transport: "cli"},
			"memory_extractor":    {Tier: protocol.TierFast, Transport: "cli"},
			"codesearch_reranker": {Tier: protocol.TierFast, Transport: "cli"},
			"estimator":           {Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"},
		},
	}
}

// Load reads the YAML file at path and returns the parsed AgentConfig.
// When the file does not exist or the agent block is absent, built-in
// defaults are returned. Parse errors are surfaced as-is.
// Validation of field values lives in Validate() — this function only parses.
func Load(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path accepted from caller
	if errors.Is(err, os.ErrNotExist) {
		return defaultAgentConfig(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var f configFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if f.Agent == nil {
		return defaultAgentConfig(), nil
	}
	return f.Agent, nil
}

// Validate checks AgentConfig for invalid role definitions and
// cross-runtime model mismatches, returning a descriptive error naming
// every offender. A nil config is valid (callers fall back to defaults).
//
// Validation rules:
//   - CLI role overrides must be all-or-nothing: setting Runtime without
//     Model (or vice versa) is rejected.
//   - A tier or explicit role override whose model string belongs to a
//     different runtime than declared (e.g., runtime=codex with
//     model=claude-opus-4-7) is rejected.
func Validate(c *AgentConfig) error {
	if c == nil {
		return nil
	}

	var errs []string

	for tier, tc := range c.Tiers {
		if msg := checkRuntimeModelMatch(fmt.Sprintf("tier %q", string(tier)), tc.Runtime, tc.Model); msg != "" {
			errs = append(errs, msg)
		}
	}

	for name, role := range c.Roles {
		if role.Transport != "cli" {
			continue
		}
		hasRuntime := role.Runtime != ""
		hasModel := role.Model != ""
		if hasRuntime != hasModel {
			var missing string
			if hasRuntime {
				missing = "model"
			} else {
				missing = "runtime"
			}
			errs = append(errs, fmt.Sprintf("role %q: CLI override is partial — %s is set but %s is missing; set both or neither", name, roleSetField(role), missing))
			continue
		}
		if msg := checkRuntimeModelMatch(fmt.Sprintf("role %q", name), role.Runtime, role.Model); msg != "" {
			errs = append(errs, msg)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid agent config:\n  %s", strings.Join(errs, "\n  "))
}

// checkRuntimeModelMatch returns an error message when the declared runtime
// disagrees with the runtime inferred from the model string. An empty model,
// empty runtime, or unknown/legacy model name skips the check.
func checkRuntimeModelMatch(label, runtime, model string) string {
	if runtime == "" || model == "" {
		return ""
	}
	modelRT := inferModelRuntime(model)
	if modelRT == "" || modelRT == runtime {
		return ""
	}
	return fmt.Sprintf("%s: model %q belongs to runtime %q but %s declares runtime %q", label, model, modelRT, label, runtime)
}

// inferModelRuntime guesses which runtime a provider-native model string belongs to.
// Returns an empty string when the model string is ambiguous (tier alias, legacy name, or unknown).
func inferModelRuntime(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(lower, "claude") {
		return "claude"
	}
	if strings.Contains(lower, "gpt") || strings.Contains(lower, "codex") {
		return "codex"
	}
	return ""
}

// roleSetField returns the name of the field that IS set in a partial override.
func roleSetField(r RoleConfig) string {
	if r.Runtime != "" {
		return "runtime"
	}
	return "model"
}
