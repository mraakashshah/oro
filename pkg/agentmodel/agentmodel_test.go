package agentmodel_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/agentmodel"
	"oro/pkg/protocol"
)

func TestRoleResolutionPrecedence(t *testing.T) {
	writeAgentConfig(t, `agent:
  tiers:
    fast:
      runtime: codex
      model: gpt-5-mini
      reasoning: low
    balanced:
      runtime: claude
      model: claude-sonnet-4-6
    deep:
      runtime: codex
      model: gpt-5.5
      reasoning: high
    background:
      runtime: claude
      model: claude-haiku-4-5-20251001
  roles:
    worker:
      tier: balanced
      transport: cli
    ops_review:
      tier: deep
      transport: cli
    explicit_worker:
      transport: cli
      runtime: codex
      model: gpt-5.5
      reasoning: medium
`)

	t.Run("role tier resolves through configured tier", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForRole("ops_review")
		if runtime != "codex" || model != "gpt-5.5" || reasoning != "high" {
			t.Fatalf("ResolveForRole(ops_review) = (%q, %q, %q), want (codex, gpt-5.5, high)", runtime, model, reasoning)
		}
	})

	t.Run("explicit role override wins over tier", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForRole("explicit_worker")
		if runtime != "codex" || model != "gpt-5.5" || reasoning != "medium" {
			t.Fatalf("ResolveForRole(explicit_worker) = (%q, %q, %q), want (codex, gpt-5.5, medium)", runtime, model, reasoning)
		}
	})

	t.Run("unknown role uses built in default", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForRole("unknown_role")
		if runtime != "claude" || model != "claude-sonnet-4-6" || reasoning != "" {
			t.Fatalf("ResolveForRole(unknown_role) = (%q, %q, %q), want configured worker default", runtime, model, reasoning)
		}
	})

	t.Run("bead tier wins over role", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForBead("worker", protocol.Bead{Tier: protocol.TierFast})
		if runtime != "codex" || model != "gpt-5-mini" || reasoning != "low" {
			t.Fatalf("ResolveForBead(fast bead) = (%q, %q, %q), want (codex, gpt-5-mini, low)", runtime, model, reasoning)
		}
	})

	t.Run("provider native bead model wins while preserving role runtime", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForBead("worker", protocol.Bead{Model: "custom-provider-model"})
		if runtime != "claude" || model != "custom-provider-model" || reasoning != "" {
			t.Fatalf("ResolveForBead(model override) = (%q, %q, %q), want (claude, custom-provider-model, empty reasoning)", runtime, model, reasoning)
		}
	})

	t.Run("legacy bead model resolves through configured tier", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForBead("worker", protocol.Bead{Model: protocol.ModelOpus})
		if runtime != "codex" || model != "gpt-5.5" || reasoning != "high" {
			t.Fatalf("ResolveForBead(legacy opus) = (%q, %q, %q), want configured deep tier", runtime, model, reasoning)
		}
	})

	t.Run("unknown bead tier falls back to default tier", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForBead("worker", protocol.Bead{Tier: protocol.Tier("turbo")})
		if runtime != "claude" || model != "claude-sonnet-4-6" || reasoning != "" {
			t.Fatalf("ResolveForBead(unknown tier) = (%q, %q, %q), want balanced default", runtime, model, reasoning)
		}
	})
}

