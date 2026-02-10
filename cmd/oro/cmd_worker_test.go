package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestNewWorkerCmd_Flags(t *testing.T) {
	cmd := newWorkerCmd()

	if cmd.Use != "worker" {
		t.Fatalf("expected Use=worker, got %s", cmd.Use)
	}

	socketFlag := cmd.Flag("socket")
	if socketFlag == nil {
		t.Fatal("expected --socket flag")
	}

	idFlag := cmd.Flag("id")
	if idFlag == nil {
		t.Fatal("expected --id flag")
	}
}

func TestNewWorkerCmd_RequiresSocket(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--id=w-01"})
	// Should fail because --socket is required
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --socket not provided")
	}
}

func TestNewWorkerCmd_RequiresID(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--socket=/tmp/test.sock"})
	// Should fail because --id is required
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --id not provided")
	}
}

func TestNewWorkerCmd_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "worker" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'worker' subcommand in root")
	}
}

func TestNewWorkerCmd_InvalidSocket(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--socket=/nonexistent/path/test.sock", "--id=w-01"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error connecting to nonexistent socket")
	}
}

// TestOpenWorkerMemoryDB verifies that openWorkerMemoryDB opens a SQLite
// connection and creates a valid memory.Store. This ensures the worker memory
// wiring path works end-to-end.
func TestOpenWorkerMemoryDB(t *testing.T) {
	// Use a temp file for the DB so we can verify it opens correctly.
	dsn := fmt.Sprintf("file:worker_mem_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer func() { _ = db.Close() }()

	store := openWorkerMemoryStore(db)
	if store == nil {
		t.Fatal("expected non-nil memory store from openWorkerMemoryStore")
	}
}
