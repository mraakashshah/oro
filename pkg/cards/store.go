package cards

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // sqlite driver
)

// Store is the card store interface.
type Store interface {
	Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error)
	Show(ctx context.Context, id string) (*Card, error)
	List(ctx context.Context, q ListQuery) ([]Card, error)

	RecordCardEvent(ctx context.Context, e CardEvent) error
	Create(ctx context.Context, c CardCreateParams) (*Card, error)
	Retire(ctx context.Context, id, reason string, supersededBy string) error

	WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error
}

// SQLiteCardStore is the SQLite-backed implementation of Store.
type SQLiteCardStore struct {
	db *sql.DB
}

// NewStore opens a cards store against an existing *sql.DB and applies the schema.
func NewStore(db *sql.DB) (*SQLiteCardStore, error) {
	if _, err := db.ExecContext(context.Background(), schemaDDL); err != nil {
		return nil, fmt.Errorf("apply cards schema: %w", err)
	}
	return &SQLiteCardStore{db: db}, nil
}

// --- helpers ---

func newCardID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback: time-based. Collisions are possible but rare in tests.
		return fmt.Sprintf("card-%x", time.Now().UnixNano())
	}
	return "card-" + hex.EncodeToString(b)
}

func marshalTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func unmarshalTags(s string) []string {
	if s == "" || s == "[]" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return []string{}
	}
	return tags
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse RFC3339Nano %q: %w", s, err)
	}
	return t, nil
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func scanCard(row interface { //nolint:funlen // 18-column SELECT requires 18 Scan destinations
	Scan(...any) error
},
) (*Card, error) {
	var (
		id                  string
		cardType            string
		title               string
		bodySummary         string
		bodyFull            string
		bodyDeep            sql.NullString
		tags                string
		score               float64
		promotionConfidence sql.NullFloat64
		decayAnchor         string
		lastContradictedAt  sql.NullString
		lastNackedAt        sql.NullString
		createdAt           string
		updatedAt           string
		retiredAt           sql.NullString
		supersededBy        sql.NullString
		emergedFrom         sql.NullString
		retiredReason       sql.NullString
	)

	if err := row.Scan(
		&id, &cardType, &title, &bodySummary, &bodyFull, &bodyDeep,
		&tags, &score, &promotionConfidence, &decayAnchor,
		&lastContradictedAt, &lastNackedAt, &createdAt, &updatedAt,
		&retiredAt, &supersededBy, &emergedFrom, &retiredReason,
	); err != nil {
		return nil, fmt.Errorf("scan card row: %w", err)
	}

	anchor, err := parseTime(decayAnchor)
	if err != nil {
		return nil, fmt.Errorf("parse decay_anchor: %w", err)
	}
	createdParsed, err := parseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	updatedParsed, err := parseTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	c := &Card{
		ID:          id,
		Type:        CardType(cardType),
		Title:       title,
		BodySummary: bodySummary,
		BodyFull:    bodyFull,
		Tags:        unmarshalTags(tags),
		Score:       score,
		DecayAnchor: anchor,
		CreatedAt:   createdParsed,
		UpdatedAt:   updatedParsed,
	}
	if bodyDeep.Valid {
		c.BodyDeep = &bodyDeep.String
	}
	if promotionConfidence.Valid {
		c.PromotionConfidence = &promotionConfidence.Float64
	}
	c.LastContradictedAt = parseTimePtr(lastContradictedAt)
	c.LastNackedAt = parseTimePtr(lastNackedAt)
	c.RetiredAt = parseTimePtr(retiredAt)
	if supersededBy.Valid {
		c.SupersededBy = &supersededBy.String
	}
	if emergedFrom.Valid {
		c.EmergedFrom = &emergedFrom.String
	}
	if retiredReason.Valid {
		c.RetiredReason = &retiredReason.String
	}
	return c, nil
}

const cardSelectCols = `
  id, type, title, body_summary, body_full, body_deep,
  tags, score, promotion_confidence, decay_anchor,
  last_contradicted_at, last_nacked_at, created_at, updated_at,
  retired_at, superseded_by, emerged_from, retired_reason`

// --- Store methods ---

