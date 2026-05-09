package config_test

import (
	"reflect"
	"testing"

	"oro/pkg/config"
	"oro/pkg/protocol"
)

func TestAgentConfigStructFields(t *testing.T) {
	t.Run("AgentConfig exposes Tiers APIModels Roles Transport", func(t *testing.T) {
		agentType := reflect.TypeOf(config.AgentConfig{})

		tiersField, ok := agentType.FieldByName("Tiers")
		if !ok {
			t.Fatal("AgentConfig missing Tiers field")
		}
		wantTiers := reflect.TypeOf(map[protocol.Tier]config.TierConfig{})
		if tiersField.Type != wantTiers {
			t.Errorf("Tiers type: got %v, want %v", tiersField.Type, wantTiers)
		}

		apiModelsField, ok := agentType.FieldByName("APIModels")
		if !ok {
			t.Fatal("AgentConfig missing APIModels field")
		}
		wantAPIModels := reflect.TypeOf(map[string]string{})
		if apiModelsField.Type != wantAPIModels {
			t.Errorf("APIModels type: got %v, want %v", apiModelsField.Type, wantAPIModels)
		}

		rolesField, ok := agentType.FieldByName("Roles")
		if !ok {
			t.Fatal("AgentConfig missing Roles field")
		}
		wantRoles := reflect.TypeOf(map[string]config.RoleConfig{})
		if rolesField.Type != wantRoles {
			t.Errorf("Roles type: got %v, want %v", rolesField.Type, wantRoles)
		}

		if _, ok := agentType.FieldByName("Transport"); !ok {
			t.Fatal("AgentConfig missing Transport field")
		}
	})

	t.Run("TierConfig has Runtime and Model fields", func(t *testing.T) {
		tc := config.TierConfig{Runtime: "claude", Model: "claude-sonnet-4-6"}
		if tc.Runtime != "claude" {
			t.Errorf("TierConfig.Runtime: got %q, want %q", tc.Runtime, "claude")
		}
		if tc.Model != "claude-sonnet-4-6" {
			t.Errorf("TierConfig.Model: got %q, want %q", tc.Model, "claude-sonnet-4-6")
		}

		tierType := reflect.TypeOf(config.TierConfig{})
		for _, name := range []string{"Runtime", "Model"} {
			if _, ok := tierType.FieldByName(name); !ok {
				t.Errorf("TierConfig missing field %q", name)
			}
		}
	})

	t.Run("RoleConfig has Tier Transport Runtime Model Provider APIModel", func(t *testing.T) {
		rc := config.RoleConfig{
			Tier:      protocol.TierBalanced,
			Transport: "cli",
			Runtime:   "claude",
			Model:     "claude-sonnet-4-6",
			Provider:  "anthropic",
			APIModel:  "anthropic_fast",
		}
		if rc.Tier != protocol.TierBalanced {
			t.Errorf("RoleConfig.Tier: got %q, want %q", rc.Tier, protocol.TierBalanced)
		}
		if rc.Transport != "cli" {
			t.Errorf("RoleConfig.Transport: got %q, want %q", rc.Transport, "cli")
		}
		if rc.Runtime != "claude" {
			t.Errorf("RoleConfig.Runtime: got %q, want %q", rc.Runtime, "claude")
		}
		if rc.Model != "claude-sonnet-4-6" {
			t.Errorf("RoleConfig.Model: got %q, want %q", rc.Model, "claude-sonnet-4-6")
		}
		if rc.Provider != "anthropic" {
			t.Errorf("RoleConfig.Provider: got %q, want %q", rc.Provider, "anthropic")
		}
		if rc.APIModel != "anthropic_fast" {
			t.Errorf("RoleConfig.APIModel: got %q, want %q", rc.APIModel, "anthropic_fast")
		}

		roleType := reflect.TypeOf(config.RoleConfig{})
		for _, name := range []string{"Tier", "Transport", "Runtime", "Model", "Provider", "APIModel"} {
			if _, ok := roleType.FieldByName(name); !ok {
				t.Errorf("RoleConfig missing field %q", name)
			}
		}
		tierField, _ := roleType.FieldByName("Tier")
		if tierField.Type != reflect.TypeOf(protocol.Tier("")) {
			t.Errorf("RoleConfig.Tier type: got %v, want protocol.Tier", tierField.Type)
		}
	})

	t.Run("AgentConfig struct literal roundtrip", func(t *testing.T) {
		cfg := config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierFast: {Runtime: "claude", Model: "claude-haiku-4-5-20251001"},
				protocol.TierDeep: {Runtime: "codex", Model: "gpt-5-codex"},
			},
			APIModels: map[string]string{
				"anthropic_fast": "claude-haiku-4-5-20251001",
			},
			Roles: map[string]config.RoleConfig{
				"worker":    {Tier: protocol.TierBalanced, Transport: "cli"},
				"estimator": {Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"},
			},
			Transport: config.TransportConfig{},
		}

		if len(cfg.Tiers) != 2 {
			t.Errorf("expected 2 tiers, got %d", len(cfg.Tiers))
		}
		if cfg.Tiers[protocol.TierFast].Runtime != "claude" {
			t.Errorf("fast tier Runtime: got %q, want claude", cfg.Tiers[protocol.TierFast].Runtime)
		}
		if cfg.Tiers[protocol.TierDeep].Runtime != "codex" {
			t.Errorf("deep tier Runtime: got %q, want codex", cfg.Tiers[protocol.TierDeep].Runtime)
		}
		if cfg.APIModels["anthropic_fast"] != "claude-haiku-4-5-20251001" {
			t.Errorf("APIModels[anthropic_fast]: got %q", cfg.APIModels["anthropic_fast"])
		}
		if cfg.Roles["estimator"].Provider != "anthropic" {
			t.Errorf("estimator Provider: got %q", cfg.Roles["estimator"].Provider)
		}
	})
}
