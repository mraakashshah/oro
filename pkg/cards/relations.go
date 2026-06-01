package cards

import (
	"context"
	"fmt"
)

// RelationSignal identifies the evidence source for a card relation.
type RelationSignal string

// Relation signals supported by AddRelation.
const (
	RelationSignalCall      RelationSignal = "call"
	RelationSignalComention RelationSignal = "comention"
	RelationSignalWikilink  RelationSignal = "wikilink"
	RelationSignalNamespace RelationSignal = "namespace"
	RelationSignalLineage   RelationSignal = "lineage"
)

// Strength returns the additive ranking weight for the relation signal.
func (sig RelationSignal) Strength() int {
	switch sig {
	case RelationSignalCall:
		return 3
	case RelationSignalComention, RelationSignalWikilink:
		return 2
	case RelationSignalNamespace:
		return 1
	default:
		return 0
	}
}

// AddRelation records relation evidence between two cards.
func (s *SQLiteCardStore) AddRelation(ctx context.Context, srcID, dstID string, sig RelationSignal) error {
	if srcID == dstID {
		return fmt.Errorf("add relation: source and target must differ")
	}
	strength := sig.Strength()
	if strength == 0 {
		return fmt.Errorf("add relation: unknown signal %q", sig)
	}

	if err := insertRelation(ctx, s.db, srcID, dstID, sig, strength); err != nil {
		return err
	}
	if isSymmetricRelation(sig) {
		if err := insertRelation(ctx, s.db, dstID, srcID, sig, strength); err != nil {
			return err
		}
	}
	return nil
}

func insertRelation(
	ctx context.Context,
	exec sqlExecutor,
	srcID string,
	dstID string,
	sig RelationSignal,
	strength int,
) error {
	_, err := exec.ExecContext(ctx, `
		INSERT INTO card_relations(source_id, target_id, signal, strength)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id, target_id, signal)
		DO UPDATE SET strength = strength + excluded.strength`,
		srcID,
		dstID,
		sig,
		strength,
	)
	if err != nil {
		return fmt.Errorf("insert relation %s -> %s %s: %w", srcID, dstID, sig, err)
	}
	return nil
}

// SeeAlso returns one-hop related cards ordered by total relation strength.
func (s *SQLiteCardStore) SeeAlso(ctx context.Context, cardID string, limit int) ([]CardSummary, error) {
	query := `
		SELECT c.id, c.type, c.title, c.body_summary, c.body_full, c.body_deep,
		       c.tags, c.score, c.promotion_confidence, c.decay_anchor,
		       c.last_contradicted_at, c.last_nacked_at, c.created_at, c.updated_at,
		       c.retired_at, c.superseded_by, c.emerged_from, c.retired_reason
		  FROM cards c
		  JOIN (
			SELECT target_id, SUM(strength) AS total_strength
			  FROM card_relations
			 WHERE source_id = ? AND target_id != ?
			 GROUP BY target_id
		  ) related ON related.target_id = c.id
		 WHERE c.retired_at IS NULL
		 ORDER BY related.total_strength DESC, c.id ASC`
	args := []any{cardID, cardID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query see also for %s: %w", cardID, err)
	}
	defer func() { _ = rows.Close() }()

	var summaries []CardSummary
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan see also card: %w", err)
		}
		summaries = append(summaries, toInlinedCard(*card))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate see also cards: %w", err)
	}
	return summaries, nil
}

func isSymmetricRelation(sig RelationSignal) bool {
	return sig == RelationSignalComention || sig == RelationSignalNamespace
}
