package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Severity mirrors the existing review prompt vocabulary.
type Severity string

const (
	// SevCritical identifies a critical structured review finding.
	SevCritical Severity = "critical"
	// SevImportant identifies an important structured review finding.
	SevImportant Severity = "important"
	// SevMinor identifies a minor structured review finding.
	SevMinor Severity = "minor"
)

// Evidence pins a finding to a file:line(:quote) the reviewer was shown.
type Evidence struct {
	File      string `json:"file"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Quote     string `json:"quote,omitempty"`
}

// Finding is the shared structured review finding emitted by reviewers.
type Finding struct {
	ID         string     `json:"id"`
	Severity   Severity   `json:"severity"`
	Category   string     `json:"category"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Evidence   []Evidence `json:"evidence"`
	Confidence int        `json:"confidence"`
	Sources    []string   `json:"sources"`
	// SourceFamilies names independent evidence families used by cheap triage.
	SourceFamilies []string              `json:"source_families,omitempty"`
	Origin         string                `json:"origin"`
	Status         string                `json:"status,omitempty"`
	History        []FindingHistoryEntry `json:"history,omitempty"`
}

// FindingHistoryEntry records an append-only triage status change for a finding.
type FindingHistoryEntry struct {
	Status string `json:"status"`
	Actor  string `json:"actor,omitempty"`
	Note   string `json:"note,omitempty"`
	At     string `json:"at,omitempty"`
}

// ReviewReport is the parsed structured output of one reviewer pass.
type ReviewReport struct {
	Reviewer string    `json:"reviewer"`
	Findings []Finding `json:"findings"`
	Verdict  Verdict   `json:"verdict"`
	Raw      string    `json:"-"`
}

// FindingID returns the content-addressed identifier for a review finding.
//
//oro:testonly — wired into production by subsequent structured-review phases.
func FindingID(beadID string, f Finding) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		beadID,
		f.Category,
		normalizeTitle(f.Title),
		canonicalEvidence(f.Evidence),
	}, "\x00")))
	return "fnd_" + hex.EncodeToString(h[:8])
}

func canonicalEvidence(ev []Evidence) string {
	ordered := append([]Evidence(nil), ev...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].File != ordered[j].File {
			return ordered[i].File < ordered[j].File
		}
		if ordered[i].LineStart != ordered[j].LineStart {
			return ordered[i].LineStart < ordered[j].LineStart
		}
		if ordered[i].LineEnd != ordered[j].LineEnd {
			return ordered[i].LineEnd < ordered[j].LineEnd
		}
		return ordered[i].Quote < ordered[j].Quote
	})

	b, err := json.Marshal(ordered)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func normalizeTitle(s string) string {
	return strings.TrimSpace(strings.TrimRight(strings.ToLower(strings.Join(strings.Fields(s), " ")), ".,:;!?"))
}
