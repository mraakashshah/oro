package dispatcher //nolint:testpackage // white-box test needs access to llmEstimator fields

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// anthropicResponse mimics the subset of the Anthropic Messages API response we use.
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func TestEstimateBeadMinutes(t *testing.T) {
	t.Run("calls haiku model with system prompt asking for integer 1-30", func(t *testing.T) {
		var capturedModel string
		var capturedSystem string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if m, ok := body["model"].(string); ok {
				capturedModel = m
			}
			if s, ok := body["system"].(string); ok {
				capturedSystem = s
			}
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "12"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		e := &llmEstimator{
			apiKey:  "test-key",
			client:  srv.Client(),
			baseURL: srv.URL,
		}

		got := e.Estimate(context.Background(), "Some bead title", "Some acceptance criteria")
		if got != 12 {
			t.Errorf("expected 12, got %d", got)
		}
		if !strings.Contains(capturedModel, "haiku") {
			t.Errorf("expected haiku model, got %q", capturedModel)
		}
		if !strings.Contains(capturedSystem, "1") || !strings.Contains(capturedSystem, "30") {
			t.Errorf("system prompt should mention integer range 1-30, got %q", capturedSystem)
		}
	})

	t.Run("returns 0 for empty title", func(t *testing.T) {
		e := &llmEstimator{
			apiKey:  "test-key",
			client:  &http.Client{},
			baseURL: "http://unused.invalid",
		}
		got := e.Estimate(context.Background(), "", "some acceptance")
		if got != 0 {
			t.Errorf("expected 0 for empty title, got %d", got)
		}
	})

	t.Run("returns 0 when apiKey is empty (no-op for missing ANTHROPIC_API_KEY)", func(t *testing.T) {
		e := &llmEstimator{
			apiKey:  "",
			client:  &http.Client{},
			baseURL: "http://unused.invalid",
		}
		got := e.Estimate(context.Background(), "Some title", "Some acceptance")
		if got != 0 {
			t.Errorf("expected 0 for empty apiKey, got %d", got)
		}
	})

	t.Run("returns 0 on API error (5xx)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}))
		defer srv.Close()

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL}
		got := e.Estimate(context.Background(), "Some title", "Some acceptance")
		if got != 0 {
			t.Errorf("expected 0 on 500 error, got %d", got)
		}
	})

	t.Run("returns 0 on API error (429 rate limit)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		}))
		defer srv.Close()

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL}
		got := e.Estimate(context.Background(), "Some title", "Some acceptance")
		if got != 0 {
			t.Errorf("expected 0 on rate limit error, got %d", got)
		}
	})

	t.Run("returns 0 on unparseable response (text not a number)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "about fifteen minutes"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL}
		got := e.Estimate(context.Background(), "Some title", "Some acceptance")
		if got != 0 {
			t.Errorf("expected 0 for non-numeric response, got %d", got)
		}
	})

	t.Run("returns 0 on context already cancelled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Should not be reached because ctx is already done
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "5"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL}
		got := e.Estimate(ctx, "Some title", "Some acceptance")
		if got != 0 {
			t.Errorf("expected 0 for cancelled context, got %d", got)
		}
	})

	t.Run("respects 5s internal timeout — slow server returns 0", func(t *testing.T) {
		// The estimator must impose a 5s timeout internally.
		// Use a server that delays longer than 5s; verify Estimate returns 0.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Second):
			}
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "3"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		// Use a client with no timeout — the estimator's internal 5s timeout must kick in.
		e := &llmEstimator{
			apiKey:  "test-key",
			client:  srv.Client(),
			baseURL: srv.URL,
		}
		start := time.Now()
		got := e.Estimate(context.Background(), "Some title", "Some acceptance")
		elapsed := time.Since(start)

		if got != 0 {
			t.Errorf("expected 0 when server is slow (timeout), got %d", got)
		}
		// Verify it did time out (took at least a tiny bit, but not 10s).
		// A 5s timeout means elapsed should be between ~4.9s and ~6s.
		if elapsed > 7*time.Second {
			t.Errorf("Estimate took %v — expected to return within 7s via internal 5s timeout", elapsed)
		}
	})

	t.Run("parses numeric response correctly (trims whitespace)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "  7  \n"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer srv.Close()

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL}
		got := e.Estimate(context.Background(), "Implement feature X", "Tests pass")
		if got != 7 {
			t.Errorf("expected 7, got %d", got)
		}
	})
}
