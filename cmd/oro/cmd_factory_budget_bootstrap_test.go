package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/factory"
	"oro/pkg/storage"
)

func TestFactoryBudgetBootstrap(t *testing.T) {
	t.Run("first publication survives process death", testFactoryBootstrapCrashes)
	t.Run("used authority never reconstructs", testFactoryBootstrapSealed)
	t.Run("legacy v1 remains sealed only", testFactoryBootstrapLegacy)
	t.Run("concurrent first lock creation", testFactoryBootstrapLockCreation)
	t.Run("first image recovery has no reset", testFactoryBootstrapRecoveryEdges)
	t.Run("parent staging is not authority", testFactoryBootstrapStaging)
	t.Run("death before initial authority publication", testFactoryBootstrapUnpublishedDeath)
	t.Run("parent authority cannot be a contained alias", testFactoryBootstrapParentAlias)
	t.Run("recovery proves directory durability before success", testFactoryBootstrapDurability)
	t.Run("recovery syncs parent before creating root", testFactoryBootstrapParentBeforeRoot)
}

func testFactoryBootstrapParentBeforeRoot(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	record := factoryBootstrapKill(t, root, spec, "bootstrap-rename")
	syncError := errors.New("bootstrap parent durability unavailable")
	syncCalls := 0
	hooks := factoryBudgetHooks{syncParent: func(*os.Root) error {
		syncCalls++
		return syncError
	}}
	got, err := admitFactoryHostBudgetWithHooks(t.Context(), root, spec, nil, hooks)
	if !errors.Is(err, syncError) || got.ID != "" || syncCalls != 1 {
		t.Fatalf("recovery returned before parent durability: %+v %v; sync calls %d", got, err, syncCalls)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery created root before bootstrap parent durability: %v", err)
	}
	marker := filepath.Join(filepath.Dir(root), ".oro-factory-budget-"+factoryBudgetHash([]byte(root))+".init")
	data, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(data, record) {
		t.Fatalf("parent sync failure changed or sealed bootstrap authority: %s %v", data, err)
	}
	got, err = admitFactoryHostBudget(t.Context(), root, spec, nil)
	if err != nil || !bytes.Contains(record, []byte(got.ID)) || got.ID == "" {
		t.Fatalf("durability retry lost original reservation: %+v %v", got, err)
	}
}

func testFactoryBootstrapDurability(t *testing.T) {
	t.Helper()
	for _, checkpoint := range []string{"ledger-rename", "phase-rename"} {
		t.Run(checkpoint, func(t *testing.T) {
			root, spec, _ := factoryBootstrapInterrupted(t, checkpoint)
			syncError := errors.New("directory durability unavailable")
			failed := func(*os.Root) error { return syncError }
			hooks := factoryBudgetHooks{}
			if checkpoint == "ledger-rename" {
				hooks.syncImage = failed
			} else {
				hooks.syncParent = failed
			}
			got, err := admitFactoryHostBudgetWithHooks(t.Context(), root, spec, nil, hooks)
			if !errors.Is(err, syncError) || got.ID != "" {
				t.Fatalf("recovery returned reservation without directory durability: %+v %v", got, err)
			}
			if checkpoint == "ledger-rename" {
				marker := filepath.Join(filepath.Dir(root), ".oro-factory-budget-"+factoryBudgetHash([]byte(root))+".init")
				data, err := os.ReadFile(marker)
				if err != nil || !bytes.Contains(data, []byte(`"phase":"bootstrap"`)) {
					t.Fatalf("image sync failure sealed initialization: %s %v", data, err)
				}
			}
			if _, err := admitFactoryHostBudget(t.Context(), root, spec, nil); err != nil {
				t.Fatalf("durability retry failed: %v", err)
			}
		})
	}
}

func testFactoryBootstrapParentAlias(t *testing.T) {
	t.Helper()
	for _, dangling := range []bool{false, true} {
		t.Run(fmt.Sprint(dangling), func(t *testing.T) {
			root, spec, _ := factoryBootstrapInterrupted(t, "bootstrap")
			marker := filepath.Join(filepath.Dir(root), ".oro-factory-budget-"+factoryBudgetHash([]byte(root))+".init")
			target := marker + ".copy"
			if err := os.Rename(marker, target); err != nil {
				t.Fatal(err)
			}
			if dangling {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Base(target), marker); err != nil {
				t.Fatal(err)
			}
			if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64)); err == nil {
				t.Fatal("aliased parent authority allowed initialization")
			}
			info, err := os.Lstat(marker)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("malformed authority was replaced: %v", err)
			}
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("malformed authority allocated state: %v", err)
			}
		})
	}
}

func testFactoryBootstrapUnpublishedDeath(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	factoryBootstrapKill(t, root, spec, "bootstrap-stage")
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unpublished bootstrap created state: %v", err)
	}
	spec.ID = "replacement"
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(spec.Runtime.HostRequiredBytes+100)); err != nil {
		t.Fatalf("uncommitted initial stage prevented fresh admission: %v", err)
	}
	access, err := lockFactoryBudget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer access.close()
	ledger, _, err := access.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Entries) != 1 || ledger.Entries[0].ObjectiveID != "replacement" {
		t.Fatalf("uncommitted parent stage became authority: %+v", ledger)
	}
}

