//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"oro/pkg/factory"
)

func (backend *factorySupervisorBackend) StartController(ctx context.Context, scope factory.ScopeHandle) error {
	if err := backend.validateController(scope); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, backend.view.Deadline)
	defer cancel()
	call := &factoryControllerCall{scope: scope, cancel: cancel, done: make(chan struct{})}
	if err := backend.registerController(ctx, call); err != nil {
		return err
	}
	defer close(call.done)
	if err := backend.verifyControllerStart(ctx, scope); err != nil {
		return err
	}
	request := factoryControllerRequest{Version: 1, Scope: scope, StateRoot: backend.view.StateRoot, ObjectiveID: backend.view.ObjectiveID, Runtime: backend.ref, Deadline: backend.view.Deadline}
	plistPath, socket, digest, err := backend.prepareControllerArtifacts(request)
	if err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for controller: %w", err)
	}
	defer listener.Close()
	deadline, _ := ctx.Deadline()
	if err := errors.Join(os.Chmod(socket, 0o600), listener.SetDeadline(deadline)); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.SetDeadline(time.Now()) })
	defer stop()
	code, err := backend.host.launchctl(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plistPath)
	if err != nil || code != 0 {
		return fmt.Errorf("controller bootstrap unconfirmed (exit %d): %w", code, errors.Join(err, errors.New("bootstrap acknowledgment unavailable")))
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		return fmt.Errorf("accept controller: %w", err)
	}
	defer conn.Close()
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("bound controller socket: %w", err)
	}
	return backend.grantController(ctx, conn, request, digest, call)
}

func (backend *factorySupervisorBackend) prepareControllerArtifacts(request factoryControllerRequest) (plistPath, socket, digest string, resultErr error) {
	data, err := encodeFactoryControllerRequest(request)
	if err != nil {
		return "", "", "", err
	}
	dir, socket, err := backend.host.prepareDirectories(request.Scope)
	if err != nil {
		return "", "", "", err
	}
	requestPath := filepath.Join(dir, "request.json")
	if err := writeFactoryPrivateArtifact(requestPath, data); err != nil {
		return "", "", "", err
	}
	digest = factoryHostDigest(data)
	plist, err := factoryControllerPlist(request, backend.host.config.OroPath, dir, requestPath, digest, socket)
	if err != nil {
		return "", "", "", err
	}
	plistPath = filepath.Join(dir, "job.plist")
	if err := writeFactoryPrivateArtifact(plistPath, plist); err != nil {
		return "", "", "", err
	}
	return plistPath, socket, digest, nil
}

func (backend *factorySupervisorBackend) verifyControllerStart(ctx context.Context, scope factory.ScopeHandle) error {
	if err := factoryHostDeadline(ctx, backend.view.Deadline); err != nil {
		return err
	}
	observation, err := backend.host.authority.Observe(ctx, scope)
	if err != nil {
		return fmt.Errorf("observe controller reservation: %w", err)
	}
	if observation.Phase != "reserved" || observation.PID != 0 || observation.PGID != 0 || !observation.Deadline.Equal(backend.view.Deadline) {
		return errors.New("controller requires exact unconsumed reservation")
	}
	ref, err := factory.ReadManagedRuntimeReference(ctx, backend.view.StateRoot, backend.view.ObjectiveID)
	if err != nil || ref != backend.ref {
		return errors.Join(errors.New("controller activation changed"), err)
	}
	artifact, err := readFactoryRuntimeConfig(ctx, backend.ref.Path, backend.ref.SHA256)
	if err != nil {
		return err
	}
	if !factoryDockerStorageIdentityMatches(backend.view, artifact.factoryRuntimeConfig) || !factoryCleanupRuntimeIdentityMatches(backend.host.config, artifact.factoryRuntimeConfig) {
		return errors.New("controller runtime identity changed")
	}
	return verifyFactoryRuntimeProgram(ctx, backend.host.config.OroPath, backend.host.config.OroSHA256)
}

