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

	if got := cfg.Tiers[protocol.TierFast]; got != (config.TierConfig{Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"}) {
		t.Errorf("default tiers.fast = %+v, want Luna low", got)
	}
	if got := cfg.Tiers[protocol.TierBalanced]; got != (config.TierConfig{Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"}) {
		t.Errorf("default tiers.balanced = %+v, want Terra medium", got)
	}
	if got := cfg.Tiers[protocol.TierDeep]; got != (config.TierConfig{Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"}) {
		t.Errorf("default tiers.deep = %+v, want Sol high", got)
	}
	if got := cfg.Tiers[protocol.TierBackground]; got != (config.TierConfig{Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"}) {
		t.Errorf("default tiers.background = %+v, want Luna low", got)
	}
	if got := cfg.Roles["worker"]; got != (config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"}) {
		t.Errorf("default roles.worker = %+v, want Terra medium", got)
	}
	if got := cfg.Roles["ops_review"]; got != (config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"}) {
		t.Errorf("default roles.ops_review = %+v, want Claude Fable CLI", got)
	}
	if got := cfg.Roles["estimator"]; got != (config.RoleConfig{Tier: protocol.TierFast, Transport: "cli"}) {
		t.Errorf("default roles.estimator = %+v, want CLI fast tier", got)
	}
	if len(cfg.APIModels) != 0 {
		t.Errorf("default api_models = %+v, want no API-key-backed models", cfg.APIModels)
	}
}

func TestLoadWithPrecedencePrefersProjectAgentBlock(t *testing.T) {
	projectConfigPath := filepath.Join(t.TempDir(), "config.yaml")
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	projectConfig := `agent:
  roles:
    worker:
      runtime: project-runtime
      model: project-model
      reasoning: project-reasoning
`
	if err := os.WriteFile(projectConfigPath, []byte(projectConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	globalConfig := `agent:
  roles:
    worker:
      runtime: global-runtime
      model: global-model
      reasoning: global-reasoning
`
	if err := os.WriteFile(filepath.Join(oroHome, "config.yaml"), []byte(globalConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadWithPrecedence(projectConfigPath)
	if err != nil {
		t.Fatalf("LoadWithPrecedence returned unexpected error: %v", err)
	}

	worker := cfg.Roles["worker"]
	if worker.Runtime != "project-runtime" || worker.Model != "project-model" || worker.Reasoning != "project-reasoning" {
		t.Errorf("worker role = %+v, want project routing values", worker)
	}
}

func TestLoadWithPrecedenceFallsBackToGlobalWithoutProjectAgentBlock(t *testing.T) {
	projectConfigPath := filepath.Join(t.TempDir(), "config.yaml")
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	globalConfig := `agent:
  roles:
    worker:
      runtime: global-runtime
      model: global-model
      reasoning: global-reasoning
`
	if err := os.WriteFile(filepath.Join(oroHome, "config.yaml"), []byte(globalConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("project config absent", func(t *testing.T) {
		cfg, err := config.LoadWithPrecedence(projectConfigPath)
		if err != nil {
			t.Fatalf("LoadWithPrecedence returned unexpected error: %v", err)
		}
		if got := cfg.Roles["worker"].Model; got != "global-model" {
			t.Errorf("worker model = %q, want global-model", got)
		}
	})

	t.Run("project config has no agent block", func(t *testing.T) {
		if err := os.WriteFile(projectConfigPath, []byte("project: example\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg, err := config.LoadWithPrecedence(projectConfigPath)
		if err != nil {
			t.Fatalf("LoadWithPrecedence returned unexpected error: %v", err)
		}
		if got := cfg.Roles["worker"].Model; got != "global-model" {
			t.Errorf("worker model = %q, want global-model", got)
		}
	})
}

func TestLoadWithPrecedenceSurfacesMalformedProjectAgentBlock(t *testing.T) {
	projectConfigPath := filepath.Join(t.TempDir(), "config.yaml")
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	if err := os.WriteFile(projectConfigPath, []byte("agent: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroHome, "config.yaml"), []byte("agent: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.LoadWithPrecedence(projectConfigPath)
	if err == nil {
		t.Fatal("LoadWithPrecedence returned nil error for malformed project agent config")
	}
	if !strings.Contains(err.Error(), projectConfigPath) {
		t.Errorf("error = %q, want project config path %q", err, projectConfigPath)
	}
}

func TestDefaultAgentConfigLockedProviderRoleTable(t *testing.T) {
	cfg := config.DefaultAgentConfig()

	for tier, want := range map[protocol.Tier]config.TierConfig{
		protocol.TierFast:       {Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
		protocol.TierBalanced:   {Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"},
		protocol.TierDeep:       {Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		protocol.TierBackground: {Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
	} {
		if got := cfg.Tiers[tier]; got != want {
			t.Fatalf("tier %s = %+v, want %+v", tier, got, want)
		}
	}

	for role, want := range map[string]config.RoleConfig{
		"spec_writer":             {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"spec_challenger":         {Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"},
		"worker":                  {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"},
		"worker_escalation":       {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_review":              {Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"},
		"ops_review_triage":       {Transport: "cli", Runtime: "claude", Model: "fable"},
		"ops_review_correctness":  {Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"},
		"ops_review_security":     {Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"},
		"ops_review_adversarial":  {Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"},
		"ops_review_design":       {Transport: "cli", Runtime: "claude", Model: "fable"},
		"ops_review_test":         {Transport: "cli", Runtime: "claude", Model: "fable"},
		"ops_review_architecture": {Transport: "cli", Runtime: "claude", Model: "fable"},
		"ops_escalation":          {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_merge":               {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_diagnosis":           {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_decompose":           {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_epic_fix":            {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_write_ac":            {Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		"ops_dream":               {Transport: "cli", Tier: protocol.TierFast},
		"memory_extractor":        {Transport: "cli", Tier: protocol.TierFast},
		"codesearch_reranker":     {Transport: "cli", Tier: protocol.TierFast},
		"estimator":               {Transport: "cli", Tier: protocol.TierFast},
	} {
		if got := cfg.Roles[role]; got != want {
			t.Fatalf("role %s = %+v, want %+v", role, got, want)
		}
	}
}

func TestDefaultAgentConfigUsesLunaTerraSolAndFableEffort(t *testing.T) {
	cfg := config.DefaultAgentConfig()

	wantTiers := map[protocol.Tier]config.TierConfig{
		protocol.TierFast:       {Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
		protocol.TierBalanced:   {Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"},
		protocol.TierDeep:       {Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		protocol.TierBackground: {Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
	}
	for tier, want := range wantTiers {
		if got := cfg.Tiers[tier]; got != want {
			t.Errorf("tier %s = %+v, want %+v", tier, got, want)
		}
	}

	wantChallenger := config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"}
	if got := cfg.Roles["spec_challenger"]; got != wantChallenger {
		t.Errorf("spec_challenger = %+v, want %+v", got, wantChallenger)
	}
}

func TestProviderModePresets(t *testing.T) {
	cases := map[string]struct {
		mode        config.ProviderMode
		worker      config.RoleConfig
		challenger  config.RoleConfig
		review      config.RoleConfig
		merge       config.RoleConfig
		fastTier    config.TierConfig
		deepTier    config.TierConfig
		estimator   config.RoleConfig
		invalidMode bool
	}{
		"codex only": {
			mode:   config.ProviderModeCodexOnly,
			worker: config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"},
			challenger: config.RoleConfig{
				Transport: "cli",
				Runtime:   "codex",
				Model:     "gpt-5.6-sol",
				Reasoning: "high",
			},
			review:    config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
			merge:     config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
			fastTier:  config.TierConfig{Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
			deepTier:  config.TierConfig{Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
			estimator: config.RoleConfig{Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"},
		},
		"claude only": {
			mode:   config.ProviderModeClaudeOnly,
			worker: config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "claude-sonnet-4-6"},
			challenger: config.RoleConfig{
				Transport: "cli",
				Runtime:   "claude",
				Model:     "claude-opus-4-7",
			},
			review:    config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
			merge:     config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
			fastTier:  config.TierConfig{Runtime: "claude", Model: "claude-haiku-4-5-20251001"},
			deepTier:  config.TierConfig{Runtime: "claude", Model: "claude-opus-4-7"},
			estimator: config.RoleConfig{Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"},
		},
		"codex coding claude review": {
			mode:   config.ProviderModeCodexCodingClaudeReview,
			worker: config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"},
			challenger: config.RoleConfig{
				Transport: "cli",
				Runtime:   "claude",
				Model:     "fable",
				Reasoning: "xhigh",
			},
			review:    config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"},
			merge:     config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
			fastTier:  config.TierConfig{Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
			deepTier:  config.TierConfig{Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
			estimator: config.RoleConfig{Transport: "cli", Tier: protocol.TierFast},
		},
		"claude coding codex review": {
			mode:   config.ProviderModeClaudeCodingCodexReview,
			worker: config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "claude-sonnet-4-6"},
			challenger: config.RoleConfig{
				Transport: "cli",
				Runtime:   "codex",
				Model:     "gpt-5.6-sol",
				Reasoning: "high",
			},
			review:    config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
			merge:     config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "claude-opus-4-7"},
			fastTier:  config.TierConfig{Runtime: "claude", Model: "claude-haiku-4-5-20251001"},
			deepTier:  config.TierConfig{Runtime: "claude", Model: "claude-opus-4-7"},
			estimator: config.RoleConfig{Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"},
		},
		"unknown": {
			mode:        config.ProviderMode("both"),
			invalidMode: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := config.DefaultAgentConfig()
			cfg.ProviderMode = tc.mode
			err := config.ApplyProviderMode(cfg)
			if tc.invalidMode {
				if err == nil {
					t.Fatalf("ApplyProviderMode(%q) error = nil, want error", tc.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyProviderMode(%q): %v", tc.mode, err)
			}
			if got := cfg.Roles["worker"]; got != tc.worker {
				t.Fatalf("worker role = %+v, want %+v", got, tc.worker)
			}
			if got := cfg.Roles["spec_challenger"]; got != tc.challenger {
				t.Fatalf("spec_challenger role = %+v, want %+v", got, tc.challenger)
			}
			if got := cfg.Roles["ops_review"]; got != tc.review {
				t.Fatalf("ops_review role = %+v, want %+v", got, tc.review)
			}
			if got := cfg.Roles["ops_merge"]; got != tc.merge {
				t.Fatalf("ops_merge role = %+v, want %+v", got, tc.merge)
			}
			if got := cfg.Tiers[protocol.TierFast]; got != tc.fastTier {
				t.Fatalf("fast tier = %+v, want %+v", got, tc.fastTier)
			}
			if got := cfg.Tiers[protocol.TierDeep]; got != tc.deepTier {
				t.Fatalf("deep tier = %+v, want %+v", got, tc.deepTier)
			}
			if got := cfg.Roles["estimator"]; got != tc.estimator {
				t.Fatalf("estimator role = %+v, want %+v", got, tc.estimator)
			}
		})
	}
}

func TestCodexCodingClaudeReviewPresetCompleteRouting(t *testing.T) {
	cfg := &config.AgentConfig{
		ProviderMode: config.ProviderModeCodexCodingClaudeReview,
		Roles: map[string]config.RoleConfig{
			"custom_role": {Transport: "cli", Runtime: "claude", Model: "custom-model"},
		},
	}
	if err := config.ApplyProviderMode(cfg); err != nil {
		t.Fatal(err)
	}

	for tier, want := range map[protocol.Tier]config.TierConfig{
		protocol.TierFast:       {Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
		protocol.TierBalanced:   {Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"},
		protocol.TierDeep:       {Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"},
		protocol.TierBackground: {Runtime: "codex", Model: "gpt-5.6-luna", Reasoning: "low"},
	} {
		if got := cfg.Tiers[tier]; got != want {
			t.Errorf("tier %s = %+v, want %+v", tier, got, want)
		}
	}

	fableXHigh := config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "fable", Reasoning: "xhigh"}
	for _, role := range []string{
		"spec_challenger", "ops_review", "ops_review_correctness",
		"ops_review_security", "ops_review_adversarial",
	} {
		if got := cfg.Roles[role]; got != fableXHigh {
			t.Errorf("role %s = %+v, want Claude Fable xhigh CLI", role, got)
		}
	}

	fable := config.RoleConfig{Transport: "cli", Runtime: "claude", Model: "fable"}
	for _, role := range []string{"ops_review_triage", "ops_review_design", "ops_review_test", "ops_review_architecture"} {
		if got := cfg.Roles[role]; got != fable {
			t.Errorf("role %s = %+v, want Claude Fable CLI", role, got)
		}
	}

	sol := config.RoleConfig{Transport: "cli", Runtime: "codex", Model: "gpt-5.6-sol", Reasoning: "high"}
	for _, role := range []string{
		"spec_writer", "worker_escalation", "ops_escalation", "ops_merge", "ops_diagnosis",
		"ops_epic_fix", "ops_write_ac", "ops_decompose",
	} {
		if got := cfg.Roles[role]; got != sol {
			t.Errorf("role %s = %+v, want Sol high", role, got)
		}
	}

	fast := config.RoleConfig{Transport: "cli", Tier: protocol.TierFast}
	for _, role := range []string{"ops_dream", "memory_extractor", "codesearch_reranker", "estimator"} {
		if got := cfg.Roles[role]; got != fast {
			t.Errorf("role %s = %+v, want CLI fast tier", role, got)
		}
	}
	if got := cfg.Roles["custom_role"]; got.Model != "custom-model" {
		t.Errorf("custom role was overwritten: %+v", got)
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
	if got := cfg.Tiers[protocol.TierBalanced]; got != (config.TierConfig{Runtime: "codex", Model: "gpt-5.6-terra", Reasoning: "medium"}) {
		t.Errorf("missing-file default tiers.balanced = %+v, want Terra medium", got)
	}
}

func TestAgentConfigPartialOverrideRejected(t *testing.T) {
	t.Run("unknown provider mode rejected", func(t *testing.T) {
		cfg := &config.AgentConfig{ProviderMode: config.ProviderMode("both")}
		err := config.Validate(cfg)
		if err == nil {
			t.Fatal("expected validation error for unknown provider mode, got nil")
		}
		if !strings.Contains(err.Error(), "provider_mode") || !strings.Contains(err.Error(), "both") {
			t.Errorf("error must name the invalid provider mode; got: %v", err)
		}
	})

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

func TestGradeRoleLadder(t *testing.T) {
	cfg := config.DefaultAgentConfig()

	if got, want := cfg.Roles["grade"], (config.RoleConfig{
		Transport: "cli",
		Runtime:   "codex",
		Model:     "gpt-5.6-terra",
		Reasoning: "low",
	}); got != want {
		t.Errorf("grade role = %+v, want %+v", got, want)
	}

	got := config.GradeLadder(*cfg)
	want := []config.RoleRung{
		{Model: "gpt-5.6-terra", Reasoning: "low"},
		{Model: "gpt-5.6-sol", Reasoning: "high"},
		{Model: "gpt-5.6-sol", Reasoning: "xhigh"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GradeLadder() = %+v, want %+v", got, want)
	}

	for _, rung := range got {
		if err := config.Validate(&config.AgentConfig{Roles: map[string]config.RoleConfig{
			"grade": {Transport: "cli", Runtime: "codex", Model: rung.Model, Reasoning: rung.Reasoning},
		}}); err != nil {
			t.Errorf("grade ladder rung %+v failed reasoning validation: %v", rung, err)
		}
	}
}
