package agentassets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	codexMarketplaceManifestTarget = ".agents/plugins/marketplace.json"
	codexPluginRoot                = "plugins/oro"
	codexPluginManifestTarget      = codexPluginRoot + "/.codex-plugin/plugin.json"
	codexHooksTarget               = codexPluginRoot + "/hooks.json"
)

// CodexGenerator generates Codex-specific runtime assets.
type CodexGenerator struct{}

// PluginPackage returns a local Codex marketplace package for the Oro plugin.
func (g CodexGenerator) PluginPackage(hooksDir string) ([]RuleAsset, error) {
	marketplaceContent, err := marshalCodexAsset(codexMarketplaceManifest{
		Name:      "oro-local",
		Interface: codexMarketplaceInterface{DisplayName: "Oro Local"},
		Plugins: []codexMarketplacePlugin{{
			Name:     "oro",
			Source:   codexMarketplaceSource{Source: "local", Path: "./plugins/oro"},
			Policy:   codexMarketplacePolicy{Installation: "AVAILABLE", Authentication: "ON_INSTALL"},
			Category: "Productivity",
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal codex marketplace manifest: %w", err)
	}

	pluginContent, err := marshalCodexAsset(codexPluginManifest{
		Name:        "oro",
		Version:     "0.1.0",
		Description: "Oro workflow guidance, hooks, and task orchestration support for Codex.",
		Author: codexPluginAuthor{
			Name: "Oro",
			URL:  "https://github.com/aakashshah/oro",
		},
		Homepage:   "https://github.com/aakashshah/oro",
		Repository: "https://github.com/aakashshah/oro",
		License:    "MIT",
		Keywords:   []string{"oro", "agents", "workflow", "task-orchestration"},
		Skills:     "./skills/",
		Interface: codexPluginInterface{
			DisplayName:      "Oro",
			ShortDescription: "Workflow guidance and hooks for Oro-backed Codex sessions",
			LongDescription:  "Oro adds portable workflow guidance, task execution discipline, and project hooks for Codex sessions that participate in Oro-managed work.",
			DeveloperName:    "Oro",
			Category:         "Productivity",
			Capabilities:     []string{"Interactive", "Read", "Write"},
			WebsiteURL:       "https://github.com/aakashshah/oro",
			DefaultPrompt: []string{
				"Continue my Oro task",
				"Review the active Oro worktree",
				"Run the Oro quality gate",
			},
			Screenshots: []string{},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal codex plugin manifest: %w", err)
	}

	hooksContent, err := marshalCodexAsset(codexHooksFile{Hooks: g.Hooks(hooksDir)})
	if err != nil {
		return nil, fmt.Errorf("marshal codex hooks: %w", err)
	}

	return []RuleAsset{
		{Source: "codex/marketplace.json", Target: codexMarketplaceManifestTarget, Content: marketplaceContent},
		{Source: "codex/plugin.json", Target: codexPluginManifestTarget, Content: pluginContent},
		{Source: "codex/hooks.json", Target: codexHooksTarget, Content: hooksContent},
	}, nil
}

// Hooks returns the portable hooks wiring for a Codex hooks.json file.
func (g CodexGenerator) Hooks(hooksDir string) map[string][]HookGroup {
	py := func(s string) string { return "python3 " + path.Join(hooksDir, s) }
	sh := func(s string) string { return path.Join(hooksDir, s) }

	return map[string][]HookGroup{
		"SessionStart": {{
			Matcher: "",
			Hooks:   []HookSpec{{Type: "command", Command: py("session_start_global.py")}},
		}},
		"PreToolUse": {{
			Matcher: "Bash",
			Hooks:   []HookSpec{{Type: "command", Command: py("enforce_skills.py")}},
		}},
		"PostToolUse": {
			{Matcher: "Bash", Hooks: []HookSpec{
				{Type: "command", Command: py("prompt_injection_guard.py")},
				{Type: "command", Command: py("context_pruner.py")},
			}},
			{Matcher: "apply_patch", Hooks: []HookSpec{
				{Type: "command", Command: sh("auto-format.sh")},
			}},
		},
		"UserPromptSubmit": {{
			Matcher: "",
			Hooks:   []HookSpec{{Type: "command", Command: py("enforce_skills.py")}},
		}},
		"Stop": {{
			Matcher: "",
			Hooks:   []HookSpec{{Type: "command", Command: sh("stop-checklist.sh")}},
		}},
	}
}

func marshalCodexAsset(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	return append(data, '\n'), nil
}

// InstallCodexPluginPackage writes a Codex local marketplace package below targetDir.
func InstallCodexPluginPackage(ctx context.Context, targetDir string, assets []RuleAsset) error {
	allow := make(map[string]bool, len(assets))
	for _, asset := range assets {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("install codex plugin package: %w", err)
		}

		cleanTarget, err := cleanCodexPluginTarget(asset.Target)
		if err != nil {
			return err
		}
		allow[cleanTarget] = true

		destPath := filepath.Join(targetDir, filepath.FromSlash(cleanTarget))
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil { //nolint:gosec // plugin package files must be readable
			return fmt.Errorf("create codex plugin dir: %w", err)
		}
		if err := writeCodexAssetFile(destPath, asset.Content); err != nil {
			return fmt.Errorf("write %s: %w", cleanTarget, err)
		}
	}

	return removeStaleCodexPluginFiles(targetDir, allow)
}

func writeCodexAssetFile(destPath string, content []byte) error {
	existing, err := os.ReadFile(destPath) //nolint:gosec // path is validated by cleanCodexPluginTarget
	if err == nil && bytes.Equal(existing, content) {
		if info, statErr := os.Stat(destPath); statErr == nil && info.Mode().Perm() == 0o644 {
			return nil
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing codex asset: %w", err)
	}
	if err := os.WriteFile(destPath, content, 0o644); err != nil { //nolint:gosec // plugin package files must be readable
		return fmt.Errorf("write codex asset: %w", err)
	}
	return nil
}

func removeStaleCodexPluginFiles(targetDir string, allow map[string]bool) error {
	managedDirs := []string{
		filepath.Dir(codexMarketplaceManifestTarget),
		codexPluginRoot,
		filepath.Dir(codexPluginManifestTarget),
	}
	for _, managedDir := range managedDirs {
		dirPath := filepath.Join(targetDir, filepath.FromSlash(managedDir))
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read codex plugin dir %s: %w", managedDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			cleanTarget := path.Join(managedDir, entry.Name())
			if allow[cleanTarget] || path.Ext(entry.Name()) != ".json" {
				continue
			}
			if err := os.Remove(filepath.Join(dirPath, entry.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale codex plugin file %s: %w", cleanTarget, err)
			}
		}
	}
	return nil
}

func cleanCodexPluginTarget(target string) (string, error) {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) {
		return "", fmt.Errorf("codex plugin target %q escapes marketplace root", target)
	}

	cleanTarget := path.Clean(filepath.ToSlash(target))
	if strings.HasPrefix(cleanTarget, "../") || cleanTarget == ".." {
		return "", fmt.Errorf("codex plugin target %q escapes marketplace root", target)
	}

	switch cleanTarget {
	case codexMarketplaceManifestTarget, codexPluginManifestTarget, codexHooksTarget:
		return cleanTarget, nil
	default:
		if strings.HasPrefix(cleanTarget, codexPluginRoot+"/skills/") {
			return cleanTarget, nil
		}
		return "", fmt.Errorf("codex plugin target %q is not part of the Oro plugin package", target)
	}
}

type codexMarketplaceManifest struct {
	Name      string                    `json:"name"`
	Interface codexMarketplaceInterface `json:"interface"`
	Plugins   []codexMarketplacePlugin  `json:"plugins"`
}

type codexMarketplaceInterface struct {
	DisplayName string `json:"displayName"`
}

type codexMarketplacePlugin struct {
	Name     string                 `json:"name"`
	Source   codexMarketplaceSource `json:"source"`
	Policy   codexMarketplacePolicy `json:"policy"`
	Category string                 `json:"category"`
}

type codexMarketplaceSource struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type codexMarketplacePolicy struct {
	Installation   string `json:"installation"`
	Authentication string `json:"authentication"`
}

type codexPluginManifest struct {
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	Author      codexPluginAuthor    `json:"author"`
	Homepage    string               `json:"homepage"`
	Repository  string               `json:"repository"`
	License     string               `json:"license"`
	Keywords    []string             `json:"keywords"`
	Skills      string               `json:"skills"`
	Interface   codexPluginInterface `json:"interface"`
}

type codexPluginAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type codexPluginInterface struct {
	DisplayName      string   `json:"displayName"`
	ShortDescription string   `json:"shortDescription"`
	LongDescription  string   `json:"longDescription"`
	DeveloperName    string   `json:"developerName"`
	Category         string   `json:"category"`
	Capabilities     []string `json:"capabilities"`
	WebsiteURL       string   `json:"websiteURL"`
	DefaultPrompt    []string `json:"defaultPrompt"`
	Screenshots      []string `json:"screenshots"`
}

type codexHooksFile struct {
	Hooks map[string][]HookGroup `json:"hooks"`
}
