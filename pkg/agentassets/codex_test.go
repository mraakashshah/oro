package agentassets_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"oro/pkg/agentassets"
)

func TestCodexGeneratorEmitsValidPluginPackage(t *testing.T) {
	assets, err := (agentassets.CodexGenerator{}).PluginPackage("/Users/alice/.oro/hooks")
	if err != nil {
		t.Fatalf("PluginPackage returned error: %v", err)
	}

	assetByTarget := make(map[string]agentassets.RuleAsset, len(assets))
	for _, asset := range assets {
		assetByTarget[asset.Target] = asset
	}

	for _, target := range []string{
		filepath.ToSlash(filepath.Join(".agents", "plugins", "marketplace.json")),
		filepath.ToSlash(filepath.Join("plugins", "oro", ".codex-plugin", "plugin.json")),
		filepath.ToSlash(filepath.Join("plugins", "oro", "hooks.json")),
	} {
		if _, ok := assetByTarget[target]; !ok {
			t.Fatalf("Codex plugin package missing %s; targets: %#v", target, assetTargets(assets))
		}
	}

	var manifest map[string]any
	if err := json.Unmarshal(assetByTarget["plugins/oro/.codex-plugin/plugin.json"].Content, &manifest); err != nil {
		t.Fatalf("plugin.json is invalid JSON: %v", err)
	}
	for _, field := range []string{"name", "version", "description", "author", "license", "interface"} {
		if _, ok := manifest[field]; !ok {
			t.Fatalf("plugin.json missing required field %q: %s", field, assetByTarget["plugins/oro/.codex-plugin/plugin.json"].Content)
		}
	}
	if manifest["name"] != "oro" {
		t.Fatalf("plugin name = %q, want oro", manifest["name"])
	}
	if manifest["description"] == "" || manifest["license"] == "" {
		t.Fatalf("plugin manifest must populate description and license, got %#v", manifest)
	}
	iface, ok := manifest["interface"].(map[string]any)
	if !ok {
		t.Fatalf("plugin interface = %#v, want object", manifest["interface"])
	}
	for _, field := range []string{"displayName", "shortDescription", "longDescription", "developerName", "category", "capabilities", "defaultPrompt"} {
		if _, ok := iface[field]; !ok {
			t.Fatalf("plugin interface missing field %q: %#v", field, iface)
		}
	}

	var marketplace struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(assetByTarget[".agents/plugins/marketplace.json"].Content, &marketplace); err != nil {
		t.Fatalf("marketplace.json is invalid JSON: %v", err)
	}
	if marketplace.Name != "oro-local" || len(marketplace.Plugins) != 1 {
		t.Fatalf("marketplace = %+v, want one oro-local plugin", marketplace)
	}
	if marketplace.Plugins[0].Name != "oro" || marketplace.Plugins[0].Source.Source != "local" || marketplace.Plugins[0].Source.Path != "./plugins/oro" {
		t.Fatalf("marketplace plugin source = %+v, want local ./plugins/oro", marketplace.Plugins[0])
	}

	var hooksFile struct {
		Hooks map[string][]agentassets.HookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(assetByTarget["plugins/oro/hooks.json"].Content, &hooksFile); err != nil {
		t.Fatalf("hooks.json is invalid JSON: %v", err)
	}
	wantEvents := []string{"SessionStart", "PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop"}
	for _, event := range wantEvents {
		if len(hooksFile.Hooks[event]) == 0 {
			t.Fatalf("hooks.json missing %s HookSpec content: %#v", event, hooksFile.Hooks)
		}
	}

	postToolUse := hooksFile.Hooks["PostToolUse"]
	if len(postToolUse) < 2 {
		t.Fatalf("PostToolUse hooks = %#v, want guarded and formatting hooks", postToolUse)
	}
	if postToolUse[0].Matcher != "Bash" {
		t.Fatalf("PostToolUse matcher = %q, want Codex Bash matcher", postToolUse[0].Matcher)
	}
	if got := postToolUse[0].Hooks[0].Command; got != "python3 /Users/alice/.oro/hooks/prompt_injection_guard.py" {
		t.Fatalf("PostToolUse guard command = %q", got)
	}
	if !slices.ContainsFunc(postToolUse, func(group agentassets.HookGroup) bool {
		return group.Matcher == "apply_patch" &&
			len(group.Hooks) == 1 &&
			group.Hooks[0].Command == "/Users/alice/.oro/hooks/auto-format.sh"
	}) {
		t.Fatalf("PostToolUse hooks missing apply_patch auto-format HookSpec: %#v", postToolUse)
	}
}

func assetTargets(assets []agentassets.RuleAsset) []string {
	targets := make([]string, 0, len(assets))
	for _, asset := range assets {
		targets = append(targets, asset.Target)
	}
	slices.Sort(targets)
	return targets
}

func TestInstallCodexPluginPackageWritesMarketplaceFiles(t *testing.T) {
	ctx := context.Background()
	targetDir := t.TempDir()
	assets := []agentassets.RuleAsset{
		{
			Source:  "codex/marketplace.json",
			Target:  ".agents/plugins/marketplace.json",
			Content: []byte("marketplace\n"),
		},
		{
			Source:  "codex/plugin.json",
			Target:  "plugins/oro/.codex-plugin/plugin.json",
			Content: []byte("plugin\n"),
		},
		{
			Source:  "codex/hooks.json",
			Target:  "plugins/oro/hooks.json",
			Content: []byte("hooks\n"),
		},
		{
			Source:  "codex/skills/using-skills/SKILL.md",
			Target:  "plugins/oro/skills/using-skills/SKILL.md",
			Content: []byte("skill\n"),
		},
	}

	if err := agentassets.InstallCodexPluginPackage(ctx, targetDir, assets); err != nil {
		t.Fatalf("InstallCodexPluginPackage returned error: %v", err)
	}

	assertCodexPackageFileContent(t, filepath.Join(targetDir, ".agents", "plugins", "marketplace.json"), "marketplace\n")
	assertCodexPackageFileContent(t, filepath.Join(targetDir, "plugins", "oro", ".codex-plugin", "plugin.json"), "plugin\n")
	assertCodexPackageFileContent(t, filepath.Join(targetDir, "plugins", "oro", "hooks.json"), "hooks\n")
	assertCodexPackageFileContent(t, filepath.Join(targetDir, "plugins", "oro", "skills", "using-skills", "SKILL.md"), "skill\n")
}

func TestInstallCodexPluginPackageRejectsInvalidTargets(t *testing.T) {
	ctx := context.Background()
	targetDir := t.TempDir()

	tests := []struct {
		name    string
		target  string
		wantErr string
	}{
		{
			name:    "absolute path",
			target:  "/plugins/oro/hooks.json",
			wantErr: "escapes",
		},
		{
			name:    "path traversal",
			target:  "plugins/oro/../../outside.json",
			wantErr: "part of the Oro plugin package",
		},
		{
			name:    "unknown file",
			target:  "plugins/oro/unknown.json",
			wantErr: "part of the Oro plugin package",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := agentassets.InstallCodexPluginPackage(ctx, targetDir, []agentassets.RuleAsset{{
				Source:  "codex/bad.json",
				Target:  tc.target,
				Content: []byte("bad\n"),
			}})
			if err == nil {
				t.Fatal("expected invalid target to return error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error to contain %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestInstallCodexPluginPackageHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := agentassets.InstallCodexPluginPackage(ctx, t.TempDir(), []agentassets.RuleAsset{{
		Source:  "codex/hooks.json",
		Target:  "plugins/oro/hooks.json",
		Content: []byte("hooks\n"),
	}})
	if err == nil {
		t.Fatal("expected canceled context to return error")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func assertCodexPackageFileContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
