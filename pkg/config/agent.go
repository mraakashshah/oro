package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/protocol"

	"gopkg.in/yaml.v3"
)

// AgentConfig holds the agent runtime configuration: per-tier CLI settings,
// API-only model keys, and per-role overrides.
type AgentConfig struct {
	ProviderMode ProviderMode                 `yaml:"provider_mode,omitempty"`
	Tiers        map[protocol.Tier]TierConfig `yaml:"tiers,omitempty"`
	APIModels    map[string]string            `yaml:"api_models,omitempty"`
	Roles        map[string]RoleConfig        `yaml:"roles,omitempty"`
	Transport    TransportConfig              `yaml:"transport,omitempty"`
	Factory      FactoryConfig                `yaml:"factory,omitempty"`
}

// ProviderMode names a built-in provider routing preset.
type ProviderMode string

const (
	// ProviderModeCodexOnly routes coding and review roles to Codex.
	ProviderModeCodexOnly ProviderMode = "codex-only"
	// ProviderModeClaudeOnly routes coding and review roles to Claude.
	ProviderModeClaudeOnly ProviderMode = "claude-only"
	// ProviderModeCodexCodingClaudeReview routes coding roles to Codex and review roles to Claude.
	ProviderModeCodexCodingClaudeReview ProviderMode = "codex-coding-claude-review"
	// ProviderModeClaudeCodingCodexReview routes coding roles to Claude and review roles to Codex.
	ProviderModeClaudeCodingCodexReview ProviderMode = "claude-coding-codex-review"
)

// TierConfig specifies the runtime and model for a provider-neutral routing tier.
// Used by CLI-spawn roles (transport: cli).
type TierConfig struct {
	Runtime   string `yaml:"runtime"`
	Model     string `yaml:"model"`
	Reasoning string `yaml:"reasoning,omitempty"`
}

// RoleConfig specifies the routing configuration for a named role.
// CLI roles (transport: cli) resolve via Tier or explicit Runtime+Model.
// API roles (transport: api) resolve via Provider and APIModel.
type RoleConfig struct {
	Tier      protocol.Tier `yaml:"tier,omitempty"`
	Transport string        `yaml:"transport"`
	Runtime   string        `yaml:"runtime,omitempty"`
	Model     string        `yaml:"model,omitempty"`
	Reasoning string        `yaml:"reasoning,omitempty"`
	Provider  string        `yaml:"provider,omitempty"`
	APIModel  string        `yaml:"api_model,omitempty"`
}

// RoleRung identifies a model and reasoning level in a role escalation ladder.
type RoleRung struct {
	Model     string
	Reasoning string
}

// TransportConfig holds global transport-level settings (reserved for future use).
type TransportConfig struct{}

// configFile is the top-level YAML document wrapper used only for parsing.
type configFile struct {
	Agent   *AgentConfig       `yaml:"agent"`
	Storage *storagePolicyFile `yaml:"storage"`
	Factory *FactoryConfig     `yaml:"factory"`
}

func defaultAgentConfig() *AgentConfig {
	coding := oroCodexProfile()
	review := fableProfile()

	return &AgentConfig{
		Tiers:     tiersForProvider(coding),
		APIModels: map[string]string{},
		Roles:     rolesForProviderMode(coding, review),
		Factory:   defaultFactoryConfig(),
	}
}

// DefaultAgentConfig returns Oro's built-in agent runtime configuration.
func DefaultAgentConfig() *AgentConfig {
	return defaultAgentConfig()
}