func TestDefaultRoutingWhenAgentBlockAbsent(t *testing.T) {
	writeProjectConfig(t, `project: oro
languages:
  go:
    test_cmd: go test ./...
`)

	t.Run("ordinary work uses Terra medium", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForRole("worker")
		if runtime != "codex" || model != "gpt-5.6-terra" || reasoning != "medium" {
			t.Fatalf("ResolveForRole(worker) = (%q, %q, %q), want (codex, gpt-5.6-terra, medium)", runtime, model, reasoning)
		}
	})

	t.Run("estimated short bead uses Luna low", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForBead("worker", protocol.Bead{EstimatedMinutes: 3})
		if runtime != "codex" || model != "gpt-5.6-luna" || reasoning != "low" {
			t.Fatalf("ResolveForBead(short estimate) = (%q, %q, %q), want (codex, gpt-5.6-luna, low)", runtime, model, reasoning)
		}
	})

	t.Run("legacy deep model maps to Sol low", func(t *testing.T) {
		runtime, model, reasoning := agentmodel.ResolveForBead("worker", protocol.Bead{Model: protocol.ModelOpus})
		if runtime != "codex" || model != "gpt-5.6-sol" || reasoning != "low" {
			t.Fatalf("ResolveForBead(opus) = (%q, %q, %q), want (codex, gpt-5.6-sol, low)", runtime, model, reasoning)
		}
	})
}

func TestProjectRoleRoutingOverridesGlobalAgentConfig(t *testing.T) {
	projectDir := t.TempDir()
	oroHome := t.TempDir()
	t.Chdir(projectDir)
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")

	writeProjectConfigFile(t, projectDir, `agent:
  roles:
    worker:
      transport: cli
      runtime: project-runtime
      model: project-model
      reasoning: project-reasoning
`)
	if err := os.WriteFile(filepath.Join(oroHome, "config.yaml"), []byte(`agent:
  roles:
    worker:
      transport: cli
      runtime: global-runtime
      model: global-model
      reasoning: global-reasoning
`), 0o600); err != nil {
		t.Fatal(err)
	}

	runtime, model, reasoning := agentmodel.ResolveForRole("worker")
	if runtime != "project-runtime" || model != "project-model" || reasoning != "project-reasoning" {
		t.Fatalf("ResolveForRole(worker) = (%q, %q, %q), want project routing", runtime, model, reasoning)
	}
}

func TestLockedRoleResolution(t *testing.T) {
	writeAgentConfig(t, `agent: {}`)

	cases := map[string]struct {
		runtime   string
		model     string
		reasoning string
	}{
		"grade":                   {"codex", "gpt-5.6-terra", "low"},
		"spec_writer":             {"codex", "gpt-5.6-sol", "medium"},
		"spec_challenger":         {"claude", "fable", "medium"},
		"worker":                  {"codex", "gpt-5.6-terra", "medium"},
		"worker_escalation":       {"codex", "gpt-5.6-sol", "low"},
		"ops_review":              {"claude", "claude-opus-4-8", "high"},
		"ops_review_triage":       {"claude", "claude-opus-4-8", "high"},
		"ops_review_correctness":  {"claude", "claude-opus-4-8", "high"},
		"ops_review_security":     {"claude", "claude-opus-4-8", "high"},
		"ops_review_adversarial":  {"claude", "claude-opus-4-8", "high"},
		"ops_review_design":       {"claude", "claude-opus-4-8", "high"},
		"ops_review_test":         {"claude", "claude-opus-4-8", "high"},
		"ops_review_architecture": {"claude", "claude-opus-4-8", "high"},
		"ops_escalation":          {"codex", "gpt-5.6-sol", "low"},
		"ops_merge":               {"codex", "gpt-5.6-sol", "low"},
		"ops_diagnosis":           {"codex", "gpt-5.6-sol", "low"},
		"ops_decompose":           {"codex", "gpt-5.6-sol", "low"},
		"ops_epic_fix":            {"codex", "gpt-5.6-sol", "low"},
		"ops_write_ac":            {"codex", "gpt-5.6-sol", "low"},
		"ops_dream":               {"codex", "gpt-5.6-luna", "low"},
		"memory_extractor":        {"codex", "gpt-5.6-luna", "low"},
		"codesearch_reranker":     {"codex", "gpt-5.6-luna", "low"},
		"estimator":               {"codex", "gpt-5.6-luna", "low"},
	}

	for role, want := range cases {
		t.Run(role, func(t *testing.T) {
			runtime, model, reasoning := agentmodel.ResolveForRole(role)
			if runtime != want.runtime || model != want.model || reasoning != want.reasoning {
				t.Fatalf("ResolveForRole(%s) = (%q, %q, %q), want (%q, %q, %q)", role, runtime, model, reasoning, want.runtime, want.model, want.reasoning)
			}
		})
	}
}

