package storage_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestNamespaceCancellationTargetsAllOwners(t *testing.T) {
	t.Parallel()

	owners := []storage.LeaseOwner{
		{LeaseID: "worker", Namespace: "oversized", Identity: cancellationIdentity(101)},
		{LeaseID: "reviewer", Namespace: "oversized", Identity: cancellationIdentity(202)},
		{LeaseID: "reused-pid", Namespace: "oversized", Identity: cancellationIdentity(303)},
		{LeaseID: "other-namespace", Namespace: "other", Identity: cancellationIdentity(404)},
	}

	for _, tt := range []struct {
		name          string
		critical      bool
		wantSleeps    []time.Duration
		wantTerminals int
	}{
		{name: "normal pressure grants grace", wantSleeps: []time.Duration{30 * time.Second}, wantTerminals: 2},
		{name: "critical pressure skips grace", critical: true, wantTerminals: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var signals []cancellationSignal
			var sleeps []time.Duration
			canceller := storage.NewNamespaceCanceller(
				func(pid int) (storage.ProcessIdentity, error) {
					if pid == 303 {
						identity := cancellationIdentity(pid)
						identity.StartMarker = "reused"
						return identity, nil
					}
					return cancellationIdentity(pid), nil
				},
				func(processGroup int, signal os.Signal) error {
					signals = append(signals, cancellationSignal{processGroup: processGroup, signal: signal})
					return nil
				},
				func(delay time.Duration) { sleeps = append(sleeps, delay) },
			)

			if err := canceller.CancelOversizedNamespace("oversized", owners, tt.critical); err != nil {
				t.Fatalf("CancelOversizedNamespace() error = %v", err)
			}
			if len(sleeps) != len(tt.wantSleeps) {
				t.Fatalf("grace waits = %v, want %v", sleeps, tt.wantSleeps)
			}
			for index, want := range tt.wantSleeps {
				if sleeps[index] != want {
					t.Errorf("grace wait %d = %s, want %s", index, sleeps[index], want)
				}
			}

			wantSignals := []cancellationSignal{
				{processGroup: 101, signal: os.Interrupt},
				{processGroup: 202, signal: os.Interrupt},
			}
			for _, owner := range owners[:2] {
				wantSignals = append(wantSignals, cancellationSignal{processGroup: owner.Identity.ProcessGroup, signal: os.Kill})
			}
			if len(signals) != len(wantSignals) {
				t.Fatalf("signals = %v, want %v", signals, wantSignals)
			}
			for index, want := range wantSignals {
				if signals[index] != want {
					t.Errorf("signal %d = %+v, want %+v", index, signals[index], want)
				}
			}
			if terminalSignals := len(signals) - 2; terminalSignals != tt.wantTerminals {
				t.Errorf("terminal signals = %d, want %d", terminalSignals, tt.wantTerminals)
			}
		})
	}
}

type cancellationSignal struct {
	processGroup int
	signal       os.Signal
}

func cancellationIdentity(pid int) storage.ProcessIdentity {
	return storage.ProcessIdentity{
		PID:          pid,
		StartMarker:  fmt.Sprintf("start-%d", pid),
		Executable:   "oro",
		ProcessGroup: pid,
	}
}
