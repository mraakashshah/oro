package protocol //nolint:testpackage // white-box tests pin queue-view migration failure semantics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCanonicalBeadQueueViewDefinitionsAreCompleteAndNormalized(t *testing.T) {
	t.Parallel()

	definitions := canonicalBeadQueueViewDefinitions()
	wantNames := []string{"beads_ready", "beads_blocked", "review_checkpoints_blocking_assignment"}
	if len(definitions) != len(wantNames) {
		t.Fatalf("canonical definitions = %d, want %d: %#v", len(definitions), len(wantNames), definitions)
	}
	for _, name := range wantNames {
		definition, ok := definitions[name]
		if !ok {
			t.Errorf("canonical definitions missing %q", name)
			continue
		}
		if !strings.HasPrefix(definition, "createview"+name+"as") {
			t.Errorf("canonical definition %q = %q", name, definition)
		}
		if strings.ContainsAny(definition, " \n\r\t") || strings.Contains(definition, "ifnotexists") {
			t.Errorf("canonical definition %q is not normalized: %q", name, definition)
		}
	}

	const formatted = " CREATE\n VIEW IF NOT EXISTS Example_View AS\r\n SELECT\t1 "
	if got, want := normalizeBeadQueueViewSQL(formatted), "createviewexample_viewasselect1"; got != want {
		t.Fatalf("normalizeBeadQueueViewSQL() = %q, want %q", got, want)
	}
}

func TestHasCanonicalBeadQueueViewsChecksDefinitionsAndRows(t *testing.T) {
	t.Parallel()

	canonicalRows := scriptedCanonicalRows()
	tests := []struct {
		name       string
		query      scriptedQuery
		want       bool
		wantErr    string
		wantClosed int
	}{
		{name: "all canonical", query: scriptedQuery{columns: []string{"name", "sql"}, values: canonicalRows}, want: true, wantClosed: 1},
		{name: "one missing", query: scriptedQuery{columns: []string{"name", "sql"}, values: canonicalRows[:2]}, wantClosed: 1},
		{name: "one stale", query: scriptedQuery{columns: []string{"name", "sql"}, values: replaceScriptedDefinition(canonicalRows, "beads_ready", "CREATE VIEW beads_ready AS SELECT 1")}, wantClosed: 1},
		{name: "unknown name", query: scriptedQuery{columns: []string{"name", "sql"}, values: append(append([][]driver.Value(nil), canonicalRows[:2]...), []driver.Value{"not_a_queue_view", "CREATE VIEW not_a_queue_view AS SELECT 1"})}, wantClosed: 1},
		{name: "query failure", query: scriptedQuery{err: errors.New("query exploded")}, wantErr: "query queue view definitions: query exploded"},
		{name: "scan failure", query: scriptedQuery{columns: []string{"name"}, values: [][]driver.Value{{"beads_ready"}}}, wantErr: "scan queue view definition", wantClosed: 1},
		{name: "iteration failure", query: scriptedQuery{columns: []string{"name", "sql"}, terminalErr: errors.New("rows exploded")}, wantErr: "iterate queue view definitions: rows exploded", wantClosed: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := &queueViewDBScript{queries: []scriptedQuery{test.query}}
			db := openScriptedQueueViewDB(t, script)

			got, err := hasCanonicalBeadQueueViews(context.Background(), db)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("hasCanonicalBeadQueueViews() error = %v, want containing %q", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatalf("hasCanonicalBeadQueueViews() error = %v", err)
			}
			if got != test.want {
				t.Errorf("hasCanonicalBeadQueueViews() = %v, want %v", got, test.want)
			}
			if got := script.rowsCloseCount(); got != test.wantClosed {
				t.Errorf("closed row sets = %d, want %d", got, test.wantClosed)
			}
		})
	}
}

func TestEnsureCanonicalBeadQueueViewsFastPathDoesNotWrite(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{queries: []scriptedQuery{{columns: []string{"name", "sql"}, values: scriptedCanonicalRows()}}}
	db := openScriptedQueueViewDB(t, script)

	if err := ensureCanonicalBeadQueueViews(context.Background(), db); err != nil {
		t.Fatalf("ensure canonical views: %v", err)
	}
	if got := script.executed(); len(got) != 0 {
		t.Fatalf("canonical fast path executed writes: %v", got)
	}
}

