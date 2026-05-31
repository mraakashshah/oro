package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"oro/pkg/protocol"
)

// TemplateFuncMap returns a template.FuncMap with dashboard helpers.
func TemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"statusSymbol":      statusSymbol,
		"epicProgressBar":   epicProgressBar,
		"plainStatus":       plainStatus,
		"titleFor":          titleFor,
		"escalationSubtype": escalationSubtype,
		"relativeTime":      relativeTime,
		"truncTitle":        truncTitle,
		"eventSymbol":       eventSymbol,
		"eventSymbolClass":  eventSymbolClass,
		"eventSummary":      eventSummary,
		"contextClass":      contextClass,
		"heartbeatClass":    heartbeatClass,
		"heartbeatLabel":    heartbeatLabel,
	}
}

func statusSymbol(status string) string {
	return plainStatus(status)
}

func epicProgressBar(closed, total int) string {
	const segments = 8
	if total <= 0 {
		return strings.Repeat("░", segments)
	}
	filled := int(math.Round(float64(closed) / float64(total) * segments))
	if filled < 0 {
		filled = 0
	}
	if filled > segments {
		filled = segments
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", segments-filled)
}

func plainStatus(s string) string {
	if s == "" {
		return ""
	}
	words := strings.Fields(strings.ReplaceAll(strings.ToLower(s), "_", " "))
	if len(words) == 0 {
		return ""
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

func titleFor(m map[string]string, id string) string {
	if title := m[id]; title != "" {
		return title
	}
	return id
}

func escalationSubtype(payload string) string {
	var data struct {
		Subtype protocol.EscalationType `json:"subtype"`
	}
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return ""
	}
	return string(data.Subtype)
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

func eventSummary(e protocol.Event, titles map[string]string) string {
	label := eventLabel(e.BeadID, "bead", titles)
	epicLabel := eventLabel(e.BeadID, "epic", titles)
	switch e.Type {
	case "merged":
		return fmt.Sprintf("merged %s", label)
	case "quality_gate_rejected":
		return fmt.Sprintf("quality gate rejected %s", label)
	case "merge_conflict":
		return fmt.Sprintf("merge conflict on %s", label)
	case "qg_stuck_detected":
		return fmt.Sprintf("quality gate stuck on %s", label)
	case "handoff":
		return fmt.Sprintf("handoff for %s", label)
	case "escalation":
		return fmt.Sprintf("escalation for %s", label)
	case "epic_acceptance_passed":
		return fmt.Sprintf("epic acceptance passed for %s", epicLabel)
	case "epic_acceptance_failed":
		return fmt.Sprintf("epic acceptance failed for %s", epicLabel)
	default:
		if e.BeadID != "" {
			return fmt.Sprintf("%s %s", e.Type, titleFor(titles, e.BeadID))
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

func eventLabel(id, fallback string, titles map[string]string) string {
	if id == "" {
		return fallback
	}
	return titleFor(titles, id)
}