// Show returns a card by ID. Returns ErrNotFound if the card does not exist.
func (s *SQLiteCardStore) Show(ctx context.Context, id string) (*Card, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT`+cardSelectCols+` FROM cards WHERE id = ?`, id)
	c, err := scanCard(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("show card %s: %w", id, err)
	}
	return c, nil
}

// List returns cards matching the query.
func (s *SQLiteCardStore) List(ctx context.Context, q ListQuery) ([]Card, error) {
	where := []string{}
	args := []any{}

	if !q.IncludeRetired {
		where = append(where, "retired_at IS NULL")
	}
	if q.Type != "" {
		where = append(where, "type = ?")
		args = append(args, string(q.Type))
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	query := `SELECT` + cardSelectCols + ` FROM cards` + clause + ` ORDER BY created_at DESC`

	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
		if q.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, q.Offset)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	defer rows.Close()

	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list cards rows: %w", err)
	}
	return cards, nil
}

// Relevant returns cards relevant to the query, sorted by effective score × relevance weight.
func (s *SQLiteCardStore) Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT`+cardSelectCols+` FROM cards WHERE retired_at IS NULL`)
	if err != nil {
		return RelevantCards{}, fmt.Errorf("relevant query: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	candidates, err := collectScoredCards(rows, q, now)
	if err != nil {
		return RelevantCards{}, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	return RelevantCards{
		Deck:    toSummaries(candidates),
		Inlined: buildInlined(candidates, q.MaxTokens),
	}, nil
}

type scoredCard struct {
	card  Card
	score float64
}

// scoreCardForRelevance returns (combinedScore, include). Pure filtering + scoring logic.
func scoreCardForRelevance(c *Card, q RelevanceQuery, now time.Time) (float64, bool) {
	eff := EffectiveScore(c, now)
	isSuppressed := SuppressionMultiplier(c.Type, c.LastContradictedAt, now) == 0.0
	if !q.IncludeSuppressed && isSuppressed {
		return 0, false
	}
	// When IncludeSuppressed=true, use unsuppressed score so a suppressed card isn't
	// also filtered by the low-score threshold (its eff is 0 due to suppression).
	scoreForThreshold := eff
	if isSuppressed && q.IncludeSuppressed {
		scoreForThreshold = c.Score * DecayMultiplier(c.Type, c.DecayAnchor, now)
	}
	if !q.IncludeLowScore && scoreForThreshold < DefaultThreshold {
		return 0, false
	}
	return eff * relevanceScore(c, q), true
}

func collectScoredCards(rows *sql.Rows, q RelevanceQuery, now time.Time) ([]scoredCard, error) {
	var out []scoredCard
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		combined, ok := scoreCardForRelevance(c, q, now)
		if !ok {
			continue
		}
		out = append(out, scoredCard{card: *c, score: combined})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cards rows: %w", err)
	}
	return out, nil
}

func toSummary(c Card) CardSummary {
	return CardSummary{
		ID:          c.ID,
		Type:        c.Type,
		Title:       c.Title,
		BodySummary: c.BodySummary,
		BodyFull:    c.BodyFull,
		Score:       c.Score,
		Tags:        c.Tags,
	}
}

func toSummaries(candidates []scoredCard) []CardSummary {
	out := make([]CardSummary, 0, len(candidates))
	for _, sc := range candidates {
		out = append(out, toSummary(sc.card))
	}
	return out
}

// buildInlined returns cards whose body_full fits within maxTokens budget.
func buildInlined(candidates []scoredCard, maxTokens int) []CardSummary {
	if maxTokens <= 0 {
		return nil
	}
	var out []CardSummary
	budget := maxTokens
	for _, sc := range candidates {
		tokens := estimateTokens(sc.card.BodyFull)
		if tokens > budget {
			break
		}
		budget -= tokens
		out = append(out, toSummary(sc.card))
	}
	return out
}

// relevanceScore computes a [0,1] relevance weight for a card given a query.
// Weighted combination per spec §5.9.
func relevanceScore(c *Card, q RelevanceQuery) float64 {
	tagScore := jaccardSimilarity(c.Tags, q.BeadTags)          // weight 0.4
	textScore := wordOverlap(c.BodySummary, q.BeadDescription) // weight 0.3
	symbolScore := symbolOverlap(c.Tags, q.SymbolHints)        // weight 0.2
	typeScore := beadTypeMatch(c.Type, q.BeadType)             // weight 0.1

	return tagScore*0.4 + textScore*0.3 + symbolScore*0.2 + typeScore*0.1
}

func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	setA := make(map[string]bool, len(a))
	for _, v := range a {
		setA[v] = true
	}
	intersection := 0
	union := len(setA)
	for _, v := range b {
		if setA[v] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0.0
	}
	return float64(intersection) / float64(union)
}