func (backend *factorySupervisorBackend) grantController(ctx context.Context, conn *net.UnixConn, request factoryControllerRequest, digest string, call *factoryControllerCall) error {
	var hello factoryHostExecHello
	if err := readFactoryRuntimeFrame(conn, &hello); err != nil {
		return err
	}
	pid, uid, err := factoryHostPeer(conn)
	if err != nil {
		return err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("inspect controller group: %w", err)
	}
	expected := factoryHostExecHello{Version: 1, Operation: "HELLO", RequestSHA256: digest, Scope: request.Scope, PID: pid, PGID: pgid}
	if uid != os.Getuid() || pid <= 1 || pgid != pid || hello != expected {
		return errors.New("controller HELLO differs from authenticated leader")
	}
	if err := backend.lockController(ctx); err != nil {
		return err
	}
	defer func() { <-backend.gate }()
	if err := backend.recordControllerGrant(ctx, request, call, pid, pgid); err != nil {
		return err
	}
	response := factoryHostExecGo{Version: 1, Operation: "GO", RequestSHA256: digest, Scope: request.Scope, PID: pid, PGID: pgid, Deadline: request.Deadline}
	if err := json.NewEncoder(conn).Encode(response); err != nil {
		return fmt.Errorf("send controller GO: %w", err)
	}
	return nil
}

func (backend *factorySupervisorBackend) recordControllerGrant(ctx context.Context, request factoryControllerRequest, call *factoryControllerCall, pid, pgid int) error {
	if call.retiring {
		return errors.New("controller is retiring")
	}
	if err := factoryHostDeadline(ctx, request.Deadline); err != nil {
		return err
	}
	if err := backend.host.authority.RecordHello(ctx, request.Scope, pid, pgid); err != nil {
		return fmt.Errorf("record controller hello: %w", err)
	}
	observation, err := backend.host.authority.Observe(ctx, request.Scope)
	if err != nil {
		return fmt.Errorf("observe controller grant: %w", err)
	}
	granted, err := backend.host.authority.Granted(ctx, request.Scope)
	if err != nil {
		return fmt.Errorf("check controller grant: %w", err)
	}
	if !granted || observation.Phase != "granted" || observation.PID != pid || observation.PGID != pgid || !observation.Deadline.Equal(request.Deadline) {
		return errors.New("controller grant changed before GO")
	}
	return factoryHostDeadline(ctx, request.Deadline)
}

func factoryControllerPlist(request factoryControllerRequest, program, dir, path, digest, socket string) ([]byte, error) {
	if err := validateFactoryControllerRequest(request); err != nil {
		return nil, err
	}
	if !factoryJobAbsolutePath(program) || !factoryJobAbsolutePath(dir) || path != filepath.Join(dir, "request.json") || !factoryJobLowerHex(digest, 64) || !factoryJobAbsolutePath(socket) || len(socket) >= 104 {
		return nil, errors.New("invalid controller plist arguments")
	}
	argv := factoryJobXMLNode("array", "")
	for _, arg := range []string{program, "factory", "_controller", "--request", path, "--request-sha256", digest, "--socket", socket} {
		argv.Children = append(argv.Children, factoryJobXMLNode("string", arg))
	}
	props := []struct {
		key   string
		value factoryJobXMLValue
	}{
		{"Label", factoryJobXMLNode("string", request.Scope.Name)},
		{"Program", factoryJobXMLNode("string", program)},
		{"ProgramArguments", argv},
		{"WorkingDirectory", factoryJobXMLNode("string", dir)},
		{"RunAtLoad", factoryJobXMLNode("true", "")},
		{"KeepAlive", factoryJobXMLNode("false", "")},
		{"AbandonProcessGroup", factoryJobXMLNode("false", "")},
		{"EnableGlobbing", factoryJobXMLNode("false", "")},
		{"ExitTimeOut", factoryJobXMLNode("integer", "5")},
		{"Umask", factoryJobXMLNode("integer", "63")},
		{"StandardInPath", factoryJobXMLNode("string", "/dev/null")},
		{"StandardOutPath", factoryJobXMLNode("string", "/dev/null")},
		{"StandardErrorPath", factoryJobXMLNode("string", "/dev/null")},
	}
	dict := factoryJobXMLNode("dict", "")
	for _, prop := range props {
		dict.Children = append(dict.Children, factoryJobXMLNode("key", prop.key), prop.value)
	}
	doc := factoryJobXMLNode("plist", "")
	doc.Version, doc.Children = "1.0", []factoryJobXMLValue{dict}
	data, err := xml.MarshalIndent(doc, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("encode controller plist: %w", err)
	}
	data = bytes.ReplaceAll(data, []byte("<true></true>"), []byte("<true/>"))
	data = bytes.ReplaceAll(data, []byte("<false></false>"), []byte("<false/>"))
	if len(data)+factoryControllerRequestLimit > 1<<20 {
		return nil, errors.New("controller artifacts exceed scope metadata allowance")
	}
	return append([]byte(xml.Header), data...), nil
}
