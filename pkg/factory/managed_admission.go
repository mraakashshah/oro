package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

const (
	managedExecutableMaxBytes = 512 << 20
	managedManifestMaxBytes   = 2 << 20
)

type managedFrozenProgram struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

type managedManifest struct {
	Spec        ManagedSpec                     `json:"spec"`
	PolicyHash  string                          `json:"policy_hash"`
	Programs    map[string]managedFrozenProgram `json:"programs"`
	Reservation HostReservation                 `json:"reservation"`
	CreatedAt   time.Time                       `json:"created_at"`
	Deadline    time.Time                       `json:"deadline"`
	HardStop    time.Time                       `json:"hard_stop"`
}

type managedAdmissionHooks struct {
	afterMarker  func() error
	beforeCommit func(*sql.Tx) error
}

// AdmitManaged freezes a managed objective before any supervised execution.
func AdmitManaged(ctx context.Context, stateDir string, spec ManagedSpec, reservation HostReservation) (Status, error) {
	return admitManaged(ctx, stateDir, spec, reservation, managedAdmissionHooks{})
}

func admitManaged(ctx context.Context, stateDir string, spec ManagedSpec, reservation HostReservation, hooks managedAdmissionHooks) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, fmt.Errorf("managed admission canceled: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	prepared, digest, err := PrepareManagedSpecIdentity(spec)
	if err != nil {
		return Status{}, err
	}
	if reservation.SpecSHA256 != digest {
		return Status{}, errors.New("host reservation specification identity mismatch")
	}
	spec = prepared
	root, spec, err := canonicalManagedPaths(stateDir, spec)
	if err != nil {
		return Status{}, err
	}
	if err := validateHostReservation(reservation, spec, root); err != nil {
		return Status{}, err
	}
	release, err := lockManagedSupervisor(root, spec.ID)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = release() }()
	manifest, err := ensureManagedMarker(ctx, root, spec, reservation)
	if err != nil {
		return Status{}, err
	}
	if hooks.afterMarker != nil {
		if err := hooks.afterMarker(); err != nil {
			return Status{}, err
		}
	}
	return finishManagedAdmission(ctx, root, manifest, hooks)
}

func canonicalManagedPaths(stateDir string, spec ManagedSpec) (string, ManagedSpec, error) {
	if err := validateManagedSpec(spec); err != nil {
		return "", spec, err
	}
	root, err := resolveAncestors(stateDir)
	if err != nil {
		return "", spec, err
	}
	spec.Repository, err = filepath.EvalSymlinks(spec.Repository)
	if err != nil {
		return "", spec, fmt.Errorf("resolve managed repository: %w", err)
	}
	if containedPath(spec.Repository, root) {
		return "", spec, errors.New("managed state must be outside the repository")
	}
	for _, program := range []*string{&spec.Runtime.ModelExecutable, &spec.Runtime.GitExecutable} {
		*program, err = resolveAncestors(*program)
		if err != nil {
			return "", spec, err
		}
	}
	return root, spec, validateManagedSpec(spec)
}

// lockManagedSupervisor is shared by admission, legacy Run, supervision and stop.
// Its metadata precedes the objective directory, with supervisor-before-objective
// lock ordering. A legacy controller therefore cannot race marker publication.
func lockManagedSupervisor(stateDir, id string) (func() error, error) {
	root, err := resolveAncestors(stateDir)
	if err != nil {
		return nil, err
	}
	if !managedIdentifier(id) {
		return nil, errors.New("invalid managed supervisor objective ID")
	}
	lockDir := filepath.Join(root, ".managed", "locks", id)
	actual, err := resolveAncestors(lockDir)
	if err != nil || actual != lockDir {
		return nil, errors.New("managed supervisor metadata must not traverse symlinks")
	}
	if err := managedRegularOrAbsent(filepath.Join(lockDir, "objective.lock")); err != nil {
		return nil, err
	}
	return lockObjective(lockDir)
}

