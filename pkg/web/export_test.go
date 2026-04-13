package web

import (
	"html/template"
	"net/http"
)

// HandlerForTemplate returns an http.Handler that invokes renderTemplate with
// the given template and name. Used only in tests to exercise the buffered
// render path.
func HandlerForTemplate(data DashboardData, tmpl *template.Template, name string) http.Handler {
	h := &handler{data: data}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.renderTemplate(w, r, tmpl, name, nil)
	})
}
