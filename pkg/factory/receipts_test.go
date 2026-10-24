package factory //nolint:testpackage // Receipt admission exercises private evidence invariants.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCandidateReceipts(t *testing.T) {
	t.Run("acceptance binds every identity", runnerTestAcceptanceEvidence)
	t.Run("review binds every identity", runnerTestReviewEvidence)
	t.Run("invalid acceptance", runnerTestRejectAcceptance)
	t.Run("invalid review", runnerTestRejectReview)
	t.Run("invalid required checks", runnerTestInvalidChecks)
	t.Run("missing evidence identities", runnerTestMissingIdentities)
	t.Run("process failure wins", runnerTestReceiptProcessFailure)
}

func runnerTestAcceptanceEvidence(t *testing.T) {
	t.Helper()
	output := []byte(`{"candidate_sha":"candidate","passed":true,"checks":["positive","negative","extra"],"feedback":"done"}`)
	got, err := acceptEvidence(commandResult{Stdout: output}, "candidate", "base", "policy", []string{"positive", "negative"})
	want := evidence{Stage: "acceptance", CandidateSHA: "candidate", BaseSHA: "base", PolicyHash: "policy", OutputHash: fmt.Sprintf("%x", sha256.Sum256(output))}
	if err != nil || got == nil || *got != want {
		t.Fatalf("evidence = %#v, error %v; want %#v", got, err, want)
	}
}

func runnerTestReviewEvidence(t *testing.T) {
	t.Helper()
	output := []byte(`{"candidate_sha":"candidate","approved":true,"feedback":"reviewed"}`)
	got, err := reviewEvidence(commandResult{Stdout: output}, "candidate", "base", "policy")
	want := evidence{Stage: "review", CandidateSHA: "candidate", BaseSHA: "base", PolicyHash: "policy", OutputHash: fmt.Sprintf("%x", sha256.Sum256(output))}
	if err != nil || got == nil || *got != want {
		t.Fatalf("evidence = %#v, error %v; want %#v", got, err, want)
	}
}

func runnerTestRejectAcceptance(t *testing.T) {
	t.Helper()
	for name, output := range map[string]string{
		"missing SHA":        `{"passed":true,"checks":["positive"]}`,
		"empty SHA":          `{"candidate_sha":"","passed":true,"checks":["positive"]}`,
		"wrong SHA":          `{"candidate_sha":"other","passed":true,"checks":["positive"]}`,
		"no checks":          `{"candidate_sha":"candidate","passed":true}`,
		"missing required":   `{"candidate_sha":"candidate","passed":true,"checks":["other"]}`,
		"duplicate supplied": `{"candidate_sha":"candidate","passed":true,"checks":["positive","positive"]}`,
		"empty supplied":     `{"candidate_sha":"candidate","passed":true,"checks":["positive",""]}`,
		"negative":           `{"candidate_sha":"candidate","passed":false,"checks":["positive"],"feedback":"repair addition"}`,
		"missing verdict":    `{"candidate_sha":"candidate","checks":["positive"]}`,
		"unknown field":      `{"candidate_sha":"candidate","passed":true,"checks":["positive"],"override":true}`,
		"trailing document":  `{"candidate_sha":"candidate","passed":true,"checks":["positive"]} {}`,
		"trailing garbage":   `{"candidate_sha":"candidate","passed":true,"checks":["positive"]} done`,
		"malformed":          `{"candidate_sha":"candidate",`,
		"null":               `null`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := acceptEvidence(commandResult{Stdout: []byte(output), Stderr: []byte("checker diagnostic")}, "candidate", "base", "policy", []string{"positive"})
			if err == nil || got != nil || !strings.Contains(err.Error(), "checker diagnostic") {
				t.Fatalf("invalid acceptance got %#v, error %v", got, err)
			}
			if name == "negative" && !strings.Contains(err.Error(), "repair addition") {
				t.Fatalf("feedback lost: %v", err)
			}
		})
	}
}

