package web

import (
	"fmt"
	"html/template"
	"time"
)

// TemplateFuncMap returns a template.FuncMap with dashboard helpers.
func TemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"statusSymbol": statusSymbol,
		"heatColor":    heatColor,
		"relativeTime": relativeTime,
		"truncTitle":   truncTitle,
	}
}

func statusSymbol(status string) string {
	switch status {
	case "open":
		return "♪"
	case "in_progress":
		return "●"
	case "blocked":
		return "⊘"
	case "closed":
		return "✓"
	default:
		return status
	}
}

func heatColor(createdAt string) string {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return "heat-green"
	}
	age := time.Since(t)
	days := int(age.Hours() / 24)
	switch {
	case days <= 7:
		return "heat-green"
	case days <= 21:
		return "heat-gold"
	default:
		return "heat-red"
	}
}

func relativeTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func truncTitle(s string, maxChars int) string {
	if maxChars <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + "…"
}
