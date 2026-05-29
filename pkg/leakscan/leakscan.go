package leakscan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Severity describes the risk level of a matched leak pattern.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
)

// Action describes how callers should handle a matched leak.
type Action string

const (
	ActionBlock  Action = "block"
	ActionRedact Action = "redact"
	ActionWarn   Action = "warn"
)

// Pattern defines one secret detector.
type Pattern struct {
	Name     string
	Re       *regexp.Regexp
	Severity Severity
	Action   Action
	prefix   string
}

// Match is a single secret finding. Secret is always masked.
type Match struct {
	Pattern  string   `json:"pattern"`
	Severity Severity `json:"severity"`
	Action   Action   `json:"action"`
	Secret   string   `json:"secret"`
	Start    int      `json:"start"`
	End      int      `json:"end"`
}

// Result contains all findings from a scan and a redacted copy of the input.
type Result struct {
	Matches     []Match
	ShouldBlock bool
	Redacted    string
}

// Allowlist suppresses matches by pattern name or exact raw secret.
type Allowlist struct {
	Secrets  []string
	Patterns []string
}

// SummaryMatch is the JSON-safe summary representation of a match.
type SummaryMatch struct {
	Pattern  string   `json:"pattern"`
	Severity Severity `json:"severity"`
	Action   Action   `json:"action"`
	Secret   string   `json:"secret"`
}

// DefaultPatterns returns the built-in RE2-compatible secret detectors.
func DefaultPatterns() []Pattern {
	return []Pattern{
		mustPattern("openai_api_key", `sk-(?:proj-)?[a-zA-Z0-9]{20,}(?:T3BlbkFJ[a-zA-Z0-9_-]*)?`, SeverityCritical, ActionBlock, "sk-"),
		mustPattern("anthropic_api_key", `sk-ant-api[a-zA-Z0-9_-]{90,}`, SeverityCritical, ActionBlock, "sk-ant-api"),
		mustPattern("aws_access_key", `AKIA[0-9A-Z]{16}`, SeverityCritical, ActionBlock, "AKIA"),
		mustPattern("github_token", `gh[pousr]_[A-Za-z0-9_]{36,}`, SeverityCritical, ActionBlock, "gh"),
		mustPattern("github_fine_grained_pat", `github_pat_[a-zA-Z0-9]{22}_[a-zA-Z0-9]{59}`, SeverityCritical, ActionBlock, "github_pat_"),
		mustPattern("stripe_api_key", `sk_(?:live|test)_[a-zA-Z0-9]{24,}`, SeverityCritical, ActionBlock, "sk_"),
		mustPattern("pem_private_key", `-----BEGIN\s+(?:RSA\s+)?PRIVATE\s+KEY-----`, SeverityCritical, ActionBlock, "-----BEGIN"),
		mustPattern("ssh_private_key", `-----BEGIN\s+(?:OPENSSH|EC|DSA)\s+PRIVATE\s+KEY-----`, SeverityCritical, ActionBlock, "-----BEGIN"),
		mustPattern("google_api_key", `AIza[0-9A-Za-z_-]{35}`, SeverityHigh, ActionBlock, "AIza"),
		mustPattern("slack_token", `xox[baprs]-[0-9a-zA-Z-]{10,}`, SeverityHigh, ActionBlock, "xox"),
		mustPattern("twilio_api_key", `SK[a-fA-F0-9]{32}`, SeverityHigh, ActionBlock, "SK"),
		mustPattern("sendgrid_api_key", `SG\.[a-zA-Z0-9_-]{22}\.[a-zA-Z0-9_-]{43}`, SeverityHigh, ActionBlock, "SG."),
		mustPattern("bearer_token", `Bearer\s+[a-zA-Z0-9_-]{20,}`, SeverityHigh, ActionRedact, "Bearer"),
		mustPattern("auth_header", `(?i)authorization:\s*[a-zA-Z]+\s+[a-zA-Z0-9_-]{20,}`, SeverityHigh, ActionRedact, "authorization:"),
		mustPattern("openrouter_api_key", `\bsk-or-v1-[a-fA-F0-9]{40,}`, SeverityCritical, ActionBlock, "sk-or-v1-"),
		mustPattern("anthropic_oauth_token", `\bsk-ant-oat\d{2}-[a-zA-Z0-9_-]{50,}`, SeverityCritical, ActionBlock, "sk-ant-oat"),
		mustPattern("telegram_bot_token", `\b\d{8,12}:AA[A-Za-z0-9_-]{30,}\b`, SeverityCritical, ActionBlock, ":AA"),
		mustPattern("groq_api_key", `\bgsk_[A-Za-z0-9]{30,}`, SeverityCritical, ActionBlock, "gsk_"),
		mustPattern("high_entropy_hex", `\b[a-fA-F0-9]{64}\b`, SeverityMedium, ActionWarn, ""),
	}
}

