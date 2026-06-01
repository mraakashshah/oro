package langprofile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoProjectRoot is returned by ReadConfig when projectRoot is empty.
var ErrNoProjectRoot = errors.New("projectRoot must not be empty")

// Config represents the .oro/config.yaml structure.
type Config struct {
	Languages map[string]LanguageConfig `yaml:"languages"`
	Memory    MemoryConfig              `yaml:"memory"`
}

// LanguageConfig holds the configuration for a single language.
type LanguageConfig struct {
	Formatters  []string `yaml:"formatters,omitempty"`
	Linters     []string `yaml:"linters,omitempty"`
	TestCmd     string   `yaml:"test_cmd,omitempty"`
	TypeCheck   string   `yaml:"type_check,omitempty"`
	Security    string   `yaml:"security,omitempty"`
	CodingRules []string `yaml:"coding_rules,omitempty"`
}

// MemoryConfig holds memory-related configuration nested under Config.
type MemoryConfig struct {
	Semantic SemanticMemoryConfig `yaml:"semantic"`
}

// SemanticMemoryConfig holds configuration for the semantic memory subsystem.
// Enabled and Rerank use *bool so that explicit false survives a round-trip
// through YAML (distinguishes "unset" from "explicitly false").
type SemanticMemoryConfig struct {
	Enabled   *bool  `yaml:"enabled,omitempty"`
	Rerank    *bool  `yaml:"rerank,omitempty"`
	ANNTopK   int    `yaml:"ann_top_k,omitempty"`
	FinalTopK int    `yaml:"final_top_k,omitempty"`
	ModelDir  string `yaml:"model_dir,omitempty"`
}

// EnabledOrDefault returns the Enabled value, defaulting to true when unset.
//
//oro:testonly
func (c *SemanticMemoryConfig) EnabledOrDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// RerankOrDefault returns the Rerank value, defaulting to true when unset.
//
//oro:testonly
func (c *SemanticMemoryConfig) RerankOrDefault() bool {
	if c.Rerank == nil {
		return true
	}
	return *c.Rerank
}

// RerankEnabled returns the production rerank flag. It defaults off so
// semantic reranking remains an explicit rollout opt-in.
func (c *SemanticMemoryConfig) RerankEnabled() bool {
	if c.Rerank == nil {
		return false
	}
	return *c.Rerank
}

// Defaults returns a Config with all fields set to their default values.
// Used when no config file is present.
func Defaults() *Config {
	return (&Config{}).WithDefaults()
}

// WithDefaults returns a copy of c with unset fields filled with default values.
// Explicit values (including false for *bool fields) are preserved.
func (c *Config) WithDefaults() *Config {
	out := *c

	homeDir, _ := os.UserHomeDir()
	defaultModelDir := filepath.Join(homeDir, ".oro", "models")

	sem := out.Memory.Semantic
	if sem.ANNTopK == 0 {
		sem.ANNTopK = 50
	}
	if sem.FinalTopK == 0 {
		sem.FinalTopK = 10
	}
	if sem.ModelDir == "" {
		sem.ModelDir = defaultModelDir
	} else {
		sem.ModelDir = expandTilde(sem.ModelDir, homeDir)
	}
	out.Memory.Semantic = sem

	return &out
}

