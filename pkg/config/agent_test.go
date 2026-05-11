package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

	t.Run("TierConfig has Runtime Model and Reasoning fields", func(t *testing.T) {
		tc := config.TierConfig{Runtime: "claude", Model: "claude-sonnet-4-6", Reasoning: "low"}
		if tc.Runtime != "claude" {
			t.Errorf("TierConfig.Runtime: got %q, want %q", tc.Runtime, "claude")
		}
		if tc.Model != "claude-sonnet-4-6" {
			t.Errorf("TierConfig.Model: got %q, want %q", tc.Model, "claude-sonnet-4-6")
		}
		if tc.Reasoning != "low" {
			t.Errorf("TierConfig.Reasoning: got %q, want %q", tc.Reasoning, "low")
		}

		tierType := reflect.TypeFor[config.TierConfig]()
		for _, name := range []string{"Runtime", "Model", "Reasoning"} {
			if _, ok := tierType.FieldByName(name); !ok {
				t.Errorf("TierConfig missing field %q", name)
			}
		}
	})

	t.Run("RoleConfig has Tier Transport Runtime Model Reasoning Provider APIModel", func(t *testing.T) {
		rc := config.RoleConfig{
			Tier:      protocol.TierBalanced,
			Transport: "cli",
			Runtime:   "claude",
			Model:     "claude-sonnet-4-6",
			Reasoning: "medium",
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
		if rc.Reasoning != "medium" {
			t.Errorf("RoleConfig.Reasoning: got %q, want %q", rc.Reasoning, "medium")
		}
		if rc.Provider != "anthropic" {
			t.Errorf("RoleConfig.Provider: got %q, want %q", rc.Provider, "anthropic")
		}
		if rc.APIModel != "anthropic_fast" {
			t.Errorf("RoleConfig.APIModel: got %q, want %q", rc.APIModel, "anthropic_fast")
		}

		roleType := reflect.TypeFor[config.RoleConfig]()
		for _, name := range []string{"Tier", "Transport", "Runtime", "Model", "Reasoning", "Provider", "APIModel"} {
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
				protocol.TierDeep: {Runtime: "codex", Model: "gpt-5.5", Reasoning: "high"},
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
		if cfg.Tiers[protocol.TierDeep].Reasoning != "high" {
			t.Errorf("deep tier Reasoning: got %q, want high", cfg.Tiers[protocol.TierDeep].Reasoning)
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
      reasoning: low
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
	if got := cfg.Tiers[protocol.TierFast].Reasoning; got != "low" {
		t.Errorf("tiers.fast.reasoning = %q, want %q", got, "low")
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

func TestDefaultAgentConfigLockedProviderRoleTable(t *testing.T) {
	cfg := config.DefaultAgentConfig()

	for tier, want := range map[protocol.Tier]config.TierConfig{
		protocol.TierFast:       {Runtime: "codex", Model: "gpt-5.5", Reasoning: "low"},
		protocol.TierBalanced:   {Runtime: "codex", Model: "gpt-5.5", Reasoning: "low"},
		protocol.TierDeep:       {Runtime: "codex", Model: "gpt-5.5", Reasoning: "high"},
		protocol.TierBackground: {Runtime: "codex", Model: "gpt-5.5", Reasoning: "low"},
	} {
		if got := cfg.Tiers[tier]; got != want {
			t.Fatalf("tier %s = %+v, want %+v", tier, got, want)
		}
	}

	for role, want := range map[string]config.RoleConfig{
		"spec_writer":       {Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
		"spec_challenger":   {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "xhigh"},
		"worker":            {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "low"},
		"worker_escalation": {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "medium"},
		"ops_review":        {Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
		"ops_escalation":    {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "high"},
		"ops_merge":         {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "high"},
		"ops_diagnosis":     {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "high"},
		"ops_decompose":     {Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
		"ops_epic_fix":      {Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
		"ops_write_ac":      {Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
		"ops_dream":         {Transport: "cli", Tier: protocol.TierFast},
	} {
		if got := cfg.Roles[role]; got != want {
			t.Fatalf("role %s = %+v, want %+v", role, got, want)
		}
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

func TestAgentConfigPartialOverrideRejected(t *testing.T) {
	t.Run("runtime set without model", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"worker": {
					Transport: "cli",
					Runtime:   "codex",
					// Model omitted — partial override, must be rejected
				},
			},
		}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected error for partial CLI override (runtime without model), got nil")
		}
		if !strings.Contains(err.Error(), "worker") {
			t.Errorf("error must name the offending role %q; got: %v", "worker", err)
		}
		if !strings.Contains(err.Error(), "model") {
			t.Errorf("error must name the missing field \"model\"; got: %v", err)
		}
	})

	t.Run("model set without runtime", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"ops_review": {
					Transport: "cli",
					Model:     "gpt-5.5",
					// Runtime omitted — partial override, must be rejected
				},
			},
		}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected error for partial CLI override (model without runtime), got nil")
		}
		if !strings.Contains(err.Error(), "ops_review") {
			t.Errorf("error must name the offending role %q; got: %v", "ops_review", err)
		}
		if !strings.Contains(err.Error(), "runtime") {
			t.Errorf("error must name the missing field \"runtime\"; got: %v", err)
		}
	})

	t.Run("full explicit override accepted", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"worker": {
					Transport: "cli",
					Runtime:   "codex",
					Model:     "gpt-5.5",
				},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Errorf("expected no error for full explicit override, got: %v", err)
		}
	})

	t.Run("tier-only CLI role accepted", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"worker": {
					Transport: "cli",
					Tier:      protocol.TierBalanced,
				},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Errorf("expected no error for tier-only CLI role, got: %v", err)
		}
	})

	t.Run("nil config accepted", func(t *testing.T) {
		if err := config.Validate(nil); err != nil {
			t.Errorf("expected no error for nil config, got: %v", err)
		}
	})

	t.Run("empty role accepted", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"worker": {Transport: "cli"},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Errorf("expected no error for empty CLI role (falls back to defaults), got: %v", err)
		}
	})
}

