package web

import (
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
	ID         string
	State      string
	BeadID     string
	ContextPct int
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
	SubscribeSSE() chan string
	UnsubscribeSSE(ch chan string)
	// HealthError returns nil when the system is healthy, or a descriptive
	// error when the swarm is degraded. The index handler renders this in
	// the page so operators see problems immediately.
	HealthError() error
}

// indexData is the top-level template data for the full-page render.
type indexData struct {
	HealthErr string
	Parade    paradeData
}

// paradeData holds the four bead buckets rendered by the parade fragment.
type paradeData struct {
	Ready      []protocol.Bead
	InProgress []protocol.Bead
	Blocked    []protocol.Bead
	Closed     []protocol.Bead
}

// handler is the concrete http.Handler returned by NewHandler.
type handler struct {
	data        DashboardData
	indexTmpl   *template.Template
	paradeTmpl  *template.Template
	workersTmpl *template.Template
	detailTmpl  *template.Template
	eventsTmpl  *template.Template
}

// NewHandler returns an http.Handler that serves the web dashboard.
// templates must contain: index.html, parade.html, workers.html, detail.html.
func NewHandler(data DashboardData, templates fs.FS) http.Handler {
	mustParse := func(files ...string) *template.Template {
		t, err := template.New("").Funcs(TemplateFuncMap()).ParseFS(templates, files...)
		if err != nil {
			panic(fmt.Sprintf("web.NewHandler: parse templates %v: %v", files, err))
		}
		return t
	}

	h := &handler{
		data:        data,
		indexTmpl:   mustParse("index.html", "parade.html"),
		paradeTmpl:  mustParse("parade.html"),
		workersTmpl: mustParse("workers.html"),
		detailTmpl:  mustParse("detail.html"),
		eventsTmpl:  mustParse("events.html"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.indexHandler)
	mux.HandleFunc("GET /fragments/parade", h.paradeHandler)
	mux.HandleFunc("GET /fragments/workers", h.workersHandler)
	mux.HandleFunc("GET /fragments/detail/{id}", h.detailHandler)
	mux.HandleFunc("GET /fragments/events", h.eventsHandler)
	mux.HandleFunc("GET /events", h.sseHandler)
	return mux
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
	var healthMsg string
	if herr := h.data.HealthError(); herr != nil {
		healthMsg = herr.Error()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.indexTmpl.ExecuteTemplate(w, "index.html", indexData{
		HealthErr: healthMsg,
		Parade:    parade,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) paradeHandler(w http.ResponseWriter, r *http.Request) {
	data, err := h.loadParadeData(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.paradeTmpl.ExecuteTemplate(w, "parade-content", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) workersHandler(w http.ResponseWriter, r *http.Request) {
	workers, err := h.data.Workers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.workersTmpl.ExecuteTemplate(w, "workers.html", workers); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.detailTmpl.ExecuteTemplate(w, "detail.html", detail); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) eventsHandler(w http.ResponseWriter, r *http.Request) {
	events, err := h.data.RecentEvents(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.eventsTmpl.ExecuteTemplate(w, "events.html", events); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
func (h *handler) loadParadeData(ctx context.Context) (paradeData, error) {
	ready, err := h.data.ReadyBeads(ctx)
	if err != nil {
		return paradeData{}, fmt.Errorf("ready beads: %w", err)
	}
	inProgress, err := h.data.InProgressBeads(ctx)
	if err != nil {
		return paradeData{}, fmt.Errorf("in-progress beads: %w", err)
	}
	blocked, err := h.data.BlockedBeads(ctx)
	if err != nil {
		return paradeData{}, fmt.Errorf("blocked beads: %w", err)
	}
	closed, err := h.data.ClosedBeads(ctx, 20)
	if err != nil {
		return paradeData{}, fmt.Errorf("closed beads: %w", err)
	}
	return paradeData{
		Ready:      ready,
		InProgress: inProgress,
		Blocked:    blocked,
		Closed:     closed,
	}, nil
}
