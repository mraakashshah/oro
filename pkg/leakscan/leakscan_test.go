package leakscan_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"oro/pkg/leakscan"
)

func TestScan_DefaultPatternsDetectsCredentialFamilies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  string
		pattern string
	}{
		{name: "aws", secret: "AKIA1234567890ABCDEF", pattern: "aws_access_key"},
		{name: "openai", secret: "sk-abcdefghijklmnopqrstuvwxyz123456", pattern: "openai_api_key"},
		{name: "anthropic", secret: "sk-ant-api" + strings.Repeat("A", 90), pattern: "anthropic_api_key"},
		{name: "anthropic_oauth", secret: "sk-ant-oat01-" + strings.Repeat("B", 50), pattern: "anthropic_oauth_token"},
		{name: "github", secret: "ghp_" + strings.Repeat("a", 36), pattern: "github_token"},
		{name: "github_pat", secret: "github_pat_" + strings.Repeat("A", 22) + "_" + strings.Repeat("B", 59), pattern: "github_fine_grained_pat"},
		{name: "stripe", secret: "sk_live_" + strings.Repeat("a", 24), pattern: "stripe_api_key"},
		{name: "pem", secret: "-----BEGIN PRIVATE KEY-----", pattern: "pem_private_key"},
		{name: "ssh", secret: "-----BEGIN OPENSSH PRIVATE KEY-----", pattern: "ssh_private_key"},
		{name: "google", secret: "AIza" + strings.Repeat("a", 35), pattern: "google_api_key"},
		{name: "slack", secret: "xoxb-" + strings.Repeat("a", 10), pattern: "slack_token"},
		{name: "twilio", secret: "SK" + strings.Repeat("a", 32), pattern: "twilio_api_key"},
		{name: "sendgrid", secret: "SG." + strings.Repeat("a", 22) + "." + strings.Repeat("b", 43), pattern: "sendgrid_api_key"},
		{name: "openrouter", secret: "sk-or-v1-" + strings.Repeat("a", 40), pattern: "openrouter_api_key"},
		{name: "telegram", secret: "123456789:AA" + strings.Repeat("a", 30), pattern: "telegram_bot_token"},
		{name: "groq", secret: "gsk_" + strings.Repeat("a", 30), pattern: "groq_api_key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := leakscan.Scan("token="+tc.secret, leakscan.DefaultPatterns(), leakscan.Allowlist{})
			if !result.ShouldBlock {
				t.Fatalf("ShouldBlock = false, want true; matches=%v", result.Matches)
			}
			if len(result.Matches) != 1 {
				t.Fatalf("matches=%d, want 1: %#v", len(result.Matches), result.Matches)
			}
			if result.Matches[0].Pattern != tc.pattern {
				t.Fatalf("pattern=%q, want %q", result.Matches[0].Pattern, tc.pattern)
			}
			assertNoRawSecret(t, result, tc.secret)
		})
	}
}

func TestScan_RedactActionsDoNotBlock(t *testing.T) {
	t.Parallel()

	secret := "Bearer " + strings.Repeat("a", 24)
	result := leakscan.Scan("Authorization: "+secret, leakscan.DefaultPatterns(), leakscan.Allowlist{})
	if result.ShouldBlock {
		t.Fatalf("ShouldBlock = true, want false")
	}
	if len(result.Matches) == 0 {
		t.Fatalf("matches empty, want bearer/auth redaction match")
	}
	assertNoRawSecret(t, result, secret)
}

func TestScanDiff_OnlyAddedLines(t *testing.T) {
	t.Parallel()

	blocked := "sk-or-v1-" + strings.Repeat("a", 40)
	removed := "AKIA1234567890ABCDEF"
	diff := strings.Join([]string{
		"diff --git a/file b/file",
		"+++ b/file",
		"+const token = \"" + blocked + "\"",
		"-const old = \"" + removed + "\"",
		" context",
	}, "\n")

	result := leakscan.ScanDiff(diff, leakscan.DefaultPatterns(), leakscan.Allowlist{})
	if !result.ShouldBlock {
		t.Fatalf("ShouldBlock = false, want true")
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches=%d, want 1: %#v", len(result.Matches), result.Matches)
	}
	if result.Matches[0].Masked != leakscan.Mask(blocked) {
		t.Fatalf("matched secret=%q, want mask for added secret", result.Matches[0].Masked)
	}
	if strings.Contains(result.Redacted, removed) {
		t.Fatalf("removed-line secret was redacted/scanned: %q", result.Redacted)
	}
}

