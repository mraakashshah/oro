// Package agentmodel resolves provider-neutral Oro roles and bead routing
// metadata into concrete runtime/model pairs.
package agentmodel

import (
	"path/filepath"
	"strings"

	"oro/pkg/config"
	"oro/pkg/protocol"
)

const defaultConfigPath = ".oro/config.yaml"

type loadedConfig struct {
	cfg *config.AgentConfig
}

// ResolveForRole returns the runtime, model, and reasoning effort configured for role.
func ResolveForRole(role string) (runtime, model, reasoning string) {
	cfg := loadAgentConfig()
	return resolveRole(cfg.cfg, role)
}

// GradeLadder returns the configured grade role and its escalation rungs.
func GradeLadder() []config.RoleRung {
	cfg := loadAgentConfig()
	return config.GradeLadder(*cfg.cfg)
}

// UsesRuntime reports whether any effective CLI tier or role can route work to runtime.
func UsesRuntime(runtime string) bool {
	want := strings.TrimSpace(strings.ToLower(runtime))
	if want == "" {
		return false
	}

	cfg := loadAgentConfig().cfg
	for _, tier := range cfg.Tiers {
		if strings.EqualFold(tier.Runtime, want) {
			return true
		}
	}
	for role, roleCfg := range cfg.Roles {
		if roleCfg.Transport != "cli" {
			continue
		}
		resolved, _, _ := resolveRole(cfg, role)
		if strings.EqualFold(resolved, want) {
			return true
		}
	}
	return false
}

// ResolveForBead returns the runtime and model configured for role, with bead
// routing overrides applied. Precedence is: known bead tier; legacy model
// mapped through the configured tier; estimate-derived tier; role
// explicit runtime/model; role tier; default tier.
func ResolveForBead(role string, b protocol.Bead) (runtime, model, reasoning string) {
	cfg := loadAgentConfig()
	explicitModel := b.ResolveModel()

	if tier, ok := beadTier(b); ok {
		return resolveTier(cfg.cfg, tier)
	}

	runtime, model, reasoning = resolveRole(cfg.cfg, role)
	if explicitModel != "" {
		return runtime, explicitModel, reasoning
	}
	return runtime, model, reasoning
}

func loadAgentConfig() loadedConfig {
	configPath := filepath.Clean(defaultConfigPath)
	cfg, err := config.LoadWithPrecedence(configPath)
	if err != nil {
		return loadedConfig{cfg: config.DefaultAgentConfig()}
	}
	return loadedConfig{cfg: withDefaults(cfg)}
}

func withDefaults(cfg *config.AgentConfig) *config.AgentConfig {
	if cfg == nil {
		return config.DefaultAgentConfig()
	}

	defaults := config.DefaultAgentConfig()
	if cfg.Tiers == nil {
		cfg.Tiers = defaults.Tiers
	} else {
		for tier, tierCfg := range defaults.Tiers {
			if _, ok := cfg.Tiers[tier]; !ok {
				cfg.Tiers[tier] = tierCfg
			}
		}
	}

	if cfg.Roles == nil {
		cfg.Roles = defaults.Roles
	} else {
		for role, roleCfg := range defaults.Roles {
			if _, ok := cfg.Roles[role]; !ok {
				cfg.Roles[role] = roleCfg
			}
		}
	}

	if cfg.APIModels == nil {
		cfg.APIModels = defaults.APIModels
	}

	return cfg
}

func resolveRole(cfg *config.AgentConfig, role string) (runtime, model, reasoning string) {
	roleCfg, ok := cfg.Roles[role]
	if !ok {
		roleCfg = cfg.Roles["worker"]
	}

	if roleCfg.Runtime != "" && roleCfg.Model != "" {
		return roleCfg.Runtime, roleCfg.Model, roleCfg.Reasoning
	}

	tier := roleCfg.Tier
	if !tier.IsKnown() {
		tier = protocol.DefaultTier
	}
	return resolveTier(cfg, tier)
}

func beadTier(b protocol.Bead) (protocol.Tier, bool) {
	if b.Tier != "" {
		if b.Tier.IsKnown() {
			return b.Tier, true
		}
		return protocol.DefaultTier, true
	}
	if tier, ok := protocol.LegacyModelToTier(b.Model); ok {
		return tier, true
	}
	if b.Model == "" && b.EstimatedMinutes > 0 {
		return b.ResolveTier(), true
	}
	return "", false
}

func resolveTier(cfg *config.AgentConfig, tier protocol.Tier) (runtime, model, reasoning string) {
	if !tier.IsKnown() {
		tier = protocol.DefaultTier
	}

	tierCfg, ok := cfg.Tiers[tier]
	if !ok {
		tierCfg = cfg.Tiers[protocol.DefaultTier]
	}
	return tierCfg.Runtime, tierCfg.Model, tierCfg.Reasoning
}
