// Package leakscan detects credential-like strings and returns masked findings.
package leakscan

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Severity describes the risk level of a matched leak pattern.
type Severity string

const (
	// SeverityCritical marks credentials that should usually block output.
	SeverityCritical Severity = "critical"
	// SeverityHigh marks credentials that are serious but may have redaction policies.
	SeverityHigh Severity = "high"
	// SeverityMedium marks suspicious values that usually warn.
	SeverityMedium Severity = "medium"
)

// Action describes how callers should handle a matched leak.
type Action string

const (
	// ActionBlock means callers should reject the scanned content.
	ActionBlock Action = "block"
	// ActionRedact means callers may continue with the redacted content.
	ActionRedact Action = "redact"
	// ActionWarn means callers may continue after surfacing a warning.
	ActionWarn Action = "warn"
)

// Pattern defines one secret detector.
type Pattern struct {
	Name     string
	Re       *regexp.Regexp
	Severity Severity
	Action   Action
	prefix   string
}

// Match is a single secret finding. Masked is always first-four/last-four masked.
type Match struct {
	Pattern  string   `json:"pattern"`
	Severity Severity `json:"severity"`
	Action   Action   `json:"action"`
	Masked   string   `json:"masked"`
	Start    int      `json:"start"`
	End      int      `json:"end"`
}

// Result contains all findings from a scan and a redacted copy of the input.
type Result struct {
	Matches     []Match
	ShouldBlock bool
	Redacted    string
}

// Allowlist suppresses matches by exact raw secret, fixture path, or placeholder.
type Allowlist struct {
	Literals      map[string]bool
	PathGlobs     []string
	PlaceholderRe *regexp.Regexp
}

// SummaryMatch is the JSON-safe summary representation of a match.
type SummaryMatch struct {
	Pattern  string   `json:"pattern"`
	Severity Severity `json:"severity"`
	Action   Action   `json:"action"`
	Masked   string   `json:"masked"`
}

// DefaultPatterns returns the built-in RE2-compatible secret detectors.
//
//oro:testonly — production integration is deferred to the leakscan boundary wiring bead.
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
	matches = appendNonOverlapping(matches, entropyCandidates(content, 4.0, allow)...)
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

