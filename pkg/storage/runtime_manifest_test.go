//nolint:testpackage // The atomic filesystem seam is deliberately private.
package storage

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRuntimeManifestAtomicRoundTrip(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test root: %v", err)
	}
	cacheRoot := mustMkdir(t, filepath.Join(root, "cache"))
	tmpRoot := mustMkdir(t, filepath.Join(root, "tmp"))
	evidenceRoot := mustMkdir(t, filepath.Join(root, "evidence"))
	manifestRoot := mustMkdir(t, filepath.Join(root, "manifests"))
	evidencePath := filepath.Join(evidenceRoot, "mutation.json")
	if err := os.WriteFile(evidencePath, []byte("durable evidence"), 0o600); err != nil {
		t.Fatalf("write evidence fixture: %v", err)
	}

	manifestPath := filepath.Join(manifestRoot, "runtime-manifest.json")
	manifest := validRuntimeManifest(manifestPath, cacheRoot, tmpRoot, evidenceRoot)
	writeAndExpect(t, manifestPath, manifest)
	for _, state := range []ManifestState{
		ManifestActive,
		ManifestFinalizing,
		ManifestFinalized,
		ManifestReclaimable,
	} {
		manifest.State = state
		if state == ManifestFinalizing {
			manifest.EvidencePath = evidencePath
			manifest.EvidenceSHA256 = strings.Repeat("a", 64)
		}
		writeAndExpect(t, manifestPath, manifest)
	}

	for _, from := range []ManifestState{ManifestActive, ManifestFinalizing} {
		t.Run("interrupt "+string(from), func(t *testing.T) {
			path := filepath.Join(root, "interrupt-"+string(from)+".json")
			candidate := validRuntimeManifest(path, cacheRoot, tmpRoot, evidenceRoot)
			writeAndExpect(t, path, candidate)
			candidate.State = ManifestActive
			writeAndExpect(t, path, candidate)
			if from == ManifestFinalizing {
				candidate.State = ManifestFinalizing
				candidate.EvidencePath = evidencePath
				candidate.EvidenceSHA256 = strings.Repeat("a", 64)
				writeAndExpect(t, path, candidate)
			}
			candidate.State = ManifestInterrupted
			writeAndExpect(t, path, candidate)
		})
	}

	invalidFresh := []struct {
		name   string
		mutate func(*RuntimeManifest)
	}{
		{name: "zero schema", mutate: func(m *RuntimeManifest) { m.SchemaVersion = 0 }},
		{name: "unknown schema", mutate: func(m *RuntimeManifest) { m.SchemaVersion = 2 }},
		{name: "missing reservation", mutate: func(m *RuntimeManifest) { m.ReservationID = "" }},
		{name: "missing lease", mutate: func(m *RuntimeManifest) { m.LeaseID = "" }},
		{name: "relative manifest path", mutate: func(m *RuntimeManifest) { m.ManifestPath = "manifest.json" }},
		{name: "relative root", mutate: func(m *RuntimeManifest) { m.Roots[0].Path = "cache" }},
		{name: "non-clean root", mutate: func(m *RuntimeManifest) { m.Roots[0].Path += "/../cache" }},
		{name: "filesystem root", mutate: func(m *RuntimeManifest) {
			m.Roots[0].Path = filepath.VolumeName(root) + string(filepath.Separator)
		}},
		{name: "duplicate path same class", mutate: func(m *RuntimeManifest) { m.Roots = append(m.Roots, m.Roots[0]) }},
		{name: "duplicate path different class", mutate: func(m *RuntimeManifest) {
			m.Roots = append(m.Roots, ManagedRoot{Path: m.Roots[0].Path, Class: RootTemp, Disposition: RootDisposable})
		}},
		{name: "missing evidence root", mutate: func(m *RuntimeManifest) { m.Roots = m.Roots[:2] }},
		{name: "second evidence root", mutate: func(m *RuntimeManifest) {
			m.Roots = append(m.Roots, ManagedRoot{Path: filepath.Join(root, "evidence-2"), Class: RootEvidence, Disposition: RootDurable})
		}},
		{name: "missing temp root", mutate: func(m *RuntimeManifest) { m.Roots = []ManagedRoot{m.Roots[0], m.Roots[2]} }},
		{name: "cache durable", mutate: func(m *RuntimeManifest) { m.Roots[0].Disposition = RootDurable }},
		{name: "temp shared", mutate: func(m *RuntimeManifest) { m.Roots[1].Disposition = RootShared }},
		{name: "evidence disposable", mutate: func(m *RuntimeManifest) { m.Roots[2].Disposition = RootDisposable }},
		{name: "unknown class", mutate: func(m *RuntimeManifest) { m.Roots[0].Class = RootClass("unknown") }},
		{name: "unknown disposition", mutate: func(m *RuntimeManifest) { m.Roots[0].Disposition = RootDisposition("unknown") }},
	}
	for index, test := range invalidFresh {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, "invalid-fresh-"+string(rune('a'+index))+".json")
			candidate := validRuntimeManifest(path, cacheRoot, tmpRoot, evidenceRoot)
			candidate.Roots = append([]ManagedRoot(nil), candidate.Roots...)
			test.mutate(&candidate)
			if err := WriteRuntimeManifestAtomic(path, candidate); err == nil {
				t.Fatal("invalid manifest accepted")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid initial write exposed manifest: %v", err)
			}
			assertNoManifestTemps(t, filepath.Dir(path))
		})
	}

	symlinkTarget := mustMkdir(t, filepath.Join(root, "symlink-target"))
	symlinkRoot := filepath.Join(root, "symlink-root")
	if err := os.Symlink(symlinkTarget, symlinkRoot); err != nil {
		t.Fatalf("create symlink fixture: %v", err)
	}
	symlinkPath := filepath.Join(manifestRoot, "symlink-manifest.json")
	symlinkManifest := validRuntimeManifest(symlinkPath, cacheRoot, tmpRoot, evidenceRoot)
	symlinkManifest.Roots[1].Path = filepath.Join(symlinkRoot, "nested")
	if err := WriteRuntimeManifestAtomic(symlinkPath, symlinkManifest); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	sharedTarget := mustMkdir(t, filepath.Join(root, "shared-target"))
	for _, name := range []string{"manifests", "cache", "tmp", "evidence"} {
		mustMkdir(t, filepath.Join(sharedTarget, name))
	}
	sharedAlias := filepath.Join(root, "shared-alias")
	if err := os.Symlink(sharedTarget, sharedAlias); err != nil {
		t.Fatalf("create shared ancestor symlink: %v", err)
	}
	sharedPath := filepath.Join(sharedAlias, "manifests", "runtime.json")
	sharedManifest := validRuntimeManifest(
		sharedPath,
		filepath.Join(sharedAlias, "cache"),
		filepath.Join(sharedAlias, "tmp"),
		filepath.Join(sharedAlias, "evidence"),
	)
	if err := WriteRuntimeManifestAtomic(sharedPath, sharedManifest); err == nil {
		t.Fatal("shared symlink ancestor accepted")
	}

	rootMutations := []struct {
		name   string
		mutate func(*RuntimeManifest)
	}{
		{name: "path", mutate: func(m *RuntimeManifest) { m.Roots[0].Path = filepath.Join(root, "other-cache") }},
		{name: "class", mutate: func(m *RuntimeManifest) { m.Roots[0].Class = RootTemp }},
		{name: "shared to disposable", mutate: func(m *RuntimeManifest) { m.Roots[0].Disposition = RootDisposable }},
	}
	for index, test := range rootMutations {
		t.Run("immutable root "+test.name, func(t *testing.T) {
			path := filepath.Join(root, "immutable-root-"+string(rune('a'+index))+".json")
			candidate := validRuntimeManifest(path, cacheRoot, tmpRoot, evidenceRoot)
			candidate.Roots[0].Disposition = RootShared
			writeAndExpect(t, path, candidate)
			candidate.State = ManifestActive
			writeAndExpect(t, path, candidate)
			preserved := candidate
			preserved.Roots = append([]ManagedRoot(nil), candidate.Roots...)
			candidate.Roots = append([]ManagedRoot(nil), candidate.Roots...)
			candidate.State = ManifestFinalizing
			candidate.EvidencePath = evidencePath
			candidate.EvidenceSHA256 = strings.Repeat("a", 64)
			test.mutate(&candidate)
			if err := WriteRuntimeManifestAtomic(path, candidate); err == nil {
				t.Fatal("root identity mutation accepted")
			}
			got, err := ReadRuntimeManifest(path)
			if err != nil || !reflect.DeepEqual(got, preserved) {
				t.Fatalf("root mutation changed manifest: got=%#v err=%v", got, err)
			}
		})
	}

	evidenceInvalid := []struct {
		name   string
		mutate func(*RuntimeManifest)
	}{
		{name: "missing evidence", mutate: func(m *RuntimeManifest) {}},
		{name: "path only", mutate: func(m *RuntimeManifest) { m.EvidencePath = evidencePath }},
		{name: "hash only", mutate: func(m *RuntimeManifest) { m.EvidenceSHA256 = strings.Repeat("a", 64) }},
		{name: "uppercase hash", mutate: func(m *RuntimeManifest) {
			m.EvidencePath = evidencePath
			m.EvidenceSHA256 = strings.Repeat("A", 64)
		}},
		{name: "short hash", mutate: func(m *RuntimeManifest) {
			m.EvidencePath = evidencePath
			m.EvidenceSHA256 = strings.Repeat("a", 63)
		}},
		{name: "nonhex hash", mutate: func(m *RuntimeManifest) {
			m.EvidencePath = evidencePath
			m.EvidenceSHA256 = strings.Repeat("g", 64)
		}},
		{name: "outside durable root", mutate: func(m *RuntimeManifest) {
			m.EvidencePath = filepath.Join(root, "outside.json")
			m.EvidenceSHA256 = strings.Repeat("a", 64)
		}},
	}
	for index, test := range evidenceInvalid {
		t.Run("evidence "+test.name, func(t *testing.T) {
			path := filepath.Join(root, "invalid-evidence-"+string(rune('a'+index))+".json")
			candidate := validRuntimeManifest(path, cacheRoot, tmpRoot, evidenceRoot)
			writeAndExpect(t, path, candidate)
			candidate.State = ManifestActive
			writeAndExpect(t, path, candidate)
			candidate.State = ManifestFinalizing
			candidate.EvidencePath = evidencePath
			candidate.EvidenceSHA256 = strings.Repeat("a", 64)
			writeAndExpect(t, path, candidate)
			preserved := candidate
			candidate.State = ManifestFinalized
			candidate.EvidencePath = ""
			candidate.EvidenceSHA256 = ""
			test.mutate(&candidate)
			if err := WriteRuntimeManifestAtomic(path, candidate); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			got, err := ReadRuntimeManifest(path)
			if err != nil || !reflect.DeepEqual(got, preserved) {
				t.Fatalf("failed write did not preserve prior manifest: got=%#v err=%v", got, err)
			}
			assertNoManifestTemps(t, filepath.Dir(path))
		})
	}

	illegalTransitions := []struct{ from, to ManifestState }{
		{ManifestAllocating, ManifestFinalizing},
		{ManifestActive, ManifestFinalized},
		{ManifestFinalized, ManifestFinalized},
		{ManifestFinalized, ManifestActive},
		{ManifestInterrupted, ManifestActive},
		{ManifestReclaimable, ManifestActive},
	}
	for index, transition := range illegalTransitions {
		t.Run("illegal transition "+string(transition.from)+" to "+string(transition.to), func(t *testing.T) {
			path := filepath.Join(root, "invalid-transition-"+string(rune('a'+index))+".json")
			candidate := manifestAtState(t, path, cacheRoot, tmpRoot, evidenceRoot, evidencePath, transition.from)
			candidate.State = transition.to
			if err := WriteRuntimeManifestAtomic(path, candidate); err == nil {
				t.Fatal("illegal transition accepted")
			}
		})
	}

	malformedPath := filepath.Join(root, "malformed.json")
	if err := os.WriteFile(malformedPath, []byte("{\"schema_version\":1"), 0o600); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	if _, err := ReadRuntimeManifest(malformedPath); err == nil {
		t.Fatal("ReadRuntimeManifest accepted truncated JSON")
	}

	t.Run("atomic operation order and failures", func(t *testing.T) {
		stages := []string{"success", "file sync", "file close", "rename", "directory sync"}
		for index, stage := range stages {
			t.Run(stage, func(t *testing.T) {
				path := filepath.Join(root, "atomic-"+string(rune('a'+index))+".json")
				candidate := validRuntimeManifest(path, cacheRoot, tmpRoot, evidenceRoot)
				writeAndExpect(t, path, candidate)
				candidate.State = ManifestActive
				writeAndExpect(t, path, candidate)
				preserved := candidate
				candidate.State = ManifestFinalizing
				candidate.EvidencePath = evidencePath
				candidate.EvidenceSHA256 = strings.Repeat("a", 64)

				operations := make([]string, 0, 9)
				ops := newRuntimeManifestAtomicOps()
				originalCreate, originalChmod, originalWrite := ops.createTemp, ops.chmod, ops.write
				originalSync, originalClose := ops.sync, ops.close
				originalRename, originalOpen := ops.rename, ops.open
				ops.createTemp = func(dir, pattern string) (*os.File, error) {
					operations = append(operations, "create")
					return originalCreate(dir, pattern)
				}
				ops.chmod = func(file *os.File, mode os.FileMode) error {
					operations = append(operations, "chmod")
					return originalChmod(file, mode)
				}
				ops.write = func(file *os.File, contents []byte) error {
					operations = append(operations, "write")
					return originalWrite(file, contents)
				}
				ops.sync = func(file *os.File) error {
					if file.Name() == filepath.Dir(path) {
						operations = append(operations, "sync-directory")
						if stage == "directory sync" {
							return errors.New("injected directory sync failure")
						}
					} else {
						operations = append(operations, "sync-file")
						if stage == "file sync" {
							return errors.New("injected file sync failure")
						}
					}
					return originalSync(file)
				}
				ops.close = func(file *os.File) error {
					if file.Name() == filepath.Dir(path) {
						operations = append(operations, "close-directory")
					} else {
						operations = append(operations, "close-file")
						if stage == "file close" {
							_ = originalClose(file)
							return errors.New("injected file close failure")
						}
					}
					return originalClose(file)
				}
				ops.rename = func(oldPath, newPath string) error {
					operations = append(operations, "rename")
					if stage == "rename" {
						return errors.New("injected rename failure")
					}
					return originalRename(oldPath, newPath)
				}
				ops.open = func(dir string) (*os.File, error) {
					operations = append(operations, "open-directory")
					return originalOpen(dir)
				}

				err := writeRuntimeManifestAtomic(path, candidate, ops)
				if stage == "success" {
					if err != nil {
						t.Fatalf("atomic success error = %v", err)
					}
					want := []string{"create", "chmod", "write", "sync-file", "close-file", "rename", "open-directory", "sync-directory", "close-directory"}
					if !reflect.DeepEqual(operations, want) {
						t.Fatalf("atomic operations = %v, want %v", operations, want)
					}
				} else if err == nil {
					t.Fatal("injected atomic failure returned nil")
				}

				got, readErr := ReadRuntimeManifest(path)
				if readErr != nil {
					t.Fatalf("read after atomic operation: %v", readErr)
				}
				want := preserved
				if stage == "success" || stage == "directory sync" {
					want = candidate
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("manifest after %s = %#v, want %#v", stage, got, want)
				}
				assertNoManifestTemps(t, filepath.Dir(path))
			})
		}
	})
}

