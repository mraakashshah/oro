package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"oro/pkg/cards"
	"oro/pkg/protocol"
)

// CardCandidate is the proposed card prose shape graded by the voice gate.
type CardCandidate = cards.CardCandidate

// Card is the proposed durable card shape graded by the grade worker.
type Card = cards.Card

// GradeVerdict is the closed set of verdicts emitted by the grade worker.
type GradeVerdict string

const (
	// GradeVerdictCorrect means the evidence supports applying the proposal.
	GradeVerdictCorrect GradeVerdict = "correct"
	// GradeVerdictIncorrect means the evidence refutes the proposal.
	GradeVerdictIncorrect GradeVerdict = "incorrect"
	// GradeVerdictPartial means the proposal is useful but needs human review.
	GradeVerdictPartial GradeVerdict = "partial"
	// GradeVerdictUnresolvable means the evidence is insufficient or conflicted.
	GradeVerdictUnresolvable GradeVerdict = "unresolvable"
)

// GradeEvidence is the oro-native context supplied to the grade worker.
type GradeEvidence struct {
	Events          []cards.CardEvent
	SeeAlso         []cards.CardSummary
	VectorNeighbors []cards.CardSummary
	OriginatingBead GradeBeadEvidence
}

// GradeBeadEvidence summarizes the bead that produced the proposal.
type GradeBeadEvidence struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	QGOutcome    string `json:"qg_outcome"`
	MergeOutcome string `json:"merge_outcome"`
	Summary      string `json:"summary"`
}

type gradeWorkerResult struct {
	Verdict    GradeVerdict `json:"verdict"`
	Confidence float64      `json:"confidence"`
	Reasoning  string       `json:"reasoning"`
}

type voiceGateJudgeResult struct {
	Score    int    `json:"score"`
	Feedback string `json:"feedback"`
}

func buildGradePrompt(proposal Card, evidence GradeEvidence) string {
	var b strings.Builder
	b.WriteString("You are grading an Oro card proposal against local project evidence.\n")
	b.WriteString("Use only the evidence in this prompt. If the evidence is missing or conflicted, choose unresolvable.\n\n")
	b.WriteString("Return only JSON: {\"verdict\":\"correct|incorrect|partial|unresolvable\",\"confidence\":<0-1>,\"reasoning\":\"<concise evidence-based rationale>\"}.\n\n")

	b.WriteString("## Proposal\n")
	b.WriteString(mustJSON(proposal))
	b.WriteString("\n\n")

	b.WriteString("## Evidence\n")
	b.WriteString("### card_events history\n")
	writeCardEvents(&b, evidence.Events)
	b.WriteString("\n### SeeAlso related cards\n")
	writeCardSummaries(&b, evidence.SeeAlso)
	b.WriteString("\n### Phase 2 vector neighbours\n")
	writeCardSummaries(&b, evidence.VectorNeighbors)
	b.WriteString("\n### originating bead\n")
	b.WriteString(mustJSON(evidence.OriginatingBead))
	b.WriteString("\n")

	return b.String()
}

func writeCardEvents(b *strings.Builder, events []cards.CardEvent) {
	if len(events) == 0 {
		b.WriteString("(none)\n")
		return
	}
	for _, event := range events {
		b.WriteString("- ")
		b.WriteString(mustJSON(event))
		b.WriteString("\n")
	}
}

func writeCardSummaries(b *strings.Builder, summaries []cards.CardSummary) {
	if len(summaries) == 0 {
		b.WriteString("(none)\n")
		return
	}
	for _, summary := range summaries {
		b.WriteString("- ")
		b.WriteString(mustJSON(summary))
		b.WriteString("\n")
	}
}

func parseGradeWorkerOutput(out string) (gradeWorkerResult, bool) {
	var result gradeWorkerResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		return gradeWorkerResult{}, false
	}
	if !isValidGradeVerdict(result.Verdict) {
		return gradeWorkerResult{}, false
	}
	result.Confidence = clampGradeConfidence(result.Confidence)
	return result, true
}

func isValidGradeVerdict(verdict GradeVerdict) bool {
	switch verdict {
	case GradeVerdictCorrect, GradeVerdictIncorrect, GradeVerdictPartial, GradeVerdictUnresolvable:
		return true
	default:
		return false
	}
}

