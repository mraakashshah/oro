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
		".epics",
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

func TestCSSOverflowSafety(t *testing.T) {
	css := readDashboardCSS(t)

	layout := cssRule(t, css, ".layout")
	assertDeclaration(t, layout, "grid-template-columns", "minmax(0, 1fr) 1fr")

	for _, selector := range []string{
		".bead-card__title",
		".worker-row__bead",
		".event-feed__text",
		".epic-title",
	} {
		rule := cssRule(t, css, selector)
		assertDeclaration(t, rule, "text-overflow", "ellipsis")
		assertRuleOrFlexParentDeclaration(t, css, selector, "min-width", "0")
	}

	detailAC := cssRule(t, css, ".detail-ac")
	assertDeclaration(t, detailAC, "overflow-wrap", "anywhere")
	assertDeclaration(t, detailAC, "white-space", "pre-wrap")

	detailWrap := cssRule(t, css, ".detail-wrap")
	assertDeclaration(t, detailWrap, "overflow-wrap", "anywhere")
	assertDeclaration(t, detailWrap, "white-space", "pre-wrap")
}

func readDashboardCSS(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file path")
	}
	cssPath := filepath.Join(filepath.Dir(thisFile), "static", "style.css")

	data, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q): %v", cssPath, err)
	}
	return string(data)
}

func cssRule(t *testing.T, css, selector string) map[string]string {
	t.Helper()

	start := strings.Index(css, selector+" {")
	if start < 0 {
		t.Fatalf("style.css missing rule for %s", selector)
	}
	bodyStart := strings.Index(css[start:], "{")
	if bodyStart < 0 {
		t.Fatalf("style.css malformed rule for %s", selector)
	}
	bodyStart += start + 1
	bodyEnd := strings.Index(css[bodyStart:], "}")
	if bodyEnd < 0 {
		t.Fatalf("style.css malformed rule for %s", selector)
	}

	rule := make(map[string]string)
	for _, declaration := range strings.Split(css[bodyStart:bodyStart+bodyEnd], ";") {
		name, value, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}
		rule[strings.TrimSpace(name)] = strings.Join(strings.Fields(value), " ")
	}
	return rule
}

func assertDeclaration(t *testing.T, rule map[string]string, name, want string) {
	t.Helper()

	if got := rule[name]; got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func assertRuleOrFlexParentDeclaration(t *testing.T, css, selector, name, want string) {
	t.Helper()

	rule := cssRule(t, css, selector)
	if rule[name] == want {
		return
	}
	parent := selectorParent(selector)
	if parent != "" {
		parentRule := cssRule(t, css, parent)
		if parentRule["display"] == "flex" && parentRule[name] == want {
			return
		}
	}
	t.Fatalf("%s must declare %s:%s on itself or on flex parent %s", selector, name, want, parent)
}

func selectorParent(selector string) string {
	base, _, ok := strings.Cut(selector, "__")
	if !ok {
		return ""
	}
	return base
}
