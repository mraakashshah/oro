package cards

import (
	"context"
	"fmt"
	"sort"
)

const calibrationColdStartResolved = 5

// Scorecard summarizes resolved proposal grading accuracy.
type Scorecard struct {
	Resolved       int
	Skipped        bool
	Buckets        []ScorecardBucket
	ActiveBiasTags []string
}

// ScorecardBucket is one card-type by bead-type calibration bucket.
type ScorecardBucket struct {
	CardType CardType
	BeadType string
	Resolved int
	Correct  int
	Accuracy float64
	Brier    float64
}

type calibrationRow struct {
	cardType CardType
	beadType string
	verdict  GradeVerdictValue
	count    int
}

type calibrationAccumulator struct {
	cardType CardType
	beadType string
	resolved int
	correct  int
}

// Calibration aggregates resolved proposal grades into a calibration scorecard.
func (s *SQLiteCardStore) Calibration(ctx context.Context) (Scorecard, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH origin AS (
			SELECT card_id, MIN(bead_id) AS bead_id
			  FROM card_events
			 WHERE bead_id IS NOT NULL AND bead_id != ''
			 GROUP BY card_id
		)
		SELECT c.type, COALESCE(b.type, 'unknown') AS bead_type, c.grade_verdict, COUNT(*)
		  FROM cards c
		  LEFT JOIN origin o ON o.card_id = c.id
		  LEFT JOIN beads b ON b.id = o.bead_id
		 WHERE c.grade_state IN ('applied', 'rejected')
		   AND c.grade_verdict IN ('correct', 'incorrect', 'partial')
		 GROUP BY c.type, bead_type, c.grade_verdict
		 ORDER BY c.type, bead_type, c.grade_verdict`)
	if err != nil {
		return Scorecard{}, fmt.Errorf("query calibration rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	accumulators := map[string]*calibrationAccumulator{}
	total := 0
	for rows.Next() {
		row, err := scanCalibrationRow(rows)
		if err != nil {
			return Scorecard{}, err
		}
		key := string(row.cardType) + "\x00" + row.beadType
		acc := accumulators[key]
		if acc == nil {
			acc = &calibrationAccumulator{cardType: row.cardType, beadType: row.beadType}
			accumulators[key] = acc
		}
		acc.resolved += row.count
		total += row.count
		if row.verdict == GradeVerdictCorrect {
			acc.correct += row.count
		}
	}
	if err := rows.Err(); err != nil {
		return Scorecard{}, fmt.Errorf("iterate calibration rows: %w", err)
	}

	scorecard := Scorecard{Resolved: total}
	if total < calibrationColdStartResolved {
		scorecard.Skipped = true
		return scorecard, nil
	}

	scorecard.Buckets = calibrationBuckets(accumulators)
	scorecard.ActiveBiasTags = activeBiasTags(scorecard.Buckets)
	return scorecard, nil
}

func scanCalibrationRow(row interface{ Scan(...any) error }) (calibrationRow, error) {
	var result calibrationRow
	var cardType, verdict string
	if err := row.Scan(&cardType, &result.beadType, &verdict, &result.count); err != nil {
		return calibrationRow{}, fmt.Errorf("scan calibration row: %w", err)
	}
	result.cardType = CardType(cardType)
	result.verdict = GradeVerdictValue(verdict)
	return result, nil
}

func calibrationBuckets(accumulators map[string]*calibrationAccumulator) []ScorecardBucket {
	buckets := make([]ScorecardBucket, 0, len(accumulators))
	for _, acc := range accumulators {
		accuracy := float64(acc.correct) / float64(acc.resolved)
		buckets = append(buckets, ScorecardBucket{
			CardType: acc.cardType,
			BeadType: acc.beadType,
			Resolved: acc.resolved,
			Correct:  acc.correct,
			Accuracy: accuracy,
			Brier:    historicalBucketBrier(acc.correct, acc.resolved),
		})
	}
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].CardType != buckets[j].CardType {
			return buckets[i].CardType < buckets[j].CardType
		}
		return buckets[i].BeadType < buckets[j].BeadType
	})
	return buckets
}

func historicalBucketBrier(correct, resolved int) float64 {
	if resolved == 0 {
		return 0
	}
	rate := float64(correct) / float64(resolved)
	wrong := resolved - correct
	return (float64(correct)*(1-rate)*(1-rate) + float64(wrong)*rate*rate) / float64(resolved)
}

func activeBiasTags(buckets []ScorecardBucket) []string {
	tags := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.Accuracy < 0.5 {
			tags = append(tags, "low_accuracy:"+string(bucket.CardType)+":"+bucket.BeadType)
		}
	}
	sort.Strings(tags)
	return tags
}
