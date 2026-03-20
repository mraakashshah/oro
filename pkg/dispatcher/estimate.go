package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// estimatorModel is the Anthropic model used for bead complexity estimation.
// Haiku is chosen for speed and low cost.
const estimatorModel = "claude-haiku-4-5-20251001"

// estimatorBaseURL is the default Anthropic API endpoint.
const estimatorBaseURL = "https://api.anthropic.com"

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

// llmEstimator uses the Anthropic Messages API with the haiku model to estimate
// bead complexity in minutes.
type llmEstimator struct {
	apiKey  string
	client  *http.Client
	baseURL string // overrideable for testing; defaults to estimatorBaseURL
}

// NewBeadEstimator constructs a BeadEstimator backed by the Anthropic Messages API.
// Returns an estimator that always returns 0 when ANTHROPIC_API_KEY is not set.
func NewBeadEstimator() BeadEstimator {
	return &llmEstimator{
		apiKey:  os.Getenv("ANTHROPIC_API_KEY"),
		client:  &http.Client{},
		baseURL: estimatorBaseURL,
	}
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

// Estimate calls the Anthropic haiku model and returns estimated minutes for the bead.
// Returns 0 on empty title, missing API key, timeout, API error, or unparseable response.
func (e *llmEstimator) Estimate(ctx context.Context, title, acceptance string) int {
	if strings.TrimSpace(title) == "" || e.apiKey == "" {
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
		Model:     estimatorModel,
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
