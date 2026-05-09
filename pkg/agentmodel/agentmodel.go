// Package agentmodel resolves provider-neutral Oro roles and bead routing
// metadata into concrete runtime/model pairs.
package agentmodel

import (
	"os"
	"path/filepath"

	"oro/pkg/config"
	"oro/pkg/protocol"

	"gopkg.in/yaml.v3"
)

const defaultConfigPath = ".oro/config.yaml"

type loadedConfig struct {
	cfg            *config.AgentConfig
	hasAgentBlock  bool
	legacyDefaults bool
}

// ResolveForRole returns the runtime and model configured for role.
func ResolveForRole(role string) (runtime, model string) {
	cfg := loadAgentConfig()
	return resolveRole(cfg.cfg, role)
}

// ResolveForBead returns the runtime and model configured for role, with bead
// routing overrides applied. Precedence is: absent agent block preserves legacy
// explicit model values; known bead tier; legacy model mapped through the
// configured tier when an agent block exists; estimate-derived tier; role
// explicit runtime/model; role tier; default tier.
func ResolveForBead(role string, b protocol.Bead) (runtime, model string) {
	cfg := loadAgentConfig()
	explicitModel := b.ResolveModel()

	if cfg.legacyDefaults && explicitModel != "" {
		return "claude", explicitModel
	}

	if tier, ok := beadTier(b, cfg.hasAgentBlock); ok {
		return resolveTier(cfg.cfg, tier)
	}

	runtime, model = resolveRole(cfg.cfg, role)
	if explicitModel != "" {
		return runtime, explicitModel
	}
	return runtime, model
}

func loadAgentConfig() loadedConfig {
	configPath := filepath.Clean(defaultConfigPath)
	if !hasAgentBlock(configPath) {
		return loadedConfig{cfg: legacyAgentConfig(), legacyDefaults: true}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return loadedConfig{cfg: legacyAgentConfig(), legacyDefaults: true}
	}
	return loadedConfig{cfg: withDefaults(cfg), hasAgentBlock: true}
}

func hasAgentBlock(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // project-local config path
	if err != nil {
		return false
	}
	var doc struct {
		Agent *config.AgentConfig `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	return doc.Agent != nil
}

func legacyAgentConfig() *config.AgentConfig {
	cfg := config.DefaultAgentConfig()
	cfg.Tiers = map[protocol.Tier]config.TierConfig{
		protocol.TierFast:       {Runtime: "claude", Model: protocol.ModelHaiku},
		protocol.TierBalanced:   {Runtime: "claude", Model: protocol.ModelSonnet},
		protocol.TierDeep:       {Runtime: "claude", Model: protocol.ModelOpus},
		protocol.TierBackground: {Runtime: "claude", Model: protocol.ModelHaiku},
	}
	return cfg
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

func resolveRole(cfg *config.AgentConfig, role string) (runtime, model string) {
	roleCfg, ok := cfg.Roles[role]
	if !ok {
		roleCfg = cfg.Roles["worker"]
	}

	if roleCfg.Runtime != "" && roleCfg.Model != "" {
		return roleCfg.Runtime, roleCfg.Model
	}

	tier := roleCfg.Tier
	if !tier.IsKnown() {
		tier = protocol.DefaultTier
	}
	return resolveTier(cfg, tier)
}

func beadTier(b protocol.Bead, hasAgentBlock bool) (protocol.Tier, bool) {
	if b.Tier != "" {
		if b.Tier.IsKnown() {
			return b.Tier, true
		}
		return protocol.DefaultTier, true
	}
	if hasAgentBlock {
		if tier, ok := protocol.LegacyModelToTier(b.Model); ok {
			return tier, true
		}
	}
	if b.Model == "" && b.EstimatedMinutes > 0 {
		return b.ResolveTier(), true
	}
	return "", false
}

func resolveTier(cfg *config.AgentConfig, tier protocol.Tier) (runtime, model string) {
	if !tier.IsKnown() {
		tier = protocol.DefaultTier
	}

	tierCfg, ok := cfg.Tiers[tier]
	if !ok {
		tierCfg = cfg.Tiers[protocol.DefaultTier]
	}
	return tierCfg.Runtime, tierCfg.Model
}
