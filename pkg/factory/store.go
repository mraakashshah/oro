package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"oro/pkg/dbutil"
)

type snapshot struct {
	Spec          Spec
	PolicyHash    string
	PolicyDigests map[string]string
	CreatedAt     time.Time
	Deadline      time.Time
	Revision      int64
	State         string
	Attempts      int
	BaseSHA       string
	CandidateSHA  string
	Worktree      string
	Feedback      string
	Reason        string
	Acceptance    *evidence
	Review        *evidence
}

type factoryStore struct {
	db   *sql.DB
	conn *sql.Conn
}

func openStore(ctx context.Context, dir string) (*factoryStore, error) {
	db, err := dbutil.OpenDB(filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, fmt.Errorf("open factory database: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("acquire transition connection: %w", err)
	}
	store := &factoryStore{db: db, conn: conn}
	if err := store.initialize(ctx); err != nil {
		_ = store.close()
		return nil, err
	}
	return store, nil
}

func (s *factoryStore) initialize(ctx context.Context) error {
	if _, err := s.conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return fmt.Errorf("set factory durability: %w", err)
	}
	var mode int
	if err := s.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&mode); err != nil {
		return fmt.Errorf("verify factory durability: %w", err)
	}
	if mode != 2 {
		return errors.New("factory transition connection requires synchronous=FULL")
	}
	_, err := s.conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS snapshots (
 id INTEGER PRIMARY KEY CHECK (id=1),
 revision INTEGER NOT NULL CHECK (revision>0),
 body BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
 revision INTEGER PRIMARY KEY CHECK (revision>0),
 state TEXT NOT NULL,
 body BLOB NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("initialize factory schema: %w", err)
	}
	return nil
}

func (s *factoryStore) load(ctx context.Context) (snapshot, error) {
	return decodeSnapshot(s.conn.QueryRowContext(ctx, "SELECT body FROM snapshots WHERE id=1"))
}

func decodeSnapshot(row *sql.Row) (snapshot, error) {
	var body []byte
	if err := row.Scan(&body); err != nil {
		return snapshot{}, fmt.Errorf("read factory snapshot: %w", err)
	}
	var state snapshot
	if err := json.Unmarshal(body, &state); err != nil {
		return snapshot{}, fmt.Errorf("decode factory snapshot: %w", err)
	}
	return state, nil
}

// save increments the persisted revision. The value argument stays unchanged;
// callers reload after saving or advance their local revision explicitly.
func (s *factoryStore) save(ctx context.Context, next snapshot, expected int64) error {
	if expected < 0 {
		return errors.New("expected revision must be nonnegative")
	}
	next.Revision = expected + 1
	body, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode factory snapshot: %w", err)
	}
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin factory transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeSnapshot(ctx, tx, body, expected); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events(revision, state, body) VALUES(?, ?, ?)", next.Revision, next.State, body); err != nil {
		return fmt.Errorf("append factory event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit factory transition: %w", err)
	}
	return nil
}

func writeSnapshot(ctx context.Context, tx *sql.Tx, body []byte, expected int64) error {
	var result sql.Result
	var err error
	if expected == 0 {
		result, err = tx.ExecContext(ctx, "INSERT INTO snapshots(id, revision, body) VALUES(1, 1, ?) ON CONFLICT(id) DO NOTHING", body)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE snapshots SET revision=?, body=? WHERE id=1 AND revision=?", expected+1, body, expected)
	}
	if err != nil {
		return fmt.Errorf("persist factory snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check factory revision: %w", err)
	}
	if changed != 1 {
		return errors.New("stale factory snapshot revision")
	}
	return nil
}

func (s *factoryStore) close() error {
	if err := errors.Join(s.conn.Close(), s.db.Close()); err != nil {
		return fmt.Errorf("close factory database: %w", err)
	}
	return nil
}

func readSnapshot(ctx context.Context, dir string) (snapshot, error) {
	path, err := filepath.Abs(filepath.Join(dir, "state.db"))
	if err != nil {
		return snapshot{}, fmt.Errorf("resolve factory database: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return snapshot{}, fmt.Errorf("open factory status: %w", err)
	}
	defer func() { _ = db.Close() }()
	return decodeSnapshot(db.QueryRowContext(ctx, "SELECT body FROM snapshots WHERE id=1"))
}