func wordOverlap(text, query string) float64 {
	if query == "" {
		return 0.5 // neutral when no description provided
	}
	textWords := tokenize(text)
	queryWords := tokenize(query)
	if len(queryWords) == 0 {
		return 0.0
	}
	textSet := make(map[string]bool, len(textWords))
	for _, w := range textWords {
		textSet[w] = true
	}
	matches := 0
	for _, w := range queryWords {
		if textSet[w] {
			matches++
		}
	}
	return float64(matches) / float64(len(queryWords))
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	return strings.FieldsFunc(s, func(r rune) bool {
		if 'a' <= r && r <= 'z' {
			return false
		}
		if '0' <= r && r <= '9' {
			return false
		}
		return true
	})
}

func symbolOverlap(cardTags, symbolHints []string) float64 {
	if len(symbolHints) == 0 {
		return 0.0
	}
	tagSet := make(map[string]bool, len(cardTags))
	for _, t := range cardTags {
		tagSet[t] = true
	}
	matches := 0
	for _, s := range symbolHints {
		if tagSet[s] {
			matches++
		}
	}
	return float64(matches) / float64(len(symbolHints))
}

func beadTypeMatch(cardType CardType, beadType string) float64 {
	// Rules and patterns always relevant.
	if cardType == CardTypeRule || cardType == CardTypePattern {
		return 1.0
	}
	// Tastes and decisions: only when bead type suggests they matter.
	switch cardType {
	case CardTypeTaste:
		return 0.5
	case CardTypeDecision:
		if beadType == "research" || beadType == "premortem" {
			return 1.0
		}
		return 0.3
	case CardTypeFact:
		return 0.7
	}
	return 0.5
}

func estimateTokens(s string) int {
	// Rough approximation: 1 token ≈ 4 characters.
	return int(math.Ceil(float64(len(s)) / 4.0))
}

// Create inserts a new card and returns it.
func (s *SQLiteCardStore) Create(ctx context.Context, p CardCreateParams) (*Card, error) {
	id := p.ID
	if id == "" {
		id = newCardID()
	}
	now := nowRFC3339()
	tags := marshalTags(p.Tags)

	var bodyDeep *string
	if p.BodyDeep != nil {
		bodyDeep = p.BodyDeep
	}
	var emergedFrom *string
	if p.EmergedFrom != nil {
		emergedFrom = p.EmergedFrom
	}
	var promotionConf *float64
	if p.PromotionConfidence != nil {
		promotionConf = p.PromotionConfidence
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cards (
			id, type, title, body_summary, body_full, body_deep,
			tags, score, promotion_confidence, decay_anchor,
			created_at, updated_at, emerged_from
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1.0, ?, ?, ?, ?, ?)`,
		id, string(p.Type), p.Title, p.BodySummary, p.BodyFull, bodyDeep,
		tags, promotionConf, now, now, now, emergedFrom,
	)
	if err != nil {
		return nil, fmt.Errorf("create card: %w", err)
	}

	// Record the created event.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO card_events (card_id, ts, actor, kind)
		VALUES (?, ?, 'system', 'created')`, id, now)
	if err != nil {
		return nil, fmt.Errorf("record created event: %w", err)
	}

	return s.Show(ctx, id)
}

