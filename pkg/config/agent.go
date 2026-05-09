package config

import "oro/pkg/protocol"

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
