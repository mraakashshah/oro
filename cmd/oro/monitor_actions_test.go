package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMonitorActionLedgerRecordsAndFindsRecentActions(t *testing.T) {
	ctx := context.Background()
	db, err := openStateDB(":memory:")
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer func() { _ = db.Close() }()

	recent, err := recentMonitorAction(ctx, nil, "restart", "daemon", time.Hour)
	if err != nil {
		t.Fatalf("nil db recent action: %v", err)
	}
	if recent {
		t.Fatal("nil db should not report a recent action")
	}
	if err := recordMonitorAction(ctx, nil, monitorAction{Action: "restart", Key: "daemon"}); err != nil {
		t.Fatalf("nil db record action: %v", err)
	}
	if err := recordMonitorAction(ctx, db, monitorAction{Action: "restart", Key: "daemon", Payload: `{"pid":123}`}); err != nil {
		t.Fatalf("record action: %v", err)
	}

	recent, err = recentMonitorAction(ctx, db, "restart", "daemon", 0)
	if err != nil {
		t.Fatalf("recent action: %v", err)
	}
	if !recent {
		t.Fatal("recorded action should be recent")
	}

	recent, err = recentMonitorAction(ctx, db, "restart", "other", time.Hour)
	if err != nil {
		t.Fatalf("recent action for other key: %v", err)
	}
	if recent {
		t.Fatal("different action key should not be recent")
	}
}

func TestPendingMonitorPauseRequiresUnconsumedQGPause(t *testing.T) {
	ctx := context.Background()
	db, err := openStateDB(":memory:")
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer func() { _ = db.Close() }()

	pause, ok, err := pendingMonitorPause(ctx, db)
	if err != nil {
		t.Fatalf("pending monitor pause before record: %v", err)
	}
	if ok {
		t.Fatalf("pending monitor pause = %+v, want none without a monitor-owned pause", pause)
	}
	if err := recordMonitorAction(ctx, db, monitorAction{Action: monitorActionQGChurnPause, Key: "qg:durable"}); err != nil {
		t.Fatalf("record qg pause: %v", err)
	}

	pause, ok, err = pendingMonitorPause(ctx, db)
	if err != nil {
		t.Fatalf("pending monitor pause after record: %v", err)
	}
	if !ok || pause.Action != monitorActionQGChurnPause || pause.Key != "qg:durable" {
		t.Fatalf("pending monitor pause = %+v, %v; want durable qg pause", pause, ok)
	}
	if err := recordMonitorAction(ctx, db, monitorAction{Action: monitorActionQGChurnResume, Key: pause.Key}); err != nil {
		t.Fatalf("record qg resume: %v", err)
	}

	if pause, ok, err = pendingMonitorPause(ctx, db); err != nil || ok {
		t.Fatalf("pending monitor pause after resume = %+v, %v, %v; want none", pause, ok, err)
	}
	if err := recordMonitorAction(ctx, db, monitorAction{Action: monitorActionQGChurnPause, Key: "qg:durable"}); err != nil {
		t.Fatalf("record repeated qg pause: %v", err)
	}
	if pause, ok, err = pendingMonitorPause(ctx, db); err != nil || !ok || pause.Key != "qg:durable" {
		t.Fatalf("pending repeated monitor pause = %+v, %v, %v; want latest unconsumed pause", pause, ok, err)
	}
}

func TestMonitorActionLedgerWrapsSQLErrors(t *testing.T) {
	ctx := context.Background()
	db, err := openStateDB(":memory:")
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `DROP TABLE monitor_actions`); err != nil {
		t.Fatalf("drop monitor_actions: %v", err)
	}

	if _, err := recentMonitorAction(ctx, db, "restart", "daemon", time.Hour); err == nil || !strings.Contains(err.Error(), "query monitor action ledger") {
		t.Fatalf("recentMonitorAction error = %v, want query wrapper", err)
	}
	if err := recordMonitorAction(ctx, db, monitorAction{Action: "restart", Key: "daemon"}); err == nil || !strings.Contains(err.Error(), "insert monitor action ledger row") {
		t.Fatalf("recordMonitorAction error = %v, want insert wrapper", err)
	}
}
