package ops

import "strings"

// buildAuditPrompt returns the shared instructions for every static audit
// section. Section-specific guidance is appended by collectPersonaReviews.
func buildAuditPrompt(_ AuditOpts) string {
	return strings.Join([]string{
		"# Whole-Repository Audit",
		"",
		"## Whole-Repository Audit",
		"Audit the current worktree as a whole, not only its recent diff. Focus on static evidence that you can verify from repository files.",
		"",
		"Report only concrete findings. Each finding must cite repository evidence and a specific remediation.",
		"",
		"## Structured Audit Output",
		"Return one JSON object in a fenced json block or as the full response body.",
		"Schema:",
		`{"reviewer":"<section-id>","verdict":"approved|rejected|failed","findings":[{"severity":"critical|important|minor","category":"<category>","title":"<short title>","detail":"<specific issue and fix>","evidence":[{"file":"path/from/repo","line_start":1,"line_end":1,"quote":"literal shown text"}],"confidence":75,"origin":"introduced|pre_existing"}]}`,
		"End with exactly one terminal line: VERDICT: APPROVED, VERDICT: REJECTED, or VERDICT: FAILED.",
	}, "\n")
}

func auditSections() []Persona {
	return []Persona{
		{ID: "code-quality", Role: OpsAudit.Role(), Fragment: "\n\n## Audit Section: code-quality\nReview readability, oversized files or functions, dead code, unnecessary abstraction, and separation of logic from presentation."},
		{ID: "tests-safety", Role: OpsAudit.Role(), Fragment: "\n\n## Audit Section: tests-safety\nReview critical-path coverage, behavior-versus-implementation tests, flaky or skipped tests, quarantined tests, and determinism."},
		{ID: "data-migrations", Role: OpsAudit.Role(), Fragment: "\n\n## Audit Section: data-migrations\nReview schema constraints, migration reversibility and safety, identifier consistency, and timestamp consistency."},
		{ID: "security-static", Role: OpsAudit.Role(), Fragment: "\n\n## Audit Section: security-static\nReview secrets in code or logs, dependency vulnerabilities, injection patterns, and privileged-path handling."},
		{ID: "perf-patterns", Role: OpsAudit.Role(), Fragment: "\n\n## Audit Section: perf-patterns\nReview N+1 queries, unbounded operations, missing pagination or batching, and synchronous work that should be asynchronous."},
		{ID: "dx-deps-docs", Role: OpsAudit.Role(), Fragment: "\n\n## Audit Section: dx-deps-docs\nReview pinned versions, setup documentation accuracy, outdated or abandoned dependencies, documentation rot, and broken references."},
	}
}

// AuditSectionIDs returns the canonical whole-repository audit section IDs.
func AuditSectionIDs() []string {
	sections := auditSections()
	ids := make([]string, 0, len(sections))
	for _, section := range sections {
		ids = append(ids, section.ID)
	}
	return ids
}
