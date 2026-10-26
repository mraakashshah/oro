package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"oro/pkg/factory"
)

const (
	factoryBudgetMaxEntries    = 4096
	factoryBudgetSnapshotBytes = 8 << 20
	factoryBudgetSnapshotName  = "host-reservations.json"
	factoryBudgetPendingName   = ".pending-host-reservations.json"
)

// A root has at most one committed and one staged 8 MiB image. The shared
// 32 MiB allowance covers both, parent marker/lock and filesystem metadata.
// Rows are never removed, so released objective IDs cannot reset their budget.
type factoryBudgetSnapshot struct {
	Version    int                  `json:"version"`
	RootSHA256 string               `json:"root_sha256"`
	Revision   int64                `json:"revision"`
	Entries    []factoryBudgetEntry `json:"entries"`
	Checksum   string               `json:"checksum"`
}

type factoryBudgetEntry struct {
	ID           string `json:"id"`
	ObjectiveID  string `json:"objective_id"`
	Bytes        int64  `json:"bytes"`
	ReserveBytes int64  `json:"reserve_bytes"`
	SpecSHA256   string `json:"spec_sha256"`
	Released     bool   `json:"released"`
}

func (entry factoryBudgetEntry) reservation(root string) factory.HostReservation {
	return factory.HostReservation{ID: entry.ID, StateRoot: root, ObjectiveID: entry.ObjectiveID, Bytes: entry.Bytes, SpecSHA256: entry.SpecSHA256}
}

func (ledger factoryBudgetSnapshot) objective(id string) (factoryBudgetEntry, bool) {
	for _, entry := range ledger.Entries {
		if entry.ObjectiveID == id {
			return entry, true
		}
	}
	return factoryBudgetEntry{}, false
}

type factoryBudgetHooks struct {
	beforeRename, afterRename func() error
	bootstrap                 func(string) error
	syncParent, syncImage     func(*os.Root) error
}

type factoryBudgetAccess struct {
	rootPath, rootName, rootHash, markerName string
	parent                                   *os.Root
	lock                                     *os.File
}

func lockFactoryBudget(ctx context.Context, path string) (*factoryBudgetAccess, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("host budget context: %w", err)
	}
	canonical, err := canonicalFactoryBudgetRoot(path)
	if err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(canonical))
	if err != nil {
		return nil, fmt.Errorf("open host budget parent: %w", err)
	}
	access := &factoryBudgetAccess{rootPath: canonical, rootName: filepath.Base(canonical), rootHash: factoryBudgetHash([]byte(canonical)), parent: parent}
	prefix := ".oro-factory-budget-" + access.rootHash
	access.markerName = prefix + ".init"
	// Exclusive creation distinguishes the creator from contenders. Concurrent
	// nonexclusive os.Root creation can return ENOENT on Darwin despite the file
	// already existing; reopen only a positively observed EEXIST, without reset.
	lock, err := openFactoryBudgetFile(parent, prefix+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		lock, err = openFactoryBudgetFile(parent, prefix+".lock", os.O_RDWR, 0)
	}
	if err != nil {
		access.close()
		return nil, err
	}
	access.lock = lock
	if err := waitFactoryBudgetLock(ctx, lock); err != nil {
		access.close()
		return nil, err
	}
	return access, nil
}

func canonicalFactoryBudgetRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return "", errors.New("host budget root must be a clean absolute non-root path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err == nil {
		return canonical, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve host budget root: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("resolve existing host budget parent: %w", err)
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func waitFactoryBudgetLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait host budget lock: %w", err)
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock host budget: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait host budget lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (access *factoryBudgetAccess) close() {
	if access.lock != nil {
		_ = access.lock.Close()
	}
	if access.parent != nil {
		_ = access.parent.Close()
	}
}

func openFactoryBudgetFile(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	// os.Root confines symlinks to its directory, but does not preserve a caller's
	// O_NOFOLLOW request. A contained or dangling alias is still invalid authority.
	if err := checkFactoryBudgetLeaf(root, name); err != nil {
		return nil, err
	}
	file, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, fmt.Errorf("open host budget file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect host budget file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		_ = file.Close()
		return nil, errors.New("host budget file must be private, owned, regular and have no hardlink aliases")
	}
	entry, err := root.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() || !os.SameFile(info, entry) {
		_ = file.Close()
		return nil, errors.New("host budget file path changed or became an alias")
	}
	return file, nil
}

func checkFactoryBudgetLeaf(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect host budget path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("host budget path must be regular and cannot be an alias")
	}
	return nil
}

