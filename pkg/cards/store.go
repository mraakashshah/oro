package cards

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
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
	ListProposed(ctx context.Context) ([]Card, error)
	PendingLearnings(ctx context.Context, beadID string) ([]PendingLearning, error)
	ReviewQueue(ctx context.Context) ([]PendingLearning, error)

	RecordCardEvent(ctx context.Context, e CardEvent) error
	AppendLearningPending(ctx context.Context, beadID string, c CardCandidate) (int64, error)
	PromoteLearning(ctx context.Context, learningID int64) (cardID string, err error)
	PromoteLearningAsProposal(ctx context.Context, learningID int64) (cardID string, err error)
	ResolveProposal(ctx context.Context, cardID string, o GradeOutcome) error
	RejectLearning(ctx context.Context, id int64, reason string) error
	DeferToReviewQueue(ctx context.Context, id int64, reason string) error
	Create(ctx context.Context, c CardCreateParams) (*Card, error)
	Retire(ctx context.Context, id, reason string, supersededBy string) error
	AddRelation(ctx context.Context, srcID, dstID string, sig RelationSignal) error
	SeeAlso(ctx context.Context, cardID string, limit int) ([]CardSummary, error)
	Lineage(ctx context.Context, id string) ([]Card, error)
	LatestInChain(ctx context.Context, id string) (*Card, error)
	Reindex(ctx context.Context) (int, error)

	WithReadTx(ctx context.Context, fn func(tx ReadTx) error) error
}

// LearningPromotionJourney describes the audit record committed with a native
// learning promotion. Payload receives the generated card ID while the same
// transaction is still open.
type LearningPromotionJourney struct {
	BeadID  string
	Ts      string
	Actor   string
	Event   string
	Payload func(cardID string) (string, error)
}

// Embedder computes dense embedding vectors for card semantic recall.
type Embedder interface {
	Embed(text string) []float32
	Dim() int
	Name() string
}

// StoreOption configures a SQLiteCardStore.
type StoreOption func(*SQLiteCardStore)

// WithEmbedder configures the store to embed cards on create and reindex.
func WithEmbedder(embedder Embedder) StoreOption {
	return func(s *SQLiteCardStore) {
		s.embedder = embedder
	}
}

// SQLiteCardStore is the SQLite-backed implementation of Store.
type SQLiteCardStore struct {
	db       *sql.DB
	embedder Embedder
}

