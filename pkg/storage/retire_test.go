package storage //nolint:testpackage // white-box test pauses the package-private remover to verify tombstone ordering.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNamespaceRetirementWaitsForLeases(t *testing.T) {
	t.Parallel()

	for _, reason := range []RetirementReason{RetirementPostMerge, RetirementNonOperative} {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			root := t.TempDir()
			namespace := "0123456789abcdef0123456789abcdef"
			path := filepath.Join(root, namespace)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("create namespace: %v", err)
			}
			if err := os.WriteFile(filepath.Join(path, "scratch"), []byte("data"), 0o600); err != nil {
				t.Fatalf("write namespace data: %v", err)
			}

			catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
			if err != nil {
				t.Fatalf("open catalog: %v", err)
			}
			t.Cleanup(func() { _ = catalog.Close() })

			now := time.Now().UTC()
			lease, err := catalog.AcquireLease(ctx, LeaseRequest{
				ID:           LeaseID("lease-" + string(reason)),
				Namespace:    namespace,
				ControllerID: "controller",
				OwnerID:      "owner",
				PID:          1,
				ProcessStart: now.Add(-time.Minute),
				AcquiredAt:   now,
				HeartbeatAt:  now,
			})
			if err != nil {
				t.Fatalf("acquire lease: %v", err)
			}

			retirer := NewNamespaceRetirer(catalog, root)
			retirer.pollInterval = time.Millisecond
			removalStarted := make(chan string, 1)
			allowRemoval := make(chan struct{})
			retirer.removeAll = func(tombstone string) error {
				removalStarted <- tombstone
				<-allowRemoval
				return os.RemoveAll(tombstone)
			}

			started := time.Now()
			if err := retirer.Retire(ctx, namespace, reason); err != nil {
				t.Fatalf("schedule retirement: %v", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("retirement blocked caller for %s", elapsed)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("leased namespace was removed: %v", err)
			}

			if err := catalog.ReleaseLease(ctx, lease.ID); err != nil {
				t.Fatalf("release lease: %v", err)
			}

			var tombstone string
			select {
			case tombstone = <-removalStarted:
			case <-time.After(time.Second):
				t.Fatal("retirement did not begin after lease release")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("namespace still exists after tombstoning: %v", err)
			}
			if info, err := os.Stat(tombstone); err != nil || !info.IsDir() {
				t.Fatalf("tombstone did not precede removal: info=%v err=%v", info, err)
			}

			close(allowRemoval)
			waitForRetirement(t, retirer)
			if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
				t.Fatalf("tombstone was not removed: %v", err)
			}
		})
	}
}

func TestNamespaceRetirementAllowsReretirement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	namespace := "0123456789abcdef0123456789abcdef"
	path := filepath.Join(root, namespace)

	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	retirer := NewNamespaceRetirer(catalog, root)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create namespace for attempt %d: %v", attempt, err)
		}
		if err := retirer.Retire(ctx, namespace, RetirementPostMerge); err != nil {
			t.Fatalf("schedule retirement attempt %d: %v", attempt, err)
		}
		waitForRetirement(t, retirer)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("namespace remains after retirement attempt %d: %v", attempt, err)
		}
	}
}

func waitForRetirement(t *testing.T, retirer *NamespaceRetirer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := retirer.Wait(ctx); err != nil {
		t.Fatalf("wait for retirement: %v", err)
	}
}
