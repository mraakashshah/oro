package ops

import (
	"encoding/json"
	"strings"
)

// buildJanitorPrompt assembles the deterministic-cleanliness triage prompt.
func buildJanitorPrompt(opts JanitorOpts) string {
	var b strings.Builder
	b.WriteString("You are Oro's janitor triage agent. Assess deterministic cleanliness-detector candidates for actionable codebase rot.\n\n")
	b.WriteString("## Detector candidates (JSON)\n")
	b.Write(janitorPromptJSON(opts.Candidates))
	b.WriteString("\n\n## Suppressed findings (JSON)\n")
	b.Write(janitorPromptJSON(opts.Suppressed))
	b.WriteString("\n\n## Open bead titles (JSON)\n")
	b.Write(janitorPromptJSON(opts.OpenTitles))
	b.WriteString("\n\n## Output schema\n")
	b.WriteString(`[{"severity":"critical|important|minor","category":"<detector-backed category>","title":"<short title>","detail":"<specific issue and remediation>","evidence":[{"file":"path/from/repo","line_start":1,"line_end":1,"quote":"literal evidence"}],"confidence":75,"sources":["<detector>"],"origin":"pre_existing"}]`)
	b.WriteString("\nSeverity must be critical, important, or minor; confidence must be an integer from 0 to 100. Evidence must cite candidate-backed repository lines, and sources must name the deterministic detector. Use origin pre_existing for existing-tree rot.\n")
	b.WriteString("\n\n## Authority\n")
	b.WriteString("Emit Finding JSON ONLY. NEVER create tasks yourself. The dispatcher files beads; the epic_fix shell-out pattern is explicitly not used.\n")
	b.WriteString("Return either [] when no candidate warrants a finding, or a JSON array of Finding objects. Do not wrap the JSON in Markdown fences or add commentary.\n")
	return b.String()
}

func janitorPromptJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("[]")
	}
	if string(encoded) == "null" {
		return []byte("[]")
	}
	return encoded
}