func clampGradeConfidence(confidence float64) float64 {
	switch {
	case confidence < 0:
		return 0
	case confidence > 1:
		return 1
	default:
		return confidence
	}
}

// buildVoiceGatePrompt asks a cheap judge to score proposed card prose against
// the Oro card voice rubric.
func buildVoiceGatePrompt(card CardCandidate) string {
	var b strings.Builder
	b.WriteString("You are grading proposed Oro card prose for project voice.\n")
	b.WriteString("Score the prose from 1 to 5.\n\n")
	b.WriteString("Rubric:\n")
	b.WriteString("- 5: terse, operational, specific, and reusable.\n")
	b.WriteString("- 3: understandable but wordy, generic, or weakly actionable.\n")
	b.WriteString("- 1: hype, marketing voice, vague praise, or narrative filler.\n\n")
	b.WriteString("Return only JSON: {\"score\":<1-5>,\"feedback\":\"<brief actionable feedback>\"}.\n\n")
	b.WriteString("Card:\n")
	b.WriteString(mustJSON(card))
	b.WriteString("\n")
	return b.String()
}

func voiceGate(ctx context.Context, spawner BatchSpawner, card CardCandidate) (CardCandidate, error) {
	current := card
	for attempt := 0; attempt <= 2; attempt++ {
		judge, ok, err := runVoiceGateJudge(ctx, spawner, current)
		if err != nil {
			return CardCandidate{}, err
		}
		if !ok {
			return card, nil
		}
		if judge.Score >= 3 {
			return current, nil
		}
		if attempt == 2 {
			return current, nil
		}
		regenerated, ok, err := runVoiceGateRegeneration(ctx, spawner, current, judge.Feedback)
		if err != nil {
			return CardCandidate{}, err
		}
		if !ok {
			return card, nil
		}
		current = regenerated
	}
	return current, nil
}

func runVoiceGateJudge(ctx context.Context, spawner BatchSpawner, card CardCandidate) (voiceGateJudgeResult, bool, error) {
	out, err := runVoiceGateProcess(ctx, spawner, buildVoiceGatePrompt(card))
	if err != nil {
		return voiceGateJudgeResult{}, false, err
	}
	var result voiceGateJudgeResult
	if !parseVoiceGateJSON(out, &result) {
		return voiceGateJudgeResult{}, false, nil
	}
	return result, true, nil
}

func runVoiceGateRegeneration(
	ctx context.Context,
	spawner BatchSpawner,
	card CardCandidate,
	feedback string,
) (candidate CardCandidate, ok bool, err error) {
	out, err := runVoiceGateProcess(ctx, spawner, buildVoiceGateRegenerationPrompt(card, feedback))
	if err != nil {
		return CardCandidate{}, false, err
	}
	var regenerated CardCandidate
	if !parseVoiceGateJSON(out, &regenerated) {
		return CardCandidate{}, false, nil
	}
	return regenerated, true, nil
}

func parseVoiceGateJSON(out string, dst any) bool {
	return json.Unmarshal([]byte(strings.TrimSpace(out)), dst) == nil
}

func runVoiceGateProcess(ctx context.Context, spawner BatchSpawner, prompt string) (string, error) {
	proc, err := spawner.Spawn(ctx, protocol.ModelHaiku, prompt, "")
	if err != nil {
		return "", fmt.Errorf("spawn voice gate: %w", err)
	}
	if err := proc.Wait(); err != nil {
		return "", fmt.Errorf("wait for voice gate: %w", err)
	}
	out, err := proc.Output()
	if err != nil {
		return "", fmt.Errorf("read voice gate output: %w", err)
	}
	return out, nil
}

func buildVoiceGateRegenerationPrompt(card CardCandidate, feedback string) string {
	var b strings.Builder
	b.WriteString("Rewrite this proposed Oro card so it fits the project voice.\n")
	b.WriteString("Keep the same factual meaning. Use terse, operational, specific prose.\n")
	b.WriteString("Return only a JSON CardCandidate with the same schema.\n\n")
	b.WriteString("Judge feedback:\n")
	b.WriteString(feedback)
	b.WriteString("\n\nCard:\n")
	b.WriteString(mustJSON(card))
	b.WriteString("\n")
	return b.String()
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}
