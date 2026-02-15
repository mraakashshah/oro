package langprofile

import (
	"fmt"
	"strings"
)

// Config represents the .oro/config.yaml structure.
type Config struct {
	Languages map[string]LanguageConfig `yaml:"languages"`
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
