package web_test

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"

	"oro/pkg/web"
)

func TestWorkersTemplate(t *testing.T) {
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(os.DirFS("templates"), "workers.html")
	if err != nil {
		t.Fatalf("parse workers.html: %v", err)
	}

	workers := []web.WorkerInfo{
		{
			ID:                "worker-1",
			State:             "busy",
			BeadID:            "oro-ip1",
			ContextPct:        42,
			LastHeartbeatSecs: 4,
		},
		{
			ID:                "worker-2",
			State:             "idle",
			BeadID:            "",
			ContextPct:        85,
			LastHeartbeatSecs: 45,
		},
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workers.html", workers); err != nil {
		t.Fatalf("execute workers.html: %v", err)
	}
	body := buf.String()

	for _, want := range []string{
		"worker-row__dot--busy",
		"worker-row__dot--idle",
		"worker-1",
		"worker-2",
		"oro-ip1",
		"idle",
		"42%",
		"85%",
		"4s ago",
		"45s ago",
		"worker-row__heartbeat--warn",
		"worker-row__context--danger",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("workers.html missing %q:\n%s", want, body)
		}
	}
}