func manifestAtState(t *testing.T, path, cacheRoot, tmpRoot, evidenceRoot, evidencePath string, target ManifestState) RuntimeManifest {
	t.Helper()
	manifest := validRuntimeManifest(path, cacheRoot, tmpRoot, evidenceRoot)
	writeAndExpect(t, path, manifest)
	if target == ManifestInterrupted {
		manifest.State = ManifestActive
		writeAndExpect(t, path, manifest)
		manifest.State = ManifestInterrupted
		writeAndExpect(t, path, manifest)
		return manifest
	}
	for _, state := range []ManifestState{ManifestActive, ManifestFinalizing, ManifestFinalized, ManifestReclaimable} {
		if target == ManifestAllocating {
			break
		}
		manifest.State = state
		if state == ManifestFinalizing {
			manifest.EvidencePath = evidencePath
			manifest.EvidenceSHA256 = strings.Repeat("a", 64)
		}
		writeAndExpect(t, path, manifest)
		if state == target {
			break
		}
	}
	return manifest
}

func writeAndExpect(t *testing.T, path string, manifest RuntimeManifest) {
	t.Helper()
	if err := WriteRuntimeManifestAtomic(path, manifest); err != nil {
		t.Fatalf("WriteRuntimeManifestAtomic(%s) error = %v", manifest.State, err)
	}
	got, err := ReadRuntimeManifest(path)
	if err != nil {
		t.Fatalf("ReadRuntimeManifest() error = %v", err)
	}
	if !reflect.DeepEqual(got, manifest) {
		t.Fatalf("manifest round trip mismatch:\n got: %#v\nwant: %#v", got, manifest)
	}
}

