package components

import (
	"strings"
	"testing"
	"time"

	"oro/pkg/mg/data"
)

func TestNewFooterParadeBindings(t *testing.T) {
	f := NewFooter(80, false)
	if len(f.Bindings) != len(ParadeBindings) {
		t.Fatalf("expected %d bindings, got %d", len(ParadeBindings), len(f.Bindings))
	}
	for i, b := range f.Bindings {
		if b.Key != ParadeBindings[i].Key || b.Desc != ParadeBindings[i].Desc {
			t.Fatalf("binding %d: got {%s,%s}, want {%s,%s}", i, b.Key, b.Desc, ParadeBindings[i].Key, ParadeBindings[i].Desc)
		}
	}
}

func TestNewFooterDetailBindings(t *testing.T) {
	f := NewFooter(80, true)
	if len(f.Bindings) != len(DetailBindings) {
		t.Fatalf("expected %d bindings, got %d", len(DetailBindings), len(f.Bindings))
	}
	for i, b := range f.Bindings {
		if b.Key != DetailBindings[i].Key || b.Desc != DetailBindings[i].Desc {
			t.Fatalf("binding %d: got {%s,%s}, want {%s,%s}", i, b.Key, b.Desc, DetailBindings[i].Key, DetailBindings[i].Desc)
		}
	}
}

func TestBulkFooterContainsCount(t *testing.T) {
	output := BulkFooter(80, 5)
	if !strings.Contains(output, "5") {
		t.Fatal("BulkFooter output should contain the selection count")
	}
}

func TestBulkFooterNoGasTownNoSling(t *testing.T) {
	output := BulkFooter(80, 2)
	if strings.Contains(output, "sling") {
		t.Fatal("BulkFooter should not contain 'sling'")
	}
}

func TestFooterViewWithStoreContext(t *testing.T) {
	f := Footer{
		Width:       120,
		Bindings:    ParadeBindings,
		SourceMode:  data.SourceCLI,
		LastRefresh: time.Now(),
		StoreContext: &data.StoreContext{
			Database: "mardi_gras",
			Backend:  "sqlite",
		},
	}
	output := f.View()
	if !strings.Contains(output, "beadstore") || !strings.Contains(output, "(native)") {
		t.Fatalf("footer should identify native beadstore source, got: %s", output)
	}
	if legacyLabel := "bd" + " list"; strings.Contains(output, legacyLabel) {
		t.Fatalf("footer should not label native source with legacy list wording, got: %s", output)
	}
	if !strings.Contains(output, "mardi_gras/sqlite") {
		t.Fatalf("footer should contain database/backend, got: %s", output)
	}
}

func TestFooterViewWithStoreContextNoBackend(t *testing.T) {
	f := Footer{
		Width:       120,
		Bindings:    ParadeBindings,
		SourceMode:  data.SourceCLI,
		LastRefresh: time.Now(),
		StoreContext: &data.StoreContext{
			Database: "mardi_gras",
		},
	}
	output := f.View()
	if !strings.Contains(output, "mardi_gras") {
		t.Fatalf("footer should contain database name, got: %s", output)
	}
	if strings.Contains(output, "mardi_gras/") {
		t.Fatalf("footer should not have trailing slash without backend, got: %s", output)
	}
}

func TestFooterViewWithoutStoreContext(t *testing.T) {
	f := Footer{
		Width:       120,
		Bindings:    ParadeBindings,
		SourceMode:  data.SourceCLI,
		LastRefresh: time.Now(),
	}
	output := f.View()
	if legacyLabel := "bd" + " list"; strings.Contains(output, legacyLabel) {
		t.Fatalf("footer should not label native source with legacy list wording, got: %s", output)
	}
	if strings.Contains(output, "mardi_gras") {
		t.Fatalf("footer should not contain context info when nil, got: %s", output)
	}
}
