package cards

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// ParseWikilinks extracts [[target]] references from a card body.
func ParseWikilinks(body string) []string {
	var links []string
	seen := make(map[string]struct{})
	for {
		start := strings.Index(body, "[[")
		if start == -1 {
			return links
		}
		body = body[start+2:]
		end := strings.Index(body, "]]")
		if end == -1 {
			return links
		}
		target := strings.TrimSpace(body[:end])
		if target != "" {
			if _, ok := seen[target]; !ok {
				seen[target] = struct{}{}
				links = append(links, target)
			}
		}
		body = body[end+2:]
	}
}

// Lineage walks from a card backward through cards superseded by descendants.
func (s *SQLiteCardStore) Lineage(ctx context.Context, id string) ([]Card, error) {
	current, err := s.Show(ctx, id)
	if err != nil {
		return nil, err
	}
	visited := map[string]struct{}{current.ID: {}}
	lineage := []Card{*current}

	for {
		previous, err := s.previousInChain(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		if previous == nil {
			return lineage, nil
		}
		if _, ok := visited[previous.ID]; ok {
			return nil, ErrCycleDetected
		}
		visited[previous.ID] = struct{}{}
		lineage = append(lineage, *previous)
		current = previous
	}
}

// LatestInChain walks superseded_by links forward to the newest active card.
func (s *SQLiteCardStore) LatestInChain(ctx context.Context, id string) (*Card, error) {
	current, err := s.Show(ctx, id)
	if err != nil {
		return nil, err
	}
	visited := map[string]struct{}{current.ID: {}}

	for current.SupersededBy != nil && *current.SupersededBy != "" {
		next, err := s.Show(ctx, *current.SupersededBy)
		if err != nil {
			return nil, err
		}
		if _, ok := visited[next.ID]; ok {
			return nil, ErrCycleDetected
		}
		visited[next.ID] = struct{}{}
		current = next
	}
	if current.RetiredAt != nil {
		return nil, ErrNotFound
	}
	return current, nil
}

func (s *SQLiteCardStore) previousInChain(ctx context.Context, id string) (*Card, error) {
	row := s.db.QueryRowContext(ctx, `SELECT`+cardSelectCols+` FROM cards WHERE superseded_by = ? LIMIT 1`, id)
	card, err := scanCard(row)
	if err != nil {
		if errorsIsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return card, nil
}

func addWikilinkRelations(ctx context.Context, exec sqlReadWriter, sourceID, body string) error {
	for _, target := range ParseWikilinks(body) {
		ids, err := cardIDsByTitle(ctx, exec, target)
		if err != nil {
			return err
		}
		for _, targetID := range ids {
			if targetID == sourceID {
				continue
			}
			if err := insertRelation(ctx, exec, sourceID, targetID, RelationSignalWikilink, RelationSignalWikilink.Strength()); err != nil {
				return err
			}
		}
	}
	return nil
}

func cardIDsByTitle(ctx context.Context, exec sqlReadWriter, title string) ([]string, error) {
	rows, err := exec.QueryContext(ctx, `SELECT id FROM cards WHERE title = ? AND retired_at IS NULL`, title)
	if err != nil {
		return nil, fmt.Errorf("query wikilink target %q: %w", title, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan wikilink target %q: %w", title, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wikilink targets %q: %w", title, err)
	}
	return ids, nil
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