// NativeTransactionIdentity identifies the database connection pool used for
// native transactions. Callers can use it to ensure a cross-store atomic
// operation is only selected when both stores share the same database.
func (s *SQLiteCardStore) NativeTransactionIdentity() any {
	return s.db
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlReadWriter interface {
	sqlExecutor
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// NewStore opens a cards store against an existing *sql.DB and applies the schema.
func NewStore(db *sql.DB, opts ...StoreOption) (*SQLiteCardStore, error) {
	if _, err := db.ExecContext(context.Background(), schemaDDL); err != nil {
		return nil, fmt.Errorf("apply cards schema: %w", err)
	}
	if err := ensureColumn(db, "card_events", "session_id", "ALTER TABLE card_events ADD COLUMN session_id TEXT"); err != nil {
		return nil, fmt.Errorf("ensure card event session id: %w", err)
	}
	if err := ensureColumn(db, "cards", "embedding", "ALTER TABLE cards ADD COLUMN embedding BLOB"); err != nil {
		return nil, fmt.Errorf("ensure card embedding: %w", err)
	}
	if err := ensureColumn(db, "cards", "embedding_model", "ALTER TABLE cards ADD COLUMN embedding_model TEXT"); err != nil {
		return nil, fmt.Errorf("ensure card embedding model: %w", err)
	}
	if err := ensureColumn(db, "cards", "grade_state", "ALTER TABLE cards ADD COLUMN grade_state TEXT"); err != nil {
		return nil, fmt.Errorf("ensure card grade state: %w", err)
	}
	if err := ensureColumn(db, "cards", "grade_verdict", "ALTER TABLE cards ADD COLUMN grade_verdict TEXT"); err != nil {
		return nil, fmt.Errorf("ensure card grade verdict: %w", err)
	}
	if err := ensureColumn(db, "cards", "grade_confidence", "ALTER TABLE cards ADD COLUMN grade_confidence REAL"); err != nil {
		return nil, fmt.Errorf("ensure card grade confidence: %w", err)
	}
	if err := ensureColumn(db, "cards", "proposal_hash", "ALTER TABLE cards ADD COLUMN proposal_hash TEXT"); err != nil {
		return nil, fmt.Errorf("ensure card proposal hash: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		CREATE INDEX IF NOT EXISTS idx_cards_grade_state ON cards(grade_state) WHERE retired_at IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_cards_proposal_hash ON cards(proposal_hash) WHERE proposal_hash IS NOT NULL;
	`); err != nil {
		return nil, fmt.Errorf("ensure card grade indexes: %w", err)
	}
	store := &SQLiteCardStore{db: db}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
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

func cardEmbeddingText(title, bodySummary string) string {
	return title + "\n" + bodySummary
}

func encodeEmbedding(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
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

type relevantRowScanner struct {
	row     interface{ Scan(...any) error }
	symbols *sql.NullString
}

// Scan appends the relevance-only symbols column to the base card scan.
func (s relevantRowScanner) Scan(dest ...any) error {
	if err := s.row.Scan(append(dest, s.symbols)...); err != nil {
		return fmt.Errorf("scan relevant card row: %w", err)
	}
	return nil
}

func scanRelevantCard(row interface{ Scan(...any) error }) (*Card, error) {
	var symbols sql.NullString
	c, err := scanCard(relevantRowScanner{row: row, symbols: &symbols})
	if err != nil {
		return nil, err
	}
	c.Symbols = splitSymbolList(symbols)
	return c, nil
}

func splitSymbolList(symbols sql.NullString) []string {
	if !symbols.Valid || symbols.String == "" {
		return nil
	}
	return strings.Split(symbols.String, "\x1f")
}

func scanPendingLearning(row interface {
	Scan(...any) error
},
) (PendingLearning, error) {
	var (
		pending           PendingLearning
		ts                string
		rawCandidate      string
		promotedTo        sql.NullString
		rejectedAt        sql.NullString
		reason            sql.NullString
		queuedForReviewAt sql.NullString
	)
	if err := row.Scan(
		&pending.ID, &pending.BeadID, &ts, &rawCandidate,
		&promotedTo, &rejectedAt, &reason, &queuedForReviewAt,
	); err != nil {
		return PendingLearning{}, fmt.Errorf("scan pending learning row: %w", err)
	}
	parsedTS, err := parseTime(ts)
	if err != nil {
		return PendingLearning{}, fmt.Errorf("parse pending learning ts: %w", err)
	}
	pending.TS = parsedTS
	if err := json.Unmarshal([]byte(rawCandidate), &pending.Candidate); err != nil {
		return PendingLearning{}, fmt.Errorf("parse pending learning candidate: %w", err)
	}
	if promotedTo.Valid {
		pending.PromotedTo = &promotedTo.String
	}
	if rejectedAt.Valid {
		parsedRejectedAt, err := parseTime(rejectedAt.String)
		if err != nil {
			return PendingLearning{}, fmt.Errorf("parse pending learning rejected_at: %w", err)
		}
		pending.RejectedAt = &parsedRejectedAt
	}
	if reason.Valid {
		pending.Reason = &reason.String
	}
	if queuedForReviewAt.Valid {
		parsedQueuedAt, err := parseTime(queuedForReviewAt.String)
		if err != nil {
			return PendingLearning{}, fmt.Errorf("parse pending learning queued_for_review_at: %w", err)
		}
		pending.QueuedForReviewAt = &parsedQueuedAt
	}
	return pending, nil
}

const cardSelectCols = `
  id, type, title, body_summary, body_full, body_deep,
  tags, score, promotion_confidence, decay_anchor,
  last_contradicted_at, last_nacked_at, created_at, updated_at,
  retired_at, superseded_by, emerged_from, retired_reason`

const pendingLearningSelectCols = `
  id, bead_id, ts, candidate, promoted_to, rejected_at, reason, queued_for_review_at`

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

// ListProposed returns non-retired cards awaiting a grade outcome.
func (s *SQLiteCardStore) ListProposed(ctx context.Context) ([]Card, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT`+cardSelectCols+`
		FROM cards
		WHERE retired_at IS NULL AND grade_state = ?
		ORDER BY created_at ASC`, GradeStateProposed)
	if err != nil {
		return nil, fmt.Errorf("query proposed cards: %w", err)
	}
	defer rows.Close()

	var proposed []Card
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan proposed card: %w", err)
		}
		proposed = append(proposed, *card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proposed cards: %w", err)
	}
	return proposed, nil
}

// PendingLearnings returns pending, non-terminal learning candidates for a bead.
func (s *SQLiteCardStore) PendingLearnings(ctx context.Context, beadID string) ([]PendingLearning, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT`+pendingLearningSelectCols+`
		FROM bead_learnings_pending
		WHERE bead_id = ? AND promoted_to IS NULL AND rejected_at IS NULL
		ORDER BY id ASC`, beadID)
	if err != nil {
		return nil, fmt.Errorf("query pending learnings: %w", err)
	}
	defer rows.Close()

	var pending []PendingLearning
	for rows.Next() {
		learning, err := scanPendingLearning(rows)
		if err != nil {
			return nil, err
		}
		pending = append(pending, learning)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending learnings rows: %w", err)
	}
	return pending, nil
}

// ReviewQueue returns unresolved learning candidates explicitly queued for review.
func (s *SQLiteCardStore) ReviewQueue(ctx context.Context) ([]PendingLearning, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT`+pendingLearningSelectCols+`
		FROM bead_learnings_pending
		WHERE queued_for_review_at IS NOT NULL
		  AND promoted_to IS NULL
		  AND rejected_at IS NULL
		ORDER BY queued_for_review_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query review queue: %w", err)
	}
	defer rows.Close()

	var pending []PendingLearning
	for rows.Next() {
		learning, err := scanPendingLearning(rows)
		if err != nil {
			return nil, err
		}
		pending = append(pending, learning)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review queue rows: %w", err)
	}
	return pending, nil
}

func queryPendingLearningForUpdate(ctx context.Context, tx *sql.Tx, learningID int64) (PendingLearning, error) {
	row := tx.QueryRowContext(ctx, `SELECT`+pendingLearningSelectCols+`
		FROM bead_learnings_pending
		WHERE id = ?`, learningID)
	learning, err := scanPendingLearning(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingLearning{}, fmt.Errorf("%w: learning %d", ErrNotFound, learningID)
	}
	if err != nil {
		return PendingLearning{}, err
	}
	return learning, nil
}

// Relevant returns cards relevant to the query, sorted by effective score × relevance weight.
func (s *SQLiteCardStore) Relevant(ctx context.Context, q RelevanceQuery) (RelevantCards, error) {
	rows, err := s.db.QueryContext(ctx,
		relevantCardsQuery())
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
	if q.WSeeAlso > 0 {
		candidates, err = s.addGraphBonuses(ctx, candidates, q)
		if err != nil {
			return RelevantCards{}, err
		}
	}

	return RelevantCards{
		Deck:    toDeckCards(candidates),
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
		c, err := scanRelevantCard(rows)
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

func (s *SQLiteCardStore) addGraphBonuses(
	ctx context.Context,
	candidates []scoredCard,
	q RelevanceQuery,
) ([]scoredCard, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	seeds := keywordSeedIDs(candidates, q.SeededCardIDs)
	if len(seeds) == 0 {
		return candidates, nil
	}
	strengths, err := s.relationStrengthsFromSeeds(ctx, seeds)
	if err != nil {
		return nil, err
	}
	if len(strengths) == 0 {
		return candidates, nil
	}
	seedFloor := candidates[0].score
	for i := range candidates {
		bonus := q.WSeeAlso * graphBonus(candidates[i].card, seeds, strengths)
		if bonus == 0 {
			continue
		}
		candidates[i].score = math.Min(candidates[i].score+bonus, math.Nextafter(seedFloor, 0))
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	return candidates, nil
}

func keywordSeedIDs(candidates []scoredCard, seeded []string) []string {
	if len(seeded) > 0 {
		return seeded
	}
	var seeds []string
	for _, candidate := range candidates {
		if candidate.score <= 0 {
			continue
		}
		seeds = append(seeds, candidate.card.ID)
	}
	return seeds
}

func (s *SQLiteCardStore) relationStrengthsFromSeeds(ctx context.Context, seeds []string) (map[string]int, error) {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(seeds)), ",")
	//nolint:gosec // placeholders are generated from seed count; values are still bound args.
	query := `SELECT target_id, SUM(strength)
		FROM card_relations
		WHERE source_id IN (` + placeholders + `) AND target_id NOT IN (` + placeholders + `)
		GROUP BY target_id`
	args := make([]any, 0, len(seeds)*2)
	for _, seed := range seeds {
		args = append(args, seed)
	}
	for _, seed := range seeds {
		args = append(args, seed)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query relation strengths from seeds: %w", err)
	}
	defer rows.Close()

	strengths := make(map[string]int)
	for rows.Next() {
		var targetID string
		var strength int
		if err := rows.Scan(&targetID, &strength); err != nil {
			return nil, fmt.Errorf("scan relation strength: %w", err)
		}
		strengths[targetID] = strength
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relation strengths: %w", err)
	}
	return strengths, nil
}

func graphBonus(c Card, seeds []string, strengths map[string]int) float64 {
	for _, seed := range seeds {
		if c.ID == seed {
			return 0
		}
	}
	strength := strengths[c.ID]
	if strength <= 0 {
		return 0
	}
	return 1 + 0.1*math.Log1p(float64(strength))
}

func toInlinedCard(c Card) InlinedCard {
	return InlinedCard{
		ID:          c.ID,
		Type:        c.Type,
		Title:       c.Title,
		BodySummary: c.BodySummary,
		BodyFull:    c.BodyFull,
		Score:       c.Score,
		Tags:        c.Tags,
	}
}

func toDeckCard(c Card) DeckCard {
	return DeckCard{
		ID:          c.ID,
		Type:        c.Type,
		Title:       c.Title,
		BodySummary: c.BodySummary,
		Score:       c.Score,
		Tags:        c.Tags,
	}
}

func toDeckCards(candidates []scoredCard) []DeckCard {
	out := make([]DeckCard, 0, len(candidates))
	for _, sc := range candidates {
		out = append(out, toDeckCard(sc.card))
	}
	return out
}

// buildInlined returns cards whose body_full fits within maxTokens budget.
func buildInlined(candidates []scoredCard, maxTokens int) []InlinedCard {
	if maxTokens <= 0 {
		return nil
	}
	var out []InlinedCard
	budget := maxTokens
	for _, sc := range candidates {
		tokens := estimateTokens(sc.card.BodyFull)
		if tokens > budget {
			break
		}
		budget -= tokens
		out = append(out, toInlinedCard(sc.card))
	}
	return out
}

// relevanceScore computes a [0,1] relevance weight for a card given a query.
// Weighted combination per spec §5.9.
func relevanceScore(c *Card, q RelevanceQuery) float64 {
	tagScore := jaccardSimilarity(c.Tags, q.BeadTags)          // weight 0.4
	textScore := wordOverlap(c.BodySummary, q.BeadDescription) // weight 0.3
	symbolScore := symbolOverlap(c.Symbols, q.SymbolHints)     // weight 0.2
	typeScore := beadTypeMatch(c.Type, q.BeadType)             // weight 0.1

	return tagScore*0.4 + textScore*0.3 + symbolScore*0.2 + typeScore*0.1
}

func relevantCardsQuery() string {
	return `SELECT` + cardSelectCols + `,
		(SELECT group_concat(symbol, char(31)) FROM card_symbols WHERE card_id = cards.id)
		FROM cards
		WHERE retired_at IS NULL
		  AND (grade_state IS NULL OR grade_state NOT IN ('proposed', 'rejected'))`
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
		if beadType == "research" {
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

// AppendLearningPending inserts a worker-emitted card candidate for later review.
func (s *SQLiteCardStore) AppendLearningPending(ctx context.Context, beadID string, c CardCandidate) (int64, error) {
	candidate, err := json.Marshal(c)
	if err != nil {
		return 0, fmt.Errorf("marshal pending learning candidate: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO bead_learnings_pending (bead_id, ts, candidate)
		VALUES (?, ?, ?)`, beadID, nowRFC3339(), string(candidate))
	if err != nil {
		return 0, fmt.Errorf("append pending learning: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("pending learning last insert id: %w", err)
	}
	return id, nil
}

func insertCard(ctx context.Context, exec sqlExecutor, p CardCreateParams, embedder Embedder) (string, error) {
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
	var embedding []byte
	var embeddingModel *string
	if embedder != nil {
		embedding = encodeEmbedding(embedder.Embed(cardEmbeddingText(p.Title, p.BodySummary)))
		model := embedder.Name()
		embeddingModel = &model
	}

	_, err := exec.ExecContext(ctx, `
		INSERT INTO cards (
			id, type, title, body_summary, body_full, body_deep,
			tags, score, promotion_confidence, decay_anchor,
			created_at, updated_at, emerged_from, embedding, embedding_model,
			grade_state, proposal_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1.0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(p.Type), p.Title, p.BodySummary, p.BodyFull, bodyDeep,
		tags, promotionConf, now, now, now, emergedFrom, embedding, embeddingModel,
		nullableString(p.GradeState), nullableString(p.ProposalHash),
	)
	if err != nil {
		return "", fmt.Errorf("create card: %w", err)
	}

	// Record the created event.
	_, err = exec.ExecContext(ctx, `
		INSERT INTO card_events (card_id, ts, actor, kind)
		VALUES (?, ?, 'system', 'created')`, id, now)
	if err != nil {
		return "", fmt.Errorf("record created event: %w", err)
	}
	if readWriter, ok := exec.(sqlReadWriter); ok {
		if err := addWikilinkRelations(ctx, readWriter, id, p.BodyFull); err != nil {
			return "", err
		}
	}
	return id, nil
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Create inserts a new card and returns it.
func (s *SQLiteCardStore) Create(ctx context.Context, p CardCreateParams) (*Card, error) {
	id, err := insertCard(ctx, s.db, p, s.embedder)
	if err != nil {
		return nil, err
	}

	return s.Show(ctx, id)
}

// Reindex backfills embeddings for non-retired cards whose embedding is NULL.
func (s *SQLiteCardStore) Reindex(ctx context.Context) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, body_summary
		  FROM cards
		 WHERE embedding IS NULL AND retired_at IS NULL
		 ORDER BY created_at ASC`)
	if err != nil {
		return 0, fmt.Errorf("query cards for reindex: %w", err)
	}

	type pendingEmbedding struct {
		id          string
		title       string
		bodySummary string
	}
	var pending []pendingEmbedding
	for rows.Next() {
		var p pendingEmbedding
		if err := rows.Scan(&p.id, &p.title, &p.bodySummary); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan card for reindex: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close reindex rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate cards for reindex: %w", err)
	}

	model := s.embedder.Name()
	for i, p := range pending {
		embedding := encodeEmbedding(s.embedder.Embed(cardEmbeddingText(p.title, p.bodySummary)))
		result, err := s.db.ExecContext(ctx, `
			UPDATE cards
			   SET embedding = ?, embedding_model = ?, updated_at = ?
			 WHERE id = ? AND embedding IS NULL`,
			embedding, model, nowRFC3339(), p.id)
		if err != nil {
			return i, fmt.Errorf("backfill card embedding %s: %w", p.id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return i, fmt.Errorf("backfill card embedding rows affected %s: %w", p.id, err)
		}
		if affected == 0 {
			continue
		}
	}
	return len(pending), nil
}

// PromoteLearning creates a card from a pending learning and marks it resolved.
func (s *SQLiteCardStore) PromoteLearning(ctx context.Context, learningID int64) (cardID string, err error) {
	return s.promoteLearning(ctx, learningID, false)
}

// PromoteLearningAsProposal creates a proposed card from a pending learning and marks it resolved.
func (s *SQLiteCardStore) PromoteLearningAsProposal(ctx context.Context, learningID int64) (cardID string, err error) {
	return s.promoteLearning(ctx, learningID, true)
}

// PromoteLearningWithJourney creates and resolves a learning together with its
// bead journey record. A failure in either mutation rolls back both.
func (s *SQLiteCardStore) PromoteLearningWithJourney(
	ctx context.Context,
	learningID int64,
	asProposal bool,
	journey LearningPromotionJourney,
) (cardID string, err error) {
	return s.promoteLearningWithJourney(ctx, learningID, asProposal, &journey)
}

// ResolveProposal records a terminal grade outcome for a proposed card.
func (s *SQLiteCardStore) ResolveProposal(ctx context.Context, cardID string, o GradeOutcome) error {
	state, err := resolvedProposalState(o.Action)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE cards
		   SET grade_state = ?, grade_verdict = ?, grade_confidence = ?, updated_at = ?
		 WHERE id = ? AND grade_state = ?`,
		state, o.Verdict, o.Confidence, nowRFC3339(), cardID, GradeStateProposed)
	if err != nil {
		return fmt.Errorf("resolve proposal %s: %w", cardID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve proposal rows affected: %w", err)
	}
	if affected == 1 {
		return nil
	}
	if _, err := s.Show(ctx, cardID); err != nil {
		return err
	}
	return ErrAlreadyResolved
}

func resolvedProposalState(action GradeAction) (GradeState, error) {
	switch action {
	case GradeActionApply:
		return GradeStateApplied, nil
	case GradeActionRejectAndRetire:
		return GradeStateRejected, nil
	default:
		return "", fmt.Errorf("resolve proposal: unsupported grade action %q", action)
	}
}

func (s *SQLiteCardStore) promoteLearning(ctx context.Context, learningID int64, asProposal bool) (cardID string, err error) {
	return s.promoteLearningWithJourney(ctx, learningID, asProposal, nil)
}

func (s *SQLiteCardStore) promoteLearningWithJourney(
	ctx context.Context,
	learningID int64,
	asProposal bool,
	journey *LearningPromotionJourney,
) (cardID string, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		learning, err := queryPendingLearningForUpdate(ctx, tx, learningID)
		if err != nil {
			return err
		}
		if learning.PromotedTo != nil || learning.RejectedAt != nil {
			return ErrAlreadyResolved
		}
		confidence := learning.Candidate.Confidence
		emergedFrom := learning.BeadID
		params := CardCreateParams{
			Type:                CardType(learning.Candidate.Type),
			Title:               learning.Candidate.Title,
			BodySummary:         learning.Candidate.BodySummary,
			BodyFull:            learning.Candidate.BodyFull,
			Tags:                learning.Candidate.Tags,
			EmergedFrom:         &emergedFrom,
			PromotionConfidence: &confidence,
		}
		if asProposal {
			params.GradeState = string(GradeStateProposed)
			params.ProposalHash = learningProposalHash(learning.Candidate)
		}
		id, err := insertCard(ctx, tx, params, s.embedder)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE bead_learnings_pending
			   SET promoted_to = ?
			 WHERE id = ? AND promoted_to IS NULL AND rejected_at IS NULL`,
			id, learningID)
		if err != nil {
			return fmt.Errorf("mark learning promoted: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark learning promoted rows affected: %w", err)
		}
		if affected != 1 {
			return ErrAlreadyResolved
		}
		if err := insertLearningPromotionJourney(ctx, tx, id, journey); err != nil {
			return err
		}
		cardID = id
		return nil
	})
	if err != nil {
		return "", err
	}
	return cardID, nil
}

