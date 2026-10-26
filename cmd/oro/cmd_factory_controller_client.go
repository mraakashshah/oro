package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func readFactoryControllerRequest(path, digest string) (factoryControllerRequest, error) {
	var zero factoryControllerRequest
	data, err := readFactoryControllerBytes(path, digest)
	if err != nil {
		return zero, err
	}
	if factoryHostDigest(data) != digest {
		return zero, errors.New("controller request digest mismatch")
	}
	request, err := decodeFactoryControllerRequest(data)
	if err != nil {
		return zero, err
	}
	if err := verifyFactoryControllerRequestPath(path, request); err != nil {
		return zero, err
	}
	return request, nil
}

//nolint:wrapcheck // Propagate file errors to the one-shot command.
func readFactoryControllerBytes(path, digest string) ([]byte, error) {
	if !factoryJobAbsolutePath(path) || filepath.Base(path) != "request.json" || !factoryJobLowerHex(digest, 64) {
		return nil, errors.New("invalid controller request path or digest")
	}
	if err := factoryHostPrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // Validated path and no-follow open.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := factoryHostPrivateFile(f); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || info.Mode().Perm() != 0o400 {
		return nil, errors.New("controller request is not uniquely immutable")
	}
	data, err := io.ReadAll(io.LimitReader(f, factoryControllerRequestLimit+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func verifyFactoryControllerRequestPath(path string, request factoryControllerRequest) error {
	objective := filepath.Join(request.StateRoot, request.ObjectiveID)
	parent := filepath.Join(objective, "host-scopes")
	dir := filepath.Join(parent, request.Scope.ID+"."+request.Scope.Nonce)
	if path != filepath.Join(dir, "request.json") {
		return errors.New("controller request path differs from scope")
	}
	for _, p := range []string{request.StateRoot, objective, parent, dir} {
		if err := factoryHostPrivateDirectory(p); err != nil {
			return err
		}
	}
	return nil
}

func authorizeFactoryController(ctx context.Context, request factoryControllerRequest, digest, socket string) error {
	pid, pgid, err := validateFactoryControllerPeer(request, digest, socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	if err := factoryHostDeadline(ctx, request.Deadline); err != nil {
		return err
	}
	return exchangeFactoryControllerGrant(ctx, request, digest, socket, pid, pgid)
}

func validateFactoryControllerPeer(request factoryControllerRequest, digest, socket string) (pid, pgid int, resultErr error) {
	if err := validateFactoryControllerRequest(request); err != nil {
		return 0, 0, err
	}
	expected := filepath.Join("/private/tmp/of-"+factoryLaunchdObjectiveHash(factoryLaunchdConfig{StateRoot: request.StateRoot, ObjectiveID: request.ObjectiveID}), request.Scope.ID+request.Scope.Nonce, "s")
	if socket != expected || !factoryJobLowerHex(digest, 64) {
		return 0, 0, errors.New("invalid controller socket binding")
	}
	for _, p := range []string{filepath.Dir(filepath.Dir(socket)), filepath.Dir(socket)} {
		if err := factoryHostPrivateDirectory(p); err != nil {
			return 0, 0, err
		}
	}
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || !factoryHostPrivateOwner(info) {
		return 0, 0, errors.New("controller socket is not private")
	}
	pid = os.Getpid()
	pgid, err = syscall.Getpgid(0)
	if err != nil || pid <= 1 || pgid != pid {
		return 0, 0, errors.New("controller must be process group leader")
	}
	return pid, pgid, nil
}

//nolint:wrapcheck // Propagate socket errors to the one-shot command.
func exchangeFactoryControllerGrant(ctx context.Context, request factoryControllerRequest, digest, socket string, pid, pgid int) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer connection.Close()
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		return errors.New("invalid controller connection")
	}
	_, uid, err := factoryHostPeer(conn)
	if err != nil || uid != os.Getuid() {
		return errors.New("controller server UID mismatch")
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	hello := factoryHostExecHello{Version: 1, Operation: "HELLO", RequestSHA256: digest, Scope: request.Scope, PID: pid, PGID: pgid}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		return fmt.Errorf("send controller HELLO: %w", err)
	}
	var response factoryHostExecGo
	if err := readFactoryHostGo(conn, &response); err != nil {
		return err
	}
	if !factoryHostGoMatches(response, hello, request.Deadline) {
		return errors.New("controller GO mismatch")
	}
	return factoryHostDeadline(ctx, request.Deadline)
}
