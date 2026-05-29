package leakscan

import (
	"regexp"
	"testing"
)

func TestEntropy_DirectCandidatesHonorMinimumBits(t *testing.T) {
	t.Parallel()

	matches := entropyCandidates("token=qwertyuiopASDFGHJKLzxcvbnm123456", 4.0, Allowlist{})
	if len(matches) != 1 {
		t.Fatalf("matches=%d, want 1: %#v", len(matches), matches)
	}
	if matches[0].Action != ActionWarn {
		t.Fatalf("action=%q, want warn", matches[0].Action)
	}
	if matches[0].Pattern != "high_entropy_token" {
		t.Fatalf("pattern=%q, want high_entropy_token", matches[0].Pattern)
	}

	if got := entropyCandidates("token=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 4.0, Allowlist{}); len(got) != 0 {
		t.Fatalf("low entropy matches=%d, want 0: %#v", len(got), got)
	}
}

func TestAllowlist_PathGlobsAndPlaceholders(t *testing.T) {
	t.Parallel()

	allow := Allowlist{
		PathGlobs:     []string{"fixtures/*.txt"},
		PlaceholderRe: regexp.MustCompile(`EXAMPLE$`),
	}
	if !allow.containsPath("pkg/leakscan/testdata/example.txt") {
		t.Fatalf("default testdata glob did not match nested testdata fixture")
	}
	if !allow.containsPath("pkg/leakscan/leakscan_test.go") {
		t.Fatalf("default *_test.go glob did not match test fixture")
	}
	if !allow.containsPath("fixtures/secrets.txt") {
		t.Fatalf("custom path glob did not match")
	}
	if !allow.contains("aws_access_key", "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("placeholder regex did not exempt donor example key")
	}
}