func readFactoryBudgetFile(root *os.Root, name string, limit int64) ([]byte, error) {
	file, err := openFactoryBudgetFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read host budget file: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("host budget file exceeds bounded size")
	}
	return data, nil
}

func (access *factoryBudgetAccess) load(ctx context.Context) (factoryBudgetSnapshot, bool, error) {
	return access.loadWithHooks(ctx, factoryBudgetHooks{})
}

func (access *factoryBudgetAccess) loadWithHooks(ctx context.Context, hooks factoryBudgetHooks) (factoryBudgetSnapshot, bool, error) {
	empty := factoryBudgetSnapshot{Version: 1, RootSHA256: access.rootHash, Entries: []factoryBudgetEntry{}}
	marker, err := readFactoryBudgetFile(access.parent, access.markerName, factoryBudgetBootstrapBytes)
	if errors.Is(err, os.ErrNotExist) {
		if err := access.prepareFreshBootstrap(); err != nil {
			return empty, false, err
		}
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if err := access.recoverParentAuthority(ctx, marker, hooks); err != nil {
		return empty, false, err
	}
	ledger, initialized, err := access.loadCommitted()
	if err != nil {
		return empty, false, err
	}
	// A predecessor may have died after phase rename but before directory sync.
	// Observing initialized is insufficient until its directory is durable too.
	if err := syncFactoryBootstrapDirectory(access.parent, hooks.syncParent); err != nil {
		return empty, false, err
	}
	return ledger, initialized, nil
}

func (access *factoryBudgetAccess) loadCommitted() (factoryBudgetSnapshot, bool, error) {
	var empty factoryBudgetSnapshot
	directory, err := access.openMetadata()
	if err != nil {
		return empty, false, err
	}
	defer func() { _ = directory.Close() }()
	data, err := readFactoryBudgetFile(directory, factoryBudgetSnapshotName, factoryBudgetSnapshotBytes)
	if err != nil {
		return empty, false, fmt.Errorf("committed host budget authority unavailable; refusing reset: %w", err)
	}
	ledger, err := decodeFactoryBudgetSnapshot(data, access.rootHash)
	if err != nil {
		return empty, false, err
	}
	// Only a validated committed image permits retiring an uncommitted stage.
	if err := retireFactoryBudgetStage(directory); err != nil {
		return empty, false, err
	}
	return ledger, true, nil
}

func (access *factoryBudgetAccess) requireEmptyRoot() error {
	info, err := access.parent.Lstat(access.rootName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect uninitialized host budget root: %w", err)
	}
	if !info.IsDir() {
		return errors.New("host budget root must be an existing directory or absent")
	}
	root, err := access.parent.Open(access.rootName)
	if err != nil {
		return fmt.Errorf("open uninitialized host budget root: %w", err)
	}
	defer root.Close()
	names, err := root.Readdirnames(1)
	if len(names) != 0 || !errors.Is(err, io.EOF) {
		return errors.New("nonempty factory root lacks host budget initialization authority; refusing adoption")
	}
	return nil
}

func (access *factoryBudgetAccess) markerContents() string {
	return "oro-factory-host-budget-v1\n" + access.rootHash + "\n"
}

func (access *factoryBudgetAccess) openMetadata() (*os.Root, error) {
	for _, name := range []string{access.rootName, filepath.Join(access.rootName, ".managed")} {
		info, err := access.parent.Lstat(name)
		if err != nil {
			return nil, fmt.Errorf("inspect host budget directory: %w", err)
		}
		if !info.IsDir() {
			return nil, errors.New("host budget authority directory cannot be a symlink")
		}
	}
	root, err := access.parent.OpenRoot(filepath.Join(access.rootName, ".managed"))
	if err != nil {
		return nil, fmt.Errorf("open host budget metadata: %w", err)
	}
	return root, nil
}

func retireFactoryBudgetStage(directory *os.Root) error {
	return retireFactoryBudgetNamedStage(directory, factoryBudgetPendingName, factoryBudgetSnapshotBytes)
}

func retireFactoryBudgetNamedStage(directory *os.Root, name string, limit int64) error {
	file, err := openFactoryBudgetFile(directory, name, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("inspect uncommitted host budget stage: %w", err)
	}
	if info.Size() > limit {
		return errors.New("uncommitted host budget stage exceeds bound")
	}
	if err := directory.Remove(name); err != nil {
		return fmt.Errorf("retire uncommitted host budget stage: %w", err)
	}
	return syncFactoryBudgetDirectory(directory)
}

func decodeFactoryBudgetSnapshot(data []byte, rootHash string) (factoryBudgetSnapshot, error) {
	var ledger factoryBudgetSnapshot
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, fmt.Errorf("decode committed host budget authority: %w", err)
	}
	encoded, err := encodeFactoryBudgetSnapshot(ledger)
	if err != nil {
		return ledger, err
	}
	if ledger.RootSHA256 != rootHash || !bytes.Equal(data, encoded) {
		return ledger, errors.New("committed host budget authority identity or checksum is corrupt")
	}
	return ledger, nil
}