func TestSummarize_NeverLeaksRawSecret(t *testing.T) {
	t.Parallel()

	secret := "github_pat_" + strings.Repeat("A", 22) + "_" + strings.Repeat("B", 59)
	result := leakscan.Scan(secret, leakscan.DefaultPatterns(), leakscan.Allowlist{})
	summary := leakscan.Summarize(result)
	if strings.Contains(summary, secret) {
		t.Fatalf("summary leaked raw secret")
	}
	if !strings.Contains(summary, leakscan.Mask(secret)) {
		t.Fatalf("summary=%q missing mask %q", summary, leakscan.Mask(secret))
	}

	data := leakscan.SummaryJSON(result)
	if strings.Contains(string(data), secret) {
		t.Fatalf("summary JSON leaked raw secret")
	}
	var decoded []leakscan.SummaryMatch
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal summary JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Masked != leakscan.Mask(secret) {
		t.Fatalf("decoded summary=%#v, want masked secret", decoded)
	}
}

func TestZWSP_NotCaught(t *testing.T) {
	t.Parallel()

	result := leakscan.Scan("sk-\u200babcdefghijklmnopqrstuvwxyz123456", leakscan.DefaultPatterns(), leakscan.Allowlist{})
	if len(result.Matches) != 0 {
		t.Fatalf("ZWSP-split key unexpectedly caught: %#v", result.Matches)
	}
}

func TestPercentEncoded_NotCaught(t *testing.T) {
	t.Parallel()

	result := leakscan.Scan("sk%2Dabcdefghijklmnopqrstuvwxyz123456", leakscan.DefaultPatterns(), leakscan.Allowlist{})
	if len(result.Matches) != 0 {
		t.Fatalf("percent-encoded key unexpectedly caught: %#v", result.Matches)
	}
}

func TestEntropy_HighEntropyBase64Warns(t *testing.T) {
	t.Parallel()

	secret := "qwertyuiopASDFGHJKLzxcvbnm123456"
	result := leakscan.Scan("session="+secret, leakscan.DefaultPatterns(), leakscan.Allowlist{})
	if result.ShouldBlock {
		t.Fatalf("ShouldBlock = true, want false for entropy warning")
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches=%d, want 1: %#v", len(result.Matches), result.Matches)
	}
	match := result.Matches[0]
	if match.Pattern != "high_entropy_token" {
		t.Fatalf("pattern=%q, want high_entropy_token", match.Pattern)
	}
	if match.Action != leakscan.ActionWarn {
		t.Fatalf("action=%q, want warn", match.Action)
	}
	assertNoRawSecret(t, result, secret)
}

func TestEntropy_LowEntropyRunIgnored(t *testing.T) {
	t.Parallel()

	result := leakscan.Scan("fixture="+strings.Repeat("a", 32), leakscan.DefaultPatterns(), leakscan.Allowlist{})
	if len(result.Matches) != 0 {
		t.Fatalf("matches=%d, want 0: %#v", len(result.Matches), result.Matches)
	}
}

func TestAllowlist_LiteralAndPlaceholderExemptEntropy(t *testing.T) {
	t.Parallel()

	literal := "qwertyuiopASDFGHJKLzxcvbnm123456"
	placeholder := "AKIAIOSFODNN7EXAMPLE"
	allow := leakscan.Allowlist{
		Literals:      map[string]bool{literal: true},
		PlaceholderRe: regexp.MustCompile(`EXAMPLE$`),
	}

	for _, token := range []string{literal, placeholder} {
		t.Run(token, func(t *testing.T) {
			result := leakscan.Scan("token="+token, leakscan.DefaultPatterns(), allow)
			if len(result.Matches) != 0 {
				t.Fatalf("matches=%d, want 0: %#v", len(result.Matches), result.Matches)
			}
		})
	}
}

func TestAllowlist_LoadsYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "leakscan-allowlist.yaml")
	err := os.WriteFile(path, []byte(strings.Join([]string{
		"literals:",
		"  - qwertyuiopASDFGHJKLzxcvbnm123456",
		"path_globs:",
		"  - testdata/**",
		"  - '*_test.go'",
		"placeholder_regex: 'EXAMPLE$'",
		"",
	}, "\n")), 0o600)
	if err != nil {
		t.Fatalf("write allowlist: %v", err)
	}

	allow, err := leakscan.LoadAllowlist(path)
	if err != nil {
		t.Fatalf("LoadAllowlist: %v", err)
	}
	if !allow.Literals["qwertyuiopASDFGHJKLzxcvbnm123456"] {
		t.Fatalf("literal allowlist missing loaded token: %#v", allow.Literals)
	}
	for _, want := range []string{"testdata/**", "*_test.go"} {
		if !containsString(allow.PathGlobs, want) {
			t.Fatalf("PathGlobs=%#v, missing %q", allow.PathGlobs, want)
		}
	}
	if allow.PlaceholderRe == nil || !allow.PlaceholderRe.MatchString("AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("PlaceholderRe did not match donor example key")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertNoRawSecret(t *testing.T, result leakscan.Result, secret string) {
	t.Helper()
	if strings.Contains(result.Redacted, secret) {
		t.Fatalf("redacted content leaked raw secret")
	}
	for _, match := range result.Matches {
		if match.Masked == secret {
			t.Fatalf("match leaked raw secret: %#v", match)
		}
	}
}
