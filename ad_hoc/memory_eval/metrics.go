package memoryeval

import "fmt"

const (
	WarmMRRRatio = 1.30
	ColdMRRRatio = 1.20
)

type GateResult struct {
	Pass   bool
	Reason string
}

func CheckGate(baseMRR, warmMRR, coldMRR float64) GateResult {
	if baseMRR == 0 {
		return GateResult{
			Pass:   false,
			Reason: "baseline MRR is 0 — search is broken, cannot compute ratio",
		}
	}
	if warmMRR < WarmMRRRatio*baseMRR {
		return GateResult{
			Pass:   false,
			Reason: fmt.Sprintf("warm MRR %.4f < 1.30×%.4f baseline", warmMRR, baseMRR),
		}
	}
	if coldMRR < ColdMRRRatio*baseMRR {
		return GateResult{
			Pass:   false,
			Reason: fmt.Sprintf("cold MRR %.4f < 1.20×%.4f baseline", coldMRR, baseMRR),
		}
	}
	return GateResult{Pass: true}
}

// MRR returns the reciprocal rank of relevantID in topKIDs[:k].
// Returns 0 if relevantID is absent, k <= 0, or topKIDs is empty.
// Caller averages over queries to get Mean Reciprocal Rank.
func MRR(topKIDs []int64, relevantID int64, k int) float64 {
	if k <= 0 || len(topKIDs) == 0 {
		return 0
	}
	limit := k
	if limit > len(topKIDs) {
		limit = len(topKIDs)
	}
	for i, id := range topKIDs[:limit] {
		if id == relevantID {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// HitAtK returns 1.0 if relevantID appears in topKIDs[:k], else 0.0.
// Returns 0 if k <= 0 or topKIDs is empty.
func HitAtK(topKIDs []int64, relevantID int64, k int) float64 {
	if k <= 0 || len(topKIDs) == 0 {
		return 0
	}
	limit := k
	if limit > len(topKIDs) {
		limit = len(topKIDs)
	}
	for _, id := range topKIDs[:limit] {
		if id == relevantID {
			return 1.0
		}
	}
	return 0
}
