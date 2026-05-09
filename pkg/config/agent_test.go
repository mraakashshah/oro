package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"oro/pkg/config"
	"oro/pkg/protocol"
)

func TestAgentConfigStructFields(t *testing.T) {
	t.Run("AgentConfig exposes Tiers APIModels Roles Transport", func(t *testing.T) {
		agentType := reflect.TypeFor[config.AgentConfig]()

		tiersField, ok := agentType.FieldByName("Tiers")
		if !ok {
			t.Fatal("AgentConfig missing Tiers field")
		}
		wantTiers := reflect.TypeFor[map[protocol.Tier]config.TierConfig]()
		if tiersField.Type != wantTiers {
			t.Errorf("Tiers type: got %v, want %v", tiersField.Type, wantTiers)
		}

		apiModelsField, ok := agentType.FieldByName("APIModels")
		if !ok {
			t.Fatal("AgentConfig missing APIModels field")
		}
		wantAPIModels := reflect.TypeFor[map[string]string]()
		if apiModelsField.Type != wantAPIModels {
			t.Errorf("APIModels type: got %v, want %v", apiModelsField.Type, wantAPIModels)
		}

		rolesField, ok := agentType.FieldByName("Roles")
		if !ok {
			t.Fatal("AgentConfig missing Roles field")
		}
		wantRoles := reflect.TypeFor[map[string]config.RoleConfig]()
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

		tierType := reflect.TypeFor[config.TierConfig]()
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

		roleType := reflect.TypeFor[config.RoleConfig]()
		for _, name := range []string{"Tier", "Transport", "Runtime", "Model", "Provider", "APIModel"} {
			if _, ok := roleType.FieldByName(name); !ok {
				t.Errorf("RoleConfig missing field %q", name)
			}
		}
		tierField, _ := roleType.FieldByName("Tier")
		if tierField.Type != reflect.TypeFor[protocol.Tier]() {
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

func TestAgentConfigLoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `agent:
  tiers:
    fast:
      runtime: testruntime
      model: test-model-fast
    balanced:
      runtime: testruntime
      model: test-model-balanced
    deep:
      runtime: testruntime
      model: test-model-deep
    background:
      runtime: testruntime
      model: test-model-bg
  api_models:
    myfastagent: some-model
  roles:
    worker:
      tier: balanced
      transport: cli
    estimator:
      transport: api
      provider: anthropic
      api_model: myfastagent
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}

	if got := cfg.Tiers[protocol.TierFast].Runtime; got != "testruntime" {
		t.Errorf("tiers.fast.runtime = %q, want %q", got, "testruntime")
	}
	if got := cfg.Tiers[protocol.TierFast].Model; got != "test-model-fast" {
		t.Errorf("tiers.fast.model = %q, want %q", got, "test-model-fast")
	}
	if got := cfg.Tiers[protocol.TierBalanced].Model; got != "test-model-balanced" {
		t.Errorf("tiers.balanced.model = %q, want %q", got, "test-model-balanced")
	}
	if got := cfg.APIModels["myfastagent"]; got != "some-model" {
		t.Errorf("api_models.myfastagent = %q, want %q", got, "some-model")
	}
	if got := cfg.Roles["worker"].Tier; got != protocol.TierBalanced {
		t.Errorf("roles.worker.tier = %q, want %q", got, protocol.TierBalanced)
	}
	if got := cfg.Roles["worker"].Transport; got != "cli" {
		t.Errorf("roles.worker.transport = %q, want %q", got, "cli")
	}
	if got := cfg.Roles["estimator"].Transport; got != "api" {
		t.Errorf("roles.estimator.transport = %q, want %q", got, "api")
	}
	if got := cfg.Roles["estimator"].Provider; got != "anthropic" {
		t.Errorf("roles.estimator.provider = %q, want %q", got, "anthropic")
	}
	if got := cfg.Roles["estimator"].APIModel; got != "myfastagent" {
		t.Errorf("roles.estimator.api_model = %q, want %q", got, "myfastagent")
	}
}

func TestAgentConfigLoadMissingBlockReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `project: myproject
languages:
  go:
    test_cmd: go test ./...
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config")
	}

	if got := cfg.Tiers[protocol.TierFast].Runtime; got != "claude" {
		t.Errorf("default tiers.fast.runtime = %q, want %q", got, "claude")
	}
	if got := cfg.Tiers[protocol.TierBalanced].Model; got != "claude-sonnet-4-6" {
		t.Errorf("default tiers.balanced.model = %q, want %q", got, "claude-sonnet-4-6")
	}
	if got := cfg.Tiers[protocol.TierDeep].Model; got != "claude-opus-4-7" {
		t.Errorf("default tiers.deep.model = %q, want %q", got, "claude-opus-4-7")
	}
	if got := cfg.Tiers[protocol.TierBackground].Runtime; got != "claude" {
		t.Errorf("default tiers.background.runtime = %q, want %q", got, "claude")
	}
	if got := cfg.Roles["worker"].Tier; got != protocol.TierBalanced {
		t.Errorf("default roles.worker.tier = %q, want %q", got, protocol.TierBalanced)
	}
	if got := cfg.Roles["worker"].Transport; got != "cli" {
		t.Errorf("default roles.worker.transport = %q, want %q", got, "cli")
	}
	if got := cfg.Roles["estimator"].Transport; got != "api" {
		t.Errorf("default roles.estimator.transport = %q, want %q", got, "api")
	}
	if got := cfg.APIModels["anthropic_fast"]; got != "claude-haiku-4-5-20251001" {
		t.Errorf("default api_models.anthropic_fast = %q, want %q", got, "claude-haiku-4-5-20251001")
	}
}

func TestAgentConfigLoadSurfacesParseErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")

	if err := os.WriteFile(path, []byte("agent: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for malformed yaml, got nil")
	}
}

func TestAgentConfigLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load returned unexpected error for missing file: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil config for missing file")
	}
	if got := cfg.Tiers[protocol.TierBalanced].Model; got != "claude-sonnet-4-6" {
		t.Errorf("missing-file default tiers.balanced.model = %q, want %q", got, "claude-sonnet-4-6")
	}
}
