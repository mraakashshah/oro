package codesearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
)

// Embedder produces vector embeddings from text.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)
}

// OllamaEmbedder calls a local Ollama HTTP API to produce embeddings.
type OllamaEmbedder struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaEmbedder creates an embedder that calls the Ollama API at baseURL.
//
//oro:testonly
func NewOllamaEmbedder(baseURL, model string) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{},
	}
}

// ollamaEmbedRequest is the JSON body for the Ollama /api/embed endpoint.
type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// ollamaEmbedResponse is the JSON response from the Ollama /api/embed endpoint.
type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// Embed produces a single embedding vector for the given text.
func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	reqBody := ollamaEmbedRequest{
		Model: o.model,
		Input: text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed request failed (is Ollama running?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed returned status %d", resp.StatusCode)
	}

	var embedResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}

	if len(embedResp.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama returned empty embeddings")
	}

	return embedResp.Embeddings[0], nil
}

// EmbedBatch produces embedding vectors for multiple texts by calling Embed sequentially.
func (o *OllamaEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	results := make([][]float64, len(texts))
	for i, text := range texts {
		vec, err := o.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("embed batch item %d: %w", i, err)
		}
		results[i] = vec
	}
	return results, nil
}

// MockEmbedder is a test double that returns deterministic embeddings.
// It produces unit vectors based on a hash of the input text.
type MockEmbedder struct {
	Dims int
}

// Embed returns a deterministic unit vector for the given text.
func (m *MockEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	return deterministicVector(text, m.Dims), nil
}

// EmbedBatch returns deterministic unit vectors for each text.
func (m *MockEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float64, error) {
	results := make([][]float64, len(texts))
	for i, text := range texts {
		results[i] = deterministicVector(text, m.Dims)
	}
	return results, nil
}

// deterministicVector produces a unit vector from a text string.
// Uses a simple hash to seed values, then normalizes to unit length.
func deterministicVector(text string, dims int) []float64 {
	vec := make([]float64, dims)
	h := uint64(0)
	for _, c := range text {
		h = h*31 + uint64(c)
	}
	for i := range dims {
		h = h*6364136223846793005 + 1442695040888963407     // LCG
		vec[i] = (float64(h)/float64(math.MaxUint64))*2 - 1 // map [0, MaxUint64] to [-1, 1]
	}
	// Normalize to unit vector.
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}