func TestEnsureCanonicalBeadQueueViewsReportsInitialInspectionFailure(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{queries: []scriptedQuery{{err: errors.New("initial inspection exploded")}}}
	db := openScriptedQueueViewDB(t, script)

	err := ensureCanonicalBeadQueueViews(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "query queue view definitions: initial inspection exploded") {
		t.Fatalf("ensure error = %v, want initial inspection failure", err)
	}
	if got := script.executed(); len(got) != 0 {
		t.Fatalf("initial inspection failure executed writes: %v", got)
	}
}

func TestEnsureCanonicalBeadQueueViewsReportsConnectionAcquisitionFailure(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{
		queries:          []scriptedQuery{{columns: []string{"name", "sql"}}},
		failConnectAfter: 1,
		connectErr:       errors.New("connection exploded"),
	}
	db := openScriptedQueueViewDB(t, script)
	db.SetMaxIdleConns(0)

	err := ensureCanonicalBeadQueueViews(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "acquire sqlite connection: connection exploded") {
		t.Fatalf("ensure error = %v, want connection acquisition failure", err)
	}
}

func TestEnsureCanonicalBeadQueueViewsReportsBeginFailure(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{
		queries:      []scriptedQuery{{columns: []string{"name", "sql"}}},
		execFailures: map[string]error{"BEGIN": errors.New("begin exploded")},
	}
	db := openScriptedQueueViewDB(t, script)

	err := ensureCanonicalBeadQueueViews(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "begin queue view refresh: begin exploded") {
		t.Fatalf("ensure error = %v, want begin failure", err)
	}
	assertExecuted(t, script, "BEGIN")
}

func TestEnsureCanonicalBeadQueueViewsRechecksAfterWriteLock(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{queries: []scriptedQuery{
		{columns: []string{"name", "sql"}},
		{columns: []string{"name", "sql"}, values: scriptedCanonicalRows()},
	}}
	db := openScriptedQueueViewDB(t, script)

	if err := ensureCanonicalBeadQueueViews(context.Background(), db); err != nil {
		t.Fatalf("ensure canonical views: %v", err)
	}
	assertExecuted(t, script, "BEGIN", "COMMIT")
}

func TestEnsureCanonicalBeadQueueViewsRollsBackAfterRecheckFailure(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{queries: []scriptedQuery{
		{columns: []string{"name", "sql"}},
		{err: errors.New("recheck exploded")},
	}}
	db := openScriptedQueueViewDB(t, script)

	err := ensureCanonicalBeadQueueViews(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "query queue view definitions: recheck exploded") {
		t.Fatalf("ensure error = %v, want recheck failure", err)
	}
	assertExecuted(t, script, "BEGIN", "ROLLBACK")
}

func TestEnsureCanonicalBeadQueueViewsRollsBackAfterInstallFailure(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{
		queries: []scriptedQuery{
			{columns: []string{"name", "sql"}},
			{columns: []string{"name", "sql"}},
		},
		execFailures: map[string]error{"INSTALL": errors.New("install exploded")},
	}
	db := openScriptedQueueViewDB(t, script)

	err := ensureCanonicalBeadQueueViews(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "install canonical queue views: install exploded") {
		t.Fatalf("ensure error = %v, want install failure", err)
	}
	assertExecuted(t, script, "BEGIN", "INSTALL", "ROLLBACK")
}

func TestEnsureCanonicalBeadQueueViewsRollsBackAfterCommitFailure(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{
		queries: []scriptedQuery{
			{columns: []string{"name", "sql"}},
			{columns: []string{"name", "sql"}},
		},
		execFailures: map[string]error{"COMMIT": errors.New("commit exploded")},
	}
	db := openScriptedQueueViewDB(t, script)

	err := ensureCanonicalBeadQueueViews(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "commit queue view refresh: commit exploded") {
		t.Fatalf("ensure error = %v, want commit failure", err)
	}
	assertExecuted(t, script, "BEGIN", "INSTALL", "COMMIT", "ROLLBACK")
}

func TestEnsureCanonicalBeadQueueViewsDoesNotRollbackAfterCommit(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{queries: []scriptedQuery{
		{columns: []string{"name", "sql"}},
		{columns: []string{"name", "sql"}},
	}}
	db := openScriptedQueueViewDB(t, script)

	if err := ensureCanonicalBeadQueueViews(context.Background(), db); err != nil {
		t.Fatalf("ensure canonical views: %v", err)
	}
	assertExecuted(t, script, "BEGIN", "INSTALL", "COMMIT")
}

