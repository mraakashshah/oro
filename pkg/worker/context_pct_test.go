package worker_test

import (
	"testing"

	"oro/pkg/worker"
)

func TestContextPct(t *testing.T) {
	t.Parallel()

	// Claude shim: parses {"event":"turn_end","context_pct":68}
	t.Run("claude/found", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseClaudeContextPct([]byte(`{"event":"turn_end","context_pct":68}`))
		if !ok {
			t.Fatal("ParseClaudeContextPct: expected ok=true, got false")
		}
		if pct != 68.0 {
			t.Errorf("ParseClaudeContextPct: want 68.0, got %v", pct)
		}
	})

	t.Run("claude/fractional", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseClaudeContextPct([]byte(`{"event":"turn_end","context_pct":45.5}`))
		if !ok {
			t.Fatal("ParseClaudeContextPct: expected ok=true for fractional value")
		}
		if pct != 45.5 {
			t.Errorf("ParseClaudeContextPct: want 45.5, got %v", pct)
		}
	})

	t.Run("claude/wrong_event_type_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseClaudeContextPct([]byte(`{"event":"turn_start","context_pct":68}`))
		if ok {
			t.Fatal("ParseClaudeContextPct: expected ok=false for non-turn_end event")
		}
		if pct != 100.0 {
			t.Errorf("ParseClaudeContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("claude/missing_context_pct_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseClaudeContextPct([]byte(`{"event":"turn_end"}`))
		if ok {
			t.Fatal("ParseClaudeContextPct: expected ok=false when context_pct absent")
		}
		if pct != 100.0 {
			t.Errorf("ParseClaudeContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("claude/non_json_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseClaudeContextPct([]byte(`not json at all`))
		if ok {
			t.Fatal("ParseClaudeContextPct: expected ok=false for non-JSON")
		}
		if pct != 100.0 {
			t.Errorf("ParseClaudeContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("claude/empty_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseClaudeContextPct([]byte(``))
		if ok {
			t.Fatal("ParseClaudeContextPct: expected ok=false for empty input")
		}
		if pct != 100.0 {
			t.Errorf("ParseClaudeContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	// Codex shim: parses {"context_pct":55} from plain-text JSON lines
	t.Run("codex/found", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseCodexContextPct([]byte(`{"context_pct":55}`))
		if !ok {
			t.Fatal("ParseCodexContextPct: expected ok=true, got false")
		}
		if pct != 55.0 {
			t.Errorf("ParseCodexContextPct: want 55.0, got %v", pct)
		}
	})

	t.Run("codex/fractional", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseCodexContextPct([]byte(`{"context_pct":33.3}`))
		if !ok {
			t.Fatal("ParseCodexContextPct: expected ok=true for fractional value")
		}
		if pct != 33.3 {
			t.Errorf("ParseCodexContextPct: want 33.3, got %v", pct)
		}
	})

	t.Run("codex/missing_field_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseCodexContextPct([]byte(`{"status":"running"}`))
		if ok {
			t.Fatal("ParseCodexContextPct: expected ok=false when context_pct absent")
		}
		if pct != 100.0 {
			t.Errorf("ParseCodexContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("codex/plain_text_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseCodexContextPct([]byte(`Applying changes to foo.go`))
		if ok {
			t.Fatal("ParseCodexContextPct: expected ok=false for plain-text line")
		}
		if pct != 100.0 {
			t.Errorf("ParseCodexContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("codex/empty_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseCodexContextPct([]byte(``))
		if ok {
			t.Fatal("ParseCodexContextPct: expected ok=false for empty input")
		}
		if pct != 100.0 {
			t.Errorf("ParseCodexContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	// Gemini shim: parses {"context_pct":42} from plain-text JSON lines
	t.Run("gemini/found", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseGeminiContextPct([]byte(`{"context_pct":42}`))
		if !ok {
			t.Fatal("ParseGeminiContextPct: expected ok=true, got false")
		}
		if pct != 42.0 {
			t.Errorf("ParseGeminiContextPct: want 42.0, got %v", pct)
		}
	})

	t.Run("gemini/fractional", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseGeminiContextPct([]byte(`{"context_pct":77.7}`))
		if !ok {
			t.Fatal("ParseGeminiContextPct: expected ok=true for fractional value")
		}
		if pct != 77.7 {
			t.Errorf("ParseGeminiContextPct: want 77.7, got %v", pct)
		}
	})

	t.Run("gemini/missing_field_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseGeminiContextPct([]byte(`{"event":"turn_end"}`))
		if ok {
			t.Fatal("ParseGeminiContextPct: expected ok=false when context_pct absent")
		}
		if pct != 100.0 {
			t.Errorf("ParseGeminiContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("gemini/non_json_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseGeminiContextPct([]byte(`not json`))
		if ok {
			t.Fatal("ParseGeminiContextPct: expected ok=false for non-JSON")
		}
		if pct != 100.0 {
			t.Errorf("ParseGeminiContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})

	t.Run("gemini/empty_falls_back", func(t *testing.T) {
		t.Parallel()
		pct, ok := worker.ParseGeminiContextPct([]byte(``))
		if ok {
			t.Fatal("ParseGeminiContextPct: expected ok=false for empty input")
		}
		if pct != 100.0 {
			t.Errorf("ParseGeminiContextPct: missing-signal fallback want 100.0, got %v", pct)
		}
	})
}

func TestContextPctFromLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		format worker.StreamFormat
		line   string
		want   float64
		ok     bool
	}{
		{"claude routes to ParseClaudeContextPct", worker.StreamFormatClaudeJSON, `{"event":"turn_end","context_pct":42}`, 42.0, true},
		{"gemini routes to ParseGeminiContextPct", worker.StreamFormatGeminiJSON, `{"context_pct":55}`, 55.0, true},
		{"line-text falls through to Codex parser", worker.StreamFormatLineText, `{"context_pct":33}`, 33.0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := worker.ContextPctFromLine(tc.format, []byte(tc.line))
			if ok != tc.ok || got != tc.want {
				t.Errorf("ContextPctFromLine(%v, %q) = (%v, %v), want (%v, %v)", tc.format, tc.line, got, ok, tc.want, tc.ok)
			}
		})
	}
}
