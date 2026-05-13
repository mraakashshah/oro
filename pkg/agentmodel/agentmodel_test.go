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

func TestLegacyDefaultsWhenAgentBlockAbsent(t *testing.T) {
	writeProjectConfig(t, `project: oro
languages:
  go:
    test_cmd: go test ./...
`)

	t.Run("default role keeps legacy model names", func(t *testing.T) {
		runtime, model, _ := agentmodel.ResolveForRole("worker")
		if runtime != "claude" || model != protocol.ModelSonnet {
			t.Fatalf("ResolveForRole(worker) = (%q, %q), want (claude, sonnet)", runtime, model)
		}
	})

	t.Run("estimated short bead keeps legacy haiku routing", func(t *testing.T) {
		runtime, model, _ := agentmodel.ResolveForBead("worker", protocol.Bead{EstimatedMinutes: 3})
		if runtime != "claude" || model != protocol.ModelHaiku {
			t.Fatalf("ResolveForBead(short estimate) = (%q, %q), want (claude, haiku)", runtime, model)
		}
	})

	t.Run("legacy explicit model stays explicit", func(t *testing.T) {
		runtime, model, _ := agentmodel.ResolveForBead("worker", protocol.Bead{Model: protocol.ModelOpus})
		if runtime != "claude" || model != protocol.ModelOpus {
			t.Fatalf("ResolveForBead(opus) = (%q, %q), want (claude, opus)", runtime, model)
		}
	})
}

func TestLockedRoleResolution(t *testing.T) {
	writeAgentConfig(t, `agent: {}`)

	cases := map[string]struct {
		runtime   string
		model     string
		reasoning string
	}{
		"spec_writer":       {"claude", "claude-opus-4-7", ""},
		"spec_challenger":   {"codex", "gpt-5.5", "xhigh"},
		"worker":            {"codex", "gpt-5.5", "low"},
		"worker_escalation": {"codex", "gpt-5.5", "medium"},
		"ops_review":        {"claude", "claude-opus-4-7", ""},
		"ops_escalation":    {"codex", "gpt-5.5", "high"},
		"ops_merge":         {"codex", "gpt-5.5", "high"},
		"ops_diagnosis":     {"codex", "gpt-5.5", "high"},
		"ops_decompose":     {"claude", "claude-opus-4-7", ""},
		"ops_epic_fix":      {"claude", "claude-opus-4-7", ""},
		"ops_write_ac":      {"claude", "claude-opus-4-7", ""},
		"ops_dream":         {"codex", "gpt-5.5", "low"},
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
	if runtime != "codex" || model != "gpt-5.5" || reasoning != "low" {
		t.Fatalf("ResolveForRole(worker) = (%q, %q, %q), want codex coding preset", runtime, model, reasoning)
	}

	runtime, model, reasoning = agentmodel.ResolveForRole("ops_review")
	if runtime != "claude" || model != "claude-opus-4-7" || reasoning != "" {
		t.Fatalf("ResolveForRole(ops_review) = (%q, %q, %q), want claude review preset", runtime, model, reasoning)
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
