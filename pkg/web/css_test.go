package web_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCSSContainsKeySelectors verifies that the CSS stylesheet exists and contains
// the required selectors and Linear-inspired visual tokens.
func TestCSSContainsKeySelectors(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file path")
	}
	cssPath := filepath.Join(filepath.Dir(thisFile), "static", "style.css")

	data, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q): %v", cssPath, err)
	}
	css := string(data)

	selectors := []string{
		"body",
		".parade",
		".sidebar",
		".bead-card",
		".worker-row",
		".bead-detail",
		".event-feed",
		".throughput",
	}
	for _, sel := range selectors {
		if !strings.Contains(css, sel) {
			t.Errorf("style.css missing selector %q", sel)
		}
	}

	tokens := []string{
		"--bg: #08090A",
		"--surface: #0E0F11",
		"--border: #1C1D20",
		"--text: #F7F8F8",
		"--text-2: #9CA0A8",
		"--text-3: #62666D",
		"--accent: #5E6AD2",
		"--green: #4CB782",
		"--amber: #E2A336",
		"--red: #EB5757",
	}
	for _, token := range tokens {
		if !strings.Contains(css, token) {
			t.Errorf("style.css missing visual token %q", token)
		}
	}

	forbidden := []string{
		"@keyframes shimmer",
		".bead-string",
		"#9B59B6", "#9b59b6",
		"#F1C40F", "#f1c40f",
		"#2ECC71", "#2ecc71",
	}
	for _, value := range forbidden {
		if strings.Contains(css, value) {
			t.Errorf("style.css contains removed token or selector %q", value)
		}
	}
}