// LoadAllowlist reads a YAML leakscan allowlist from path.
func LoadAllowlist(path string) (Allowlist, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller supplies the allowlist path
	if err != nil {
		return Allowlist{}, fmt.Errorf("read leakscan allowlist: %w", err)
	}
	var raw struct {
		Literals         []string `yaml:"literals"`
		PathGlobs        []string `yaml:"path_globs"`
		PlaceholderRegex string   `yaml:"placeholder_regex"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Allowlist{}, fmt.Errorf("parse leakscan allowlist: %w", err)
	}
	allow := Allowlist{
		Literals:  make(map[string]bool, len(raw.Literals)),
		PathGlobs: append(defaultAllowlistPathGlobs(), raw.PathGlobs...),
	}
	for _, literal := range raw.Literals {
		allow.Literals[literal] = true
	}
	if raw.PlaceholderRegex != "" {
		re, err := regexp.Compile(raw.PlaceholderRegex)
		if err != nil {
			return Allowlist{}, fmt.Errorf("compile placeholder regex: %w", err)
		}
		allow.PlaceholderRe = re
	}
	return allow, nil
}

// ScanDiff detects secrets only on added diff lines, ignoring removed lines and file headers.
//
//oro:testonly — production integration is deferred to the leakscan boundary wiring bead.
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
		parts = append(parts, fmt.Sprintf("%s %s %s", match.Pattern, match.Action, match.Masked))
	}
	return strings.Join(parts, "\n")
}

// SummaryJSON returns a JSON summary that never includes raw secrets.
//
//oro:testonly — production integration is deferred to the leakscan boundary wiring bead.
func SummaryJSON(result Result) []byte {
	summary := make([]SummaryMatch, 0, len(result.Matches))
	for _, match := range result.Matches {
		summary = append(summary, SummaryMatch{
			Pattern:  match.Pattern,
			Severity: match.Severity,
			Action:   match.Action,
			Masked:   match.Masked,
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
			if allow.contains(pattern.Name, raw) {
				continue
			}
			matches = append(matches, Match{
				Pattern:  pattern.Name,
				Severity: pattern.Severity,
				Action:   pattern.Action,
				Masked:   Mask(raw),
				Start:    loc[0],
				End:      loc[1],
			})
		}
	}
	return matches
}

func entropyCandidates(content string, minBits float64, allow Allowlist) []Match {
	var matches []Match
	entropyRunRe := regexp.MustCompile(`[A-Za-z0-9+/=_-]{20,}`)
	for _, loc := range entropyRunRe.FindAllStringIndex(content, -1) {
		if startsAfterEncodedBoundary(content, loc[0]) {
			continue
		}
		raw := content[loc[0]:loc[1]]
		candidate, start := entropyCandidateValue(raw, loc[0])
		if len(candidate) < 20 {
			continue
		}
		if allow.contains("high_entropy_token", candidate) {
			continue
		}
		if shannonBits(candidate) < minBits {
			continue
		}
		matches = append(matches, Match{
			Pattern:  "high_entropy_token",
			Severity: SeverityMedium,
			Action:   ActionWarn,
			Masked:   Mask(candidate),
			Start:    start,
			End:      start + len(candidate),
		})
	}
	return matches
}

func appendNonOverlapping(existing []Match, candidates ...Match) []Match {
	matches := existing
	for _, candidate := range candidates {
		if overlapsAny(candidate, existing) {
			continue
		}
		matches = append(matches, candidate)
	}
	return matches
}

func overlapsAny(candidate Match, existing []Match) bool {
	for _, match := range existing {
		if candidate.Start < match.End && candidate.End > match.Start {
			return true
		}
	}
	return false
}

func startsAfterEncodedBoundary(content string, start int) bool {
	if start == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(content[:start])
	return r == '%' || unicode.Is(unicode.Cf, r)
}

func entropyCandidateValue(raw string, start int) (string, int) {
	trimmed := strings.TrimRight(raw, "=")
	if idx := strings.LastIndex(trimmed, "="); idx >= 0 {
		return raw[idx+1:], start + idx + 1
	}
	return raw, start
}

func shannonBits(value string) float64 {
	if value == "" {
		return 0
	}
	counts := make(map[rune]int)
	for _, ch := range value {
		counts[ch]++
	}
	var entropy float64
	length := float64(len(value))
	for _, count := range counts {
		p := float64(count) / length
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func hasPrefix(content, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.Contains(strings.ToLower(content), strings.ToLower(prefix))
}

func (allow Allowlist) contains(pattern, secret string) bool {
	if allow.Literals[secret] {
		return true
	}
	if allow.PlaceholderRe != nil && allow.PlaceholderRe.MatchString(secret) {
		return true
	}
	return false
}

func (allow Allowlist) containsPath(path string) bool {
	for _, glob := range append(defaultAllowlistPathGlobs(), allow.PathGlobs...) {
		if matchPathGlob(glob, path) {
			return true
		}
	}
	return false
}

func matchPathGlob(glob, path string) bool {
	slashPath := filepath.ToSlash(path)
	if strings.HasSuffix(glob, "/**") {
		prefix := strings.TrimSuffix(glob, "**")
		return strings.HasPrefix(slashPath, prefix) || strings.Contains(slashPath, "/"+prefix)
	}
	matched, err := filepath.Match(glob, slashPath)
	if err == nil && matched {
		return true
	}
	matched, err = filepath.Match(glob, filepath.Base(slashPath))
	return err == nil && matched
}

func defaultAllowlistPathGlobs() []string {
	return []string{"testdata/**", "*_test.go"}
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
		b.WriteString(match.Masked)
		cursor = match.End
	}
	b.WriteString(content[cursor:])
	return b.String()
}