func lockLegacySupervisor(objectiveDir, id string) (func() error, error) {
	if filepath.Base(objectiveDir) != id {
		return nil, errors.New("canonical objective directory must retain the admitted ID")
	}
	// Use the resolved objective's authority namespace, including when a caller
	// reaches it through an objective-directory symlink under another state root.
	canonicalStateDir := filepath.Dir(objectiveDir)
	release, err := lockManagedSupervisor(canonicalStateDir, id)
	if err != nil {
		return nil, err
	}
	if err := rejectManagedLegacy(canonicalStateDir, id); err != nil {
		_ = release()
		return nil, err
	}
	return release, nil
}

func rejectManagedLegacy(stateDir, id string) error {
	root, err := resolveAncestors(stateDir)
	if err != nil {
		return err
	}
	_, err = os.Lstat(filepath.Join(root, ".managed", id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed admission marker: %w", err)
	}
	return errors.New("managed objective cannot be executed by legacy Run")
}

func ensureManagedMarker(ctx context.Context, root string, spec ManagedSpec, reservation HostReservation) (managedManifest, error) {
	manifest, err := readManagedMarker(root, spec.ID)
	if err == nil {
		if !reflect.DeepEqual(manifest.Spec, spec) || manifest.Reservation != reservation {
			return managedManifest{}, errors.New("managed specification or reservation changed since admission")
		}
		return manifest, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return managedManifest{}, err
	}
	if _, err := os.Lstat(filepath.Join(root, spec.ID)); !os.IsNotExist(err) {
		return managedManifest{}, errors.New("existing objective without managed marker cannot be adopted")
	}
	manifest, err = newManagedManifest(ctx, spec, reservation)
	if err != nil {
		return managedManifest{}, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return managedManifest{}, fmt.Errorf("encode managed marker: %w", err)
	}
	if err := publishManagedBytes(filepath.Join(root, ".managed", spec.ID+".json"), data); err != nil {
		return managedManifest{}, err
	}
	return manifest, nil
}

func newManagedManifest(ctx context.Context, spec ManagedSpec, reservation HostReservation) (managedManifest, error) {
	manifest := managedManifest{Spec: spec, Reservation: reservation, Programs: make(map[string]managedFrozenProgram)}
	digests := make(map[string]string)
	var retained int64
	for name, path := range managedProgramPaths(spec) {
		program, err := hashManagedProgram(ctx, path, managedExecutableMaxBytes, true)
		if err != nil {
			return managedManifest{}, fmt.Errorf("inspect managed %s executable: %w", name, err)
		}
		manifest.Programs[name], digests[name] = program, program.Digest
		retained += program.Bytes
	}
	if retained+managedManifestMaxBytes > reservation.Bytes {
		return managedManifest{}, errors.New("host reservation cannot retain frozen executables and manifest")
	}
	if err := validateManagedRepository(ctx, spec); err != nil {
		return managedManifest{}, err
	}
	hash, err := managedPolicyHash(spec, digests)
	if err != nil {
		return managedManifest{}, err
	}
	manifest.PolicyHash, manifest.CreatedAt = hash, time.Now().UTC()
	manifest.Deadline = manifest.CreatedAt.Add(time.Duration(spec.TimeoutSeconds) * time.Second)
	manifest.HardStop = manifest.Deadline.Add(time.Duration(spec.Recovery.CleanupTimeoutSeconds+spec.Recovery.ReconciliationSeconds) * time.Second)
	return manifest, nil
}

func managedProgramPaths(spec ManagedSpec) map[string]string {
	return map[string]string{"model": spec.Runtime.ModelExecutable, "git": spec.Runtime.GitExecutable}
}

func validateManagedRepository(ctx context.Context, spec ManagedSpec) error {
	root, err := managedAdmissionGit(ctx, spec, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || actual != spec.Repository {
		return errors.New("managed repository must be the canonical Git working-tree root")
	}
	ref := "refs/heads/" + spec.TargetBranch
	if _, err := managedAdmissionGit(ctx, spec, "rev-parse", "--verify", ref+"^{commit}"); err != nil {
		return err
	}
	worktrees, err := managedAdmissionGit(ctx, spec, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(worktrees, "\x00") {
		if field == "branch "+ref {
			return &targetCheckedOutError{branch: spec.TargetBranch}
		}
	}
	return nil
}

func managedAdmissionGit(ctx context.Context, spec ManagedSpec, args ...string) (string, error) {
	argv := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, spec.Runtime.GitExecutable, argv...) //nolint:gosec // Admission resolves and hashes this external Git executable; callers supply fixed read-only commands and literal arguments.
	cmd.Dir, cmd.Env = spec.Repository, gitEnvironment()
	result := runProcess(ctx, cmd)
	if result.Err != nil {
		return "", fmt.Errorf("read-only managed Git admission: %w", result.Err)
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func finishManagedAdmission(ctx context.Context, root string, manifest managedManifest, hooks managedAdmissionHooks) (Status, error) {
	dir := filepath.Join(root, manifest.Spec.ID)
	if err := guardManagedObjective(dir); err != nil {
		return Status{}, err
	}
	release, err := lockObjective(dir)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = release() }()
	if err := freezeManagedManifest(ctx, dir, manifest); err != nil {
		return Status{}, err
	}
	store, err := openManagedFactoryStore(ctx, dir)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = store.close() }()
	if err := initManagedSchema(ctx, store.conn); err != nil {
		return Status{}, err
	}
	return commitManagedAdmission(ctx, store, manifest, hooks)
}

func guardManagedObjective(dir string) error {
	actual, err := resolveAncestors(dir)
	if err != nil || actual != dir {
		return errors.New("managed objective directory must not traverse symlinks")
	}
	for _, name := range []string{"objective.lock", "state.init", "state.db", "state.db-journal", "state.db-wal", "state.db-shm"} {
		if err := managedRegularOrAbsent(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func managedRegularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed state path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed state file must be regular and cannot be a symlink")
	}
	return nil
}

func freezeManagedManifest(ctx context.Context, dir string, manifest managedManifest) error {
	for name, path := range managedProgramPaths(manifest.Spec) {
		identity := manifest.Programs[name]
		destination := filepath.Join(dir, name)
		if _, err := os.Lstat(destination); err == nil {
			if err := verifyManagedArtifact(ctx, destination, identity, 0o500); err != nil {
				return err
			}
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect frozen executable: %w", err)
		}
		if err := freezeManagedProgram(ctx, path, destination, identity); err != nil {
			return err
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode frozen managed manifest: %w", err)
	}
	return publishManagedBytes(filepath.Join(dir, "managed.json"), data)
}

func freezeManagedProgram(ctx context.Context, source, destination string, identity managedFrozenProgram) error {
	file, err := openManagedRegular(source, managedExecutableMaxBytes, true)
	if err != nil {
		return err
	}
	defer file.Close()
	return publishManagedArtifact(ctx, destination, managedContextReader{ctx: ctx, reader: file}, identity, 0o500)
}

func publishManagedBytes(path string, data []byte) error {
	if len(data) > managedManifestMaxBytes {
		return errors.New("managed manifest exceeds its artifact bound")
	}
	identity := managedFrozenProgram{Digest: contentHash(data), Bytes: int64(len(data))}
	return publishManagedArtifact(context.Background(), path, bytes.NewReader(data), identity, 0o400)
}

func publishManagedArtifact(ctx context.Context, path string, reader io.Reader, identity managedFrozenProgram, mode os.FileMode) error {
	if _, err := os.Lstat(path); err == nil {
		return verifyManagedArtifact(ctx, path, identity, mode)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect managed artifact: %w", err)
	}
	directory, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open managed artifact directory: %w", err)
	}
	defer directory.Close()
	// One exclusive staging name bounds retained bytes even if the publisher
	// dies before link/cleanup. An interrupted unpublished copy is never replaced.
	pending := ".pending-" + filepath.Base(path)
	file, err := directory.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create managed artifact staging file (an interrupted publisher may require recovery): %w", err)
	}
	defer func() { _ = directory.Remove(pending) }()
	defer file.Close()
	if err := writeManagedArtifact(file, reader, identity, mode); err != nil {
		return err
	}
	if err := directory.Link(pending, filepath.Base(path)); err != nil && !os.IsExist(err) {
		return fmt.Errorf("publish immutable managed artifact: %w", err)
	}
	if err := syncManagedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return verifyManagedArtifact(ctx, path, identity, mode)
}

func writeManagedArtifact(file *os.File, reader io.Reader, identity managedFrozenProgram, mode os.FileMode) error {
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, identity.Bytes))
	if err != nil {
		return fmt.Errorf("copy frozen managed artifact: %w", err)
	}
	var probe [1]byte
	extra, readErr := io.ReadFull(reader, probe[:])
	if written != identity.Bytes || extra != 0 || !errors.Is(readErr, io.EOF) || hex.EncodeToString(hash.Sum(nil)) != identity.Digest {
		return errors.New("managed artifact changed while freezing")
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("protect managed artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync managed artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close managed artifact: %w", err)
	}
	return nil
}