func TestAgentConfigCrossRuntimeMismatchRejected(t *testing.T) {
	t.Run("codex runtime with claude model is rejected (tier)", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierDeep: {
					Runtime: "codex",
					Model:   "claude-opus-4-7",
				},
			},
		}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected validation error for cross-runtime mismatch, got nil")
		}
		if !strings.Contains(err.Error(), string(protocol.TierDeep)) {
			t.Errorf("error %q does not name the offending tier %q", err.Error(), protocol.TierDeep)
		}
		if !strings.Contains(err.Error(), "claude") {
			t.Errorf("error %q does not name the conflicting runtime %q", err.Error(), "claude")
		}
	})

	t.Run("claude runtime with codex model is rejected (tier)", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierBalanced: {
					Runtime: "claude",
					Model:   "gpt-5.5",
				},
			},
		}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected validation error for cross-runtime mismatch, got nil")
		}
		if !strings.Contains(err.Error(), string(protocol.TierBalanced)) {
			t.Errorf("error %q does not name the offending tier %q", err.Error(), protocol.TierBalanced)
		}
		if !strings.Contains(err.Error(), "codex") {
			t.Errorf("error %q does not name the conflicting runtime %q", err.Error(), "codex")
		}
	})

	t.Run("matching runtime and model is accepted", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierDeep: {
					Runtime: "claude",
					Model:   "claude-opus-4-7",
				},
				protocol.TierFast: {
					Runtime: "codex",
					Model:   "gpt-5.5",
				},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Errorf("expected no error for matching runtimes, got: %v", err)
		}
	})

	t.Run("empty model skips runtime check", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierBalanced: {
					Runtime: "claude",
					Model:   "",
				},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Errorf("expected no error for empty model, got: %v", err)
		}
	})

	t.Run("role explicit override with cross-runtime mismatch is rejected", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"worker": {
					Transport: "cli",
					Runtime:   "codex",
					Model:     "claude-opus-4-7",
				},
			},
		}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected validation error for role cross-runtime mismatch, got nil")
		}
		if !strings.Contains(err.Error(), "worker") {
			t.Errorf("error %q does not name the offending role %q", err.Error(), "worker")
		}
		if !strings.Contains(err.Error(), "claude") {
			t.Errorf("error %q does not name the conflicting runtime %q", err.Error(), "claude")
		}
	})
}

func TestAgentConfigCodexReasoningValidation(t *testing.T) {
	t.Run("invalid codex tier reasoning is rejected", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierFast: {Runtime: "codex", Model: "gpt-5.5", Reasoning: "extreme"},
			},
		}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected validation error for invalid codex reasoning")
		}
		if !strings.Contains(err.Error(), "reasoning") || !strings.Contains(err.Error(), "extreme") {
			t.Fatalf("error = %v, want invalid reasoning detail", err)
		}
	})

	t.Run("valid codex role reasoning is accepted", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"worker": {Transport: "cli", Runtime: "codex", Model: "gpt-5.5", Reasoning: "xhigh"},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("expected valid codex reasoning, got %v", err)
		}
	})

	t.Run("claude role without reasoning is accepted", func(t *testing.T) {
		cfg := &config.AgentConfig{
			Roles: map[string]config.RoleConfig{
				"ops_review": {Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("expected claude role without reasoning to be valid, got %v", err)
		}
	})
}

func TestAgentConfigAPIRoleRejectsTierKey(t *testing.T) {
	cfg := &config.AgentConfig{
		Roles: map[string]config.RoleConfig{
			"estimator": {
				Transport: "api",
				Tier:      protocol.TierFast,
				Provider:  "anthropic",
				APIModel:  "anthropic_fast",
			},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for API role with tier key, got nil")
	}
	if !strings.Contains(err.Error(), "estimator") {
		t.Errorf("error must name the offending role %q; got: %v", "estimator", err)
	}
	if !strings.Contains(err.Error(), "tier") {
		t.Errorf("error must name the forbidden field %q; got: %v", "tier", err)
	}
}