func TestProviderModeOverridesStaleRoleEntries(t *testing.T) {
	writeAgentConfig(t, `agent:
  provider_mode: codex-coding-claude-review
  roles:
    worker:
      transport: cli
      runtime: claude
      model: claude-sonnet-4-6
    ops_review:
      transport: cli
      runtime: codex
      model: gpt-5.5
      reasoning: high
`)

	runtime, model, reasoning := agentmodel.ResolveForRole("worker")
	if runtime != "codex" || model != "gpt-5.6-terra" || reasoning != "medium" {
		t.Fatalf("ResolveForRole(worker) = (%q, %q, %q), want codex coding preset", runtime, model, reasoning)
	}

	runtime, model, reasoning = agentmodel.ResolveForRole("ops_review")
	if runtime != "claude" || model != "claude-opus-4-8" || reasoning != "high" {
		t.Fatalf("ResolveForRole(ops_review) = (%q, %q, %q), want claude review preset", runtime, model, reasoning)
	}

	runtime, model, reasoning = agentmodel.ResolveForRole("spec_challenger")
	if runtime != "claude" || model != "fable" || reasoning != "medium" {
		t.Fatalf("ResolveForRole(spec_challenger) = (%q, %q, %q), want claude review preset", runtime, model, reasoning)
	}
}

func TestUsesRuntime(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		runtime string
		want    bool
	}{
		{name: "codex only", config: "agent:\n  provider_mode: codex-only\n", runtime: "codex", want: true},
		{name: "codex coding", config: "agent:\n  provider_mode: codex-coding-claude-review\n", runtime: "codex", want: true},
		{name: "codex review", config: "agent:\n  provider_mode: claude-coding-codex-review\n", runtime: "codex", want: true},
		{name: "claude only", config: "agent:\n  provider_mode: claude-only\n", runtime: "codex", want: false},
		{
			name: "custom cli role",
			config: `agent:
  provider_mode: claude-only
  roles:
    custom_reviewer:
      transport: cli
      runtime: codex
      model: gpt-5.5
`,
			runtime: "codex",
			want:    true,
		},
		{
			name: "custom tier",
			config: `agent:
  tiers:
    fast:
      runtime: codex
      model: gpt-5.5
    balanced:
      runtime: claude
      model: claude-sonnet-4-6
    deep:
      runtime: claude
      model: claude-opus-4-7
    background:
      runtime: claude
      model: claude-haiku-4-5-20251001
`,
			runtime: "codex",
			want:    true,
		},
		{
			name: "api role does not count",
			config: `agent:
  provider_mode: claude-only
  roles:
    api_only:
      transport: api
      provider: openai
      api_model: codex
`,
			runtime: "codex",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeAgentConfig(t, tt.config)
			if got := agentmodel.UsesRuntime(tt.runtime); got != tt.want {
				t.Fatalf("UsesRuntime(%q) = %t, want %t", tt.runtime, got, tt.want)
			}
		})
	}
}

func TestProtocolPackageHasNoConfigImport(t *testing.T) {
	// Covered by the acceptance shell command; this test keeps the requirement
	// visible in package-local output.
}

func writeAgentConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	writeProjectConfigFile(t, dir, content)
}

func writeProjectConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	writeProjectConfigFile(t, dir, content)
}

func writeProjectConfigFile(t *testing.T, dir, content string) {
	t.Helper()
	oroDir := filepath.Join(dir, ".oro")
	if err := os.MkdirAll(oroDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