func syncManagedDirectory(path string) error {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open managed artifact parent: %w", err)
	}
	defer parent.Close()
	directory, err := parent.Open(filepath.Base(path))
	if err != nil {
		return fmt.Errorf("open managed artifact directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync managed artifact directory: %w", err)
	}
	return nil
}

func openManagedRegular(path string, limit int64, executable bool) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open managed file parent: %w", err)
	}
	defer root.Close()
	return openManagedRootRegular(root, filepath.Base(path), limit, executable)
}

type managedRegularRoot interface {
	Lstat(string) (os.FileInfo, error)
	OpenFile(string, int, os.FileMode) (*os.File, error)
}

func openManagedRootRegular(root managedRegularRoot, name string, limit int64, executable bool) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect managed file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("managed file must be regular and cannot be a symlink")
	}
	// os.Root may resolve contained symlinks despite O_NOFOLLOW. Bind the regular
	// leaf before and after opening; NONBLOCK also prevents substituted FIFO hangs.
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		// Only the initial Lstat may report absence to initialization callers.
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("managed file disappeared after inspection")
		}
		return nil, fmt.Errorf("open managed file: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > limit || (executable && (info.Size() == 0 || info.Mode().Perm()&0o111 == 0)) {
		_ = file.Close()
		return nil, errors.New("managed file must retain its inspected inode, byte bound and required executable permission")
	}
	after, err := root.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) {
		_ = file.Close()
		return nil, errors.New("managed file path changed after opening")
	}
	return file, nil
}

type managedContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader managedContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, fmt.Errorf("managed artifact read canceled: %w", err)
	}
	count, err := reader.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		return count, io.EOF
	}
	if err != nil {
		return count, fmt.Errorf("read managed artifact: %w", err)
	}
	return count, nil
}

func hashManagedProgram(ctx context.Context, path string, limit int64, executable bool) (managedFrozenProgram, error) {
	file, err := openManagedRegular(path, limit, executable)
	if err != nil {
		return managedFrozenProgram{}, err
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(managedContextReader{ctx: ctx, reader: file}, limit+1))
	if err != nil {
		return managedFrozenProgram{}, fmt.Errorf("hash managed artifact: %w", err)
	}
	if count > limit {
		return managedFrozenProgram{}, errors.New("managed artifact exceeds byte limit")
	}
	return managedFrozenProgram{Digest: hex.EncodeToString(hash.Sum(nil)), Bytes: count}, nil
}

func verifyManagedArtifact(ctx context.Context, path string, identity managedFrozenProgram, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect immutable managed artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Size() != identity.Bytes {
		return errors.New("immutable managed artifact metadata changed")
	}
	actual, err := hashManagedProgram(ctx, path, identity.Bytes, mode&0o111 != 0)
	if err != nil {
		return err
	}
	if actual != identity {
		return errors.New("immutable managed artifact identity changed")
	}
	return nil
}

