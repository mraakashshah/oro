package ops //nolint:testpackage // internal test needs access to unexported buildJanitorPrompt

import (
	"context"
	"strings"
	"testing"

	"oro/pkg/janitor"
)

func TestBuildJanitorPrompt(t *testing.T) {
	assertJanitorSpawnerSignature((*Spawner).Janitor)

	prompt := buildJanitorPrompt(JanitorOpts{
		Candidates: []janitor.Candidate{{
			Detector: "deadcode",
			File:     "pkg/unused.go",
			Line:     12,
			Title:    "unused helper",
			Detail:   "helper is unreachable",
		}},
		Suppressed: []Finding{{ID: "fnd_suppressed", Title: "intentional export"}},
		OpenTitles: []string{"Existing cleanup bead"},
	})

	for _, want := range []string{
		`"detector":"deadcode"`,
		`"fnd_suppressed"`,
		"Existing cleanup bead",
		"Finding JSON ONLY",
		"NEVER create tasks yourself",
		"dispatcher files beads",
		"epic_fix shell-out pattern is explicitly not used",
		`"severity":"critical|important|minor"`,
		`"evidence":[{"file":"path/from/repo","line_start":1,"line_end":1,"quote":"literal evidence"}]`,
		`"confidence":75`,
		`"sources":["<detector>"]`,
		`"origin":"pre_existing"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func assertJanitorSpawnerSignature(_ func(*Spawner, context.Context, JanitorOpts) <-chan Result) {}

func TestBuildJanitorPromptEmptyCandidatesIsValid(t *testing.T) {
	prompt := buildJanitorPrompt(JanitorOpts{})
	if !strings.Contains(prompt, "## Detector candidates (JSON)\n[]") {
		t.Fatalf("empty candidate prompt is invalid:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Return either []") {
		t.Fatalf("empty candidate prompt must permit no findings:\n%s", prompt)
	}
}