// Scan detects secrets in content using the supplied patterns and allowlist.
func Scan(content string, patterns []Pattern, allow Allowlist) Result {
	matches := scanMatches(content, patterns, allow)
	result := Result{
		Matches:  matches,
		Redacted: redact(content, matches),
	}
	for _, match := range matches {
		if match.Action == ActionBlock {
			result.ShouldBlock = true
			break
		}
	}
	return result
}

// ScanDiff detects secrets only on added diff lines, ignoring removed lines and file headers.
func ScanDiff(diff string, patterns []Pattern, allow Allowlist) Result {
	var added []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			added = append(added, strings.TrimPrefix(line, "+"))
		}
	}
	return Scan(strings.Join(added, "\n"), patterns, allow)
}

// Mask returns a first-four/last-four mask for a raw secret.
func Mask(secret string) string {
	if len(secret) <= 8 {
		return strings.Repeat("*", len(secret))
	}
	return secret[:4] + strings.Repeat("*", len(secret)-8) + secret[len(secret)-4:]
}

// Summarize returns a human-readable summary that never includes raw secrets.
func Summarize(result Result) string {
	if len(result.Matches) == 0 {
		return "no leaks detected"
	}
	parts := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		parts = append(parts, fmt.Sprintf("%s %s %s", match.Pattern, match.Action, match.Secret))
	}
	return strings.Join(parts, "\n")
}

// SummaryJSON returns a JSON summary that never includes raw secrets.
func SummaryJSON(result Result) []byte {
	summary := make([]SummaryMatch, 0, len(result.Matches))
	for _, match := range result.Matches {
		summary = append(summary, SummaryMatch{
			Pattern:  match.Pattern,
			Severity: match.Severity,
			Action:   match.Action,
			Secret:   match.Secret,
		})
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return []byte("[]")
	}
	return data
}

func mustPattern(name, expr string, severity Severity, action Action, prefix string) Pattern {
	return Pattern{
		Name:     name,
		Re:       regexp.MustCompile(expr),
		Severity: severity,
		Action:   action,
		prefix:   prefix,
	}
}

func scanMatches(content string, patterns []Pattern, allow Allowlist) []Match {
	var matches []Match
	for _, pattern := range patterns {
		if !hasPrefix(content, pattern.prefix) {
			continue
		}
		for _, loc := range pattern.Re.FindAllStringIndex(content, -1) {
			raw := content[loc[0]:loc[1]]
			if allow.Contains(pattern.Name, raw) {
				continue
			}
			matches = append(matches, Match{
				Pattern:  pattern.Name,
				Severity: pattern.Severity,
				Action:   pattern.Action,
				Secret:   Mask(raw),
				Start:    loc[0],
				End:      loc[1],
			})
		}
	}
	return matches
}

func hasPrefix(content, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.Contains(strings.ToLower(content), strings.ToLower(prefix))
}

func (allow Allowlist) Contains(pattern, secret string) bool {
	for _, allowedPattern := range allow.Patterns {
		if allowedPattern == pattern {
			return true
		}
	}
	for _, allowedSecret := range allow.Secrets {
		if allowedSecret == secret {
			return true
		}
	}
	return false
}

func redact(content string, matches []Match) string {
	if len(matches) == 0 {
		return content
	}
	ordered := append([]Match(nil), matches...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Start < ordered[j].Start
	})
	var b strings.Builder
	cursor := 0
	for _, match := range ordered {
		if match.Start < cursor {
			continue
		}
		b.WriteString(content[cursor:match.Start])
		b.WriteString(match.Secret)
		cursor = match.End
	}
	b.WriteString(content[cursor:])
	return b.String()
}
