//go:build cgo && darwin

// ad_hoc/memory_eval/openevaldb.go
package memoryeval

import (
	"database/sql"
	"fmt"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"

	"oro/pkg/dbutil"
)

var (
	registerOnce sync.Once
	registerErr  error
)

// OpenEvalDB opens a SQLite database with sqlite-vec loaded via the mattn
// driver. The custom driver is registered once; subsequent calls reuse it.
func OpenEvalDB(dbPath string) (*sql.DB, error) {
	registerOnce.Do(func() {
		libPath, err := dbutil.ResolveSqliteVecLibPath()
		if err != nil {
			registerErr = fmt.Errorf("run install.sh or set ORO_SQLITE_VEC_LIB: %w", err)
			return
		}
		sql.Register("sqlite3_with_vec", &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.LoadExtension(libPath, "sqlite3_vec_init")
			},
		})
	})
	if registerErr != nil {
		return nil, registerErr
	}

	db, err := sql.Open("sqlite3_with_vec", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open eval db %s: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping eval db %s: %w", dbPath, err)
	}
	return db, nil
}
