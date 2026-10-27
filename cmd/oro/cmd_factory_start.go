package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"oro/pkg/factory"
	"oro/pkg/storage"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

type factoryStartDependencies struct {
	probe      storage.DiskProbe
	executable func() (string, error)
	supported  func() error
	bootstrap  func(context.Context, string) error
}

func newFactoryStartCmd() *cobra.Command {
	return buildFactoryStartCmd(factoryStartDependencies{probe: factoryStartDiskProbe, executable: os.Executable, supported: factoryServicePlatformSupported})
}

func buildFactoryStartCmd(deps factoryStartDependencies) *cobra.Command {
	var specPath, root, docker, endpoint, credential string
	cmd := &cobra.Command{Use: "start", Short: "Register a managed factory supervisor", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := deps.supported(); err != nil {
			return err
		}
		spec, err := readFactoryManagedSpec(specPath)
		if err != nil {
			return fmt.Errorf("managed specification: %w", err)
		}
		if !factoryJobAbsolutePath(root) || !factoryJobAbsolutePath(docker) || !strings.HasPrefix(endpoint, "unix://") || !factoryJobAbsolutePath(strings.TrimPrefix(endpoint, "unix://")) || strings.ContainsAny(strings.TrimPrefix(endpoint, "unix://"), " ?#%") {
			return errors.New("start requires absolute state and Docker paths and an explicit Unix endpoint")
		}
		if cmd.Flags().Changed("model-credential-file") {
			if err := validateFactoryStartCredential(credential, root, spec.Repository); err != nil {
				return err
			}
		}
		status, err := startFactoryManaged(cmd.Context(), root, spec, docker, endpoint, deps, credential)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Registration string         `json:"registration"`
			Status       factory.Status `json:"status"`
		}{"registered", status})
	}}
	cmd.Flags().StringVar(&specPath, "spec", "", "managed specification JSON")
	cmd.Flags().StringVar(&root, "state-dir", "", "factory state directory")
	cmd.Flags().StringVar(&docker, "docker", "", "Docker executable for initial publication")
	cmd.Flags().StringVar(&endpoint, "docker-endpoint", "", "explicit Docker Unix endpoint for initial publication")
	cmd.Flags().StringVar(&credential, "model-credential-file", "", "absolute nonsecret credential reference for the frozen model helper")
	for _, flag := range []string{"spec", "state-dir", "docker", "docker-endpoint"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func startFactoryManaged(ctx context.Context, root string, spec factory.ManagedSpec, docker, endpoint string, deps factoryStartDependencies, credentials ...string) (factory.Status, error) {
	credential := factoryCredentialArgument(credentials)
	reservation, err := admitFactoryHostBudget(ctx, root, spec, deps.probe)
	if err != nil {
		return factory.Status{}, fmt.Errorf("factory host admission: %w", err)
	}
	root = reservation.StateRoot
	access, err := lockFactoryService(ctx, root, spec.ID)
	if err != nil {
		return factory.Status{}, fmt.Errorf("factory service lock: %w", err)
	}
	defer access.Close()
	view, err := factory.ReadManagedAdmission(ctx, root, spec.ID)
	if err != nil {
		if _, err := factory.AdmitManaged(ctx, root, spec, reservation); err != nil {
			return factory.Status{}, fmt.Errorf("managed admission: %w", err)
		}
		view, err = factory.ReadManagedAdmission(ctx, root, spec.ID)
		if err != nil {
			return factory.Status{}, fmt.Errorf("read admission: %w", err)
		}
	}
	ctx, cancel := context.WithDeadline(ctx, view.Deadline)
	defer cancel()
	if err := factory.VerifyManagedHostReservation(ctx, root, spec.ID, reservation); err != nil {
		return factory.Status{}, fmt.Errorf("verify host reservation: %w", err)
	}
	ref, err := factory.ReadManagedRuntimeReference(ctx, root, spec.ID)
	if errors.Is(err, factory.ErrManagedRuntimePending) {
		ref, err = publishFactoryStartRuntime(ctx, view, docker, endpoint, deps.executable, credential)
		if err != nil {
			return factory.Status{}, err
		}
		if err := factory.ActivateManaged(ctx, root, spec.ID, ref); err != nil {
			return factory.Status{}, fmt.Errorf("activate runtime: %w", err)
		}
	} else if err != nil {
		return factory.Status{}, fmt.Errorf("read active runtime: %w", err)
	}
	if err := verifyFactoryStartModelConfig(ctx, ref, credential); err != nil {
		return factory.Status{}, err
	}
	if err := verifyFactoryPublishedRuntime(ctx, view, ref); err != nil {
		return factory.Status{}, fmt.Errorf("verify active runtime: %w", err)
	}
	if err := registerFactoryStart(ctx, access, view, ref, deps); err != nil {
		return factory.Status{}, err
	}
	status, err := factory.ReadStatus(ctx, root, spec.ID)
	if err != nil {
		return factory.Status{}, fmt.Errorf("read factory status: %w", err)
	}
	return status, nil
}

func publishFactoryStartRuntime(ctx context.Context, view factory.ManagedAdmissionView, docker, endpoint string, executable func() (string, error), credentials ...string) (factory.ManagedRuntimeReference, error) {
	path, err := executable()
	if err != nil {
		return factory.ManagedRuntimeReference{}, fmt.Errorf("find Oro executable: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return factory.ManagedRuntimeReference{}, fmt.Errorf("resolve Oro executable: %w", err)
	}
	oroHash, err := factoryStartProgramDigest(ctx, path)
	if err != nil {
		return factory.ManagedRuntimeReference{}, err
	}
	dockerHash, err := factoryStartProgramDigest(ctx, docker)
	if err != nil {
		return factory.ManagedRuntimeReference{}, err
	}
	credential := factoryCredentialArgument(credentials)
	return publishFactoryRuntime(ctx, view, factoryRuntimeInputs{OroPath: path, OroSHA256: oroHash, DockerPath: docker, DockerSHA256: dockerHash, DockerEndpoint: endpoint, ModelCredentialPath: credential})
}

func factoryStartProgramDigest(ctx context.Context, path string) (string, error) {
	file, _, err := openFactoryPublicationOroSource(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, (512<<20)+1))
	if err != nil || n == 0 || n > 512<<20 {
		return "", errors.New("cannot hash bounded executable")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := verifyFactoryRuntimeUniqueProgram(ctx, path, digest); err != nil {
		return "", err
	}
	return digest, nil
}

func factoryStartDiskProbe(ctx context.Context, path string) (storage.DiskUsage, error) {
	if err := ctx.Err(); err != nil {
		return storage.DiskUsage{}, fmt.Errorf("disk probe: %w", err)
	}
	for {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.IsDir() {
				return storage.DiskUsage{}, errors.New("disk probe ancestor is not a directory")
			}
			canonical, err := filepath.EvalSymlinks(path)
			if err != nil || canonical != path {
				return storage.DiskUsage{}, errors.New("disk probe ancestor is aliased")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return storage.DiskUsage{}, fmt.Errorf("disk probe: %w", err)
		}
		next := filepath.Dir(path)
		if next == path {
			return storage.DiskUsage{}, errors.New("disk probe has no existing ancestor")
		}
		path = next
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return storage.DiskUsage{}, fmt.Errorf("disk probe: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return storage.DiskUsage{}, fmt.Errorf("disk probe: %w", err)
	}
	if stat.Bsize <= 0 {
		return storage.DiskUsage{}, errors.New("invalid disk block size")
	}
	size := uint64(stat.Bsize)
	if stat.Blocks > math.MaxInt64/size || stat.Bavail > math.MaxInt64/size {
		return storage.DiskUsage{}, errors.New("disk size overflow")
	}
	return storage.DiskUsage{CapacityBytes: stat.Blocks * size, FreeBytes: stat.Bavail * size}, nil
}

func readFactoryManagedSpec(path string) (factory.ManagedSpec, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return factory.ManagedSpec{}, fmt.Errorf("resolve specification: %w", err)
	}
	dir, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return factory.ManagedSpec{}, fmt.Errorf("open specification directory: %w", err)
	}
	defer dir.Close()
	file, err := dir.OpenFile(filepath.Base(absolute), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return factory.ManagedSpec{}, fmt.Errorf("open managed specification: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return factory.ManagedSpec{}, errors.New("specification must be a regular file")
	}
	spec, err := factory.DecodeManagedSpec(file)
	if err != nil {
		return factory.ManagedSpec{}, fmt.Errorf("decode specification: %w", err)
	}
	return spec, nil
}

func verifyFactoryStartModelConfig(ctx context.Context, ref factory.ManagedRuntimeReference, credential string) error {
	artifact, err := readFactoryRuntimeConfig(ctx, ref.Path, ref.SHA256)
	if err != nil {
		return err
	}
	if (artifact.ModelHelper == nil && credential != "") || (artifact.ModelHelper != nil && artifact.ModelHelper.CredentialPath != credential) {
		return errors.New("model helper configuration differs from committed runtime")
	}
	return nil
}

func factoryCredentialArgument(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func registerFactoryStart(ctx context.Context, access *factoryServiceAccess, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, deps factoryStartDependencies) error {
	register := registerFactoryServiceLocked
	if deps.bootstrap != nil {
		register = func(ctx context.Context, access *factoryServiceAccess, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference) error {
			return registerFactoryServiceLockedWithBootstrap(ctx, access, view, ref, deps.bootstrap)
		}
	}
	if err := register(ctx, access, view, ref); err != nil {
		return fmt.Errorf("register service: %w", err)
	}

	return nil
}

// Validate aliases before the host ledger can reserve capacity. Only parent
// directories are inspected; the credential file itself is never opened.
func validateFactoryStartCredential(path, root, repository string) error {
	if err := validateFactoryModelCredentialPath(path, root, repository); err != nil {
		return err
	}
	canonicalRoot, err := canonicalFactoryBudgetRoot(root)
	if err != nil {
		return fmt.Errorf("resolve state root: %w", err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return fmt.Errorf("resolve repository: %w", err)
	}
	parent, err := canonicalFactoryCredentialParent(filepath.Dir(path))
	if err != nil {
		return err
	}
	if parent != filepath.Dir(path) {
		return errors.New("model credential reference has an aliased parent")
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	if err := validateFactoryModelCredentialPath(resolved, canonicalRoot, canonicalRepo); err != nil {
		return err
	}
	for _, protected := range []string{canonicalRoot, canonicalRepo} {
		if err := rejectCredentialPhysicalContainment(parent, protected); err != nil {
			return err
		}
	}
	return nil
}

func canonicalFactoryCredentialParent(path string) (string, error) {
	original := path
	for {
		canonical, err := filepath.EvalSymlinks(path)
		if err == nil {
			suffix, err := filepath.Rel(path, original)
			if err != nil {
				return "", fmt.Errorf("resolve credential parent: %w", err)
			}
			return filepath.Join(canonical, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve credential parent: %w", err)
		}
		if err := rejectDanglingCredentialParent(path); err != nil {
			return "", err
		}
		next := filepath.Dir(path)
		if next == path {
			return "", errors.New("credential parent has no existing ancestor")
		}
		path = next
	}
}

func rejectDanglingCredentialParent(path string) error {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open credential parent: %w", err)
	}
	info, statErr := parent.Lstat(filepath.Base(path))
	if err := parent.Close(); err != nil {
		return fmt.Errorf("close credential parent: %w", err)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("inspect credential parent: %w", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("model credential parent is a dangling alias")
	}
	return nil
}

// Compare actual directory identities, including filesystems with case-folding
// or Unicode aliases. Missing credential ancestors remain valid references.
func rejectCredentialPhysicalContainment(parent, protected string) error {
	target, err := os.OpenRoot(protected)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open protected directory: %w", err)
	}
	defer target.Close()
	targetInfo, err := target.Stat(".")
	if err != nil {
		return fmt.Errorf("stat protected directory: %w", err)
	}
	for {
		directory, err := os.OpenRoot(parent)
		if err == nil {
			info, statErr := directory.Stat(".")
			closeErr := directory.Close()
			if statErr != nil || closeErr != nil {
				return errors.Join(statErr, closeErr)
			}
			if os.SameFile(info, targetInfo) {
				return errors.New("model credential reference is physically inside a protected directory")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("open credential ancestor: %w", err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return nil
		}
		parent = next
	}
}
