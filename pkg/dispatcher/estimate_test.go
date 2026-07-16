package dispatcher //nolint:testpackage // white-box test needs access to llmEstimator fields

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/config"
	"oro/pkg/protocol"
)

func TestNewBeadEstimatorUsesSubscriptionCLIByDefault(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	binDir := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORO_HOME", filepath.Join(home, ".oro-test"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CODEX_HOME", codexHome)
	t.Chdir(project)
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"tokens":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(codexHome, "plugins", "unsafe"), 0o755); err != nil {
		t.Fatal(err)
	}

	oroDir := filepath.Join(project, ".oro")
	if err := os.MkdirAll(oroDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("agent: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	argsPath := filepath.Join(t.TempDir(), "codex-args.txt")
	pwdPath := filepath.Join(t.TempDir(), "codex-pwd.txt")
	homePath := filepath.Join(t.TempDir(), "codex-home.txt")
	t.Setenv("ESTIMATOR_ARGS_PATH", argsPath)
	t.Setenv("ESTIMATOR_PWD_PATH", pwdPath)
	t.Setenv("ESTIMATOR_HOME_PATH", homePath)
	fakeCodex := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ESTIMATOR_ARGS_PATH\"\npwd > \"$ESTIMATOR_PWD_PATH\"\nfind \"$CODEX_HOME\" -mindepth 1 -maxdepth 2 -print > \"$ESTIMATOR_HOME_PATH\"\nprintf 'harmless diagnostic\\n' >&2\nprintf '7\\n'\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := NewBeadEstimator().Estimate(context.Background(), "Implement routing", "Tests pass")
	if got != 7 {
		t.Fatalf("default estimator = %d, want 7 from subscription CLI", got)
	}

	args, err := os.ReadFile(argsPath) //nolint:gosec // test-owned temporary path
	if err != nil {
		t.Fatalf("read fake Codex args: %v", err)
	}
	for _, want := range []string{
		"--model\ngpt-5.6-luna", "--sandbox\nread-only", "--ephemeral", "--ignore-user-config",
		"--disable\nplugins", "--disable\nshell_tool", "--disable\nunified_exec",
		`model_reasoning_effort="low"`, "Implement routing", "Tests pass",
	} {
		if !strings.Contains(string(args), want) {
			t.Errorf("Codex args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "danger-full-access") {
		t.Errorf("estimator must not use danger-full-access:\n%s", args)
	}
	pwd, err := os.ReadFile(pwdPath) //nolint:gosec // test-owned temporary path
	if err != nil {
		t.Fatalf("read fake Codex cwd: %v", err)
	}
	if got := strings.TrimSpace(string(pwd)); got == project || !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("estimator cwd = %q, want isolated temp dir outside project %q", got, project)
	}
	isolatedHome, err := os.ReadFile(homePath) //nolint:gosec // test-owned temporary path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(isolatedHome), "plugins") || strings.Contains(string(isolatedHome), codexHome) {
		t.Errorf("estimator inherited user Codex home contents:\n%s", isolatedHome)
	}
	if !strings.Contains(string(isolatedHome), "auth.json") {
		t.Errorf("isolated estimator home missing subscription auth:\n%s", isolatedHome)
	}
}

func TestNewBeadEstimatorClaudeCLIDisablesTools(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORO_HOME", filepath.Join(home, ".oro-test"))
	t.Chdir(project)

	oroDir := filepath.Join(project, ".oro")
	if err := os.MkdirAll(oroDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configYAML := `agent:
  roles:
    estimator:
      transport: cli
      runtime: claude
      model: fable
`
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	argsPath := filepath.Join(t.TempDir(), "claude-args.txt")
	t.Setenv("ESTIMATOR_ARGS_PATH", argsPath)
	fakeClaude := filepath.Join(binDir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ESTIMATOR_ARGS_PATH\"\nprintf 'harmless diagnostic\\n' >&2\nprintf '9\\n'\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := NewBeadEstimator().Estimate(context.Background(), "Review scope", "No side effects")
	if got != 9 {
		t.Fatalf("Claude CLI estimator = %d, want 9", got)
	}
	args, err := os.ReadFile(argsPath) //nolint:gosec // test-owned temporary path
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--safe-mode", "--tools\n\n", "--disable-slash-commands", "--no-session-persistence", "--strict-mcp-config"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("Claude args missing %q:\n%s", want, args)
		}
	}
}

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
			model:   "claude-haiku-4-5-20251001",
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

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL, model: "claude-haiku-4-5-20251001"}
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

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL, model: "claude-haiku-4-5-20251001"}
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

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL, model: "claude-haiku-4-5-20251001"}
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

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL, model: "claude-haiku-4-5-20251001"}
		got := e.Estimate(ctx, "Some title", "Some acceptance")
		if got != 0 {
			t.Errorf("expected 0 for cancelled context, got %d", got)
		}
	})

	t.Run("respects 5s internal timeout — slow server returns 0", func(t *testing.T) {
		// The estimator must impose a 5s timeout internally.
		// Use a server that delays longer than 5s; verify Estimate returns 0.
		// handlerExit is closed in t.Cleanup (before srv.Close) so the handler
		// exits promptly and httptest.Server.Close() does not block for 5s.
		handlerExit := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				return
			case <-handlerExit:
				return
			case <-time.After(30 * time.Second):
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
		t.Cleanup(func() {
			close(handlerExit)
			srv.Close()
		})

		// Use a client with no timeout — the estimator's internal 5s timeout must kick in.
		e := &llmEstimator{
			apiKey:  "test-key",
			client:  srv.Client(),
			baseURL: srv.URL,
			model:   "claude-haiku-4-5-20251001",
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

		e := &llmEstimator{apiKey: "test-key", client: srv.Client(), baseURL: srv.URL, model: "claude-haiku-4-5-20251001"}
		got := e.Estimate(context.Background(), "Implement feature X", "Tests pass")
		if got != 7 {
			t.Errorf("expected 7, got %d", got)
		}
	})
}

