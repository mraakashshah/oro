package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ModelAdapterConfig identifies the frozen helper and its nonsecret credential reference.
type ModelAdapterConfig struct {
	Protocol       string `json:"protocol"`
	CredentialPath string `json:"credential_path"`
	HelperSHA256   string `json:"helper_sha256"`
}

// EqualModelAdapterConfig compares optional configurations by value.
func EqualModelAdapterConfig(a, b *ModelAdapterConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ModelSourceFile is part of the frozen Terra wire contract.
type ModelSourceFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// ModelProtectedAcceptance is part of the frozen Terra wire contract.
type ModelProtectedAcceptance struct {
	CandidateSHA string   `json:"candidate_sha"`
	Passed       bool     `json:"passed"`
	Checks       []string `json:"checks"`
	Feedback     string   `json:"feedback"`
}

// ModelRoleContext is part of the frozen Terra wire contract.
type ModelRoleContext struct {
	BaseSHA                string                    `json:"base_sha"`
	SourceSHA              string                    `json:"source_sha"`
	SeedSHA256             string                    `json:"seed_sha256"`
	PolicyHash             string                    `json:"policy_hash"`
	RequiredChecks         []string                  `json:"required_checks"`
	SourceFiles            []ModelSourceFile         `json:"source_files"`
	CandidateSHA           string                    `json:"candidate_sha"`
	Acceptance             *ModelProtectedAcceptance `json:"acceptance,omitempty"`
	AcceptanceOutputSHA256 string                    `json:"acceptance_output_sha256"`
}

// ModelRequest is part of the frozen Terra wire contract.
type ModelRequest struct {
	Version                  int                     `json:"version"`
	StateRoot                string                  `json:"state_root"`
	ObjectiveID              string                  `json:"objective_id"`
	Invocation               ScopeHandle             `json:"invocation"`
	CandidateID              string                  `json:"candidate_id"`
	CandidateScope           ScopeHandle             `json:"candidate_scope"`
	CandidateBindingSHA256   string                  `json:"candidate_binding_sha256"`
	Runtime                  ManagedRuntimeReference `json:"runtime"`
	Adapter                  ModelAdapterConfig      `json:"adapter"`
	Role                     string                  `json:"role"`
	Model                    string                  `json:"model"`
	ExecutionDeadline        time.Time               `json:"execution_deadline"`
	MaxTurns                 int                     `json:"max_turns"`
	InvocationLiabilityCents int64                   `json:"invocation_liability_cents"`
	InputTokenCeiling        int                     `json:"input_token_ceiling"`
	OutputTokenCeiling       int                     `json:"output_token_ceiling"`
	TranscriptByteLimit      int64                   `json:"transcript_byte_limit"`
	ToolSocket               string                  `json:"tool_socket"`
	Prompt                   string                  `json:"prompt"`
}

// ModelTurnRecord is part of the frozen Terra wire contract.
type ModelTurnRecord struct {
	Turn            int      `json:"turn"`
	CountDispatched bool     `json:"count_dispatched"`
	ModelDispatched bool     `json:"model_dispatched"`
	ResponseID      string   `json:"response_id"`
	ObservedModel   string   `json:"observed_model"`
	Outcome         string   `json:"outcome"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	LiabilityCents  int64    `json:"liability_cents"`
	ToolCallIDs     []string `json:"tool_call_ids"`
}

// ModelResult is part of the frozen Terra wire contract.
type ModelResult struct {
	Version             int               `json:"version"`
	Invocation          ScopeHandle       `json:"invocation"`
	RequestSHA256       string            `json:"request_sha256"`
	Outcome             string            `json:"outcome"`
	TurnsDispatched     int               `json:"turns_dispatched"`
	LiabilitySpentCents int64             `json:"liability_spent_cents"`
	Turns               []ModelTurnRecord `json:"turns"`
	FinalText           string            `json:"final_text"`
	ErrorCode           string            `json:"error_code"`
}

// EncodeModelRequest validates the bounded canonical model protocol.
func EncodeModelRequest(r ModelRequest) ([]byte, error) {
	if err := validateModelRequest(r); err != nil {
		return nil, err
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("invalid bounded model request")
	}
	return data, nil
}

// DecodeModelRequest validates the bounded canonical model protocol.
func DecodeModelRequest(data []byte) (ModelRequest, error) {
	var r ModelRequest
	if len(data) > 64<<10 || !utf8.Valid(data) {
		return r, errors.New("invalid bounded model request")
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("decode model JSON: %w", err)
	}
	canonical, err := EncodeModelRequest(r)
	if err != nil || !bytes.Equal(data, canonical) {
		return ModelRequest{}, errors.New("model request is not canonical")
	}
	return r, nil
}

func validateModelRequest(r ModelRequest) error {
	if err := validateModelRequestIdentity(r); err != nil {
		return err
	}
	return validateModelRequestLimits(r)
}

func validateModelRequestIdentity(r ModelRequest) error {
	if r.Version != 1 || !managedAbsolutePath(r.StateRoot) || !utf8.ValidString(r.StateRoot) || len(r.StateRoot) > 4096 || !managedIdentifier(r.ObjectiveID) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(r.CandidateID) || validateManagedScope(r.Invocation) != nil || r.Invocation.Kind != "host" || validateManagedScope(r.CandidateScope) != nil || r.CandidateScope.Kind != "container" || !validManagedRuntimeReference(r.StateRoot, r.ObjectiveID, r.Runtime) || !managedKeeperID(r.CandidateBindingSHA256) {
		return errors.New("invalid model request identity")
	}
	return validateModelAdapterIdentity(r)
}

func validateModelAdapterIdentity(r ModelRequest) error {
	if r.Adapter.Protocol != "oro-terra-responses-v1" || !managedKeeperID(r.Adapter.HelperSHA256) || !managedAbsolutePath(r.Adapter.CredentialPath) || !utf8.ValidString(r.Adapter.CredentialPath) || strings.ContainsRune(r.Adapter.CredentialPath, 0) || len(r.Adapter.CredentialPath) > 4096 || r.Role != "worker" || r.Model != "gpt-6.1-terra" {
		return errors.New("invalid model adapter")
	}
	return nil
}

func validateModelRequestLimits(r ModelRequest) error {
	if r.ExecutionDeadline.IsZero() || r.ExecutionDeadline.Location() != time.UTC || r.MaxTurns < 1 || r.MaxTurns > 1024 || r.InvocationLiabilityCents <= 0 || r.InputTokenCeiling != 16384 || r.OutputTokenCeiling != 4096 || r.TranscriptByteLimit < 1 || r.TranscriptByteLimit > 1<<20 || !managedAbsolutePath(r.ToolSocket) || !utf8.ValidString(r.ToolSocket) || len(r.ToolSocket) >= 104 || !utf8.ValidString(r.Prompt) || len(r.Prompt) > 32<<10 {
		return errors.New("invalid model request limits")
	}
	return nil
}

// encodeModelResult validates the bounded canonical model protocol.
func encodeModelResult(r ModelResult) ([]byte, error) {
	if err := validateModelResult(r); err != nil {
		return nil, err
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("invalid bounded model result")
	}
	return data, nil
}

// decodeModelResult validates the bounded canonical model protocol.
func decodeModelResult(data []byte) (ModelResult, error) {
	var r ModelResult
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return r, errors.New("invalid bounded model result")
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("decode model JSON: %w", err)
	}
	canonical, err := encodeModelResult(r)
	if err != nil || !bytes.Equal(data, canonical) {
		return ModelResult{}, errors.New("model result is not canonical")
	}
	return r, nil
}

func validateModelResult(r ModelResult) error {
	if err := validateModelResultIdentity(r); err != nil {
		return err
	}
	if err := validateModelResultOutcome(r); err != nil {
		return err
	}
	var liability int64
	dispatched := 0
	for i, turn := range r.Turns {
		if !validModelTurn(turn, i, liability) {
			return errors.New("invalid model turn")
		}
		for _, id := range turn.ToolCallIDs {
			if id == "" || len(id) > 256 || !utf8.ValidString(id) {
				return errors.New("invalid tool call ID")
			}
		}
		liability += turn.LiabilityCents
		if turn.ModelDispatched {
			dispatched++
		}
	}
	if dispatched != r.TurnsDispatched || liability != r.LiabilitySpentCents {
		return errors.New("invalid model result accounting")
	}
	return nil
}

func validateModelResultIdentity(r ModelResult) error {
	if r.Version != 1 || validateManagedScope(r.Invocation) != nil || r.Invocation.Kind != "host" || !managedKeeperID(r.RequestSHA256) || r.Turns == nil || len(r.Turns) > 1024 || r.TurnsDispatched < 0 || r.LiabilitySpentCents < 0 || len(r.FinalText) > 64<<10 || !utf8.ValidString(r.FinalText) || len(r.ErrorCode) > 64 || (r.ErrorCode != "" && !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(r.ErrorCode)) {
		return errors.New("invalid model result")
	}
	return nil
}

func validateModelResultOutcome(r ModelResult) error {
	if r.Outcome == "completed" {
		if r.ErrorCode != "" || r.FinalText == "" {
			return errors.New("invalid completed model result")
		}
	} else if (r.Outcome != "failed" && r.Outcome != "interrupted") || r.ErrorCode == "" {
		return errors.New("invalid model result outcome")
	}
	return nil
}

func validModelTurn(turn ModelTurnRecord, index int, liability int64) bool {
	return turn.Turn == index+1 && turn.ToolCallIDs != nil && len(turn.ToolCallIDs) <= 1 && len(turn.ResponseID) <= 256 && utf8.ValidString(turn.ResponseID) && len(turn.ObservedModel) <= 256 && utf8.ValidString(turn.ObservedModel) && turn.InputTokens >= 0 && turn.OutputTokens >= 0 && turn.LiabilityCents >= 0 && liability <= math.MaxInt64-turn.LiabilityCents && (!turn.ModelDispatched || turn.CountDispatched) && validModelTurnOutcome(turn.Outcome)
}

// RunTerraModel refuses unqualified transport without reading credentials or making network requests.
func RunTerraModel(ctx context.Context, request ModelRequest, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("model context: %w", err)
	}
	if _, err := EncodeModelRequest(request); err != nil {
		return err
	}
	if output == nil {
		return errors.New("model output is required")
	}
	return errors.New("terra model adapter unavailable")
}

func validModelTurnOutcome(outcome string) bool {
	return outcome == "completed" || outcome == "incomplete" || outcome == "failed" || outcome == "unknown"
}
