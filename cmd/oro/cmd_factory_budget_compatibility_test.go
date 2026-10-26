package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/factory"
	"oro/pkg/storage"
)

func TestFactoryHostBudgetOldReservation(t *testing.T) {
	// Independent historical/current calculations for factoryBudgetFixture's
	// finite policy: X=6, Q=5, E=11; both complete request copies are retained.
	const (
		fixed      = 1924136960
		source     = 2*3584 + 2*1024 + 128<<10 + 1<<20
		pending    = 4*(1<<20) + 16384
		retained32 = fixed + 6*source + 5*(2*(32<<20)+2*pending+2*(1<<20)) + 11*(1<<20)
		retained64 = fixed + 6*source + 5*(2*(64<<20)+2*pending+2*(1<<20)) + 11*(1<<20)
	)
	for _, released := range []bool{false, true} {
		name := "active historical reservation remains charged"
		if released {
			name = "released historical objective remains terminal"
		}
		t.Run(name, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			limits, err := factory.ManagedHostRetention(spec)
			if err != nil || retained32 != 2330941440 || retained64 != 2666485760 || retained32 >= retained64 || limits.MinimumBytes != retained64 {
				t.Fatalf("invalid B32/B64 fixture: old=%d current=%d limits=%+v, %v", retained32, retained64, limits, err)
			}
			spec.Runtime.HostRequiredBytes = retained32
			seedFactoryBudgetCompatibility(t, root, spec, released)
			before := readFactoryBudgetCompatibility(t, root)
			assertFactoryBudgetHistoricalIdentity(t, root, spec, before, released)
			assertFactoryBudgetCompatibilityDenied(t, root, spec, before, "must cover retained-byte bound")
			raised := spec
			raised.Runtime.HostRequiredBytes = retained64
			assertFactoryBudgetCompatibilityDenied(t, root, raised, before, "cannot be reset or reused")
			if !released {
				if err := releaseFactoryHostReservation(t.Context(), root, before.ledger.Entries[0].ID); err == nil {
					t.Fatal("missing execution authority released historical capacity")
				}
				assertFactoryBudgetCompatibilityUnchanged(t, root, before)
			}
			assertFactoryBudgetCompatibilityFresh(t, root, raised, before)
		})
	}
}

func seedFactoryBudgetCompatibility(t *testing.T, root string, spec factory.ManagedSpec, released bool) {
	t.Helper()
	access, err := lockFactoryBudget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer access.close()
	// Seed historical persisted data directly. This is not evidence that current
	// production admission may bypass the increased minimum retention bound.
	ledger := factoryBudgetSnapshot{
		Version: 1, RootSHA256: access.rootHash, Revision: 1,
		Entries: []factoryBudgetEntry{{
			ID: strings.Repeat("a", 32), ObjectiveID: spec.ID,
			Bytes: spec.Runtime.HostRequiredBytes, ReserveBytes: spec.Runtime.HostReserveBytes,
			SpecSHA256: factoryBudgetCompatibilitySpecHash(t, spec),
		}},
	}
	if err := access.persist(t.Context(), ledger, false, factoryBudgetHooks{}); err != nil {
		t.Fatal(err)
	}
	if released {
		// A pre-existing terminal history has one additional release revision;
		// the immutable bootstrap First still contains the original active row.
		ledger.Entries[0].Released = true
		ledger.Revision++
		if err := access.publish(t.Context(), ledger, factoryBudgetHooks{}); err != nil {
			t.Fatal(err)
		}
	}
}

type factoryBudgetCompatibilityState struct {
	ledgerBytes, authorityBytes []byte
	ledger                      factoryBudgetSnapshot
	authority                   factoryBudgetBootstrap
	names                       map[string][]string
}

func readFactoryBudgetCompatibility(t *testing.T, root string) factoryBudgetCompatibilityState {
	t.Helper()
	parent, err := os.OpenRoot(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	metadata, err := os.OpenRoot(filepath.Join(root, ".managed"))
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	state := factoryBudgetCompatibilityState{names: make(map[string][]string)}
	rootHash := factoryBudgetHash([]byte(root))
	state.authorityBytes, err = readFactoryBudgetFile(parent, ".oro-factory-budget-"+rootHash+".init", factoryBudgetBootstrapBytes)
	if err != nil {
		t.Fatal(err)
	}
	state.authority, err = decodeFactoryBudgetBootstrap(state.authorityBytes, rootHash)
	if err != nil {
		t.Fatal(err)
	}
	state.ledgerBytes, err = readFactoryBudgetFile(metadata, factoryBudgetSnapshotName, factoryBudgetSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	state.ledger, err = decodeFactoryBudgetSnapshot(state.ledgerBytes, rootHash)
	if err != nil {
		t.Fatal(err)
	}
	// Do not use access.load here: recovery may remove a stage, hiding a rejected
	// admission's side effect. Read bytes and names without repairing anything.
	for _, directory := range []string{filepath.Dir(root), root, filepath.Join(root, ".managed")} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			state.names[directory] = append(state.names[directory], entry.Name())
		}
	}
	return state
}

