//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"oro/pkg/factory"
)

func (runtime *factoryLaunchdRuntime) prepare(ctx context.Context, invocation factory.HostInvocation) (*factoryLaunchdPrepared, error) {
	if err := verifyFactoryRuntimeProgram(ctx, runtime.config.OroPath, runtime.config.OroSHA256); err != nil {
		return nil, err
	}
	dir, socketPath, err := runtime.prepareDirectories(invocation.Scope)
	if err != nil {
		return nil, err
	}
	return runtime.prepareArtifacts(ctx, invocation, dir, socketPath)
}

func (runtime *factoryLaunchdRuntime) prepareDirectories(scope factory.ScopeHandle) (requestDir, socketPath string, err error) {
	objectiveDir := filepath.Join(runtime.config.StateRoot, runtime.config.ObjectiveID)
	if err := factoryHostPrivateDirectory(objectiveDir); err != nil {
		return "", "", err
	}
	parent := filepath.Join(objectiveDir, "host-scopes")
	if err := ensureFactoryPrivateDirectory(parent); err != nil {
		return "", "", err
	}
	dir := filepath.Join(parent, scope.ID+"."+scope.Nonce)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("exclusively create factory request directory: %w", err)
	}
	socketParent := "/private/tmp/of-" + factoryLaunchdObjectiveHash(runtime.config)
	if err := ensureFactoryPrivateDirectory(socketParent); err != nil {
		return "", "", err
	}
	socketDir := filepath.Join(socketParent, scope.ID+scope.Nonce)
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		return "", "", fmt.Errorf("exclusively create factory socket directory: %w", err)
	}
	return dir, filepath.Join(socketDir, "s"), nil
}

func (runtime *factoryLaunchdRuntime) prepareArtifacts(ctx context.Context, invocation factory.HostInvocation, dir, socketPath string) (*factoryLaunchdPrepared, error) {
	request := factoryHostExecRequest{Invocation: invocation, ResultPath: filepath.Join(dir, "result.pending.json")}
	data, err := encodeFactoryHostExecRequest(request)
	if err != nil {
		return nil, err
	}
	digest := factoryHostDigest(data)
	requestPath := filepath.Join(dir, "request.json")
	if err := writeFactoryPrivateArtifact(requestPath, data); err != nil {
		return nil, err
	}
	wrapper := factory.HostInvocation{
		Scope: invocation.Scope, Path: runtime.config.OroPath, Dir: dir, Deadline: invocation.Deadline, OutputLimit: invocation.OutputLimit,
		Args: []string{"factory", "_host-exec", "--request", requestPath, "--request-sha256", digest, "--socket", socketPath},
	}
	plist, err := factoryJobPlist(wrapper)
	if err != nil {
		return nil, err
	}
	plistPath := filepath.Join(dir, "job.plist")
	if err := writeFactoryPrivateArtifact(plistPath, plist); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen for factory wrapper: %w", err)
	}
	deadline, _ := ctx.Deadline()
	if err := errors.Join(os.Chmod(socketPath, 0o600), listener.SetDeadline(deadline)); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("protect factory socket: %w", err)
	}
	return &factoryLaunchdPrepared{request: request, digest: digest, plist: plistPath, listener: listener}, nil
}

func ensureFactoryPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create private factory directory: %w", err)
	}
	return factoryHostPrivateDirectory(path)
}

func writeFactoryPrivateArtifact(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400) //nolint:gosec // Derived fixed filename inside a newly exclusive private scope directory.
	if err != nil {
		return fmt.Errorf("reserve factory artifact: %w", err)
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return fmt.Errorf("persist factory artifact: %w", err)
	}
	return syncFactoryHostDirectory(filepath.Dir(path))
}
