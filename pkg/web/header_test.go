package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"oro/pkg/web"
)

func TestHeaderHealthyVsDegraded(t *testing.T) {
	t.Run("healthy renders throughput in header", func(t *testing.T) {
		data := &mockDashboard{
			throughput: &web.ThroughputData{
				BeadsPerHour:  7,
				CostPerHour:   "$1.25",
				ActiveWorkers: 3,
				TotalWorkers:  5,
				Uptime:        "2h 14m",
			},
		}
		h := web.NewHandler(data, web.Content)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Healthy", "7 beads/hr", "$1.25 cost/hr", "3/5 workers", "2h 14m uptime"} {
			if !strings.Contains(body, want) {
				t.Errorf("healthy header missing %q; body:\n%s", want, body)
			}
		}
		if strings.Contains(body, "Needs you") {
			t.Errorf("healthy header should not render degraded status; body:\n%s", body)
		}
	})

	t.Run("degraded renders issue and suppresses throughput line", func(t *testing.T) {
		data := &mockDashboard{
			healthErr: errors.New("database unreachable"),
			throughput: &web.ThroughputData{
				BeadsPerHour:  7,
				CostPerHour:   "$1.25",
				ActiveWorkers: 3,
				TotalWorkers:  5,
				Uptime:        "2h 14m",
			},
		}
		h := web.NewHandler(data, web.Content)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Needs you", "database unreachable"} {
			if !strings.Contains(body, want) {
				t.Errorf("degraded header missing %q; body:\n%s", want, body)
			}
		}
		for _, suppressed := range []string{"Healthy", "7 beads/hr", "$1.25 cost/hr", "3/5 workers", "2h 14m uptime"} {
			if strings.Contains(body, suppressed) {
				t.Errorf("degraded header should suppress %q; body:\n%s", suppressed, body)
			}
		}
	})
}

func TestHeaderFragmentHealthyVsDegraded(t *testing.T) {
	data := &mockDashboard{
		throughput: &web.ThroughputData{
			BeadsPerHour:  9,
			CostPerHour:   "$2.00",
			ActiveWorkers: 4,
			TotalWorkers:  6,
			Uptime:        "3h",
		},
	}
	h := web.NewHandler(data, web.Content)

	req := httptest.NewRequest(http.MethodGet, "/fragments/header", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fragments/header status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Healthy", "9 beads/hr", "$2.00 cost/hr", "4/6 workers", "3h uptime"} {
		if !strings.Contains(body, want) {
			t.Errorf("healthy header fragment missing %q; body:\n%s", want, body)
		}
	}

	data.healthErr = errors.New("worker heartbeat stale")
	req = httptest.NewRequest(http.MethodGet, "/fragments/header", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fragments/header degraded status = %d, want 200", rec.Code)
	}
	body = rec.Body.String()
	for _, want := range []string{"Needs you", "worker heartbeat stale"} {
		if !strings.Contains(body, want) {
			t.Errorf("degraded header fragment missing %q; body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "9 beads/hr") {
		t.Errorf("degraded header fragment should suppress throughput; body:\n%s", body)
	}
}