func validRuntimeManifest(manifestPath, cacheRoot, tmpRoot, evidenceRoot string) RuntimeManifest {
	created := time.Date(2026, time.August, 10, 17, 0, 0, 0, time.UTC)
	return RuntimeManifest{
		SchemaVersion: 1,
		Identity: RuntimeIdentity{
			TaskID: "task-1", RunID: "run-1", BeadID: "bead-1", WorkerID: "worker-1",
			AssignmentID: 7, Generation: 3,
			Process:   ProcessIdentity{PID: 42, StartMarker: "linux:12345", Executable: "/usr/local/bin/oro", ProcessGroup: 42},
			CreatedAt: created, RetainUntil: created.Add(time.Hour),
		},
		ReservationID: "reservation-1", LeaseID: "lease-1", ManifestPath: manifestPath,
		Roots: []ManagedRoot{
			{Path: cacheRoot, Class: RootCache, Disposition: RootDisposable},
			{Path: tmpRoot, Class: RootTemp, Disposition: RootDisposable},
			{Path: evidenceRoot, Class: RootEvidence, Disposition: RootDurable},
		},
		State: ManifestAllocating,
	}
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func assertNoManifestTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".runtime-manifest-*.tmp"))
	if err != nil {
		t.Fatalf("glob manifest temps: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary manifest residue: %v", matches)
	}
}
