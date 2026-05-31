package web_test

import (
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/web"
)

func TestStatusSymbol(t *testing.T) {
	fm := web.TemplateFuncMap()
	fn := fm["statusSymbol"].(func(string) string)

	tests := []struct {
		status string
		want   string
	}{
		{"open", "Open"},
		{"in_progress", "In progress"},
		{"blocked", "Blocked"},
		{"closed", "Closed"},
		{"unknown_status", "Unknown status"},
		{"", ""},
	}
	for _, tt := range tests {
		got := fn(tt.status)
		if got != tt.want {
			t.Errorf("statusSymbol(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestEpicAndStatusHelpers(t *testing.T) {
	fm := web.TemplateFuncMap()
	if _, ok := fm["heatColor"]; ok {
		t.Fatal("TemplateFuncMap exposes obsolete heatColor helper")
	}

	epicProgressBar := fm["epicProgressBar"].(func(int, int) string)
	bar := epicProgressBar(7, 9)
	if len([]rune(bar)) != 8 {
		t.Fatalf("epicProgressBar(7, 9) length = %d, want 8 segments; bar=%q", len([]rune(bar)), bar)
	}
	filled := 0
	for _, r := range bar {
		if r == '█' {
			filled++
		}
	}
	if filled < 6 || filled > 7 {
		t.Fatalf("epicProgressBar(7, 9) filled segments = %d, want 6-7; bar=%q", filled, bar)
	}

	plainStatus := fm["plainStatus"].(func(string) string)
	if got := plainStatus("in_progress"); got != "In progress" {
		t.Errorf("plainStatus(in_progress) = %q, want In progress", got)
	}
	if got := plainStatus("surprise_state"); got != "Surprise state" {
		t.Errorf("plainStatus(surprise_state) = %q, want Surprise state", got)
	}

	titleFor := fm["titleFor"].(func(map[string]string, string) string)
	if got := titleFor(map[string]string{"oro-x": "Add cards show"}, "oro-x"); got != "Add cards show" {
		t.Errorf("titleFor(existing) = %q, want Add cards show", got)
	}
	if got := titleFor(map[string]string{}, "oro-y"); got != "oro-y" {
		t.Errorf("titleFor(missing) = %q, want oro-y", got)
	}

	escalationSubtype := fm["escalationSubtype"].(func(string) string)
	if got := escalationSubtype(`{"subtype":"STUCK"}`); got != "STUCK" {
		t.Errorf("escalationSubtype(valid) = %q, want STUCK", got)
	}
	if got := escalationSubtype(`{"subtype":`); got != "" {
		t.Errorf("escalationSubtype(malformed) = %q, want empty", got)
	}
}

func TestRelativeTime(t *testing.T) {
	fm := web.TemplateFuncMap()
	fn := fm["relativeTime"].(func(string) string)

	now := time.Now()

	// seconds ago
	secAgo := now.Add(-45 * time.Second).Format(time.RFC3339)
	got := fn(secAgo)
	if got != "45s ago" {
		t.Errorf("relativeTime(45s ago) = %q, want %q", got, "45s ago")
	}

	// minutes ago
	minAgo := now.Add(-10 * time.Minute).Format(time.RFC3339)
	got = fn(minAgo)
	if got != "10m ago" {
		t.Errorf("relativeTime(10m ago) = %q, want %q", got, "10m ago")
	}

	// hours ago
	hourAgo := now.Add(-3 * time.Hour).Format(time.RFC3339)
	got = fn(hourAgo)
	if got != "3h ago" {
		t.Errorf("relativeTime(3h ago) = %q, want %q", got, "3h ago")
	}

	// days ago
	dayAgo := now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	got = fn(dayAgo)
	if got != "5d ago" {
		t.Errorf("relativeTime(5d ago) = %q, want %q", got, "5d ago")
	}

	// unparseable → return raw string
	raw := "not-a-time"
	got = fn(raw)
	if got != raw {
		t.Errorf("relativeTime(bad) = %q, want raw %q", got, raw)
	}
}

func TestTruncTitle(t *testing.T) {
	fm := web.TemplateFuncMap()
	fn := fm["truncTitle"].(func(string, int) string)

	// shorter than max → no truncation
	if got := fn("hello", 10); got != "hello" {
		t.Errorf("truncTitle short = %q, want hello", got)
	}

	// exactly max → no truncation
	if got := fn("hello", 5); got != "hello" {
		t.Errorf("truncTitle exact = %q, want hello", got)
	}

	// longer than max → truncate with ellipsis
	if got := fn("hello world", 5); got != "hello…" {
		t.Errorf("truncTitle long = %q, want hello…", got)
	}

	// max <= 0 → return full string
	if got := fn("hello", 0); got != "hello" {
		t.Errorf("truncTitle max=0 = %q, want hello", got)
	}
	if got := fn("hello", -1); got != "hello" {
		t.Errorf("truncTitle max=-1 = %q, want hello", got)
	}
}

func TestEventHelpers(t *testing.T) {
	fm := web.TemplateFuncMap()
	symbol := fm["eventSymbol"].(func(string) string)
	symbolClass := fm["eventSymbolClass"].(func(string) string)
	summary := fm["eventSummary"].(func(protocol.Event, map[string]string) string)

	if got := symbol("merged"); got != "✓" {
		t.Errorf("eventSymbol(merged) = %q, want ✓", got)
	}
	if got := symbolClass("merge_conflict"); got != "event-feed__symbol--warn" {
		t.Errorf("eventSymbolClass(merge_conflict) = %q", got)
	}
	if got := summary(protocol.Event{Type: "handoff", BeadID: "oro-123"}, nil); got != "handoff for oro-123" {
		t.Errorf("eventSummary(handoff) = %q", got)
	}
}

func TestWorkerHelpers(t *testing.T) {
	fm := web.TemplateFuncMap()
	contextClass := fm["contextClass"].(func(int) string)
	heartbeatClass := fm["heartbeatClass"].(func(float64) string)
	heartbeatLabel := fm["heartbeatLabel"].(func(float64) string)

	if got := contextClass(85); got != "worker-row__context--danger" {
		t.Errorf("contextClass(85) = %q", got)
	}
	if got := heartbeatClass(35); got != "worker-row__heartbeat--warn" {
		t.Errorf("heartbeatClass(35) = %q", got)
	}
	if got := heartbeatLabel(0); got != "just now" {
		t.Errorf("heartbeatLabel(0) = %q", got)
	}
	if got := heartbeatLabel(5); got != "5s ago" {
		t.Errorf("heartbeatLabel(5) = %q", got)
	}
}