func insertLearningPromotionJourney(ctx context.Context, tx *sql.Tx, cardID string, journey *LearningPromotionJourney) error {
	if journey == nil {
		return nil
	}
	payload, err := journey.Payload(cardID)
	if err != nil {
		return fmt.Errorf("build learning promotion journey payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bead_journey (bead_id, ts, actor, event, payload)
		VALUES (?, ?, ?, ?, ?)`,
		journey.BeadID, journey.Ts, journey.Actor, journey.Event, nullableString(payload)); err != nil {
		return fmt.Errorf("append learning promotion journey: %w", err)
	}
	return nil
}

func learningProposalHash(candidate CardCandidate) string {
	payload, err := json.Marshal(candidate)
	if err != nil {
		payload = []byte(candidate.Type + "\x00" + candidate.Title + "\x00" + candidate.BodyFull)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// RejectLearning marks an unresolved pending learning as rejected.
func (s *SQLiteCardStore) RejectLearning(ctx context.Context, id int64, reason string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		learning, err := queryPendingLearningForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if learning.PromotedTo != nil || learning.RejectedAt != nil {
			return ErrAlreadyResolved
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE bead_learnings_pending
			   SET rejected_at = ?, reason = ?
			 WHERE id = ? AND promoted_to IS NULL AND rejected_at IS NULL`,
			nowRFC3339(), reason, id)
		if err != nil {
			return fmt.Errorf("reject learning: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("reject learning rows affected: %w", err)
		}
		if affected != 1 {
			return ErrAlreadyResolved
		}
		return nil
	})
}

