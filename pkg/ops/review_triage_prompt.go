package ops

import "strings"

func buildCheapTriagePrompt(opts ReviewOpts) string {
	var b strings.Builder
	b.WriteString("You are the cheap code-review triage pass for Oro.\n")
	b.WriteString("Score candidate concerns from 0-100. When uncertain between two levels, pick the HIGHER score.\n")
	b.WriteString("Do not anchor scores around one comfortable value; use the full rubric and separate independent source families.\n\n")
	writeContext(&b, opts)
	writeProjectContext(&b, opts)
	b.WriteString("\nReturn only JSON matching this shape:\n")
	b.WriteString(`{"reviewer":"ops_review_triage","findings":[{"severity":"important","category":"correctness","title":"...","detail":"...","evidence":[{"file":"path","line_start":1,"line_end":1}],"confidence":50,"sources":["correctness"],"origin":"introduced"}],"verdict":"rejected"}`)
	return b.String()
}