// expandTilde replaces a leading "~" with the provided homeDir.
func expandTilde(path, homeDir string) string {
	if path == "~" {
		return homeDir
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

// GenerateConfig scans the project root, detects languages using the provided profiles,
// and returns a Config with resolved tool choices.
func GenerateConfig(projectRoot string, profiles []LangProfile) (*Config, error) {
	cfg := &Config{
		Languages: make(map[string]LanguageConfig),
	}

	for _, profile := range profiles {
		if !profile.Detect(projectRoot) {
			continue
		}

		langCfg := LanguageConfig{
			TestCmd:     profile.TestCmd,
			CodingRules: profile.CodingRules,
		}

		// Extract formatter names
		for _, f := range profile.Formatters {
			langCfg.Formatters = append(langCfg.Formatters, f.Name)
		}

		// Extract linter names
		for _, l := range profile.Linters {
			langCfg.Linters = append(langCfg.Linters, l.Name)
		}

		// Add optional tools
		if profile.TypeCheck != nil {
			langCfg.TypeCheck = profile.TypeCheck.Name
		}
		if profile.Security != nil {
			langCfg.Security = profile.Security.Name
		}

		cfg.Languages[profile.Language] = langCfg
	}

	return cfg, nil
}

// BuildYAML generates YAML content from the config.
func BuildYAML(cfg *Config) string {
	var content strings.Builder

	if len(cfg.Languages) == 0 {
		content.WriteString("# no languages detected in project root.\n")
		content.WriteString("# Run 'oro init' from your project directory to generate language profiles.\n")
		content.WriteString("languages: {}\n")
		return content.String()
	}

	content.WriteString("languages:\n")
	for lang, langCfg := range cfg.Languages {
		writeLanguageConfig(&content, lang, langCfg)
	}

	return content.String()
}

// ReadConfig loads .oro/config.yaml from projectRoot and returns the parsed Config.
// Returns nil,nil if the config file does not exist (graceful absence).
// Returns nil,ErrNoProjectRoot if projectRoot is empty.
// Returns nil,err if the YAML is malformed.
func ReadConfig(projectRoot string) (*Config, error) {
	if projectRoot == "" {
		return nil, ErrNoProjectRoot
	}
	return LoadConfig(filepath.Join(projectRoot, ".oro", "config.yaml"))
}

// LoadConfig loads and parses the oro config from an explicit file path.
// Returns nil,nil if the file does not exist (graceful absence).
// Returns nil,err if the file exists but the YAML is malformed.
func LoadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // configPath accepted from caller
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config.yaml: %w", err)
	}

	return &cfg, nil
}

// ResolveProjectRoot finds the real repo root from path, even when path is
// inside a git worktree. Uses git rev-parse --git-common-dir (which always
// points to the shared .git dir) rather than --show-toplevel (which returns
// the worktree root). If path is not in a git repo, returns path as-is.
func ResolveProjectRoot(path string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "--git-common-dir")
	cmd.Dir = path
	// Strip inherited git env vars (GIT_DIR etc.) so git detects the repo
	// from cmd.Dir rather than from the parent process context (e.g. hooks).
	cmd.Env = gitCleanEnv()
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo — return path unchanged (intentionally swallow error).
		return path, nil //nolint:nilerr // graceful absence: non-git paths return as-is
	}

	gitCommon := filepath.Clean(strings.TrimSpace(string(out)))
	// --git-common-dir returns a path relative to cmd.Dir when inside a worktree.
	// Resolve to absolute.
	if !filepath.IsAbs(gitCommon) {
		gitCommon = filepath.Join(path, gitCommon)
	}
	gitCommon = filepath.Clean(gitCommon)

	// .git dir lives at <repo_root>/.git — its parent is the real root.
	if filepath.Base(gitCommon) == ".git" {
		return filepath.Dir(gitCommon), nil
	}

	// Fallback: unexpected structure, return path as-is.
	return path, nil
}

// gitCleanEnv returns the current environment with git override variables removed.
// GIT_DIR, GIT_WORK_TREE, and GIT_COMMON_DIR can cause git to ignore cmd.Dir
// and instead use the parent process's git context (e.g. when running inside hooks).
func gitCleanEnv() []string {
	skip := map[string]bool{
		"GIT_DIR":        true,
		"GIT_WORK_TREE":  true,
		"GIT_COMMON_DIR": true,
	}
	env := os.Environ()
	out := env[:0]
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if !skip[key] {
			out = append(out, e)
		}
	}
	return out
}

// writeLanguageConfig writes a single language configuration to the builder.
func writeLanguageConfig(w *strings.Builder, lang string, cfg LanguageConfig) {
	fmt.Fprintf(w, "  %s:\n", lang)

	if len(cfg.Formatters) > 0 {
		w.WriteString("    formatters:\n")
		for _, f := range cfg.Formatters {
			fmt.Fprintf(w, "      - %s\n", f)
		}
	}

	if len(cfg.Linters) > 0 {
		w.WriteString("    linters:\n")
		for _, l := range cfg.Linters {
			fmt.Fprintf(w, "      - %s\n", l)
		}
	}

	if cfg.TestCmd != "" {
		fmt.Fprintf(w, "    test_cmd: %s\n", cfg.TestCmd)
	}

	if cfg.TypeCheck != "" {
		fmt.Fprintf(w, "    type_check: %s\n", cfg.TypeCheck)
	}

	if cfg.Security != "" {
		fmt.Fprintf(w, "    security: %s\n", cfg.Security)
	}

	if len(cfg.CodingRules) > 0 {
		w.WriteString("    coding_rules:\n")
		for _, rule := range cfg.CodingRules {
			fmt.Fprintf(w, "      - %s\n", rule)
		}
	}
}
