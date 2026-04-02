package web_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"oro/pkg/web"
)

// TestEmbedFS verifies that web.Content embeds the expected template and static files.
func TestEmbedFS(t *testing.T) {
	t.Run("templates directory contains expected files", func(t *testing.T) {
		entries, err := fs.ReadDir(web.Content, "templates")
		if err != nil {
			t.Fatalf("fs.ReadDir(web.Content, \"templates\"): %v", err)
		}
		want := map[string]bool{
			"index.html":      false,
			"parade.html":     false,
			"workers.html":    false,
			"detail.html":     false,
			"events.html":     false,
			"throughput.html": false,
		}
		for _, e := range entries {
			if _, ok := want[e.Name()]; ok {
				want[e.Name()] = true
			}
		}
		for name, found := range want {
			if !found {
				t.Errorf("templates/%s missing from web.Content embed.FS", name)
			}
		}
	})

	t.Run("static directory contains style.css and htmx.min.js", func(t *testing.T) {
		entries, err := fs.ReadDir(web.Content, "static")
		if err != nil {
			t.Fatalf("fs.ReadDir(web.Content, \"static\"): %v", err)
		}
		want := map[string]bool{
			"style.css":   false,
			"htmx.min.js": false,
		}
		for _, e := range entries {
			if _, ok := want[e.Name()]; ok {
				want[e.Name()] = true
			}
		}
		for name, found := range want {
			if !found {
				t.Errorf("static/%s missing from web.Content embed.FS", name)
			}
		}
	})
}

// TestStaticFileServing verifies that NewHandler serves static assets from the embed.FS.
func TestStaticFileServing(t *testing.T) {
	data := &mockDashboard{}
	h := web.NewHandler(data, web.Content)

	t.Run("GET /static/style.css returns 200 with text/css", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /static/style.css status = %d, want 200", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if len(ct) < 8 || ct[:8] != "text/css" {
			t.Errorf("Content-Type = %q, want text/css", ct)
		}
	})

	t.Run("GET /static/htmx.min.js returns 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /static/htmx.min.js status = %d, want 200", rec.Code)
		}
	})
}
