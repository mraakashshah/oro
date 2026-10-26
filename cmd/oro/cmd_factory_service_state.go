package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"oro/pkg/factory"
)

type factoryServiceAccess struct {
	root, id string
	lock     *os.File
}

func lockFactoryService(ctx context.Context, root, id string) (*factoryServiceAccess, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("service lock: %w", err)
	}
	if err := validateFactoryServiceRoot(root, id); err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("service root: %w", err)
	}
	defer func() {
		if dir != nil {
			_ = dir.Close()
		}
	}()
	dir, err = createFactoryServiceLockDirectory(dir, id)
	if err != nil {
		return nil, err
	}
	file, err := openFactoryBudgetFile(dir, "service.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		file, err = openFactoryBudgetFile(dir, "service.lock", os.O_RDWR, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("service lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		_ = file.Close()
		return nil, errors.New("invalid service lock")
	}
	if err := waitFactoryBudgetLock(ctx, file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("service lock: %w", err)
	}
	entry, err := dir.Lstat("service.lock")
	if err != nil || !os.SameFile(info, entry) {
		_ = file.Close()
		return nil, errors.New("service lock identity changed")
	}
	return &factoryServiceAccess{root: root, id: id, lock: file}, nil
}

func (access *factoryServiceAccess) Close() error {
	if err := access.lock.Close(); err != nil {
		return fmt.Errorf("close service lock: %w", err)
	}
	return nil
}

type factoryServiceIntent struct {
	Version     int                             `json:"version"`
	Root        string                          `json:"root"`
	ID          string                          `json:"id"`
	Nonce       string                          `json:"nonce"`
	Label       string                          `json:"label"`
	Runtime     factory.ManagedRuntimeReference `json:"runtime"`
	HardStop    time.Time                       `json:"hard_stop"`
	OroPath     string                          `json:"oro_path"`
	OroSHA256   string                          `json:"oro_sha256"`
	PlistSHA256 string                          `json:"plist_sha256"`
}
type factoryServiceRegistration struct {
	Version      int    `json:"version"`
	IntentSHA256 string `json:"intent_sha256"`
	Phase        string `json:"phase"`
}

func (access *factoryServiceAccess) prepare(ctx context.Context, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference) (factoryServiceIntent, factoryServiceRegistration, error) {
	var empty factoryServiceIntent
	var registration factoryServiceRegistration
	if err := access.validateAuthority(ctx, view, ref); err != nil {
		return empty, registration, err
	}
	config, err := readFactoryRuntimeConfig(ctx, ref.Path, ref.SHA256)
	if err != nil {
		return empty, registration, fmt.Errorf("service runtime: %w", err)
	}
	objective, err := os.OpenRoot(filepath.Join(access.root, access.id))
	if err != nil {
		return empty, registration, fmt.Errorf("service objective: %w", err)
	}
	defer objective.Close()
	if err := bindFactoryRuntimeRoot(objective, filepath.Join(access.root, access.id)); err != nil {
		return empty, registration, fmt.Errorf("service objective: %w", err)
	}
	intent, registration, err := readFactoryServiceBundle(objective, "service", view, ref, config.OroPath, config.OroSHA256)
	if err == nil {
		if _, stageErr := objective.Lstat(".service.stage"); !errors.Is(stageErr, os.ErrNotExist) {
			return empty, registration, errors.New("uncertain service stage alongside final bundle")
		}
		return intent, registration, nil
	}
	if _, statErr := objective.Lstat("service"); !errors.Is(statErr, os.ErrNotExist) {
		return empty, registration, fmt.Errorf("service bundle: %w", err)
	}
	intent, registration, err = access.recoverOrCreateServiceStage(objective, view, ref, config)
	if err != nil {
		return empty, registration, err
	}
	if err := ctx.Err(); err != nil {
		return empty, registration, fmt.Errorf("service publication: %w", err)
	}
	if err := objective.Rename(".service.stage", "service"); err != nil {
		return empty, registration, fmt.Errorf("service publication: %w", err)
	}
	if err := syncFactoryBudgetDirectory(objective); err != nil {
		return empty, registration, err
	}
	return intent, registration, nil
}

func validateFactoryServiceRoot(root, id string) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(id) {
		return errors.New("invalid service objective ID")
	}
	if !factoryJobAbsolutePath(root) {
		return errors.New("invalid service root")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return errors.New("aliased service root")
	}
	if err := factoryHostPrivateDirectory(root); err != nil {
		return fmt.Errorf("service root: %w", err)
	}
	return nil
}

