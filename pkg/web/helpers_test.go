package web_test

import (
	"testing"
	"time"

	"oro/pkg/web"
)

func TestStatusSymbol(t *testing.T) {
	fm := web.TemplateFuncMap()
	fn := fm["statusSymbol"].(func(string) string)

	tests := []struct {
		status string
		want   string
	}{
		{"open", "♪"},
		{"in_progress", "●"},
		{"blocked", "⊘"},
		{"closed", "✓"},
		{"unknown_status", "unknown_status"},
		{"", ""},
	}
	for _, tt := range tests {
		got := fn(tt.status)
		if got != tt.want {
			t.Errorf("statusSymbol(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestHeatColor(t *testing.T) {
	fm := web.TemplateFuncMap()
	fn := fm["heatColor"].(func(string) string)

	now := time.Now()

	// 3 days old → heat-green
	recent := now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	if got := fn(recent); got != "heat-green" {
		t.Errorf("heatColor(3d ago) = %q, want heat-green", got)
	}

	// 7 days old → heat-green (boundary)
	sevenDays := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	if got := fn(sevenDays); got != "heat-green" {
		t.Errorf("heatColor(7d ago) = %q, want heat-green", got)
	}

	// 8 days old → heat-gold
	eightDays := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	if got := fn(eightDays); got != "heat-gold" {
		t.Errorf("heatColor(8d ago) = %q, want heat-gold", got)
	}

	// 14 days old → heat-gold
	twoWeeks := now.Add(-14 * 24 * time.Hour).Format(time.RFC3339)
	if got := fn(twoWeeks); got != "heat-gold" {
		t.Errorf("heatColor(14d ago) = %q, want heat-gold", got)
	}

	// 21 days old → heat-gold (boundary)
	twentyOne := now.Add(-21 * 24 * time.Hour).Format(time.RFC3339)
	if got := fn(twentyOne); got != "heat-gold" {
		t.Errorf("heatColor(21d ago) = %q, want heat-gold", got)
	}

	// 22 days old → heat-red
	twentyTwo := now.Add(-22 * 24 * time.Hour).Format(time.RFC3339)
	if got := fn(twentyTwo); got != "heat-red" {
		t.Errorf("heatColor(22d ago) = %q, want heat-red", got)
	}

	// unparseable → heat-green
	if got := fn("not-a-time"); got != "heat-green" {
		t.Errorf("heatColor(bad) = %q, want heat-green", got)
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
