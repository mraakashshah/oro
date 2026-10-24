package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const factoryBudgetBootstrapBytes = 2 << 10

// The same parent authority atomically advances once, bootstrap -> initialized.
// Only bootstrap proves that no reservation has returned and no later allocation
// can exist. It carries the exact first image, never permission to create a new
// identity. Initialized and legacy v1 authorities never reconstruct lost state.
type factoryBudgetBootstrap struct {
	Version    int             `json:"version"`
	RootSHA256 string          `json:"root_sha256"`
	Phase      string          `json:"phase"`
	First      json.RawMessage `json:"first"`
	Checksum   string          `json:"checksum"`
}

func (hooks factoryBudgetHooks) checkpoint(name string) error {
	if hooks.bootstrap != nil {
		return hooks.bootstrap(name)
	}
	return nil
}

func (access *factoryBudgetAccess) persist(ctx context.Context, ledger factoryBudgetSnapshot, initialized bool, hooks factoryBudgetHooks) error {
	if initialized {
		return access.publish(ctx, ledger, hooks)
	}
	first, err := encodeFactoryBudgetSnapshot(ledger)
	if err != nil {
		return err
	}
	authority := factoryBudgetBootstrap{Version: 2, RootSHA256: access.rootHash, Phase: "bootstrap", First: first}
	if err := access.publishBootstrapAuthority(ctx, authority, hooks); err != nil {
		return err
	}
	return access.recoverFactoryBudgetBootstrap(ctx, authority, hooks)
}

func (access *factoryBudgetAccess) recoverParentAuthority(ctx context.Context, marker []byte, hooks factoryBudgetHooks) error {
	// The legacy v1 marker never proves an unfinished first publication.
	if string(marker) == access.markerContents() {
		return nil
	}
	authority, err := decodeFactoryBudgetBootstrap(marker, access.rootHash)
	if err != nil {
		return err
	}
	if authority.Phase != "bootstrap" {
		return nil
	}
	return access.recoverFactoryBudgetBootstrap(ctx, authority, hooks)
}

func decodeFactoryBudgetBootstrap(data []byte, rootHash string) (factoryBudgetBootstrap, error) {
	var authority factoryBudgetBootstrap
	if len(data) > factoryBudgetBootstrapBytes {
		return authority, errors.New("host bootstrap authority exceeds its bound")
	}
	if err := json.Unmarshal(data, &authority); err != nil {
		return authority, fmt.Errorf("decode host bootstrap authority: %w", err)
	}
	canonical, err := encodeFactoryBudgetBootstrap(authority)
	if err != nil {
		return authority, err
	}
	if authority.RootSHA256 != rootHash || !bytes.Equal(data, canonical) {
		return authority, errors.New("host bootstrap authority identity or checksum is corrupt")
	}
	return authority, nil
}

func encodeFactoryBudgetBootstrap(authority factoryBudgetBootstrap) ([]byte, error) {
	if authority.Version != 2 || (authority.Phase != "bootstrap" && authority.Phase != "initialized") || len(authority.First) > factoryBudgetBootstrapBytes {
		return nil, errors.New("invalid bounded host bootstrap authority")
	}
	first, err := decodeFactoryBudgetSnapshot(authority.First, authority.RootSHA256)
	if err != nil {
		return nil, err
	}
	if first.Revision != 1 || len(first.Entries) != 1 || first.Entries[0].Released {
		return nil, errors.New("host bootstrap requires exactly the original first reservation")
	}
	authority.Checksum = ""
	unsigned, err := json.Marshal(authority)
	if err != nil {
		return nil, fmt.Errorf("encode host bootstrap identity: %w", err)
	}
	authority.Checksum = factoryBudgetHash(unsigned)
	encoded, err := json.Marshal(authority)
	if err != nil {
		return nil, fmt.Errorf("encode host bootstrap authority: %w", err)
	}
	if len(encoded) > factoryBudgetBootstrapBytes {
		return nil, errors.New("host bootstrap authority exceeds its bound")
	}
	return encoded, nil
}

func (access *factoryBudgetAccess) bootstrapStage(phase string) string {
	return access.markerName + "." + phase + "-stage"
}

func (access *factoryBudgetAccess) prepareFreshBootstrap() error {
	if err := access.requireEmptyRoot(); err != nil {
		return err
	}
	// A phase-transition stage without its committed predecessor is evidence of
	// lost authority, even when someone also removed the root. It is not fresh.
	if _, err := access.parent.Lstat(access.bootstrapStage("initialized")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("host bootstrap transition survived missing parent authority; refusing reset")
	}
	return retireFactoryBudgetNamedStage(access.parent, access.bootstrapStage("bootstrap"), factoryBudgetBootstrapBytes)
}