func TestEnsureCanonicalBeadQueueViewsRepairsRealSQLiteSchema(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/state.db?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping SQLite database: %v", err)
	}
	if _, err := db.ExecContext(ctx, beadSchemaCoreDDL); err != nil {
		t.Fatalf("install core schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP VIEW IF EXISTS beads_ready; CREATE VIEW beads_ready AS SELECT 'stale' AS id`); err != nil {
		t.Fatalf("install stale ready view: %v", err)
	}

	canonical, err := hasCanonicalBeadQueueViews(ctx, db)
	if err != nil {
		t.Fatalf("inspect stale views: %v", err)
	}
	if canonical {
		t.Fatal("stale views reported canonical")
	}
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("stale-view inspection retained %d SQLite connections, want 0", inUse)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := ensureCanonicalBeadQueueViews(ctx, db); err != nil {
		t.Fatalf("repair stale views: %v", err)
	}
	canonical, err = hasCanonicalBeadQueueViews(ctx, db)
	if err != nil {
		t.Fatalf("inspect repaired views: %v", err)
	}
	if !canonical {
		t.Fatal("repaired views reported non-canonical")
	}
}

func TestMigrateBeadSchemaReportsStageFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		queryFailureAt int
		execFailureAt  int
		wantErr        string
	}{
		{name: "initial schema", execFailureAt: 1, wantErr: "migrate bead schema"},
		{name: "contract columns", queryFailureAt: 1, wantErr: "migrate bead contract columns"},
		{name: "assignment evidence", queryFailureAt: 2, wantErr: "migrate assignment evidence columns"},
		{name: "review checkpoint schema", queryFailureAt: 3, wantErr: "migrate review checkpoint schema"},
		{name: "recovery quarantine schema", queryFailureAt: 8, wantErr: "migrate recovery quarantine schema"},
		{name: "ops runs uniqueness", queryFailureAt: 10, wantErr: "migrate ops_runs legacy uniqueness"},
		{name: "refreshed core schema", execFailureAt: 3, wantErr: "refresh bead schema"},
		{name: "queue view refresh", queryFailureAt: 11, wantErr: "refresh bead queue views"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := successfulMigrateBeadSchemaQueries()
			if test.queryFailureAt > 0 {
				queries[test.queryFailureAt-1] = scriptedQuery{err: errors.New("stage exploded")}
			}
			script := &queueViewDBScript{queries: queries}
			if test.execFailureAt > 0 {
				script.execFailureAt = map[int]error{test.execFailureAt: errors.New("stage exploded")}
			}
			db := openScriptedQueueViewDB(t, script)

			err := MigrateBeadSchema(context.Background(), db)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("MigrateBeadSchema() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestMigrateBeadSchemaRefreshesCoreSchema(t *testing.T) {
	t.Parallel()

	script := &queueViewDBScript{queries: successfulMigrateBeadSchemaQueries()}
	db := openScriptedQueueViewDB(t, script)
	if err := MigrateBeadSchema(context.Background(), db); err != nil {
		t.Fatalf("MigrateBeadSchema() error = %v", err)
	}
	assertExecuted(t, script, "INSTALL", "INSTALL", "INSTALL")
}

type scriptedQuery struct {
	columns     []string
	values      [][]driver.Value
	terminalErr error
	err         error
}

type queueViewDBScript struct {
	mu               sync.Mutex
	queries          []scriptedQuery
	queryIndex       int
	execFailures     map[string]error
	execFailureAt    map[int]error
	execIndex        int
	execs            []string
	openCount        int
	failConnectAfter int
	connectErr       error
	rowsClosed       int
}

func (s *queueViewDBScript) Connect(context.Context) (driver.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openCount++
	if s.failConnectAfter > 0 && s.openCount > s.failConnectAfter {
		return nil, s.connectErr
	}
	return &scriptedQueueViewConn{script: s}, nil
}

func (*queueViewDBScript) Driver() driver.Driver { return scriptedQueueViewDriver{} }

func (s *queueViewDBScript) nextQuery() (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queryIndex >= len(s.queries) {
		return nil, fmt.Errorf("unexpected query %d", s.queryIndex+1)
	}
	query := s.queries[s.queryIndex]
	s.queryIndex++
	if query.err != nil {
		return nil, query.err
	}
	return &scriptedQueueViewRows{script: s, query: query}, nil
}

func (s *queueViewDBScript) exec(query string) (driver.Result, error) {
	kind := classifyQueueViewExec(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execIndex++
	s.execs = append(s.execs, kind)
	if err := s.execFailureAt[s.execIndex]; err != nil {
		return nil, err
	}
	if err := s.execFailures[kind]; err != nil {
		return nil, err
	}
	return driver.RowsAffected(0), nil
}

func (s *queueViewDBScript) executed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

func (s *queueViewDBScript) rowsCloseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rowsClosed
}

type scriptedQueueViewDriver struct{}

func (scriptedQueueViewDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("scripted queue-view driver requires connector")
}

type scriptedQueueViewConn struct{ script *queueViewDBScript }

func (*scriptedQueueViewConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (*scriptedQueueViewConn) Close() error { return nil }
func (*scriptedQueueViewConn) Begin() (driver.Tx, error) {
	return nil, errors.New("begin not supported")
}
func (c *scriptedQueueViewConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.script.nextQuery()
}
func (c *scriptedQueueViewConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	return c.script.exec(query)
}

type scriptedQueueViewRows struct {
	script      *queueViewDBScript
	query       scriptedQuery
	index       int
	terminalHit bool
}

func (r *scriptedQueueViewRows) Columns() []string { return r.query.columns }
func (r *scriptedQueueViewRows) Close() error {
	r.script.mu.Lock()
	r.script.rowsClosed++
	r.script.mu.Unlock()
	return nil
}
func (r *scriptedQueueViewRows) Next(dest []driver.Value) error {
	if r.index < len(r.query.values) {
		copy(dest, r.query.values[r.index])
		r.index++
		return nil
	}
	if r.query.terminalErr != nil && !r.terminalHit {
		r.terminalHit = true
		return r.query.terminalErr
	}
	return io.EOF
}

func openScriptedQueueViewDB(t *testing.T, script *queueViewDBScript) *sql.DB {
	t.Helper()
	// The scripted driver owns no external resources. Deliberately do not close
	// this DB in cleanup: a missing rows.Close call is one of the behaviors under
	// test, and sql.DB.Close would wait forever for that leaked synthetic row set.
	return sql.OpenDB(script)
}

func scriptedCanonicalRows() [][]driver.Value {
	definitions := canonicalBeadQueueViewDefinitions()
	return [][]driver.Value{
		{"beads_ready", definitions["beads_ready"]},
		{"beads_blocked", definitions["beads_blocked"]},
		{"review_checkpoints_blocking_assignment", definitions["review_checkpoints_blocking_assignment"]},
	}
}

func successfulMigrateBeadSchemaQueries() []scriptedQuery {
	emptyColumns := func() scriptedQuery {
		return scriptedQuery{columns: []string{"name", "notnull"}}
	}
	return []scriptedQuery{
		emptyColumns(), // beads columns
		emptyColumns(), // assignments columns
		emptyColumns(), // review_checkpoints columns
		emptyColumns(), // review_checkpoint_findings columns
		emptyColumns(), // review_recovery_attempts columns
		emptyColumns(), // review_quarantine_deliveries columns
		{columns: []string{"sql"}, values: [][]driver.Value{{reviewCheckpointActiveKeyIndexDDL}}},
		emptyColumns(),
		{columns: []string{"sql"}}, // beads table definition: absent
		{columns: []string{"sql"}}, // ops_runs table definition: absent
		{columns: []string{"name", "sql"}, values: scriptedCanonicalRows()},
	}
}

func replaceScriptedDefinition(rows [][]driver.Value, name, definition string) [][]driver.Value {
	replaced := make([][]driver.Value, len(rows))
	for i, row := range rows {
		replaced[i] = append([]driver.Value(nil), row...)
		if replaced[i][0] == name {
			replaced[i][1] = definition
		}
	}
	return replaced
}

func classifyQueueViewExec(query string) string {
	switch strings.TrimSpace(query) {
	case "BEGIN IMMEDIATE":
		return "BEGIN"
	case "COMMIT":
		return "COMMIT"
	case "ROLLBACK":
		return "ROLLBACK"
	default:
		return "INSTALL"
	}
}

func assertExecuted(t *testing.T, script *queueViewDBScript, want ...string) {
	t.Helper()
	got := script.executed()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("executed statements = %v, want %v", got, want)
	}
}
