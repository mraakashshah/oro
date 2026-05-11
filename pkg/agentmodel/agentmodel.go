// Package agentmodel resolves provider-neutral Oro roles and bead routing
// metadata into concrete runtime/model pairs.
package agentmodel

import (
	"path/filepath"

	"oro/pkg/config"
	"oro/pkg/protocol"
)

const defaultConfigPath = ".oro/config.yaml"

type loadedConfig struct {
	cfg            *config.AgentConfig
	hasAgentBlock  bool
	legacyDefaults bool
}

// ResolveForRole returns the runtime, model, and reasoning effort configured for role.
func ResolveForRole(role string) (runtime, model, reasoning string) {
	cfg := loadAgentConfig()
	return resolveRole(cfg.cfg, role)
}

// ResolveForBead returns the runtime and model configured for role, with bead
// routing overrides applied. Precedence is: absent agent block preserves legacy
// explicit model values; known bead tier; legacy model mapped through the
// configured tier when an agent block exists; estimate-derived tier; role
// explicit runtime/model; role tier; default tier.
func ResolveForBead(role string, b protocol.Bead) (runtime, model, reasoning string) {
	cfg := loadAgentConfig()
	explicitModel := b.ResolveModel()

	if cfg.legacyDefaults && explicitModel != "" {
		return "claude", explicitModel, ""
	}

	if tier, ok := beadTier(b, cfg.hasAgentBlock); ok {
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
	if !config.HasAgentBlockWithPrecedence(configPath) {
		return loadedConfig{cfg: legacyAgentConfig(), legacyDefaults: true}
	}

	cfg, err := config.LoadWithPrecedence(configPath)
	if err != nil {
		return loadedConfig{cfg: legacyAgentConfig(), legacyDefaults: true}
	}
	return loadedConfig{cfg: withDefaults(cfg), hasAgentBlock: true}
}

func legacyAgentConfig() *config.AgentConfig {
	cfg := config.DefaultAgentConfig()
	cfg.Tiers = map[protocol.Tier]config.TierConfig{
		protocol.TierFast:       {Runtime: "claude", Model: protocol.ModelHaiku},
		protocol.TierBalanced:   {Runtime: "claude", Model: protocol.ModelSonnet},
		protocol.TierDeep:       {Runtime: "claude", Model: protocol.ModelOpus},
		protocol.TierBackground: {Runtime: "claude", Model: protocol.ModelHaiku},
	}
	cfg.Roles = map[string]config.RoleConfig{
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
