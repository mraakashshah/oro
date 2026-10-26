//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"oro/pkg/factory"
)

type factoryLaunchdPrepared struct {
	request       factoryHostExecRequest
	digest, plist string
	listener      *net.UnixListener
}

func (runtime *factoryLaunchdRuntime) validateInvocation(ctx context.Context, invocation factory.HostInvocation) error {
	if err := runtime.validateScope(invocation.Scope); err != nil {
		return err
	}
	if err := factoryHostDeadline(ctx, invocation.Deadline); err != nil {
		return err
	}
	if invocation.Deadline.After(runtime.config.HardStop) {
		return errors.New("factory invocation exceeds admitted hard stop")
	}
	observation, err := runtime.authority.Observe(ctx, invocation.Scope)
	if err != nil {
		return fmt.Errorf("observe reserved factory invocation: %w", err)
	}
	if observation.Phase != "reserved" || observation.PID != 0 || observation.PGID != 0 || !observation.Deadline.Equal(invocation.Deadline) {
		return errors.New("factory invocation requires an exact unconsumed scope reservation")
	}
	switch invocation.Scope.Kind {
	case "host":
		return nil
	case "observer":
		return validateFactoryObserver(ctx, runtime.config, invocation)
	case "cleanup":
		permit, err := runtime.authority.CleanupPermit(ctx, invocation.Scope)
		if err != nil {
			return fmt.Errorf("read reserved factory cleanup permit: %w", err)
		}
		return validateFactoryCleanupInvocation(ctx, runtime.config, invocation, permit)
	default:
		return errors.New("factory invocation has no admitted runtime scope kind")
	}
}

func (runtime *factoryLaunchdRuntime) register(scope factory.ScopeHandle, call *factoryLaunchdCall) error {
	return runtime.registerContext(context.Background(), scope, call)
}

func (runtime *factoryLaunchdRuntime) registerContext(ctx context.Context, scope factory.ScopeHandle, call *factoryLaunchdCall) error {
	if err := runtime.lockGrant(ctx); err != nil {
		return err
	}
	defer func() { <-runtime.gate }()
	observation, err := runtime.authority.Observe(ctx, scope)
	if err != nil {
		return fmt.Errorf("observe factory registration: %w", err)
	}
	if observation.Phase != "reserved" {
		return errors.New("factory scope no longer reserved")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if _, exists := runtime.active[scope.ID]; exists {
		return errors.New("factory scope already has an active invocation")
	}
	call.scope = scope
	runtime.active[scope.ID] = call
	return nil
}

func (runtime *factoryLaunchdRuntime) unregister(scope factory.ScopeHandle) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	delete(runtime.active, scope.ID)
}

func (runtime *factoryLaunchdRuntime) perform(ctx context.Context, invocation factory.HostInvocation, call *factoryLaunchdCall) (*factoryLaunchdPrepared, factoryHostExecPublished, error) {
	var publication factoryHostExecPublished
	prepared, err := runtime.prepare(ctx, invocation)
	if err != nil {
		return nil, publication, err
	}
	defer prepared.listener.Close()
	stop := context.AfterFunc(ctx, func() { _ = prepared.listener.SetDeadline(time.Now()) })
	defer stop()
	code, err := runtime.launchctl(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), prepared.plist)
	if err != nil || code != 0 {
		return prepared, publication, fmt.Errorf("bootstrap exact factory job (exit %d): %w", code, errors.Join(err, errors.New("bootstrap acknowledgment unavailable")))
	}
	connection, err := prepared.listener.AcceptUnix()
	if err != nil {
		return prepared, publication, fmt.Errorf("accept factory wrapper: %w", err)
	}
	defer connection.Close()
	stopConnection := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopConnection()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return prepared, publication, fmt.Errorf("bound factory connection: %w", err)
	}
	goMessage, err := runtime.grant(ctx, connection, prepared, call)
	if err != nil {
		return prepared, publication, err
	}
	expected := factoryHostExecPublished{Version: 1, Operation: "PUBLISHED", RequestSHA256: prepared.digest, Scope: invocation.Scope, PID: goMessage.PID, PGID: goMessage.PGID, Deadline: invocation.Deadline}
	publication, err = receiveFactoryPublication(ctx, connection, expected)
	return prepared, publication, err
}

func (runtime *factoryLaunchdRuntime) grant(ctx context.Context, connection *net.UnixConn, prepared *factoryLaunchdPrepared, call *factoryLaunchdCall) (factoryHostExecGo, error) {
	var response factoryHostExecGo
	var hello factoryHostExecHello
	if err := readFactoryRuntimeFrame(connection, &hello); err != nil {
		return response, err
	}
	pid, uid, err := factoryHostPeer(connection)
	if err != nil {
		return response, err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return response, fmt.Errorf("inspect authenticated wrapper group: %w", err)
	}
	invocation := prepared.request.Invocation
	expected := factoryHostExecHello{Version: 1, Operation: "HELLO", RequestSHA256: prepared.digest, Scope: invocation.Scope, PID: pid, PGID: pgid}
	if uid != os.Getuid() || pgid != pid || pid <= 1 || hello != expected {
		return response, errors.New("factory HELLO is not the exact authenticated job leader")
	}
	if err := runtime.recordGrant(ctx, invocation, call, pid, pgid); err != nil {
		return response, err
	}
	response = factoryHostExecGo{Version: 1, Operation: "GO", RequestSHA256: prepared.digest, Scope: invocation.Scope, PID: pid, PGID: pgid, Deadline: invocation.Deadline}
	if err := factoryHostDeadline(ctx, invocation.Deadline); err != nil {
		return response, err
	}
	if err := json.NewEncoder(connection).Encode(response); err != nil {
		return response, fmt.Errorf("send authenticated factory GO: %w", err)
	}
	return response, nil
}

func (runtime *factoryLaunchdRuntime) recordGrant(ctx context.Context, invocation factory.HostInvocation, call *factoryLaunchdCall, pid, pgid int) error {
	// Serializing grant with retirement ensures retirement observes any recorded
	// group before stopping the job; cancellation prevents subsequent GO writes.
	if err := runtime.lockGrant(ctx); err != nil {
		return err
	}
	defer func() { <-runtime.gate }()
	if call.retiring {
		return errors.New("factory scope is already retiring")
	}
	if err := factoryHostDeadline(ctx, invocation.Deadline); err != nil {
		return err
	}
	if err := runtime.authority.RecordHello(ctx, invocation.Scope, pid, pgid); err != nil {
		return fmt.Errorf("record authenticated factory HELLO: %w", err)
	}
	observation, err := runtime.authority.Observe(ctx, invocation.Scope)
	if err != nil {
		return fmt.Errorf("observe authenticated factory grant: %w", err)
	}
	granted, err := runtime.authority.Granted(ctx, invocation.Scope)
	if err != nil {
		return fmt.Errorf("check current factory grant: %w", err)
	}
	if !granted || observation.Phase != "granted" || observation.PID != pid || observation.PGID != pgid || !observation.Deadline.Equal(invocation.Deadline) {
		return errors.New("factory scope grant changed before GO")
	}
	return nil
}
