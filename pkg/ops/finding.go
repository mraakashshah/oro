package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"oro/pkg/reviewcontract"
)

// Severity mirrors the shared review contract vocabulary.
type Severity = reviewcontract.Severity

const (
	SevCritical  = reviewcontract.SevCritical
	SevImportant = reviewcontract.SevImportant
	SevMinor     = reviewcontract.SevMinor
)

// Evidence pins a finding to a file:line(:quote) the reviewer was shown.
type Evidence = reviewcontract.Evidence

// Finding is the shared structured review finding emitted by reviewers.
type Finding = reviewcontract.Finding

// FindingHistoryEntry records an append-only triage status change for a finding.
type FindingHistoryEntry = reviewcontract.FindingHistoryEntry

// ReviewReport is the parsed structured output of one reviewer pass.
type ReviewReport struct {
	Reviewer string    `json:"reviewer"`
	Findings []Finding `json:"findings"`
	Verdict  Verdict   `json:"verdict"`
	Raw      string    `json:"-"`
}

// FindingID returns the content-addressed identifier for a review finding.
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
