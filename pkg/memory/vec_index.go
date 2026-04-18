package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sync"
)

// ErrInvalidProject is returned when a project name contains characters
// outside [a-zA-Z0-9_-].
var ErrInvalidProject = errors.New("invalid project name")

var validProjectRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// SQLiteVecIndex stores and retrieves dense float32 embeddings using sqlite-vec
// vec0 virtual tables, with one table per project partition.
type SQLiteVecIndex struct {
	db            *sql.DB
	createdTables sync.Map // project → struct{}{}
}

// NewSQLiteVecIndex returns a SQLiteVecIndex backed by db. Returns an error if
// the sqlite-vec extension is not loaded on the connection (verified via
// SELECT vec_version()).
//
//oro:testonly — wired into production by subsequent sqlite-vec load bead (oro-p545)
func NewSQLiteVecIndex(db *sql.DB) (*SQLiteVecIndex, error) {
	if _, err := db.ExecContext(context.Background(), "SELECT vec_version()"); err != nil {
		return nil, fmt.Errorf("sqlite-vec extension not loaded: %w", err)
	}
	return &SQLiteVecIndex{db: db}, nil
}

// resolveProject normalises an empty project name to "oro" and validates
// non-empty names against [a-zA-Z0-9_-].
func resolveProject(project string) (string, error) {
	if project == "" {
		return "oro", nil
	}
	if !validProjectRE.MatchString(project) {
		return "", ErrInvalidProject
	}
	return project, nil
}

// tableNameFor returns the vec0 virtual table name for the validated project.
func tableNameFor(project string) string {
	return "vec_memories_" + project
}

// ensureTable creates the vec0 virtual table for project on first call.
func (i *SQLiteVecIndex) ensureTable(ctx context.Context, project string) error {
	if _, ok := i.createdTables.Load(project); ok {
		return nil
	}
	tbl := tableNameFor(project)
	if _, err := i.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding FLOAT[384])",
		tbl,
	)); err != nil {
		return fmt.Errorf("create vec0 table %s: %w", tbl, err)
	}
	i.createdTables.Store(project, struct{}{})
	return nil
}

// Upsert stores vec at id in the project partition, creating the vec0 table on
// first call. A second Upsert with the same id overwrites the previous entry.
func (i *SQLiteVecIndex) Upsert(ctx context.Context, id int64, vec []float32, project string) error {
	proj, err := resolveProject(project)
	if err != nil {
		return err
	}
	if err := i.ensureTable(ctx, proj); err != nil {
		return err
	}
	blob := MarshalEmbedding(vec)
	if _, err := i.db.ExecContext(ctx,
		fmt.Sprintf("INSERT OR REPLACE INTO %s(rowid, embedding) VALUES (?, ?)", tableNameFor(proj)),
		id, blob,
	); err != nil {
		return fmt.Errorf("upsert into %s: %w", tableNameFor(proj), err)
	}
	return nil
}

// Search returns up to k approximate nearest neighbours from the project
// partition ordered by distance ascending. Score is set to 1 - distance.
// k <= 0 is treated as 10.
func (i *SQLiteVecIndex) Search(ctx context.Context, queryVec []float32, project string, k int) ([]ANNResult, error) {
	proj, err := resolveProject(project)
	if err != nil {
		return nil, err
	}
	if k <= 0 {
		k = 10
	}
	if err := i.ensureTable(ctx, proj); err != nil {
		return nil, err
	}
	blob := MarshalEmbedding(queryVec)
	rows, err := i.db.QueryContext(ctx,
		fmt.Sprintf(
			"SELECT rowid, distance FROM %s WHERE embedding MATCH ? ORDER BY distance ASC LIMIT ?",
			tableNameFor(proj),
		),
		blob, k,
	)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", tableNameFor(proj), err)
	}
	defer rows.Close()

	var results []ANNResult
	for rows.Next() {
		var id int64
		var dist float64
		if err := rows.Scan(&id, &dist); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		results = append(results, ANNResult{MemoryID: id, Score: 1 - dist})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search rows: %w", err)
	}
	return results, nil
}

// Delete removes id from the project partition. The vec0 table is ensured
// (CREATE IF NOT EXISTS) so the call works after a process restart where the
// in-memory createdTables cache is cold but the table exists on disk.
func (i *SQLiteVecIndex) Delete(ctx context.Context, id int64, project string) error {
	proj, err := resolveProject(project)
	if err != nil {
		return err
	}
	if err := i.ensureTable(ctx, proj); err != nil {
		return err
	}
	if _, err := i.db.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", tableNameFor(proj)),
		id,
	); err != nil {
		return fmt.Errorf("delete from %s: %w", tableNameFor(proj), err)
	}
	return nil
}
