package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"oro/pkg/config"
)

// estimatorBaseURL is the default Anthropic API endpoint.
const estimatorBaseURL = "https://api.anthropic.com"

// estimatorConfigPath is the project-local config file read on startup.
const estimatorConfigPath = ".oro/config.yaml"

// estimatorTimeout is the hard deadline applied to every Estimate call.
const estimatorTimeout = 5 * time.Second

// estimatorMaxTokens caps the response to a short integer string.
const estimatorMaxTokens = 10

// estimateSystemPrompt instructs the model to return a single integer 1–30.
const estimateSystemPrompt = `You are a software task estimator. Given a task title and acceptance criteria, estimate how many minutes an experienced developer needs to complete it. Reply with a single integer between 1 and 30. No explanation, no other text — only the integer.`

// BeadEstimator estimates the complexity of a bead as a duration in minutes.
type BeadEstimator interface {
	Estimate(ctx context.Context, title, acceptance string) int
}

// llmEstimator uses the Anthropic Messages API to estimate bead complexity in minutes.
// The model is resolved from roles.estimator.api_model in the agent config.
type llmEstimator struct {
	apiKey  string
	client  *http.Client
	baseURL string // overrideable for testing; defaults to estimatorBaseURL
	model   string // resolved from config; empty means no estimate (zero return)
}

// NewBeadEstimator constructs a BeadEstimator backed by the Anthropic Messages API.
// The model is loaded from roles.estimator.api_model in the agent config; provider
// must be "anthropic". Returns an estimator that always returns 0 when
// ANTHROPIC_API_KEY is not set or a non-anthropic provider is configured.
func NewBeadEstimator() BeadEstimator {
	cfg := loadEstimatorConfig()
	return &llmEstimator{
		apiKey:  os.Getenv("ANTHROPIC_API_KEY"),
		client:  &http.Client{},
		baseURL: estimatorBaseURL,
		model:   resolveEstimatorModel(cfg),
	}
}

// loadEstimatorConfig reads the project config and merges estimator-relevant
// defaults so roles["estimator"] and api_models are always populated.
func loadEstimatorConfig() *config.AgentConfig {
	cfg, err := config.Load(estimatorConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return config.DefaultAgentConfig()
	}
	if cfg == nil {
		return config.DefaultAgentConfig()
	}
	return mergeEstimatorDefaults(cfg)
}

// mergeEstimatorDefaults ensures the estimator role and its api_models entries
// are present, falling back to built-in defaults when the user config omits them.
func mergeEstimatorDefaults(cfg *config.AgentConfig) *config.AgentConfig {
	defaults := config.DefaultAgentConfig()
	if cfg.Roles == nil {
		cfg.Roles = defaults.Roles
	} else if _, ok := cfg.Roles["estimator"]; !ok {
		cfg.Roles["estimator"] = defaults.Roles["estimator"]
	}
	if cfg.APIModels == nil {
		cfg.APIModels = defaults.APIModels
	} else {
		for k, v := range defaults.APIModels {
			if _, ok := cfg.APIModels[k]; !ok {
				cfg.APIModels[k] = v
			}
		}
	}
	return cfg
}

// resolveEstimatorModel returns the concrete model string for the estimator role.
// It reads roles["estimator"].api_model and looks it up in api_models.
// Returns empty string if the role is missing, provider is not "anthropic",
// or the api_model key is absent from api_models.
func resolveEstimatorModel(cfg *config.AgentConfig) string {
	if cfg == nil {
		return ""
	}
	role, ok := cfg.Roles["estimator"]
	if !ok || role.Provider != "anthropic" || role.APIModel == "" {
		return ""
	}
	model, ok := cfg.APIModels[role.APIModel]
	if !ok {
		return ""
	}
	return model
}

// estimateRequest is the Anthropic Messages API request body.
type estimateRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    string            `json:"system"`
	Messages  []estimateMessage `json:"messages"`
}

type estimateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// estimateResponse holds the subset of the Anthropic Messages API response used here.
type estimateResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Estimate calls the Anthropic API and returns estimated minutes for the bead.
// Returns 0 on empty title, missing API key, unconfigured model, timeout, API error,
// or unparseable response.
func (e *llmEstimator) Estimate(ctx context.Context, title, acceptance string) int {
	if strings.TrimSpace(title) == "" || e.apiKey == "" || e.model == "" {
		return 0
	}

	ctx, cancel := context.WithTimeout(ctx, estimatorTimeout)
	defer cancel()

	n, err := e.callAPI(ctx, title, acceptance)
	if err != nil {
		return 0
	}

	return n
}

func (e *llmEstimator) callAPI(ctx context.Context, title, acceptance string) (int, error) { //nolint:cyclop // linear error-check chain; splitting would obscure flow
	data, err := json.Marshal(estimateRequest{
		Model:     e.model,
		MaxTokens: estimatorMaxTokens,
		System:    estimateSystemPrompt,
		Messages: []estimateMessage{
			{Role: "user", Content: fmt.Sprintf("Title: %s\nAcceptance criteria: %s", title, acceptance)},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("marshal estimate request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, //nolint:gosec // G107: baseURL is set from trusted construction-time config, not from user input
		e.baseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("create estimate request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", e.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := e.client.Do(req) //nolint:gosec // G704: request URL is set from trusted construction-time config, not from user input
	if err != nil {
		return 0, fmt.Errorf("call estimate API: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("estimate API status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read estimate response: %w", err)
	}

	var apiResp estimateResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return 0, fmt.Errorf("unmarshal estimate response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return 0, fmt.Errorf("empty content in estimate response")
	}

	text := strings.TrimSpace(apiResp.Content[0].Text)

	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse estimate %q: %w", text, err)
	}

	return n, nil
}
