package factory //nolint:testpackage // Test private core permit transactions, exact bindings and recovery.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testCleanupCounts(t *testing.T, j *managedJournal) [3]int {
	t.Helper()
	var counts [3]int
	for i, query := range []string{"SELECT count(*) FROM managed_reservations WHERE kind='cleanup'", "SELECT count(*) FROM managed_scopes WHERE backend='launchd'", "SELECT count(*) FROM managed_cleanup_permits"} {
		if err := j.conn.QueryRowContext(t.Context(), query).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func testManagedCleanupInvalid(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	for name, mutate := range map[string]func(*DockerCleanupPermit){
		"unknown operation":  func(p *DockerCleanupPermit) { p.Operation = "exec" },
		"stale generation":   func(p *DockerCleanupPermit) { p.Scope.Generation++ },
		"wrong target nonce": func(p *DockerCleanupPermit) { p.Target.Nonce = strings.Repeat("f", 32) },
		"untracked exact target": func(p *DockerCleanupPermit) {
			p.Target.ID = strings.Repeat("f", 32)
			p.Target.Name = strings.Replace(p.Target.Name, strings.Repeat("a", 32), p.Target.ID, 1)
			p.Binding.Name = p.Target.Name
			p.Binding.Labels["dev.getoro.factory.scope"] = p.Target.ID
		},
		"host target":               func(p *DockerCleanupPermit) { p.Target.Backend = "launchd" },
		"unknown target kind":       func(p *DockerCleanupPermit) { p.Target.Kind = "worker" },
		"arbitrary name":            func(p *DockerCleanupPermit) { p.Target.Name = "arbitrary"; p.Binding.Name = "arbitrary" },
		"wrong context":             func(p *DockerCleanupPermit) { p.Binding.Context = "foreign" },
		"wrong labels":              func(p *DockerCleanupPermit) { p.Binding.Labels["dev.getoro.factory.policy"] = strings.Repeat("f", 64) },
		"wrong resource kind":       func(p *DockerCleanupPermit) { p.Binding.Kind = "volume" },
		"unknown mutation identity": func(p *DockerCleanupPermit) { p.Binding.ContainerID = ""; p.Binding.CreatedAt = "" },
		"short container identity":  func(p *DockerCleanupPermit) { p.Binding.ContainerID = "abc" },
		"missing creation identity": func(p *DockerCleanupPermit) { p.Binding.CreatedAt = "" },
		"noncanonical creation":     func(p *DockerCleanupPermit) { p.Binding.CreatedAt = "2026-10-24T12:00:00+00:00" },
		"mutable image":             func(p *DockerCleanupPermit) { p.Binding.ImageID = "latest" },
		"container volume options":  func(p *DockerCleanupPermit) { p.Binding.VolumeOptions = map[string]string{"device": "tmpfs"} },
		"too many labels": func(p *DockerCleanupPermit) {
			for i := range 17 {
				p.Binding.Labels[strconv.Itoa(i)] = "x"
			}
		},
		"oversize descriptor value": func(p *DockerCleanupPermit) { p.Binding.Labels["extra"] = strings.Repeat("x", 1025) },
		"encoded permit exceeds limit": func(p *DockerCleanupPermit) {
			for i := range 10 {
				p.Binding.Labels[strconv.Itoa(i)] = strings.Repeat("\n", 1024)
			}
		},
		"expired deadline":  func(p *DockerCleanupPermit) { p.Deadline = time.Now().Add(-time.Second) },
		"extended deadline": func(p *DockerCleanupPermit) { p.Deadline = f.a.HardStop.Add(time.Nanosecond) },
	} {
		t.Run(name, func(t *testing.T) {
			p := DockerCleanupPermit{Scope: ScopeHandle{Generation: f.target.Generation}, Target: f.target, Operation: DockerCleanupStop, Binding: f.binding, Deadline: f.a.HardStop}
			p.Binding.Labels = maps.Clone(f.binding.Labels)
			mutate(&p)
			if scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, p.Scope.Generation, p.Target, p.Operation, p.Binding, p.Deadline); err == nil || scope != (ScopeHandle{}) {
				t.Fatalf("invalid permit reserved: %+v %v", scope, err)
			}
			if got := testCleanupCounts(t, f.j); got != ([3]int{}) {
				t.Fatalf("invalid request spent authority: %v", got)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reserveManagedCleanupInvocation(ctx, f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if _, err := f.j.reserveScope(t.Context(), ScopeHandle{ID: strings.Repeat("e", 32), Nonce: strings.Repeat("e", 32), Kind: "cleanup", Backend: "launchd", Name: "unpermitted", Generation: f.target.Generation}, f.a.HardStop, time.Now()); err == nil {
		t.Fatal("generic scope allocation bypassed typed cleanup reservation")
	}
}

func testManagedCleanupRecovery(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	first, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), first, 101, 101); err != nil {
		t.Fatal(err)
	}
	generation, _, err := f.j.beginGeneration(t.Context(), "replacement", time.Now())
	if err != nil || generation != 2 {
		t.Fatalf("stopping recovery=%d %v", generation, err)
	}
	if _, err := f.j.CleanupPermit(t.Context(), first); err == nil {
		t.Fatal("superseded cleanup permit remained current")
	}
	if err := f.j.RecordHello(t.Context(), first, 101, 101); err == nil {
		t.Fatal("stale HELLO revived cleanup")
	}
	if granted, err := f.j.Granted(t.Context(), first); err != nil || granted {
		t.Fatalf("stale grant=%v %v", granted, err)
	}
	if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop); err == nil {
		t.Fatal("retry skipped retirement of prior host helper")
	}
	if err := f.j.MarkQuiescent(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < f.a.Recovery.MaxCleanupInvocations; i++ {
		scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop)
		if err != nil || scope == first {
			t.Fatalf("fresh retry=%+v %v", scope, err)
		}
		if err := f.j.MarkQuiescent(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop); err == nil {
		t.Fatal("spent cleanup tokens were refunded")
	}
	if _, err := f.j.reserveBudget(t.Context(), generation, "host", "late", time.Now()); err == nil {
		t.Fatal("stopping recovery admitted work")
	}
	if _, _, err := f.j.beginGeneration(t.Context(), "supervisor", time.Now()); err == nil {
		t.Fatal("superseded supervisor resumed")
	}
	var used int
	if err := f.j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='supervisor'").Scan(&used); err != nil || used != 2 {
		t.Fatalf("stopping supervisor recovery uncounted: %d %v", used, err)
	}
}

func testManagedCleanupRollback(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	if _, err := f.j.conn.ExecContext(t.Context(), "CREATE TRIGGER reject_cleanup BEFORE INSERT ON managed_cleanup_permits BEGIN SELECT RAISE(ABORT,'forced permit failure'); END"); err != nil {
		t.Fatal(err)
	}
	before, err := f.j.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop); err == nil {
		t.Fatal("failed permit committed")
	}
	if got := testCleanupCounts(t, f.j); got != ([3]int{}) {
		t.Fatalf("partial permit reservation=%v", got)
	}
	after, err := f.j.supervision(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback changed supervision: %+v %v", after, err)
	}
}

func testManagedCleanupCorruption(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"malformed", "noncanonical", "wrong scope", "missing reservation", "wrong target columns", "expired"} {
		t.Run(kind, func(t *testing.T) {
			f := newCleanupFixture(t)
			scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop)
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.j.CleanupPermit(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.j.RecordHello(t.Context(), scope, 101, 101); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			query, args := "UPDATE managed_cleanup_permits SET body=?", []any{body}
			switch kind {
			case "malformed":
				args = []any{[]byte("{")}
			case "noncanonical":
				args = []any{append(body, ' ')}
			case "wrong scope":
				p.Scope.Nonce = strings.Repeat("f", 32)
				body, err = json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				args = []any{body}
			case "missing reservation":
				query, args = "DELETE FROM managed_reservations WHERE kind='cleanup'", nil
			case "wrong target columns":
				query, args = "UPDATE managed_scopes SET name='wrong' WHERE id=?", []any{f.target.ID}
			case "expired":
				p.Deadline = time.Now().Add(-time.Second)
				body, err = json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				args = []any{body}
			}
			if _, err := f.j.conn.ExecContext(t.Context(), query, args...); err != nil {
				t.Fatal(err)
			}
			if got, err := f.j.CleanupPermit(t.Context(), scope); err == nil || !reflect.DeepEqual(got, DockerCleanupPermit{}) {
				t.Fatalf("corrupt permit usable: %+v %v", got, err)
			}
			if err := f.j.RecordHello(t.Context(), scope, 101, 101); err == nil {
				t.Fatal("corrupt permit received GO")
			}
			if granted, err := f.j.Granted(t.Context(), scope); err == nil || granted {
				t.Fatalf("corrupt granted permit stayed usable: %v %v", granted, err)
			}
		})
	}
}