func readManagedMarker(root, id string) (managedManifest, error) {
	var manifest managedManifest
	file, err := openManagedRegular(filepath.Join(root, ".managed", id+".json"), managedManifestMaxBytes, false)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return manifest, fmt.Errorf("inspect managed marker mode: %w", err)
	}
	if info.Mode().Perm() != 0o400 {
		return manifest, errors.New("managed authority marker must retain read-only permissions")
	}
	data, err := io.ReadAll(io.LimitReader(file, managedManifestMaxBytes+1))
	if err != nil {
		return manifest, fmt.Errorf("read managed marker: %w", err)
	}
	if len(data) > managedManifestMaxBytes {
		return manifest, errors.New("managed marker exceeds artifact limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("decode managed marker: %w", err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, data) {
		return manifest, errors.New("managed marker is not its immutable canonical encoding")
	}
	return manifest, validateManagedManifest(root, id, manifest)
}

func validateManagedManifest(root, id string, manifest managedManifest) error {
	if err := validateManagedSpec(manifest.Spec); err != nil {
		return err
	}
	if manifest.Spec.ID != id || manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC {
		return errors.New("managed marker objective or creation identity is invalid")
	}
	if err := validateHostReservation(manifest.Reservation, manifest.Spec, root); err != nil {
		return err
	}
	deadline := manifest.CreatedAt.Add(time.Duration(manifest.Spec.TimeoutSeconds) * time.Second)
	hardStop := deadline.Add(time.Duration(manifest.Spec.Recovery.CleanupTimeoutSeconds+manifest.Spec.Recovery.ReconciliationSeconds) * time.Second)
	if !manifest.Deadline.Equal(deadline) || !manifest.HardStop.Equal(hardStop) || len(manifest.Programs) != 2 {
		return errors.New("managed marker lifetime or program identity is invalid")
	}
	digests := make(map[string]string)
	for _, name := range []string{"model", "git"} {
		program := manifest.Programs[name]
		if program.Bytes <= 0 || program.Bytes > managedExecutableMaxBytes {
			return errors.New("managed executable size is outside its bound")
		}
		digests[name] = program.Digest
	}
	hash, err := managedPolicyHash(manifest.Spec, digests)
	if err != nil || hash != manifest.PolicyHash {
		return errors.New("managed marker policy identity mismatch")
	}
	return nil
}

