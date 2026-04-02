package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// truncate returns s truncated to at most n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// freeAddr returns a random free TCP address on localhost.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free addr: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestHTTPServerStartsInRun verifies the HTTP server lifecycle within Run().
func TestHTTPServerStartsInRun(t *testing.T) {
	t.Run("WebEnabled=true starts httpServer via safeGo", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.WebEnabled = true
		d.cfg.WebAddr = freeAddr(t)

		cancel := startDispatcher(t, d)
		defer cancel()

		waitFor(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.httpServer != nil
		}, 2*time.Second)

		d.mu.Lock()
		srv := d.httpServer
		d.mu.Unlock()
		if srv == nil {
			t.Fatal("expected httpServer to be set when WebEnabled=true")
		}
	})

	t.Run("GET /healthz returns 200 when state=running", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		addr := freeAddr(t)
		d.cfg.WebEnabled = true
		d.cfg.WebAddr = addr

		cancel := startDispatcher(t, d)
		defer cancel()

		// Wait for server to be reachable.
		waitFor(t, func() bool {
			resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx // test-only convenience
			if err != nil {
				return false
			}
			resp.Body.Close()
			return true
		}, 2*time.Second)

		// Before start directive: state=inert → not 200.
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("GET /healthz when state=inert = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
		}

		// Transition to running.
		sendDirective(t, d.cfg.SocketPath, "start")
		waitFor(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.state == StateRunning
		}, 2*time.Second)

		resp, err = http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			t.Fatalf("GET /healthz after start: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /healthz = %d, want 200 when state=running", resp.StatusCode)
		}
	})

	t.Run("WebEnabled=false skips HTTP goroutine entirely", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.WebEnabled = false

		cancel := startDispatcher(t, d)
		defer cancel()

		time.Sleep(100 * time.Millisecond)

		d.mu.Lock()
		srv := d.httpServer
		d.mu.Unlock()
		if srv != nil {
			t.Error("expected httpServer to be nil when WebEnabled=false")
		}
	})

	t.Run("shutdownWithTimeout calls httpServer.Shutdown before wg.Wait", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		addr := freeAddr(t)
		d.cfg.WebEnabled = true
		d.cfg.WebAddr = addr

		cancel := startDispatcher(t, d)

		// Wait for server to be reachable.
		waitFor(t, func() bool {
			resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
			if err != nil {
				return false
			}
			resp.Body.Close()
			return true
		}, 2*time.Second)

		// Cancel triggers graceful shutdown via Run().
		cancel()

		// After shutdown, the HTTP server must no longer be accessible.
		waitFor(t, func() bool {
			resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
			if err != nil {
				return true
			}
			resp.Body.Close()
			return false
		}, 3*time.Second)

		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err == nil {
			resp.Body.Close()
			t.Error("expected HTTP server to be shut down after context cancel")
		}
	})

	t.Run("bind failure logs web_server_bind_failed and dispatcher continues", func(t *testing.T) {
		// Occupy the target port so the HTTP server cannot bind.
		blocker, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen blocker: %v", err)
		}
		defer blocker.Close()
		busyAddr := blocker.Addr().String()

		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.WebEnabled = true
		d.cfg.WebAddr = busyAddr

		cancel := startDispatcher(t, d)
		defer cancel()

		// Wait for the bind-failed event to appear in the DB.
		waitFor(t, func() bool {
			row := d.db.QueryRowContext(context.Background(),
				`SELECT 1 FROM events WHERE type = 'web_server_bind_failed' LIMIT 1`)
			var n int
			return row.Scan(&n) == nil
		}, 2*time.Second)

		// Dispatcher is still operational — can receive and respond to directives.
		sendDirective(t, d.cfg.SocketPath, "status")
	})
}

// TestHTTPServerServesDashboard verifies that startHTTPServer mounts web.NewHandler
// so that GET / returns HTML with <!DOCTYPE.
func TestHTTPServerServesDashboard(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	addr := freeAddr(t)
	d.cfg.WebEnabled = true
	d.cfg.WebAddr = addr

	cancel := startDispatcher(t, d)
	defer cancel()

	// Wait for server to be reachable.
	waitFor(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 2*time.Second)

	// Transition to running so the dispatcher is fully operational.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.state == StateRunning
	}, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<!DOCTYPE") {
		t.Errorf("GET / body missing <!DOCTYPE; got first 200 chars: %q", truncate(string(body), 200))
	}
}