func (access *factoryBudgetAccess) publishBootstrapAuthority(ctx context.Context, authority factoryBudgetBootstrap, hooks factoryBudgetHooks) error {
	encoded, err := encodeFactoryBudgetBootstrap(authority)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish host bootstrap canceled: %w", err)
	}
	name := access.bootstrapStage(authority.Phase)
	stage, err := openFactoryBudgetFile(access.parent, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer stage.Close()
	if err := writeFactoryBudgetFile(stage, encoded); err != nil {
		return err
	}
	if authority.Phase == "initialized" {
		if err := hooks.checkpoint("phase-stage"); err != nil {
			return err
		}
	} else if err := hooks.checkpoint("bootstrap-stage"); err != nil {
		return err
	}
	if err := access.parent.Rename(name, access.markerName); err != nil {
		return fmt.Errorf("publish host bootstrap phase: %w", err)
	}
	if authority.Phase == "initialized" {
		if err := hooks.checkpoint("phase-rename"); err != nil {
			return err
		}
	} else if err := hooks.checkpoint("bootstrap-rename"); err != nil {
		return err
	}
	if err := syncFactoryBootstrapDirectory(access.parent, hooks.syncParent); err != nil {
		return err
	}
	return hooks.checkpoint(authority.Phase)
}

func (access *factoryBudgetAccess) recoverFactoryBudgetBootstrap(ctx context.Context, authority factoryBudgetBootstrap, hooks factoryBudgetHooks) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("recover host bootstrap canceled: %w", err)
	}
	// A predecessor may have died after publishing bootstrap but before syncing
	// its directory. Make that authority durable before creating dependent state.
	if err := syncFactoryBootstrapDirectory(access.parent, hooks.syncParent); err != nil {
		return err
	}
	if err := access.prepareBootstrapDirectories(hooks); err != nil {
		return err
	}
	if err := access.materializeBootstrapImage(ctx, authority, hooks); err != nil {
		return err
	}
	for _, phase := range []string{"bootstrap", "initialized"} {
		if err := retireFactoryBudgetNamedStage(access.parent, access.bootstrapStage(phase), factoryBudgetBootstrapBytes); err != nil {
			return err
		}
	}
	authority.Phase = "initialized"
	if err := access.publishBootstrapAuthority(ctx, authority, hooks); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("finish host bootstrap canceled: %w", err)
	}
	return nil
}

func (access *factoryBudgetAccess) prepareBootstrapDirectories(hooks factoryBudgetHooks) error {
	if err := access.parent.Mkdir(access.rootName, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create host bootstrap root: %w", err)
	}
	if err := requireFactoryBootstrapDirectory(access.parent, access.rootName); err != nil {
		return err
	}
	if err := hooks.checkpoint("root"); err != nil {
		return err
	}
	root, err := access.parent.OpenRoot(access.rootName)
	if err != nil {
		return fmt.Errorf("open host bootstrap root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := checkFactoryBootstrapNames(root, []string{".managed"}); err != nil {
		return err
	}
	if err := root.Mkdir(".managed", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create host bootstrap metadata: %w", err)
	}
	if err := requireFactoryBootstrapDirectory(root, ".managed"); err != nil {
		return err
	}
	if err := hooks.checkpoint("metadata"); err != nil {
		return err
	}
	if err := syncFactoryBudgetDirectory(root); err != nil {
		return err
	}
	return syncFactoryBudgetDirectory(access.parent)
}

func requireFactoryBootstrapDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect host bootstrap directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("host bootstrap directory cannot be an alias or file")
	}
	return nil
}

func checkFactoryBootstrapNames(root *os.Root, allowed []string) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("inspect host bootstrap contents: %w", err)
	}
	defer directory.Close()
	names, err := directory.Readdirnames(len(allowed) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read host bootstrap contents: %w", err)
	}
	for _, name := range names {
		found := false
		for _, expected := range allowed {
			found = found || name == expected
		}
		if !found {
			return errors.New("unexpected state in unfinished host bootstrap; refusing reconstruction")
		}
	}
	return nil
}

func (access *factoryBudgetAccess) materializeBootstrapImage(ctx context.Context, authority factoryBudgetBootstrap, hooks factoryBudgetHooks) error {
	directory, err := access.openMetadata()
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if err := checkFactoryBootstrapNames(directory, []string{factoryBudgetSnapshotName, factoryBudgetPendingName}); err != nil {
		return err
	}
	data, err := readFactoryBudgetFile(directory, factoryBudgetSnapshotName, factoryBudgetSnapshotBytes)
	if err == nil && !bytes.Equal(data, authority.First) {
		return errors.New("host bootstrap conflicts with committed first image; refusing replacement")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A validated bootstrap record is positive authority for this one image.
	// Only here can an interrupted initial ledger stage be retired without a
	// committed ledger; sealed authorities still require a valid committed image.
	if stageErr := retireFactoryBudgetStage(directory); stageErr != nil {
		return stageErr
	}
	if err == nil {
		// A predecessor may have renamed the exact image without syncing its
		// directory. Never seal initialization until that name is durable.
		return syncFactoryBootstrapDirectory(directory, hooks.syncImage)
	}
	first, err := decodeFactoryBudgetSnapshot(authority.First, access.rootHash)
	if err != nil {
		return err
	}
	return access.publish(ctx, first, hooks)
}

func syncFactoryBootstrapDirectory(directory *os.Root, syncDirectory func(*os.Root) error) error {
	if syncDirectory != nil {
		return syncDirectory(directory)
	}
	return syncFactoryBudgetDirectory(directory)
}