func runnerTestRejectReview(t *testing.T) {
	t.Helper()
	for name, output := range map[string]string{
		"missing SHA":       `{"approved":true}`,
		"wrong SHA":         `{"candidate_sha":"other","approved":true}`,
		"negative":          `{"candidate_sha":"candidate","approved":false,"feedback":"repair review"}`,
		"missing verdict":   `{"candidate_sha":"candidate"}`,
		"unknown field":     `{"candidate_sha":"candidate","approved":true,"checks":[]}`,
		"trailing document": `{"candidate_sha":"candidate","approved":true} {}`,
		"trailing garbage":  `{"candidate_sha":"candidate","approved":true} done`,
		"malformed":         `{"candidate_sha":"candidate",`,
		"null":              `null`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := reviewEvidence(commandResult{Stdout: []byte(output), Stderr: []byte("review diagnostic")}, "candidate", "base", "policy")
			if err == nil || got != nil || !strings.Contains(err.Error(), "review diagnostic") {
				t.Fatalf("invalid review got %#v, error %v", got, err)
			}
			if name == "negative" && !strings.Contains(err.Error(), "repair review") {
				t.Fatalf("feedback lost: %v", err)
			}
		})
	}
}

func runnerTestInvalidChecks(t *testing.T) {
	t.Helper()
	for _, checks := range [][]string{nil, {}, {""}, {" "}, {"positive", "positive"}} {
		result := commandResult{Stdout: []byte(`{"candidate_sha":"candidate","passed":true,"checks":["positive"]}`)}
		if got, err := acceptEvidence(result, "candidate", "base", "policy", checks); err == nil || got != nil {
			t.Fatalf("invalid required checks %q admitted: %#v, %v", checks, got, err)
		}
	}
}

func runnerTestMissingIdentities(t *testing.T) {
	t.Helper()
	for _, identities := range [][3]string{{"", "base", "policy"}, {" ", "base", "policy"}, {"candidate", "", "policy"}, {"candidate", "base", ""}} {
		result := commandResult{Stdout: []byte(fmt.Sprintf(`{"candidate_sha":%q,"passed":true,"checks":["positive"]}`, identities[0]))}
		if got, err := acceptEvidence(result, identities[0], identities[1], identities[2], []string{"positive"}); err == nil || got != nil {
			t.Fatalf("missing acceptance identity admitted: %#v, %v", got, err)
		}
		result.Stdout = []byte(fmt.Sprintf(`{"candidate_sha":%q,"approved":true}`, identities[0]))
		if got, err := reviewEvidence(result, identities[0], identities[1], identities[2]); err == nil || got != nil {
			t.Fatalf("missing review identity admitted: %#v, %v", got, err)
		}
	}
}

func runnerTestReceiptProcessFailure(t *testing.T) {
	t.Helper()
	for _, failure := range []commandResult{{ExitCode: 9}, {Err: errors.New("timeout")}, {Err: errors.New("output limit")}, {Stderr: make([]byte, (1<<20)+1)}} {
		failure.Stdout = []byte(`{"candidate_sha":"candidate","passed":true,"checks":["positive"],"feedback":"attempt diagnostic"}`)
		if got, err := acceptEvidence(failure, "candidate", "base", "policy", []string{"positive"}); err == nil || got != nil || !strings.Contains(err.Error(), "attempt diagnostic") {
			t.Fatalf("failed acceptance process admitted or feedback lost: %#v, %v", got, err)
		}
		failure.Stdout = []byte(`{"candidate_sha":"candidate","approved":true,"feedback":"attempt diagnostic"}`)
		if got, err := reviewEvidence(failure, "candidate", "base", "policy"); err == nil || got != nil || !strings.Contains(err.Error(), "attempt diagnostic") {
			t.Fatalf("failed review process admitted or feedback lost: %#v, %v", got, err)
		}
	}
}