func testFactoryBootstrapLockCreation(t *testing.T) {
	t.Helper()
	for range 16 {
		root, _ := factoryBudgetFixture(t)
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for range 2 {
			workers.Go(func() {
				<-start
				access, err := lockFactoryBudget(t.Context(), root)
				if err == nil {
					access.close()
				}
				results <- err
			})
		}
		close(start)
		workers.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("concurrent stable lock creation failed: %v", err)
			}
		}
	}
}

func factoryBootstrapInterrupted(t *testing.T, checkpoint string) (string, factory.ManagedSpec, []byte) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	interrupted := errors.New("interrupted bootstrap checkpoint")
	pause := func(name string) error {
		if name == checkpoint {
			return interrupted
		}
		return nil
	}
	hooks := factoryBudgetHooks{bootstrap: pause, beforeRename: func() error { return pause("ledger-stage") }, afterRename: func() error { return pause("ledger-rename") }}
	if _, err := admitFactoryHostBudgetWithHooks(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64), hooks); !errors.Is(err, interrupted) {
		t.Fatalf("checkpoint did not interrupt initialization: %v", err)
	}
	path := filepath.Join(filepath.Dir(root), ".oro-factory-budget-"+factoryBudgetHash([]byte(root))+".init")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return root, spec, data
}

func testFactoryBootstrapRecoveryEdges(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"root loss before initialization", "conflicting first image", "corrupt first image", "unexpected objective", "unexpected metadata", "invalid parent", "oversize parent", "parent checksum"} {
		t.Run(kind, func(t *testing.T) {
			root, spec, record := factoryBootstrapInterrupted(t, "ledger-rename")
			image := filepath.Join(root, ".managed", factoryBudgetSnapshotName)
			marker := filepath.Join(filepath.Dir(root), ".oro-factory-budget-"+factoryBudgetHash([]byte(root))+".init")
			var err error
			switch kind {
			case "root loss before initialization":
				err = os.RemoveAll(root)
			case "unexpected objective":
				err = os.Mkdir(filepath.Join(root, "already-used"), 0o700)
			case "unexpected metadata":
				err = os.WriteFile(filepath.Join(root, ".managed", "foreign.json"), []byte("foreign"), 0o600)
			case "conflicting first image", "corrupt first image":
				data := []byte("corrupt")
				if kind == "conflicting first image" {
					var a factoryBudgetBootstrap
					a, err = decodeFactoryBudgetBootstrap(record, factoryBudgetHash([]byte(root)))
					if err != nil {
						t.Fatal(err)
					}
					var first factoryBudgetSnapshot
					first, err = decodeFactoryBudgetSnapshot(a.First, a.RootSHA256)
					if err != nil {
						t.Fatal(err)
					}
					first.Entries[0].Bytes++
					data, err = encodeFactoryBudgetSnapshot(first)
					if err != nil {
						t.Fatal(err)
					}
				}
				err = os.Chmod(image, 0o600)
				if err == nil {
					err = os.WriteFile(image, data, 0o400)
				}
			case "invalid parent", "oversize parent", "parent checksum":
				data := []byte("partial")
				if kind == "oversize parent" {
					data = bytes.Repeat([]byte(" "), 2049)
				}
				if kind == "parent checksum" {
					data = bytes.Replace(record, []byte(`"phase":"bootstrap"`), []byte(`"phase":"initialized"`), 1)
				}
				err = os.Chmod(marker, 0o600)
				if err == nil {
					err = os.WriteFile(marker, data, 0o400)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64))
			if kind == "root loss before initialization" {
				if err != nil || !bytes.Contains(record, []byte(got.ID)) {
					t.Fatalf("original initialization not recovered: %+v %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("uncertain initial state silently reset: %+v", got)
			}
		})
	}
}

func testFactoryBootstrapStaging(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"initial stage and empty root", "initial stage and used root", "transition stage without authority", "oversize initial stage"} {
		t.Run(kind, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			access, err := lockFactoryBudget(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			phase := "bootstrap"
			if kind == "transition stage without authority" {
				phase = "initialized"
			}
			stage := filepath.Join(filepath.Dir(root), access.bootstrapStage(phase))
			access.close()
			data := []byte("partial")
			if kind == "oversize initial stage" {
				data = bytes.Repeat([]byte("x"), 2049)
			}
			if err := os.WriteFile(stage, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(kind, "root") {
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "initial stage and used root" {
				if err := os.Mkdir(filepath.Join(root, "old-objective"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			_, err = admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64))
			if kind == "initial stage and empty root" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("uncommitted stage retained: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("uncertain parent stage allowed reset")
			}
			got, err := os.ReadFile(stage)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("rejected authority evidence erased: %v", err)
			}
		})
	}
}

type factoryBootstrapCheckpoint struct {
	Record json.RawMessage `json:"record"`
}

func factoryBootstrapKill(t *testing.T, root string, spec factory.ManagedSpec, checkpoint string) []byte {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(filepath.Dir(root), "bootstrap-spec.json")
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactoryBudgetBootstrapChild$", "--", root, input, checkpoint)
	cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Dir(root))
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "CHECKPOINT ") {
		_ = cmd.Wait()
		t.Fatalf("checkpoint %s not reached: %s %s", checkpoint, scanner.Text(), diagnostic.String())
	}
	var captured factoryBootstrapCheckpoint
	if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "CHECKPOINT ")), &captured); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("initializer was not killed")
	}
	return captured.Record
}