func (access *factoryServiceAccess) validateAuthority(ctx context.Context, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference) error {
	if view.StateRoot != access.root || view.ObjectiveID != access.id || !time.Now().Before(view.Deadline) {
		return errors.New("service admission identity or deadline invalid")
	}
	current, err := factory.ReadManagedAdmission(ctx, access.root, access.id)
	if err != nil || current != view {
		return errors.New("service admission changed")
	}
	active, err := factory.ReadManagedRuntimeReference(ctx, access.root, access.id)
	if err != nil || active != ref {
		return errors.New("service active runtime changed")
	}
	if err := verifyFactoryPublishedRuntime(ctx, view, ref); err != nil {
		return fmt.Errorf("service runtime: %w", err)
	}
	return nil
}

func (access *factoryServiceAccess) createServiceStage(objective *os.Root, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, config factoryDockerCleanupConfig) (factoryServiceIntent, factoryServiceRegistration, error) {
	var empty, intent factoryServiceIntent
	var registration factoryServiceRegistration
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return empty, registration, fmt.Errorf("service nonce: %w", err)
	}
	intent = factoryServiceIntent{Version: 1, Root: access.root, ID: access.id, Nonce: hex.EncodeToString(nonce[:]), Runtime: ref, HardStop: view.HardStop, OroPath: config.OroPath, OroSHA256: config.OroSHA256}
	intent.Label = "dev.getoro.factory." + factoryHostDigest([]byte(access.root + "\x00" + access.id))[:16] + ".supervisor." + intent.Nonce
	plist, err := factoryServicePlist(intent)
	if err != nil {
		return empty, registration, err
	}
	intent.PlistSHA256 = factoryHostDigest(plist)
	data, err := json.Marshal(intent)
	if err != nil || len(data) > 16<<10 {
		return empty, registration, errors.New("invalid service intent")
	}
	registration = factoryServiceRegistration{Version: 1, IntentSHA256: factoryHostDigest(data), Phase: "prepared"}
	reg, err := json.Marshal(registration)
	if err != nil {
		return empty, registration, fmt.Errorf("service registration: %w", err)
	}
	if err := objective.Mkdir(".service.stage", 0o700); err != nil {
		return empty, registration, fmt.Errorf("service stage: %w", err)
	}
	stage, err := openFactoryRuntimeDirectory(objective, ".service.stage")
	if err != nil {
		return empty, registration, err
	}
	for name, contents := range map[string][]byte{"intent.json": data, "service.plist": plist, "registration.json": reg} {
		if err := writeFactoryPublicationFile(stage, name, contents); err != nil {
			_ = stage.Close()
			return empty, registration, err
		}
	}
	err = syncFactoryBudgetDirectory(stage)
	_ = stage.Close()
	if err != nil {
		return empty, registration, err
	}

	return intent, registration, nil
}

func (access *factoryServiceAccess) recoverOrCreateServiceStage(objective *os.Root, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, config factoryDockerCleanupConfig) (factoryServiceIntent, factoryServiceRegistration, error) {
	if _, err := objective.Lstat(".service.stage"); errors.Is(err, os.ErrNotExist) {
		return access.createServiceStage(objective, view, ref, config)
	} else if err != nil {
		return factoryServiceIntent{}, factoryServiceRegistration{}, fmt.Errorf("service stage: %w", err)
	}
	intent, registration, err := readFactoryServiceBundle(objective, ".service.stage", view, ref, config.OroPath, config.OroSHA256)
	if err != nil || registration.Phase != "prepared" {
		return factoryServiceIntent{}, factoryServiceRegistration{}, errors.New("uncertain service stage")
	}
	return intent, registration, nil
}

func createFactoryServiceLockDirectory(dir *os.Root, id string) (*os.Root, error) {
	for _, name := range []string{".managed", "locks", id} {
		if err := dir.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			_ = dir.Close()
			return nil, fmt.Errorf("service directory: %w", err)
		}
		next, err := openFactoryRuntimeDirectory(dir, name)
		if err != nil {
			_ = dir.Close()
			return nil, fmt.Errorf("service directory: %w", err)
		}
		_ = dir.Close()
		dir = next
	}
	return dir, nil
}
