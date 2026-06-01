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

func isSymmetricRelation(sig RelationSignal) bool {
	return sig == RelationSignalComention || sig == RelationSignalNamespace
}
