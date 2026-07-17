// Package reviewcontract defines review findings without depending on runtime packages.
package reviewcontract

// Severity describes the impact of a review finding.
type Severity string

const (
	SevCritical  Severity = "critical"
	SevImportant Severity = "important"
	SevMinor     Severity = "minor"
)

// Evidence pins a finding to a source location shown to a reviewer.
type Evidence struct {
	File      string `json:"file"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Quote     string `json:"quote,omitempty"`
}

// ContractImpact identifies the review contract affected by a finding.
type ContractImpact string

const (
	ContractImplementationFix ContractImpact = "implementation_fix"
	ContractAcceptanceGap     ContractImpact = "acceptance_gap"
)

// Finding is a structured review finding.
type Finding struct {
	ID             string                `json:"id"`
	Severity       Severity              `json:"severity"`
	Category       string                `json:"category"`
	Title          string                `json:"title"`
	Detail         string                `json:"detail"`
	Evidence       []Evidence            `json:"evidence"`
	Confidence     int                   `json:"confidence"`
	Sources        []string              `json:"sources"`
	SourceFamilies []string              `json:"source_families,omitempty"`
	Origin         string                `json:"origin"`
	Status         string                `json:"status,omitempty"`
	History        []FindingHistoryEntry `json:"history,omitempty"`
	ContractImpact ContractImpact        `json:"contract_impact"`
	RequiredAction string                `json:"required_action"`
}

// FindingHistoryEntry records a finding status change.
type FindingHistoryEntry struct {
	Status string `json:"status"`
	Actor  string `json:"actor,omitempty"`
	Note   string `json:"note,omitempty"`
	At     string `json:"at,omitempty"`
}