// loadManagedManifest uses only frozen admission artifacts, never the input
// specification file or original executable paths. Controllers still obtain
// current authority from the journal before any execution.
func loadManagedManifest(ctx context.Context, stateDir, id string) (managedManifest, error) {
	if err := ctx.Err(); err != nil {
		return managedManifest{}, fmt.Errorf("load managed artifacts canceled: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	root, err := resolveAncestors(stateDir)
	if err != nil || !managedIdentifier(id) {
		return managedManifest{}, errors.New("invalid managed artifact root or objective")
	}
	manifest, err := readManagedMarker(root, id)
	if err != nil {
		return managedManifest{}, err
	}
	dir := filepath.Join(root, id)
	if err := guardManagedObjective(dir); err != nil {
		return managedManifest{}, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return managedManifest{}, fmt.Errorf("encode managed artifact identity: %w", err)
	}
	identity := managedFrozenProgram{Digest: contentHash(data), Bytes: int64(len(data))}
	if err := verifyManagedArtifact(ctx, filepath.Join(dir, "managed.json"), identity, 0o400); err != nil {
		return managedManifest{}, err
	}
	for name, program := range manifest.Programs {
		if err := verifyManagedArtifact(ctx, filepath.Join(dir, name), program, 0o500); err != nil {
			return managedManifest{}, err
		}
	}
	return manifest, nil
}

func commitManagedAdmission(ctx context.Context, store *factoryStore, manifest managedManifest, hooks managedAdmissionHooks) (Status, error) {
	state, err := store.load(ctx)
	if err == nil {
		return existingManagedAdmission(ctx, store, manifest, state)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Status{}, err
	}
	state = snapshot{
		Spec: managedSnapshotSpec(manifest.Spec), PolicyHash: manifest.PolicyHash,
		CreatedAt: manifest.CreatedAt, Deadline: manifest.Deadline, Revision: 1, State: "pending",
	}
	body, err := json.Marshal(state)
	if err != nil {
		return Status{}, fmt.Errorf("encode managed snapshot: %w", err)
	}
	tx, err := store.conn.BeginTx(ctx, nil)
	if err != nil {
		return Status{}, fmt.Errorf("begin managed admission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeSnapshot(ctx, tx, body, 0); err != nil {
		return Status{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events(revision, state, body) VALUES(1, 'pending', ?)", body); err != nil {
		return Status{}, fmt.Errorf("record managed admission event: %w", err)
	}
	if err := insertManagedAdmission(ctx, tx, managedJournalIdentity(manifest)); err != nil {
		return Status{}, err
	}
	if hooks.beforeCommit != nil {
		if err := hooks.beforeCommit(tx); err != nil {
			return Status{}, err
		}
	}
	if err := commitManagedTransaction(store.conn, tx); err != nil {
		return Status{}, fmt.Errorf("commit managed admission: %w", err)
	}
	return snapshotStatus(state), nil
}

func existingManagedAdmission(ctx context.Context, store *factoryStore, manifest managedManifest, state snapshot) (Status, error) {
	if state.PolicyHash != manifest.PolicyHash || !reflect.DeepEqual(state.Spec, managedSnapshotSpec(manifest.Spec)) ||
		!state.CreatedAt.Equal(manifest.CreatedAt) || !state.Deadline.Equal(manifest.Deadline) {
		return Status{}, errors.New("managed snapshot identity differs from frozen admission")
	}
	var body []byte
	if err := store.conn.QueryRowContext(ctx, "SELECT body FROM managed_admissions WHERE id=1").Scan(&body); err != nil {
		return Status{}, fmt.Errorf("read managed admission identity: %w", err)
	}
	var admission managedJournalAdmission
	if err := json.Unmarshal(body, &admission); err != nil {
		return Status{}, fmt.Errorf("decode managed admission identity: %w", err)
	}
	if !reflect.DeepEqual(admission, managedJournalIdentity(manifest)) {
		return Status{}, errors.New("managed journal identity differs from frozen admission")
	}
	if _, err := loadManagedActivation(ctx, store.conn, body, admission.Reservation.StateRoot, admission.ObjectiveID); err != nil {
		return Status{}, err
	}
	return snapshotStatus(state), nil
}

func managedSnapshotSpec(spec ManagedSpec) Spec {
	return Spec{
		ID: spec.ID, Objective: spec.Objective, Repository: spec.Repository, TargetBranch: spec.TargetBranch,
		RequiredChecks: append([]string(nil), spec.RequiredChecks...), MaxAttempts: spec.MaxAttempts,
		TimeoutSeconds: spec.TimeoutSeconds, StepTimeoutSeconds: spec.StepTimeoutSeconds,
	}
}

func managedJournalIdentity(manifest managedManifest) managedJournalAdmission {
	return managedJournalAdmission{
		ObjectiveID: manifest.Spec.ID, ManifestHash: manifest.PolicyHash,
		Deadline: manifest.Deadline, HardStop: manifest.HardStop, Recovery: manifest.Spec.Recovery,
		Runtime: manifest.Spec.Runtime, Reservation: manifest.Reservation,
	}
}
