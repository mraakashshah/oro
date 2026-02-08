package main

import (
	"strings"
	"testing"
)

func TestManagerPrompt(t *testing.T) {
	prompt := ManagerPrompt()

	t.Run("prompt includes bd ready loop", func(t *testing.T) {
		if !strings.Contains(prompt, "bd ready") {
			t.Error("expected manager prompt to contain 'bd ready' for finding available work")
		}
	})

	t.Run("prompt includes quality gate check", func(t *testing.T) {
		qualityTerms := []string{"test", "lint"}
		for _, term := range qualityTerms {
			if !strings.Contains(strings.ToLower(prompt), term) {
				t.Errorf("expected manager prompt to reference quality gate %q", term)
			}
		}
	})

	t.Run("prompt includes never-stop instruction", func(t *testing.T) {
		lower := strings.ToLower(prompt)
		hasNeverStop := strings.Contains(lower, "never stop") ||
			strings.Contains(lower, "do not stop") ||
			strings.Contains(lower, "continuously") ||
			strings.Contains(lower, "continuous loop")
		if !hasNeverStop {
			t.Error("expected manager prompt to contain a never-stop / continuous loop instruction")
		}
	})

	t.Run("prompt includes bd close for completing beads", func(t *testing.T) {
		if !strings.Contains(prompt, "bd close") {
			t.Error("expected manager prompt to contain 'bd close' for finishing beads")
		}
	})

	t.Run("prompt is non-empty and substantial", func(t *testing.T) {
		if len(prompt) < 200 {
			t.Errorf("expected manager prompt to be substantial (>200 chars), got %d chars", len(prompt))
		}
	})

	t.Run("prompt includes dispatch or execute instruction", func(t *testing.T) {
		lower := strings.ToLower(prompt)
		hasExecute := strings.Contains(lower, "execute") ||
			strings.Contains(lower, "dispatch") ||
			strings.Contains(lower, "pick")
		if !hasExecute {
			t.Error("expected manager prompt to instruct picking/executing/dispatching beads")
		}
	})
}
