package cards_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"oro/pkg/cards"
)

func TestCardCandidateRoundTrip(t *testing.T) {
	want := cards.CardCandidate{
		Type:        "pattern",
		Title:       "stderr breaks json parsing",
		BodySummary: "capture stdout separately from stderr",
		BodyFull:    "When parsing command JSON output, use stdout only so warnings on stderr cannot corrupt the payload.",
		Confidence:  0.82,
		Evidence:    []string{"pkg/dispatcher/beadsource_test.go:42", "card-c5eb5dd5"},
		Tags:        []string{"bd", "json"},
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got, err := cards.ParseCardCandidate(encoded)
	if err != nil {
		t.Fatalf("ParseCardCandidate: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}

	clampedHigh, err := cards.ParseCardCandidate([]byte(`{
		"type":"rule",
		"title":"high",
		"body_summary":"summary",
		"body_full":"full",
		"confidence":1.5,
		"evidence":["test"],
		"tags":[]
	}`))
	if err != nil {
		t.Fatalf("ParseCardCandidate high confidence: %v", err)
	}
	if clampedHigh.Confidence != 1 {
		t.Fatalf("confidence above 1 should clamp to 1, got %v", clampedHigh.Confidence)
	}

	clampedLow, err := cards.ParseCardCandidate([]byte(`{
		"type":"taste",
		"title":"low",
		"body_summary":"summary",
		"body_full":"full",
		"confidence":-0.25,
		"evidence":["test"],
		"tags":[]
	}`))
	if err != nil {
		t.Fatalf("ParseCardCandidate low confidence: %v", err)
	}
	if clampedLow.Confidence != 0 {
		t.Fatalf("confidence below 0 should clamp to 0, got %v", clampedLow.Confidence)
	}

	emptyEvidence, err := cards.ParseCardCandidate([]byte(`{
		"type":"fact",
		"title":"no evidence",
		"body_summary":"summary",
		"body_full":"full",
		"confidence":0.9,
		"evidence":[],
		"tags":["candidate"]
	}`))
	if err != nil {
		t.Fatalf("ParseCardCandidate empty evidence: %v", err)
	}
	if emptyEvidence.Confidence != 0.4 {
		t.Fatalf("empty evidence should cap confidence at 0.4, got %v", emptyEvidence.Confidence)
	}

	for _, validType := range []string{"rule", "pattern", "decision", "taste", "fact"} {
		t.Run("valid_type_"+validType, func(t *testing.T) {
			_, err := cards.ParseCardCandidate([]byte(`{
				"type":"` + validType + `",
				"title":"valid",
				"body_summary":"summary",
				"body_full":"full",
				"confidence":0.5,
				"evidence":["test"],
				"tags":[]
			}`))
			if err != nil {
				t.Fatalf("ParseCardCandidate valid type %q: %v", validType, err)
			}
		})
	}

	_, err = cards.ParseCardCandidate([]byte(`{
		"type":"memory",
		"title":"invalid",
		"body_summary":"summary",
		"body_full":"full",
		"confidence":0.5,
		"evidence":["test"],
		"tags":[]
	}`))
	if !errors.Is(err, cards.ErrInvalidCardType) {
		t.Fatalf("invalid type error = %v, want ErrInvalidCardType", err)
	}
}