func TestEstimatorReadsAPIModelsBlock(t *testing.T) {
	t.Run("uses model resolved from api_models via roles.estimator.api_model", func(t *testing.T) {
		cfg := &config.AgentConfig{
			APIModels: map[string]string{
				"my_estimator_key": "claude-custom-haiku",
			},
			Roles: map[string]config.RoleConfig{
				"estimator": {Transport: "api", Provider: "anthropic", APIModel: "my_estimator_key"},
			},
		}

		var capturedModel string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if m, ok := body["model"].(string); ok {
				capturedModel = m
			}
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "5"},
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
			model:   resolveEstimatorModel(cfg),
		}

		got := e.Estimate(context.Background(), "Some title", "Some criteria")
		if got != 5 {
			t.Errorf("expected 5, got %d", got)
		}
		if capturedModel != "claude-custom-haiku" {
			t.Errorf("expected model %q from api_models, got %q", "claude-custom-haiku", capturedModel)
		}
	})

	t.Run("rejects cross-provider model — non-anthropic provider yields empty model", func(t *testing.T) {
		cfg := &config.AgentConfig{
			APIModels: map[string]string{
				"gpt_key": "gpt-4o",
			},
			Roles: map[string]config.RoleConfig{
				"estimator": {Transport: "api", Provider: "openai", APIModel: "gpt_key"},
			},
		}

		model := resolveEstimatorModel(cfg)
		if model != "" {
			t.Errorf("expected empty model for non-anthropic provider, got %q", model)
		}
	})

	t.Run("returns empty model when api_model key missing from api_models", func(t *testing.T) {
		cfg := &config.AgentConfig{
			APIModels: map[string]string{},
			Roles: map[string]config.RoleConfig{
				"estimator": {Transport: "api", Provider: "anthropic", APIModel: "missing_key"},
			},
		}

		model := resolveEstimatorModel(cfg)
		if model != "" {
			t.Errorf("expected empty model for missing api_model key, got %q", model)
		}
	})
}

func TestEstimatorIgnoresTierChanges(t *testing.T) {
	t.Run("tiers.fast change does not affect estimator model", func(t *testing.T) {
		// tiers.fast is changed to opus; estimator must still use haiku from api_models.
		cfg := &config.AgentConfig{
			Tiers: map[protocol.Tier]config.TierConfig{
				protocol.TierFast: {Runtime: "claude", Model: "claude-opus-4-7"},
			},
			APIModels: map[string]string{
				"anthropic_fast": "claude-haiku-4-5-20251001",
			},
			Roles: map[string]config.RoleConfig{
				"estimator": {Transport: "api", Provider: "anthropic", APIModel: "anthropic_fast"},
			},
		}

		var capturedModel string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if m, ok := body["model"].(string); ok {
				capturedModel = m
			}
			resp := anthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "10"},
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
			model:   resolveEstimatorModel(cfg),
		}

		got := e.Estimate(context.Background(), "Some title", "Some criteria")
		if got != 10 {
			t.Errorf("expected 10, got %d", got)
		}
		if capturedModel != "claude-haiku-4-5-20251001" {
			t.Errorf("tier change should not affect estimator: expected haiku, got %q (tiers.fast=%q)",
				capturedModel, cfg.Tiers[protocol.TierFast].Model)
		}
	})
}
