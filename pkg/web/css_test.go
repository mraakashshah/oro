package web_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCSSContainsKeySelectors verifies that the CSS stylesheet exists and contains
// the required selectors, dark theme background, and Mardi Gras accent colors.
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
		".bead-string",
		"@keyframes shimmer",
	}
	for _, sel := range selectors {
		if !contains(css, sel) {
			t.Errorf("style.css missing selector %q", sel)
		}
	}

	// Dark background — #0A0A0B or similar (at minimum check for dark hex starting with #0)
	darkColors := []string{"#0A0A0B", "#0a0a0b"}
	foundDark := false
	for _, c := range darkColors {
		if contains(css, c) {
			foundDark = true
			break
		}
	}
	if !foundDark {
		t.Errorf("style.css missing dark background color (#0A0A0B)")
	}

	// Mardi Gras accent colors: purple, gold, green
	mardiGrasColors := []string{
		// purple variants
		"#9B59B6", "#9b59b6",
		// gold variants
		"#F1C40F", "#f1c40f",
		// green variants
		"#2ECC71", "#2ecc71",
	}
	type colorCheck struct {
		name     string
		variants []string
	}
	checks := []colorCheck{
		{"purple", []string{"#9B59B6", "#9b59b6"}},
		{"gold", []string{"#F1C40F", "#f1c40f"}},
		{"green", []string{"#2ECC71", "#2ecc71"}},
	}
	for _, check := range checks {
		found := false
		for _, v := range check.variants {
			if contains(css, v) {
				found = true
				break
			}
		}
		_ = mardiGrasColors // used above
		if !found {
			t.Errorf("style.css missing Mardi Gras %s accent color", check.name)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
