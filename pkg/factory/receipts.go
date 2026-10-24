package factory

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type acceptanceReceipt struct {
	CandidateSHA string   `json:"candidate_sha"`
	Passed       bool     `json:"passed"`
	Checks       []string `json:"checks"`
	Feedback     string   `json:"feedback"`
}

type reviewReceipt struct {
	CandidateSHA string `json:"candidate_sha"`
	Approved     bool   `json:"approved"`
	Feedback     string `json:"feedback"`
}

func acceptEvidence(result commandResult, candidate, base, policy string, checks []string) (*evidence, error) {
	if err := receiptPrerequisites(result, candidate, base, policy); err != nil {
		return nil, receiptFailure("acceptance", result, err)
	}
	var receipt acceptanceReceipt
	if err := decodeReceipt(result.Stdout, &receipt); err != nil {
		return nil, receiptFailure("acceptance", result, err)
	}
	if receipt.CandidateSHA != candidate || !receipt.Passed {
		return nil, receiptFailure("acceptance", result, errors.New("exact candidate SHA and passed verdict required"))
	}
	if err := receiptChecks(checks, receipt.Checks); err != nil {
		return nil, receiptFailure("acceptance", result, err)
	}
	return newEvidence("acceptance", result, candidate, base, policy), nil
}

func reviewEvidence(result commandResult, candidate, base, policy string) (*evidence, error) {
	if err := receiptPrerequisites(result, candidate, base, policy); err != nil {
		return nil, receiptFailure("review", result, err)
	}
	var receipt reviewReceipt
	if err := decodeReceipt(result.Stdout, &receipt); err != nil {
		return nil, receiptFailure("review", result, err)
	}
	if receipt.CandidateSHA != candidate || !receipt.Approved {
		return nil, receiptFailure("review", result, errors.New("exact candidate SHA and approved verdict required"))
	}
	return newEvidence("review", result, candidate, base, policy), nil
}

func receiptPrerequisites(result commandResult, candidate, base, policy string) error {
	if result.Err != nil {
		return fmt.Errorf("policy process failed: %w", result.Err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("policy exited with status %d", result.ExitCode)
	}
	if len(result.Stdout) > commandOutputLimit || len(result.Stderr) > commandOutputLimit {
		return errors.New("policy output limit exceeded")
	}
	if strings.TrimSpace(candidate) == "" || strings.TrimSpace(base) == "" || strings.TrimSpace(policy) == "" {
		return errors.New("candidate, base, and policy identities must be nonempty")
	}
	return nil
}

func decodeReceipt(output []byte, receipt any) error {
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(receipt); err != nil {
		return fmt.Errorf("decode policy receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("receipt must contain exactly one JSON document and no trailing output")
	}
	return nil
}

func receiptChecks(required, supplied []string) error {
	if len(required) == 0 {
		return errors.New("required checks must be nonempty")
	}
	wanted := make(map[string]bool, len(required))
	for _, check := range required {
		if strings.TrimSpace(check) == "" || wanted[check] {
			return errors.New("required checks must be nonempty and unique")
		}
		wanted[check] = true
	}
	seen := make(map[string]bool, len(supplied))
	for _, check := range supplied {
		if strings.TrimSpace(check) == "" || seen[check] {
			return errors.New("supplied checks must be nonempty and unique")
		}
		seen[check] = true
	}
	for _, check := range required {
		if !seen[check] {
			return fmt.Errorf("required check %q missing from receipt", check)
		}
	}
	return nil
}

func receiptFailure(stage string, result commandResult, err error) error {
	stdout := result.Stdout[:min(len(result.Stdout), commandOutputLimit)]
	stderr := result.Stderr[:min(len(result.Stderr), commandOutputLimit)]
	return fmt.Errorf("%s receipt rejected: %w; stdout: %s; stderr: %s", stage, err, stdout, stderr)
}

func newEvidence(stage string, result commandResult, candidate, base, policy string) *evidence {
	return &evidence{
		Stage: stage, CandidateSHA: candidate, BaseSHA: base, PolicyHash: policy,
		ExitCode: result.ExitCode, OutputHash: fmt.Sprintf("%x", sha256.Sum256(result.Stdout)),
	}
}