// DeferToReviewQueue marks an unresolved pending learning for manual review.
func (s *SQLiteCardStore) DeferToReviewQueue(ctx context.Context, id int64, reason string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		learning, err := queryPendingLearningForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if learning.PromotedTo != nil || learning.RejectedAt != nil {
			return ErrAlreadyResolved
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE bead_learnings_pending
			   SET queued_for_review_at = ?, reason = ?
			 WHERE id = ? AND promoted_to IS NULL AND rejected_at IS NULL`,
			nowRFC3339(), reason, id)
		if err != nil {
			return fmt.Errorf("defer learning to review queue: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("defer learning rows affected: %w", err)
		}
		if affected != 1 {
			return ErrAlreadyResolved
		}
		return nil
	})
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
		result, err := tx.ExecContext(ctx, `
			UPDATE cards
			   SET retired_at = ?, retired_reason = ?, superseded_by = ?, updated_at = ?
			 WHERE id = ? AND retired_at IS NULL`,
			now, reason, superBy, now, id)
		if err != nil {
			return fmt.Errorf("retire card: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("retire card rows affected: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
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

// NewReadTx wraps tx as a ReadTx bound to the supplied SQL transaction.
// Used by beadstore.WithReadTx to share one transaction across stores.
func NewReadTx(tx *sql.Tx) ReadTx {
	return &readTxImpl{tx: tx}
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
		relevantCardsQuery())
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
		Deck:    toDeckCards(candidates),
		Inlined: buildInlined(candidates, q.MaxTokens),
	}, nil
}