func testFactoryBootstrapCrashes(t *testing.T) {
	t.Helper()
	for _, checkpoint := range []string{"bootstrap-rename", "bootstrap", "root", "metadata", "ledger-stage", "ledger-rename", "phase-stage", "phase-rename", "initialized"} {
		t.Run(checkpoint, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			record := factoryBootstrapKill(t, root, spec, checkpoint)
			// A different contender must recover and charge the ORIGINAL reservation
			// before evaluating its own request, even though no caller got success.
			next := spec
			next.ID = "contender"
			probe := factoryBudgetProbe(spec.Runtime.HostRequiredBytes + spec.Runtime.HostReserveBytes)
			if _, err := admitFactoryHostBudget(t.Context(), root, next, probe); !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
				t.Fatalf("first reservation not recovered and charged after %s: %v", checkpoint, err)
			}
			got, err := admitFactoryHostBudget(t.Context(), root, spec, probe)
			if err != nil {
				t.Fatal(err)
			}
			if len(record) > 2048 || !bytes.Contains(record, []byte(got.ID)) {
				t.Fatalf("recovered replacement identity or oversized record: %+v %s", got, record)
			}
			if _, err := os.Stat(filepath.Join(root, spec.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bootstrap allocated objective: %v", err)
			}
			access, err := lockFactoryBudget(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := readFactoryBudgetFile(access.parent, access.markerName, 2048)
			access.close()
			if err != nil || !bytes.Contains(data, []byte(`"phase":"initialized"`)) {
				t.Fatalf("API returned before durable initialized phase: %s %v", data, err)
			}
		})
	}
}

func testFactoryBootstrapSealed(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"missing root", "missing image", "corrupt image"} {
		t.Run(kind, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			factoryBootstrapKill(t, root, spec, "initialized")
			image := filepath.Join(root, ".managed", factoryBudgetSnapshotName)
			var err error
			switch kind {
			case "missing root":
				err = os.RemoveAll(root)
			case "missing image":
				err = os.Remove(image)
			case "corrupt image":
				err = os.Chmod(image, 0o600)
				if err == nil {
					err = os.WriteFile(image, []byte("corrupt"), 0o600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64)); err == nil {
				t.Fatal("used authority was reconstructed")
			}
		})
	}
}

func testFactoryBootstrapLegacy(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	first, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	access, err := lockFactoryBudget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(root), access.markerName)
	legacy := access.markerContents()
	access.close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o400); err != nil {
		t.Fatal(err)
	}
	got, err := admitFactoryHostBudget(t.Context(), root, spec, nil)
	if err != nil || got != first {
		t.Fatalf("legacy valid authority changed: %+v %v", got, err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64)); err == nil {
		t.Fatal("ambiguous v1 initialization treated as fresh")
	}
}

func TestFactoryBudgetBootstrapChild(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index:]
	if len(args) != 3 {
		t.Fatal("invalid bootstrap child arguments")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	var spec factory.ManagedSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	pause := func(checkpoint string) error {
		if checkpoint != args[2] {
			return nil
		}
		marker := filepath.Join(filepath.Dir(args[0]), ".oro-factory-budget-"+factoryBudgetHash([]byte(args[0]))+".init")
		data, err := os.ReadFile(marker)
		if errors.Is(err, os.ErrNotExist) && checkpoint == "bootstrap-stage" {
			data, err = os.ReadFile(marker + ".bootstrap-stage")
		}
		if err != nil {
			t.Fatal(err)
		}
		// Old v1 content is still valid JSON here as a string, so RED reaches the
		// intended recovery assertion rather than failing fixture parsing.
		if !json.Valid(data) {
			data, _ = json.Marshal(string(data))
		}
		encoded, _ := json.Marshal(factoryBootstrapCheckpoint{Record: data})
		fmt.Printf("CHECKPOINT %s\n", encoded)
		<-t.Context().Done()
		return t.Context().Err()
	}
	hooks := factoryBudgetHooks{bootstrap: pause, beforeRename: func() error { return pause("ledger-stage") }, afterRename: func() error { return pause("ledger-rename") }}
	_, err = admitFactoryHostBudgetWithHooks(t.Context(), args[0], spec, factoryBudgetProbe(math.MaxInt64), hooks)
	if err != nil {
		t.Fatal(err)
	}
}
