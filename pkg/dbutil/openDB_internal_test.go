package dbutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sqliteCodeError int

func (e sqliteCodeError) Error() string { return fmt.Sprintf("sqlite error %d", e) }
func (e sqliteCodeError) Code() int     { return int(e) }

func TestRetrySQLiteBusyStopsAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	started := time.Now()
	calls := 0
	err := retrySQLiteBusy(ctx, func() error {
		calls++
		return sqliteCodeError(261) // SQLITE_BUSY_RECOVERY
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retrySQLiteBusy error = %v, want context deadline exceeded", err)
	}
	if calls < 2 {
		t.Errorf("retrySQLiteBusy calls = %d, want at least 2", calls)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Errorf("retrySQLiteBusy elapsed = %v, want <= 250ms", elapsed)
	}
}

func TestRetrySQLiteBusyReturnsNonBusyErrorWithoutRetry(t *testing.T) {
	want := errors.New("not busy")
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	calls := 0
	err := retrySQLiteBusy(ctx, func() error {
		calls++
		return want
	})

	if !errors.Is(err, want) {
		t.Fatalf("retrySQLiteBusy error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("retrySQLiteBusy calls = %d, want 1", calls)
	}
}

func TestRetrySQLiteBusyRetriesBaseAndExtendedBusyCodes(t *testing.T) {
	for _, code := range []int{5, 261} {
		t.Run(fmt.Sprintf("code_%d", code), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			calls := 0
			err := retrySQLiteBusy(ctx, func() error {
				calls++
				if calls == 1 {
					return fmt.Errorf("wrapped: %w", sqliteCodeError(code))
				}
				return nil
			})
			if err != nil {
				t.Fatalf("retrySQLiteBusy error = %v, want nil", err)
			}
			if calls != 2 {
				t.Fatalf("retrySQLiteBusy calls = %d, want 2", calls)
			}
		})
	}
}

func TestIsSQLiteBusyClassifiesOnlyBusyCodeFamily(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "base busy", err: sqliteCodeError(5), want: true},
		{name: "wrapped recovery busy", err: fmt.Errorf("wrapped: %w", sqliteCodeError(261)), want: true},
		{name: "locked", err: sqliteCodeError(6), want: false},
		{name: "plain error", err: errors.New("plain"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSQLiteBusy(tt.err); got != tt.want {
				t.Fatalf("isSQLiteBusy(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestWithBusyTimeoutPreservesURIAndReplacesEveryExistingTimeout(t *testing.T) {
	dsn, err := withBusyTimeout("file:state.db?mode=memory&cache=shared&_pragma=busy_timeout(1)&_pragma=foreign_keys(1)&_pragma=BUSY_TIMEOUT%3D2#anchor")
	if err != nil {
		t.Fatalf("withBusyTimeout: %v", err)
	}

	base, fragment, found := strings.Cut(dsn, "#")
	if !found || fragment != "anchor" {
		t.Fatalf("DSN fragment = %q, found %t, want anchor", fragment, found)
	}
	path, rawQuery, found := strings.Cut(base, "?")
	if !found || path != "file:state.db" {
		t.Fatalf("DSN path = %q, query found %t, want file:state.db", path, found)
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse configured DSN: %v", err)
	}
	if got := query.Get("mode"); got != "memory" {
		t.Errorf("mode = %q, want memory", got)
	}
	if got := query.Get("cache"); got != "shared" {
		t.Errorf("cache = %q, want shared", got)
	}
	wantPragmas := []string{"foreign_keys(1)", "busy_timeout(5000)"}
	if got := query["_pragma"]; fmt.Sprint(got) != fmt.Sprint(wantPragmas) {
		t.Fatalf("pragmas = %q, want %q", got, wantPragmas)
	}
}

func TestWithBusyTimeoutRejectsMalformedQuery(t *testing.T) {
	_, err := withBusyTimeout("file:state.db?mode=%zz")
	if err == nil {
		t.Fatal("withBusyTimeout malformed query error = nil")
	}
	if !strings.Contains(err.Error(), "parse query parameters") {
		t.Fatalf("withBusyTimeout error = %q, want parse query parameters", err)
	}
}

func TestIsBusyTimeoutPragmaRecognizesSupportedSQLiteForms(t *testing.T) {
	for _, pragma := range []string{
		"busy_timeout",
		" BUSY_TIMEOUT(1)",
		"busy_timeout=2",
		"busy_timeout 3",
		"busy_timeout\t4",
	} {
		if !isBusyTimeoutPragma(pragma) {
			t.Errorf("isBusyTimeoutPragma(%q) = false, want true", pragma)
		}
	}
	for _, pragma := range []string{"foreign_keys(1)", "busy_timeout_extra(1)", "=busy_timeout"} {
		if isBusyTimeoutPragma(pragma) {
			t.Errorf("isBusyTimeoutPragma(%q) = true, want false", pragma)
		}
	}
}

func TestOpenDBReportsParentCreationFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("create parent blocker: %v", err)
	}

	db, err := OpenDB(filepath.Join(blocker, "state.db"))
	if db != nil {
		_ = db.Close()
		t.Fatal("OpenDB database != nil on parent creation failure")
	}
	if err == nil || !strings.Contains(err.Error(), "create dir for") {
		t.Fatalf("OpenDB error = %v, want create dir for", err)
	}
}

func TestOpenDBReportsConfigurationFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := OpenDB("file:state.db?mode=%zz")
	if db != nil {
		_ = db.Close()
		t.Fatal("OpenDB database != nil on configuration failure")
	}
	if err == nil || !strings.Contains(err.Error(), "configure sqlite") {
		t.Fatalf("OpenDB error = %v, want configure sqlite", err)
	}
}

func TestOpenDBReportsPingFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	path := "file:missing.db?mode=ro"
	db, err := OpenDB(path)
	if db != nil {
		_ = db.Close()
		t.Fatal("OpenDB database != nil on ping failure")
	}
	if err == nil || !strings.Contains(err.Error(), "ping sqlite") {
		t.Fatalf("OpenDB error = %v, want ping sqlite", err)
	}
}

func TestOpenDBReportsWALFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	path := "readonly.db"
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE seed (id INTEGER)`); err != nil {
		_ = seed.Close()
		t.Fatalf("initialize seed database: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	db, err := OpenDB("file:" + path + "?mode=ro")
	if db != nil {
		_ = db.Close()
		t.Fatal("OpenDB database != nil on WAL failure")
	}
	if err == nil || !strings.Contains(err.Error(), "set WAL mode") {
		t.Fatalf("OpenDB error = %v, want set WAL mode", err)
	}
}
