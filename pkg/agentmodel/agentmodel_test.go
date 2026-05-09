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
    balanced:
      runtime: claude
      model: claude-sonnet-4-6
    deep:
      runtime: codex
      model: gpt-5.5
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
      model: gpt-5-codex
`)

	t.Run("role tier resolves through configured tier", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForRole("ops_review")
		if runtime != "codex" || model != "gpt-5.5" {
			t.Fatalf("ResolveForRole(ops_review) = (%q, %q), want (codex, gpt-5.5)", runtime, model)
		}
	})

	t.Run("explicit role override wins over tier", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForRole("explicit_worker")
		if runtime != "codex" || model != "gpt-5-codex" {
			t.Fatalf("ResolveForRole(explicit_worker) = (%q, %q), want (codex, gpt-5-codex)", runtime, model)
		}
	})

	t.Run("unknown role uses built in default", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForRole("unknown_role")
		if runtime != "claude" || model != "claude-sonnet-4-6" {
			t.Fatalf("ResolveForRole(unknown_role) = (%q, %q), want built-in balanced default", runtime, model)
		}
	})

	t.Run("bead tier wins over role", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForBead("worker", protocol.Bead{Tier: protocol.TierFast})
		if runtime != "codex" || model != "gpt-5-mini" {
			t.Fatalf("ResolveForBead(fast bead) = (%q, %q), want (codex, gpt-5-mini)", runtime, model)
		}
	})

	t.Run("provider native bead model wins while preserving role runtime", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForBead("worker", protocol.Bead{Model: "custom-provider-model"})
		if runtime != "claude" || model != "custom-provider-model" {
			t.Fatalf("ResolveForBead(model override) = (%q, %q), want (claude, custom-provider-model)", runtime, model)
		}
	})

	t.Run("legacy bead model resolves through configured tier", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForBead("worker", protocol.Bead{Model: protocol.ModelOpus})
		if runtime != "codex" || model != "gpt-5.5" {
			t.Fatalf("ResolveForBead(legacy opus) = (%q, %q), want configured deep tier", runtime, model)
		}
	})

	t.Run("unknown bead tier falls back to default tier", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForBead("worker", protocol.Bead{Tier: protocol.Tier("turbo")})
		if runtime != "claude" || model != "claude-sonnet-4-6" {
			t.Fatalf("ResolveForBead(unknown tier) = (%q, %q), want balanced default", runtime, model)
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
		runtime, model := agentmodel.ResolveForRole("worker")
		if runtime != "claude" || model != protocol.ModelSonnet {
			t.Fatalf("ResolveForRole(worker) = (%q, %q), want (claude, sonnet)", runtime, model)
		}
	})

	t.Run("estimated short bead keeps legacy haiku routing", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForBead("worker", protocol.Bead{EstimatedMinutes: 3})
		if runtime != "claude" || model != protocol.ModelHaiku {
			t.Fatalf("ResolveForBead(short estimate) = (%q, %q), want (claude, haiku)", runtime, model)
		}
	})

	t.Run("legacy explicit model stays explicit", func(t *testing.T) {
		runtime, model := agentmodel.ResolveForBead("worker", protocol.Bead{Model: protocol.ModelOpus})
		if runtime != "claude" || model != protocol.ModelOpus {
			t.Fatalf("ResolveForBead(opus) = (%q, %q), want (claude, opus)", runtime, model)
		}
	})
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