func encodeFactoryBudgetSnapshot(ledger factoryBudgetSnapshot) ([]byte, error) {
	if err := validateFactoryBudgetSnapshot(ledger); err != nil {
		return nil, err
	}
	ledger.Checksum = ""
	unsigned, err := json.Marshal(ledger)
	if err != nil {
		return nil, fmt.Errorf("encode host budget authority: %w", err)
	}
	ledger.Checksum = factoryBudgetHash(unsigned)
	data, err := json.Marshal(ledger)
	if err != nil {
		return nil, fmt.Errorf("encode signed host budget authority: %w", err)
	}
	if len(data) > factoryBudgetSnapshotBytes {
		return nil, errors.New("host budget authority exceeds snapshot bound")
	}
	return data, nil
}

func validateFactoryBudgetSnapshot(ledger factoryBudgetSnapshot) error {
	if ledger.Version != 1 || !factoryBudgetHex(ledger.RootSHA256, 32) || len(ledger.Entries) == 0 || len(ledger.Entries) > factoryBudgetMaxEntries {
		return errors.New("invalid bounded host budget authority")
	}
	ids := make(map[string]bool, len(ledger.Entries))
	var revision int64
	previous := ""
	for _, entry := range ledger.Entries {
		if !factoryBudgetHex(entry.ID, 16) || !factoryBudgetHex(entry.SpecSHA256, 32) ||
			!regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(entry.ObjectiveID) || entry.Bytes <= 0 || entry.ReserveBytes <= 0 ||
			entry.ObjectiveID <= previous || ids[entry.ID] {
			return errors.New("invalid host reservation row or ordering")
		}
		previous, ids[entry.ID] = entry.ObjectiveID, true
		revision++
		if entry.Released {
			revision++
		}
	}
	if ledger.Revision != revision {
		return errors.New("host reservation revision does not match immutable history")
	}
	return nil
}

func factoryBudgetHex(value string, length int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == length && hex.EncodeToString(decoded) == value
}

func (access *factoryBudgetAccess) publish(ctx context.Context, ledger factoryBudgetSnapshot, hooks factoryBudgetHooks) error {
	data, err := encodeFactoryBudgetSnapshot(ledger)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish host reservation context: %w", err)
	}
	directory, err := access.openMetadata()
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	stage, err := openFactoryBudgetFile(directory, factoryBudgetPendingName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer stage.Close()
	if err := writeFactoryBudgetFile(stage, data); err != nil {
		return err
	}
	if hooks.beforeRename != nil {
		if err := hooks.beforeRename(); err != nil {
			return err
		}
	}
	if err := directory.Rename(factoryBudgetPendingName, factoryBudgetSnapshotName); err != nil {
		return fmt.Errorf("commit host reservation image: %w", err)
	}
	if hooks.afterRename != nil {
		if err := hooks.afterRename(); err != nil {
			return err
		}
	}
	return syncFactoryBootstrapDirectory(directory, hooks.syncImage)
}

func writeFactoryBudgetFile(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write host budget image: %w", err)
	}
	if err := file.Chmod(0o400); err != nil {
		return fmt.Errorf("freeze host budget image: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync host budget image: %w", err)
	}
	return nil
}

func syncFactoryBudgetDirectory(directory *os.Root) error {
	file, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("open host budget directory for sync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync host budget directory: %w", err)
	}
	return nil
}
