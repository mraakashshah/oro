// Package reviewcontract owns dependency-neutral structured review finding types.
package reviewcontract

// Severity describes a structured review finding's urgency.
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

// ContractImpact identifies whether a finding changes implementation or acceptance obligations.
//
//oro:testonly
type ContractImpact string

const (
	// ContractImplementationFix identifies a finding requiring an implementation change.
	//
	//oro:testonly
	ContractImplementationFix ContractImpact = "implementation_fix"
	// ContractAcceptanceGap identifies a finding requiring an acceptance-contract revision.
	//
	//oro:testonly
	ContractAcceptanceGap ContractImpact = "acceptance_gap"
)

// FindingHistoryEntry records an append-only triage status change for a finding.
type FindingHistoryEntry struct {
	Status string `json:"status"`
	Actor  string `json:"actor,omitempty"`
	Note   string `json:"note,omitempty"`
	At     string `json:"at,omitempty"`
}

// Finding is the shared structured review finding emitted by reviewers and delivered for recovery.
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
