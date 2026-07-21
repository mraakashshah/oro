package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/config"
	"oro/pkg/langprofile"
	"oro/pkg/protocol"
)

func TestAgentBlockPrecedence(t *testing.T) {
	t.Run("project agent block wins over global config", func(t *testing.T) {
		projectRoot := t.TempDir()
		homeDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("HOME", homeDir)
		t.Setenv("ORO_HOME", oroHome)

		writeConfig(t, filepath.Join(projectRoot, ".oro", "config.yaml"), agentConfigYAML("project-model"))
		writeConfig(t, filepath.Join(homeDir, ".oro", "config.yaml"), agentConfigYAML("user-model"))
		writeConfig(t, filepath.Join(oroHome, "config.yaml"), agentConfigYAML("oro-home-model"))

		cfg, err := config.LoadWithPrecedence(filepath.Join(projectRoot, ".oro", "config.yaml"))
		if err != nil {
			t.Fatalf("LoadWithPrecedence returned error: %v", err)
		}

		if got := cfg.Tiers[protocol.TierBalanced].Model; got != "project-model" {
			t.Errorf("balanced model = %q, want %q", got, "project-model")
		}
	})

	t.Run("project agent block wins when ORO_HOME is unset", func(t *testing.T) {
		projectRoot := t.TempDir()
		homeDir := t.TempDir()
		t.Setenv("HOME", homeDir)
		t.Setenv("ORO_HOME", "")

		writeConfig(t, filepath.Join(projectRoot, ".oro", "config.yaml"), agentConfigYAML("project-model"))
		writeConfig(t, filepath.Join(homeDir, ".oro", "config.yaml"), agentConfigYAML("user-model"))

		cfg, err := config.LoadWithPrecedence(filepath.Join(projectRoot, ".oro", "config.yaml"))
		if err != nil {
			t.Fatalf("LoadWithPrecedence returned error: %v", err)
		}

		if got := cfg.Tiers[protocol.TierBalanced].Model; got != "project-model" {
			t.Errorf("balanced model = %q, want %q", got, "project-model")
		}
	})

	t.Run("global config without agent block does not override project", func(t *testing.T) {
		projectRoot := t.TempDir()
		homeDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("HOME", homeDir)
		t.Setenv("ORO_HOME", oroHome)

		writeConfig(t, filepath.Join(projectRoot, ".oro", "config.yaml"), agentConfigYAML("project-model"))
		writeConfig(t, filepath.Join(homeDir, ".oro", "config.yaml"), agentConfigYAML("user-model"))
		writeConfig(t, filepath.Join(oroHome, "config.yaml"), "project: global-only\n")

		cfg, err := config.LoadWithPrecedence(filepath.Join(projectRoot, ".oro", "config.yaml"))
		if err != nil {
			t.Fatalf("LoadWithPrecedence returned error: %v", err)
		}

		if got := cfg.Tiers[protocol.TierBalanced].Model; got != "project-model" {
			t.Errorf("balanced model = %q, want %q", got, "project-model")
		}
	})
}

func TestNonAgentBlocksRemainProjectScoped(t *testing.T) {
	projectRoot := t.TempDir()
	homeDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("ORO_HOME", oroHome)

	writeConfig(t, filepath.Join(projectRoot, ".oro", "config.yaml"), `project: project-name
languages:
  go:
    test_cmd: go test ./project/...
memory:
  semantic:
    ann_top_k: 11
`)
	writeConfig(t, filepath.Join(homeDir, ".oro", "config.yaml"), `project: user-name
languages:
  go:
    test_cmd: go test ./user/...
memory:
  semantic:
    ann_top_k: 22
`)
	writeConfig(t, filepath.Join(oroHome, "config.yaml"), `project: oro-home-name
languages:
  go:
    test_cmd: go test ./oro-home/...
memory:
  semantic:
    ann_top_k: 33
`)

	cfg, err := langprofile.ReadConfig(projectRoot)
	if err != nil {
		t.Fatalf("ReadConfig returned error: %v", err)
	}

	if got := cfg.Languages["go"].TestCmd; got != "go test ./project/..." {
		t.Errorf("languages.go.test_cmd = %q, want project-scoped value", got)
	}
	if got := cfg.Memory.Semantic.ANNTopK; got != 11 {
		t.Errorf("memory.semantic.ann_top_k = %d, want project-scoped value 11", got)
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}

func agentConfigYAML(model string) string {
	return `agent:
  tiers:
    balanced:
      runtime: test
      model: ` + model + `
`
}
