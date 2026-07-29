package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"oro/pkg/reviewcontract"
)

// Severity aliases the dependency-neutral review finding vocabulary.
type Severity = reviewcontract.Severity

const (
	// SevCritical identifies a critical structured review finding.
	SevCritical = reviewcontract.SevCritical
	// SevImportant identifies an important structured review finding.
	SevImportant = reviewcontract.SevImportant
	// SevMinor identifies a minor structured review finding.
	SevMinor = reviewcontract.SevMinor
)

// Evidence aliases the dependency-neutral evidence contract.
type Evidence = reviewcontract.Evidence

// ContractImpact aliases the dependency-neutral recovery contract impact.
type ContractImpact = reviewcontract.ContractImpact

const (
	// ContractImplementationFix identifies a finding requiring an implementation change.
	ContractImplementationFix = reviewcontract.ContractImplementationFix
	// ContractAcceptanceGap identifies a finding requiring an acceptance-contract revision.
	ContractAcceptanceGap = reviewcontract.ContractAcceptanceGap
)

// Finding aliases the dependency-neutral structured review finding contract.
type Finding = reviewcontract.Finding

// FindingHistoryEntry aliases the dependency-neutral finding history contract.
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
