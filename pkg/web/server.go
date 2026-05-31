package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"oro/pkg/protocol"
)

// WorkerInfo summarises a live worker for the web dashboard.
type WorkerInfo struct {
	ID                string
	State             string
	BeadID            string
	ContextPct        int
	LastHeartbeatSecs float64
}

// ThroughputData holds metrics for the throughput panel.
type ThroughputData struct {
	BeadsPerHour  int
	ActiveWorkers int
	TotalWorkers  int
	Uptime        string
	CostPerHour   string
}

// DashboardData is the read-only query interface for the web handler.
// pkg/dispatcher.Dispatcher satisfies this interface.
type DashboardData interface {
	ReadyBeads(ctx context.Context) ([]protocol.Bead, error)
	InProgressBeads(ctx context.Context) ([]protocol.Bead, error)
	BlockedBeads(ctx context.Context) ([]protocol.Bead, error)
	ClosedBeads(ctx context.Context, limit int) ([]protocol.Bead, error)
	ShowBead(ctx context.Context, id string) (*protocol.BeadDetail, error)
	RecentEvents(ctx context.Context, limit int) ([]protocol.Event, error)
	Workers(ctx context.Context) ([]WorkerInfo, error)
	Throughput(ctx context.Context) (*ThroughputData, error)
	SubscribeSSE() chan string
	UnsubscribeSSE(ch chan string)
	// HealthError returns nil when the system is healthy, or a descriptive
	// error when the swarm is degraded. The index handler renders this in
	// the page so operators see problems immediately.
	HealthError() error
}

// indexData is the top-level template data for the full-page render.
type indexData struct {
	HealthErr  string
	Parade     ParadeData
	Workers    []WorkerInfo
	Events     []protocol.Event
	Throughput *ThroughputData
}

// ParadeData holds the four bead buckets rendered by the parade fragment.
type ParadeData struct {
	Ready      []protocol.Bead
	InProgress []protocol.Bead
	Blocked    []protocol.Bead
	Closed     []protocol.Bead
}

// handler is the concrete http.Handler returned by NewHandler.
type handler struct {
	data           DashboardData
	indexTmpl      *template.Template
	paradeTmpl     *template.Template
	workersTmpl    *template.Template
	detailTmpl     *template.Template
	eventsTmpl     *template.Template
	throughputTmpl *template.Template
}

// NewHandler returns an http.Handler that serves the web dashboard.
// content must contain: index.html, parade.html, workers.html, detail.html,
// events.html, throughput.html at the root level (or inside a templates/
// subdirectory). If content has a static/ subdirectory, its files are served
// at /static/.
func NewHandler(data DashboardData, content fs.FS) http.Handler {
	// If content has a templates/ subdirectory, use it for template parsing.
	// This supports embed.FS (templates at templates/index.html) and test
	// fixtures (templates at index.html).
	tmplFS := content
	if _, err := fs.Stat(content, "templates"); err == nil {
		if sub, subErr := fs.Sub(content, "templates"); subErr == nil {
			tmplFS = sub
		}
	}

	mustParse := func(files ...string) *template.Template {
		t, err := template.New("").Funcs(TemplateFuncMap()).ParseFS(tmplFS, files...)
		if err != nil {
			panic(fmt.Sprintf("web.NewHandler: parse templates %v: %v", files, err))
		}
		return t
	}

	h := &handler{
		data:           data,
		indexTmpl:      mustParse("index.html", "parade.html", "workers.html", "events.html", "throughput.html"),
		paradeTmpl:     mustParse("parade.html"),
		workersTmpl:    mustParse("workers.html"),
		detailTmpl:     mustParse("detail.html"),
		eventsTmpl:     mustParse("events.html"),
		throughputTmpl: mustParse("throughput.html"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.indexHandler)
	mux.HandleFunc("GET /fragments/parade", h.paradeHandler)
	mux.HandleFunc("GET /fragments/header", h.headerHandler)
	mux.HandleFunc("GET /fragments/workers", h.workersHandler)
	mux.HandleFunc("GET /fragments/detail/{id}", h.detailHandler)
	mux.HandleFunc("GET /fragments/events", h.eventsHandler)
	mux.HandleFunc("GET /fragments/throughput", h.throughputHandler)
	mux.HandleFunc("GET /events", h.sseHandler)

	// Mount static file serving if content has a static/ subdirectory.
	if _, err := fs.Stat(content, "static"); err == nil {
		if staticSub, subErr := fs.Sub(content, "static"); subErr == nil {
			mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))
		}
	}

	return mux
}