func assertFactoryBudgetHistoricalIdentity(t *testing.T, root string, spec factory.ManagedSpec, state factoryBudgetCompatibilityState, released bool) {
	t.Helper()
	first, err := decodeFactoryBudgetSnapshot(state.authority.First, state.ledger.RootSHA256)
	if err != nil || state.authority.Phase != "initialized" || len(first.Entries) != 1 || first.Revision != 1 || first.Entries[0].Released {
		t.Fatalf("historical bootstrap identity invalid: %+v, %v", state.authority, err)
	}
	revision := int64(1)
	if released {
		revision++
	}
	if len(state.ledger.Entries) != 1 || state.ledger.Revision != revision {
		t.Fatalf("historical committed revision changed: %+v", state.ledger)
	}
	entry := state.ledger.Entries[0]
	want := factory.HostReservation{ID: strings.Repeat("a", 32), StateRoot: root, ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes, SpecSHA256: factoryBudgetCompatibilitySpecHash(t, spec)}
	if entry.reservation(root) != want || entry.SpecSHA256 != factoryBudgetCompatibilitySpecHash(t, spec) || entry.Released != released {
		t.Fatalf("historical tuple/hash/phase is not exact: %+v", entry)
	}
	entry.Released = false
	if first.Entries[0] != entry {
		t.Fatal("bootstrap First differs from original historical reservation")
	}
}

func assertFactoryBudgetCompatibilityDenied(t *testing.T, root string, spec factory.ManagedSpec, before factoryBudgetCompatibilityState, reason string) {
	t.Helper()
	probeCalls, mutations := 0, 0
	probe := func(context.Context, string) (storage.DiskUsage, error) {
		probeCalls++
		return storage.DiskUsage{}, errors.New("denied identity reached capacity probe")
	}
	mutation := func() error { mutations++; return nil }
	hooks := factoryBudgetHooks{
		beforeRename: mutation, afterRename: mutation,
		bootstrap: func(string) error { return mutation() },
	}
	inputHash := factoryBudgetCompatibilitySpecHash(t, spec)
	got, err := admitFactoryHostBudgetWithHooks(t.Context(), root, spec, probe, hooks)
	if err == nil || !strings.Contains(err.Error(), reason) || got != (factory.HostReservation{}) || probeCalls != 0 || mutations != 0 {
		t.Fatalf("historical admission was reused/probed/mutated: reservation=%+v probes=%d writes=%d, %v", got, probeCalls, mutations, err)
	}
	if factoryBudgetCompatibilitySpecHash(t, spec) != inputHash {
		t.Fatal("admission rewrote the incoming specification")
	}
	assertFactoryBudgetCompatibilityUnchanged(t, root, before)
}

func assertFactoryBudgetCompatibilityFresh(t *testing.T, root string, fresh factory.ManagedSpec, before factoryBudgetCompatibilityState) {
	t.Helper()
	fresh.ID = "fresh64"
	old := before.ledger.Entries[0]
	free := fresh.Runtime.HostRequiredBytes + fresh.Runtime.HostReserveBytes
	if !old.Released {
		if _, err := admitFactoryHostBudget(t.Context(), root, fresh, factoryBudgetProbe(free)); !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
			t.Fatalf("historical active charge disappeared: %v", err)
		}
		assertFactoryBudgetCompatibilityUnchanged(t, root, before)
		free += old.Bytes
	}
	probeCalls := 0
	probe := func(ctx context.Context, path string) (storage.DiskUsage, error) {
		probeCalls++
		if path != filepath.Join(root, fresh.ID) {
			t.Errorf("fresh probe used wrong objective: %q", path)
		}
		assertFactoryBudgetCompatibilityUnchanged(t, root, before)
		return factoryBudgetProbe(free)(ctx, path)
	}
	reservation, err := admitFactoryHostBudget(t.Context(), root, fresh, probe)
	if err != nil || probeCalls != 1 || reservation.ID == "" || reservation.ID == old.ID || reservation.ObjectiveID != fresh.ID ||
		reservation.StateRoot != root || reservation.Bytes != fresh.Runtime.HostRequiredBytes {
		t.Fatalf("fresh exact B64 positive control failed: %+v probes=%d, %v", reservation, probeCalls, err)
	}
	after := readFactoryBudgetCompatibility(t, root)
	retained, found := after.ledger.objective(old.ObjectiveID)
	added, addedOK := after.ledger.objective(fresh.ID)
	if !found || retained != old || !addedOK || added.reservation(root) != reservation || added.SpecSHA256 != factoryBudgetCompatibilitySpecHash(t, fresh) ||
		added.Released || len(after.ledger.Entries) != 2 || after.ledger.Revision != before.ledger.Revision+1 || !bytes.Equal(after.authorityBytes, before.authorityBytes) {
		t.Fatalf("new allocation amended historical authority: before=%+v after=%+v", before.ledger, after.ledger)
	}
	if repeated, err := admitFactoryHostBudget(t.Context(), root, fresh, nil); err != nil || repeated != reservation {
		t.Fatalf("fresh B64 identity was not idempotent: %+v, %v", repeated, err)
	}
	assertFactoryBudgetCompatibilityUnchanged(t, root, after)
	if _, err := os.Lstat(filepath.Join(root, fresh.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capacity allocation created execution state: %v", err)
	}
}

func assertFactoryBudgetCompatibilityUnchanged(t *testing.T, root string, before factoryBudgetCompatibilityState) {
	t.Helper()
	after := readFactoryBudgetCompatibility(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("historical ledger/bootstrap bytes, identities, revision or namespace changed: before=%+v after=%+v", before, after)
	}
}

func factoryBudgetCompatibilitySpecHash(t *testing.T, spec factory.ManagedSpec) string {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return factoryBudgetHash(data)
}