// ApplyProviderMode expands cfg.ProviderMode into explicit tier and role
// routing. Unknown custom roles are preserved; known CLI roles are overwritten
// so the preset is authoritative and not diluted by stale role entries.
func ApplyProviderMode(cfg *AgentConfig) error {
	if cfg == nil || cfg.ProviderMode == "" {
		return nil
	}

	var coding providerProfile
	var review providerProfile
	switch cfg.ProviderMode {
	case ProviderModeCodexOnly:
		coding, review = codexProfile(), codexProfile()
	case ProviderModeClaudeOnly:
		coding, review = claudeProfile(), claudeProfile()
	case ProviderModeCodexCodingClaudeReview:
		coding, review = oroCodexProfile(), fableProfile()
	case ProviderModeClaudeCodingCodexReview:
		coding, review = claudeProfile(), codexProfile()
	default:
		return fmt.Errorf("unknown provider_mode %q", cfg.ProviderMode)
	}

	cfg.Tiers = tiersForProvider(coding)
	if cfg.Roles == nil {
		cfg.Roles = make(map[string]RoleConfig)
	}
	for role, rc := range rolesForProviderMode(coding, review) {
		cfg.Roles[role] = rc
	}
	if cfg.ProviderMode != ProviderModeCodexCodingClaudeReview {
		cfg.Roles["estimator"] = RoleConfig{Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"}
		if cfg.APIModels == nil {
			cfg.APIModels = make(map[string]string)
		}
		if _, ok := cfg.APIModels["anthropic_fast"]; !ok {
			cfg.APIModels["anthropic_fast"] = "claude-haiku-4-5-20251001"
		}
	}
	return nil
}

type providerProfile struct {
	runtime             string
	fastModel           string
	balancedModel       string
	deepModel           string
	backgroundModel     string
	fastReasoning       string
	balancedReasoning   string
	deepReasoning       string
	backgroundReasoning string
	specReasoning       string
	escalationReasoning string
	challengeReasoning  string
	opsReviewModel      string
	opsReviewReasoning  string
}

func codexProfile() providerProfile {
	return oroCodexProfile()
}

func oroCodexProfile() providerProfile {
	return providerProfile{
		runtime:             "codex",
		fastModel:           "gpt-5.6-luna",
		balancedModel:       "gpt-5.6-terra",
		deepModel:           "gpt-5.6-sol",
		backgroundModel:     "gpt-5.6-luna",
		fastReasoning:       "low",
		balancedReasoning:   "medium",
		deepReasoning:       "low",
		backgroundReasoning: "low",
		specReasoning:       "medium",
		escalationReasoning: "low",
		challengeReasoning:  "high",
	}
}

func claudeProfile() providerProfile {
	return providerProfile{
		runtime:         "claude",
		fastModel:       "claude-haiku-4-5-20251001",
		balancedModel:   "claude-sonnet-4-6",
		deepModel:       "claude-opus-4-7",
		backgroundModel: "claude-haiku-4-5-20251001",
	}
}

func fableProfile() providerProfile {
	return providerProfile{
		runtime:            "claude",
		fastModel:          "fable",
		balancedModel:      "fable",
		deepModel:          "fable",
		backgroundModel:    "fable",
		deepReasoning:      "xhigh",
		challengeReasoning: "medium",
		opsReviewModel:     "claude-opus-4-8",
		opsReviewReasoning: "high",
	}
}

func tiersForProvider(p providerProfile) map[protocol.Tier]TierConfig {
	return map[protocol.Tier]TierConfig{
		protocol.TierFast:       tierConfig(p.runtime, p.fastModel, p.fastReasoning),
		protocol.TierBalanced:   tierConfig(p.runtime, p.balancedModel, p.balancedReasoning),
		protocol.TierDeep:       tierConfig(p.runtime, p.deepModel, p.deepReasoning),
		protocol.TierBackground: tierConfig(p.runtime, p.backgroundModel, p.backgroundReasoning),
	}
}

func rolesForProviderMode(coding, review providerProfile) map[string]RoleConfig {
	opsReviewModel := firstNonEmpty(review.opsReviewModel, review.deepModel)
	opsReviewReasoning := firstNonEmpty(review.opsReviewReasoning, review.deepReasoning)
	roles := map[string]RoleConfig{
		"grade":                   roleConfig(coding.runtime, coding.balancedModel, coding.fastReasoning),
		"spec_writer":             roleConfig(coding.runtime, coding.deepModel, firstNonEmpty(coding.specReasoning, coding.deepReasoning)),
		"spec_challenger":         roleConfig(review.runtime, review.deepModel, firstNonEmpty(review.challengeReasoning, review.deepReasoning)),
		"worker":                  roleConfig(coding.runtime, coding.balancedModel, coding.balancedReasoning),
		"worker_escalation":       roleConfig(coding.runtime, coding.deepModel, firstNonEmpty(coding.escalationReasoning, coding.deepReasoning)),
		"ops_review":              roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_triage":       roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_correctness":  roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_security":     roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_adversarial":  roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_design":       roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_test":         roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_review_architecture": roleConfig(review.runtime, opsReviewModel, opsReviewReasoning),
		"ops_escalation":          roleConfig(coding.runtime, coding.deepModel, coding.deepReasoning),
		"ops_merge":               roleConfig(coding.runtime, coding.deepModel, coding.deepReasoning),
		"ops_diagnosis":           roleConfig(coding.runtime, coding.deepModel, coding.deepReasoning),
		"ops_epic_fix":            roleConfig(coding.runtime, coding.deepModel, coding.deepReasoning),
		"ops_write_ac":            roleConfig(coding.runtime, coding.deepModel, coding.deepReasoning),
		"ops_decompose":           roleConfig(coding.runtime, coding.deepModel, coding.deepReasoning),
		"ops_dream":               {Tier: protocol.TierFast, Transport: "cli"},
		"memory_extractor":        {Tier: protocol.TierFast, Transport: "cli"},
		"codesearch_reranker":     {Tier: protocol.TierFast, Transport: "cli"},
		"estimator":               {Tier: protocol.TierFast, Transport: "cli"},
	}
	return roles
}

// GradeLadder returns the configured grade role and its two escalation rungs.
func GradeLadder(cfg AgentConfig) []RoleRung {
	defaults := defaultAgentConfig()
	grade := effectiveGradeRung(cfg, defaults)
	deep := effectiveTierRung(cfg, defaults, protocol.TierDeep)

	return []RoleRung{
		grade,
		deep,
		{Model: deep.Model, Reasoning: "xhigh"},
	}
}

func effectiveGradeRung(cfg AgentConfig, defaults *AgentConfig) RoleRung {
	grade, ok := cfg.Roles["grade"]
	if !ok {
		grade = defaults.Roles["grade"]
	}
	if grade.Runtime != "" && grade.Model != "" {
		return RoleRung{Model: grade.Model, Reasoning: grade.Reasoning}
	}

	tier := grade.Tier
	if !tier.IsKnown() {
		tier = protocol.DefaultTier
	}
	return effectiveTierRung(cfg, defaults, tier)
}

func effectiveTierRung(cfg AgentConfig, defaults *AgentConfig, tier protocol.Tier) RoleRung {
	tierCfg, ok := cfg.Tiers[tier]
	if !ok {
		tierCfg = defaults.Tiers[tier]
	}
	return RoleRung{Model: tierCfg.Model, Reasoning: tierCfg.Reasoning}
}

func tierConfig(runtime, model, reasoning string) TierConfig {
	return TierConfig{Runtime: runtime, Model: model, Reasoning: reasoning}
}

func roleConfig(runtime, model, reasoning string) RoleConfig {
	return RoleConfig{Transport: "cli", Runtime: runtime, Model: model, Reasoning: reasoning}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Load reads the YAML file at path and returns the parsed project config.
// When the file does not exist or the agent block is absent, built-in agent and
// local quality-gate defaults are returned. Parse errors and invalid explicit
// remote-gate configuration are surfaced to the caller.
func Load(path string) (*Config, error) {
	f, err := loadConfigFile(path)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return defaultAgentConfig(), nil
	}

	cfg := defaultAgentConfig()
	if f.Agent != nil {
		cfg = f.Agent
		if err := ApplyProviderMode(cfg); err != nil {
			return nil, fmt.Errorf("agent provider mode in %s: %w", path, err)
		}
	}
	if f.Factory != nil {
		cfg.Factory = *f.Factory
		if cfg.Factory.QualityGate.Mode == "" {
			cfg.Factory.QualityGate.Mode = RemoteGateModeLocal
		}
	}
	if cfg.Factory.Lifecycle.ManualIntegration && cfg.Factory.QualityGate.Mode == RemoteGateModeGitHubPR {
		return nil, fmt.Errorf("invalid remote gate config: github-pr mode is incompatible with manual integration")
	}
	if err := ValidateRemoteGateConfig(cfg.Factory.QualityGate); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadWithPrecedence reads an agent block from the highest-priority config
// layer that defines one. Agent configuration is project scoped, falling back
// to global config only when the project file is absent or does not contain an
// agent block.
//
// Precedence:
//  1. projectConfigPath, typically <repo>/.oro/config.yaml
//  2. $ORO_HOME/config.yaml, when ORO_HOME is set
//  3. ~/.oro/config.yaml
func LoadWithPrecedence(projectConfigPath string) (*AgentConfig, error) {
	for _, path := range agentConfigCandidates(projectConfigPath) {
		cfg, found, err := loadIfAgentBlock(path)
		if err != nil {
			return nil, err
		}
		if found {
			return cfg, nil
		}
	}
	return defaultAgentConfig(), nil
}

// HasAgentBlockWithPrecedence reports whether any config layer in precedence
// order defines an agent block.
func HasAgentBlockWithPrecedence(projectConfigPath string) bool {
	for _, path := range agentConfigCandidates(projectConfigPath) {
		_, found, err := loadIfAgentBlock(path)
		if err == nil && found {
			return true
		}
	}
	return false
}

func agentConfigCandidates(projectConfigPath string) []string {
	candidates := make([]string, 0, 3)
	if projectConfigPath != "" {
		candidates = append(candidates, projectConfigPath)
	}
	if oroHome := os.Getenv("ORO_HOME"); oroHome != "" {
		candidates = append(candidates, filepath.Join(oroHome, "config.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".oro", "config.yaml"))
	}
	return candidates
}

func loadIfAgentBlock(path string) (*AgentConfig, bool, error) {
	f, err := loadConfigFile(path)
	if err != nil || f == nil {
		return nil, false, err
	}

	if f.Agent == nil {
		return nil, false, nil
	}
	if err := ApplyProviderMode(f.Agent); err != nil {
		return nil, false, fmt.Errorf("agent provider mode in %s: %w", path, err)
	}
	return f.Agent, true, nil
}

func loadConfigFile(path string) (*configFile, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path accepted from caller
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var f configFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &f, nil
}

// Validate checks AgentConfig for invalid role definitions and
// cross-runtime model mismatches, returning a descriptive error naming
// every offender. A nil config is valid (callers fall back to defaults).
//
// Validation rules:
//   - CLI role overrides must be all-or-nothing: setting Runtime without
//     Model (or vice versa) is rejected.
//   - A tier or explicit role override whose model string belongs to a
//     different runtime than declared (e.g., runtime=codex with
//     model=claude-opus-4-7) is rejected.
func Validate(c *AgentConfig) error {
	if c == nil {
		return nil
	}

	var errs []string
	if msg := validateProviderMode(c.ProviderMode); msg != "" {
		errs = append(errs, msg)
	}

	for tier, tc := range c.Tiers {
		errs = append(errs, validateTier(tier, tc)...)
	}

	for name, role := range c.Roles {
		errs = append(errs, validateRole(name, role)...)
	}
	for _, rung := range GradeLadder(*c) {
		if msg := checkReasoning("grade ladder", c.Roles["grade"].Runtime, rung.Reasoning); msg != "" {
			errs = append(errs, msg)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid agent config:\n  %s", strings.Join(errs, "\n  "))
}

func validateProviderMode(mode ProviderMode) string {
	switch mode {
	case "", ProviderModeCodexOnly, ProviderModeClaudeOnly, ProviderModeCodexCodingClaudeReview, ProviderModeClaudeCodingCodexReview:
		return ""
	default:
		return fmt.Sprintf("provider_mode %q is invalid", mode)
	}
}

func validateTier(tier protocol.Tier, tc TierConfig) []string {
	label := fmt.Sprintf("tier %q", string(tier))
	return validationMessages(label, tc.Runtime, tc.Model, tc.Reasoning)
}

func validateRole(name string, role RoleConfig) []string {
	if role.Transport != "cli" {
		if role.Transport == "api" && role.Tier != "" {
			return []string{fmt.Sprintf("role %q: API role must not set tier; use provider and api_model", name)}
		}
		return nil
	}

	if msg := checkPartialCLIOverride(name, role); msg != "" {
		return []string{msg}
	}
	label := fmt.Sprintf("role %q", name)
	return validationMessages(label, role.Runtime, role.Model, role.Reasoning)
}

func validationMessages(label, runtime, model, reasoning string) []string {
	var errs []string
	if msg := checkRuntimeModelMatch(label, runtime, model); msg != "" {
		errs = append(errs, msg)
	}
	if msg := checkReasoning(label, runtime, reasoning); msg != "" {
		errs = append(errs, msg)
	}
	return errs
}

func checkPartialCLIOverride(name string, role RoleConfig) string {
	hasRuntime := role.Runtime != ""
	hasModel := role.Model != ""
	if hasRuntime == hasModel {
		return ""
	}
	missing := "runtime"
	if hasRuntime {
		missing = "model"
	}
	return fmt.Sprintf("role %q: CLI override is partial — %s is set but %s is missing; set both or neither", name, roleSetField(role), missing)
}

func checkReasoning(label, runtime, reasoning string) string {
	if reasoning == "" {
		return ""
	}
	if runtime != "codex" {
		return ""
	}
	switch reasoning {
	case "low", "medium", "high", "xhigh":
		return ""
	default:
		return fmt.Sprintf("%s: reasoning %q is invalid for codex; expected one of low, medium, high, xhigh", label, reasoning)
	}
}

// checkRuntimeModelMatch returns an error message when the declared runtime
// disagrees with the runtime inferred from the model string. An empty model,
// empty runtime, or unknown/legacy model name skips the check.
func checkRuntimeModelMatch(label, runtime, model string) string {
	if runtime == "" || model == "" {
		return ""
	}
	modelRT := inferModelRuntime(model)
	if modelRT == "" || modelRT == runtime {
		return ""
	}
	return fmt.Sprintf("%s: model %q belongs to runtime %q but %s declares runtime %q", label, model, modelRT, label, runtime)
}

// inferModelRuntime guesses which runtime a provider-native model string belongs to.
// Returns an empty string when the model string is ambiguous (tier alias, legacy name, or unknown).
func inferModelRuntime(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(lower, "claude") {
		return "claude"
	}
	if strings.Contains(lower, "gpt") || strings.Contains(lower, "codex") {
		return "codex"
	}
	return ""
}

// roleSetField returns the name of the field that IS set in a partial override.
func roleSetField(r RoleConfig) string {
	if r.Runtime != "" {
		return "runtime"
	}
	return "model"
}