// RecordCardEvent atomically records an event and updates the card's score.
func (s *SQLiteCardStore) RecordCardEvent(ctx context.Context, e CardEvent) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := nowRFC3339()

		beadID := sql.NullString{Valid: e.BeadID != "", String: e.BeadID}
		payload := sql.NullString{Valid: e.Payload != "", String: e.Payload}

		// 1. Insert the event (always).
		_, err := tx.ExecContext(ctx, `
			INSERT INTO card_events (card_id, ts, bead_id, actor, kind, payload)
			VALUES (?, ?, ?, ?, ?, ?)`,
			e.CardID, now, beadID, e.Actor, e.Kind, payload)
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		// 2. Apply score delta atomically.
		d := scoreDelta(e.Kind)

		// Build last_contradicted_at CASE expression.
		// CASE WHEN setContradicted THEN now WHEN clearsContradiction THEN NULL ELSE last_contradicted_at END
		var updateSQL string
		var args []any

		updateSQL = `
			UPDATE cards
			   SET score = MIN(MAX(score + ?, ?), ?),
			       decay_anchor = ?,
			       last_contradicted_at = CASE
			         WHEN ? THEN ?
			         WHEN ? THEN NULL
			         ELSE last_contradicted_at
			       END,
			       last_nacked_at = CASE WHEN ? THEN ? ELSE last_nacked_at END,
			       updated_at = ?
			 WHERE id = ?`
		args = []any{
			d.delta, ScoreFloor, ScoreCap,
			now,
			boolToInt(d.setContradicted), now,
			boolToInt(d.clearsContradiction),
			boolToInt(d.setNacked), now,
			now,
			e.CardID,
		}

		if _, err := tx.ExecContext(ctx, updateSQL, args...); err != nil {
			return fmt.Errorf("update score: %w", err)
		}

		// 3. Auto-retire if score crosses the threshold.
		_, err = tx.ExecContext(ctx, `
			UPDATE cards
			   SET retired_at = ?,
			       retired_reason = 'auto: persistent nack'
			 WHERE id = ? AND retired_at IS NULL AND score <= ?`,
			now, e.CardID, AutoRetireThresh)
		if err != nil {
			return fmt.Errorf("auto-retire card: %w", err)
		}
		return nil
	})
}

// Retire marks a card as retired.
func (s *SQLiteCardStore) Retire(ctx context.Context, id, reason, supersededBy string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := nowRFC3339()
		var superBy *string
		if supersededBy != "" {
			superBy = &supersededBy
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE cards
			   SET retired_at = ?, retired_reason = ?, superseded_by = ?, updated_at = ?
			 WHERE id = ? AND retired_at IS NULL`,
			now, reason, superBy, now, id)
		if err != nil {
			return fmt.Errorf("retire card: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO card_events (card_id, ts, actor, kind, payload)
			VALUES (?, ?, 'system', 'retired', ?)`,
			id, now, reason)
		if err != nil {
			return fmt.Errorf("insert retired event: %w", err)
		}
		return nil
	})
}

// WithReadTx runs fn inside a read transaction.
func (s *SQLiteCardStore) WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read tx: %w", err)
	}
	rtx := &readTxImpl{tx: tx}
	if err := fn(rtx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit read tx: %w", err)
	}
	return nil
}

func (s *SQLiteCardStore) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// readTxImpl implements ReadTx over an active *sql.Tx.
type readTxImpl struct {
	tx *sql.Tx
}

// Show implements ReadTx.
func (r *readTxImpl) Show(ctx context.Context, id string) (*Card, error) {
	row := r.tx.QueryRowContext(ctx,
		`SELECT`+cardSelectCols+` FROM cards WHERE id = ?`, id)
	c, err := scanCard(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("tx show card %s: %w", id, err)
	}
	return c, nil
}

// List implements ReadTx.
func (r *readTxImpl) List(ctx context.Context, q ListQuery) ([]Card, error) {
	where := []string{}
	args := []any{}
	if !q.IncludeRetired {
		where = append(where, "retired_at IS NULL")
	}
	if q.Type != "" {
		where = append(where, "type = ?")
		args = append(args, string(q.Type))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	query := `SELECT` + cardSelectCols + ` FROM cards` + clause + ` ORDER BY created_at DESC`
	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
		if q.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, q.Offset)
		}
	}
	rows, err := r.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tx list cards: %w", err)
	}
	defer rows.Close()
	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tx list cards rows: %w", err)
	}
	return cards, nil
}

// Relevant implements ReadTx.
func (r *readTxImpl) Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error) {
	rows, err := r.tx.QueryContext(ctx,
		`SELECT`+cardSelectCols+` FROM cards WHERE retired_at IS NULL`)
	if err != nil {
		return RelevantCards{}, fmt.Errorf("tx relevant query: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	candidates, err := collectScoredCards(rows, q, now)
	if err != nil {
		return RelevantCards{}, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	return RelevantCards{
		Deck:    toSummaries(candidates),
		Inlined: buildInlined(candidates, q.MaxTokens),
	}, nil
}