// renderTemplate buffers template execution before writing to w. This ensures
// that errors returned by a template after partial output do not produce a
// garbled 200 response — the ResponseWriter is untouched on error, so a clean
// 500 can still be sent.
func (h *handler) renderTemplate(w http.ResponseWriter, r *http.Request, tmpl *template.Template, name string, data any) {
	_ = r // reserved for future per-request tracing
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w) //nolint:errcheck,gosec // write errors to ResponseWriter cannot be handled after headers are sent
}

func (h *handler) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	parade, err := h.loadParadeData(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	workers, err := h.data.Workers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := h.data.RecentEvents(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	throughput, err := h.data.Throughput(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if throughput == nil {
		throughput = &ThroughputData{}
	}
	var healthMsg string
	if herr := h.data.HealthError(); herr != nil {
		healthMsg = herr.Error()
	}
	h.renderTemplate(w, r, h.indexTmpl, "index.html", indexData{
		HealthErr:  healthMsg,
		Parade:     parade,
		Workers:    workers,
		Events:     events,
		Throughput: throughput,
	})
}

func (h *handler) headerHandler(w http.ResponseWriter, r *http.Request) {
	data, err := h.loadHeaderData(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderTemplate(w, r, h.indexTmpl, "dashboard-header", data)
}

func (h *handler) paradeHandler(w http.ResponseWriter, r *http.Request) {
	data, err := h.loadParadeData(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderTemplate(w, r, h.paradeTmpl, "parade-content", data)
}

func (h *handler) loadHeaderData(ctx context.Context) (indexData, error) {
	throughput, err := h.data.Throughput(ctx)
	if err != nil {
		return indexData{}, err
	}
	if throughput == nil {
		throughput = &ThroughputData{}
	}
	var healthMsg string
	if herr := h.data.HealthError(); herr != nil {
		healthMsg = herr.Error()
	}
	return indexData{
		HealthErr:  healthMsg,
		Throughput: throughput,
	}, nil
}

func (h *handler) workersHandler(w http.ResponseWriter, r *http.Request) {
	workers, err := h.data.Workers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderTemplate(w, r, h.workersTmpl, "workers.html", workers)
}

func (h *handler) detailHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := h.data.ShowBead(r.Context(), id)
	if err != nil {
		var notFound *protocol.BeadNotFoundError
		if errors.As(err, &notFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderTemplate(w, r, h.detailTmpl, "detail.html", detail)
}

func (h *handler) eventsHandler(w http.ResponseWriter, r *http.Request) {
	events, err := h.data.RecentEvents(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderTemplate(w, r, h.eventsTmpl, "events.html", events)
}

func (h *handler) throughputHandler(w http.ResponseWriter, r *http.Request) {
	data, err := h.data.Throughput(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = &ThroughputData{}
	}
	h.renderTemplate(w, r, h.throughputTmpl, "throughput.html", data)
}

func (h *handler) sseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, hasFlusher := w.(http.Flusher)
	if hasFlusher {
		flusher.Flush()
	}

	ch := h.data.SubscribeSSE()
	defer h.data.UnsubscribeSSE(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			if hasFlusher {
				flusher.Flush()
			}
		}
	}
}

// loadParadeData fetches all four bead buckets sequentially.
func (h *handler) loadParadeData(ctx context.Context) (ParadeData, error) {
	ready, err := h.data.ReadyBeads(ctx)
	if err != nil {
		return ParadeData{}, fmt.Errorf("ready beads: %w", err)
	}
	inProgress, err := h.data.InProgressBeads(ctx)
	if err != nil {
		return ParadeData{}, fmt.Errorf("in-progress beads: %w", err)
	}
	blocked, err := h.data.BlockedBeads(ctx)
	if err != nil {
		return ParadeData{}, fmt.Errorf("blocked beads: %w", err)
	}
	closed, err := h.data.ClosedBeads(ctx, 20)
	if err != nil {
		return ParadeData{}, fmt.Errorf("closed beads: %w", err)
	}
	return ParadeData{
		Ready:      ready,
		InProgress: inProgress,
		Blocked:    blocked,
		Closed:     closed,
	}, nil
}
