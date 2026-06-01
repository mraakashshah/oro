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

type voiceGateJudgeResult struct {
	Score    int    `json:"score"`
	Feedback string `json:"feedback"`
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
