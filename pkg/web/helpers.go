package web

import (
	"fmt"
	"html/template"
	"time"

	"oro/pkg/protocol"
)

// TemplateFuncMap returns a template.FuncMap with dashboard helpers.
func TemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"statusSymbol":    statusSymbol,
		"heatColor":       heatColor,
		"relativeTime":    relativeTime,
		"truncTitle":      truncTitle,
		"eventSymbol":     eventSymbol,
		"eventSymbolClass": eventSymbolClass,
		"eventSummary":    eventSummary,
		"contextClass":    contextClass,
		"heartbeatClass":  heartbeatClass,
		"heartbeatLabel":  heartbeatLabel,
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

func eventSymbol(eventType string) string {
	switch eventType {
	case "merged", "epic_acceptance_passed":
		return "✓"
	case "quality_gate_rejected", "epic_acceptance_failed":
		return "✗"
	case "merge_conflict", "qg_stuck_detected":
		return "⚠"
	case "handoff":
		return "↻"
	case "escalation":
		return "▲"
	default:
		return "•"
	}
}

func eventSymbolClass(eventType string) string {
	switch eventType {
	case "merged", "epic_acceptance_passed":
		return "event-feed__symbol--ok"
	case "quality_gate_rejected", "epic_acceptance_failed":
		return "event-feed__symbol--fail"
	case "merge_conflict", "qg_stuck_detected":
		return "event-feed__symbol--warn"
	default:
		return "event-feed__symbol--info"
	}
}

func eventSummary(e protocol.Event) string {
	switch e.Type {
	case "merged":
		return fmt.Sprintf("merged %s", fallbackLabel(e.BeadID, "bead"))
	case "quality_gate_rejected":
		return fmt.Sprintf("quality gate rejected %s", fallbackLabel(e.BeadID, "bead"))
	case "merge_conflict":
		return fmt.Sprintf("merge conflict on %s", fallbackLabel(e.BeadID, "bead"))
	case "qg_stuck_detected":
		return fmt.Sprintf("quality gate stuck on %s", fallbackLabel(e.BeadID, "bead"))
	case "handoff":
		return fmt.Sprintf("handoff for %s", fallbackLabel(e.BeadID, "bead"))
	case "escalation":
		return fmt.Sprintf("escalation for %s", fallbackLabel(e.BeadID, "bead"))
	case "epic_acceptance_passed":
		return fmt.Sprintf("epic acceptance passed for %s", fallbackLabel(e.BeadID, "epic"))
	case "epic_acceptance_failed":
		return fmt.Sprintf("epic acceptance failed for %s", fallbackLabel(e.BeadID, "epic"))
	default:
		if e.BeadID != "" {
			return fmt.Sprintf("%s %s", e.Type, e.BeadID)
		}
		return e.Type
	}
}

func contextClass(pct int) string {
	switch {
	case pct >= 80:
		return "worker-row__context--danger"
	case pct >= 60:
		return "worker-row__context--warn"
	default:
		return ""
	}
}

func heartbeatClass(secs float64) string {
	switch {
	case secs > 30:
		return "worker-row__heartbeat--warn"
	case secs > 0:
		return "worker-row__heartbeat--ok"
	default:
		return ""
	}
}

func heartbeatLabel(secs float64) string {
	if secs <= 0 {
		return "just now"
	}
	return relativeTime(time.Now().Add(-time.Duration(secs * float64(time.Second))).Format(time.RFC3339))
}

func fallbackLabel(id, fallback string) string {
	if id == "" {
		return fallback
	}
	return id
}
