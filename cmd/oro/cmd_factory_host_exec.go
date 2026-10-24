package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"oro/pkg/factory"

	"github.com/spf13/cobra"
)

const (
	// The complete envelope includes base64 stdin. This accommodates the initial
	// 16 MiB source transfer; larger admitted policies must fit this same cap.
	factoryHostExecRequestLimit  = 32 << 20
	factoryHostExecMetadataLimit = 16384
	factoryHostExecWireLimit     = 4096
	factoryHostExecErrorLimit    = 1024
	factoryHostExecWaitDelay     = 200 * time.Millisecond
)

type factoryHostExecRequest struct {
	Invocation factory.HostInvocation `json:"invocation"`
	ResultPath string                 `json:"result_path"`
}

// A pending result is evidence only. Consumption requires a timely matching
// PUBLISHED message, exact-job retirement, process-group quiescence, and matching
// pending bytes and fields. File existence never proves wrapper success.
type factoryHostExecPending struct {
	Version       int                 `json:"version"`
	RequestSHA256 string              `json:"request_sha256"`
	Scope         factory.ScopeHandle `json:"scope"`
	Deadline      time.Time           `json:"deadline"`
	OutputLimit   int64               `json:"output_limit"`
	Result        factory.HostResult  `json:"result"`
	Error         string              `json:"error"`
}

// The backend validates kernel peer PID/UID/PGID and the frozen request digest,
// journals RecordHello, checks Granted/Observe, then emits one GO per scope.
// Both sides bound a single newline-terminated JSON message to 4096 bytes.
type factoryHostExecHello struct {
	Version       int                 `json:"version"`
	Operation     string              `json:"operation"`
	RequestSHA256 string              `json:"request_sha256"`
	Scope         factory.ScopeHandle `json:"scope"`
	PID           int                 `json:"pid"`
	PGID          int                 `json:"pgid"`
}

type factoryHostExecGo struct {
	Version       int                 `json:"version"`
	Operation     string              `json:"operation"`
	RequestSHA256 string              `json:"request_sha256"`
	Scope         factory.ScopeHandle `json:"scope"`
	PID           int                 `json:"pid"`
	PGID          int                 `json:"pgid"`
	Deadline      time.Time           `json:"deadline"`
}

// PUBLISHED is sent only after successful bounded execution/publication. The
// backend must receive the complete frame before Deadline, then retire the job
// and verify PendingSHA256 and every pending field before consuming the result.
// File existence alone never confirms successful publication.
type factoryHostExecPublished struct {
	Version       int                 `json:"version"`
	Operation     string              `json:"operation"`
	RequestSHA256 string              `json:"request_sha256"`
	Scope         factory.ScopeHandle `json:"scope"`
	PID           int                 `json:"pid"`
	PGID          int                 `json:"pgid"`
	Deadline      time.Time           `json:"deadline"`
	PendingSHA256 string              `json:"pending_sha256"`
}

func newFactoryHostExecCmd() *cobra.Command {
	var requestPath, digest, socket, deadline string
	var outputLimit int64
	command := &cobra.Command{
		Use: "_host-exec", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request, err := readFactoryHostExecRequest(requestPath, digest, deadline, outputLimit)
			if err != nil {
				return err
			}
			authority, err := openFactoryHostSocket(cmd.Context(), socket, request, digest)
			if err != nil {
				return err
			}
			defer authority.Close() //nolint:errcheck // Invocation errors remain authoritative; closing cannot grant work.
			if err := runFactoryHostExec(cmd.Context(), request, authority); err != nil {
				return err
			}
			return authority.Published(cmd.Context())
		},
	}
	command.Flags().StringVar(&requestPath, "request", "", "private immutable request path")
	command.Flags().StringVar(&digest, "request-sha256", "", "canonical request SHA-256")
	command.Flags().StringVar(&socket, "socket", "", "owned authority socket")
	command.Flags().StringVar(&deadline, "deadline", "", "immutable UTC deadline")
	command.Flags().Int64Var(&outputLimit, "output-limit", 0, "admitted byte bound per output stream")
	for _, name := range []string{"request", "request-sha256", "socket", "deadline", "output-limit"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func readFactoryHostExecRequest(path, digest, deadline string, outputLimit int64) (factoryHostExecRequest, error) {
	var request factoryHostExecRequest
	if !factoryJobLowerHex(digest, 64) {
		return request, errors.New("factory wrapper requires a private request.json and exact SHA-256")
	}
	data, err := readFactoryHostExecBytes(path)
	if err != nil {
		return request, err
	}
	if factoryHostDigest(data) != digest {
		return request, errors.New("factory host request exceeds bound or mismatches its digest")
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, fmt.Errorf("decode factory host request: %w", err)
	}
	canonical, err := encodeFactoryHostExecRequest(request)
	if err != nil {
		return request, err
	}
	if !bytes.Equal(data, canonical) || request.ResultPath != filepath.Join(filepath.Dir(path), "result.pending.json") {
		return request, errors.New("factory host request must be canonical JSON with its exact private result destination")
	}
	if deadline != request.Invocation.Deadline.UTC().Format(time.RFC3339Nano) || outputLimit != request.Invocation.OutputLimit {
		return request, errors.New("factory wrapper bounds differ from the immutable request")
	}
	return request, nil
}

func readFactoryHostExecBytes(path string) ([]byte, error) {
	if !factoryJobAbsolutePath(path) || filepath.Base(path) != "request.json" {
		return nil, errors.New("factory wrapper requires a private request.json")
	}
	if err := factoryHostPrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // Canonical private parent, no symlink, FIFO cannot block before fstat.
	if err != nil {
		return nil, fmt.Errorf("open factory host request: %w", err)
	}
	defer file.Close()
	if err := factoryHostPrivateFile(file); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, factoryHostExecRequestLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read factory host request: %w", err)
	}
	if len(data) > factoryHostExecRequestLimit {
		return nil, errors.New("factory host request exceeds its complete envelope bound")
	}
	return data, nil
}

func encodeFactoryHostExecRequest(request factoryHostExecRequest) ([]byte, error) {
	if err := validateFactoryHostExecRequest(request); err != nil {
		return nil, err
	}
	invocation := request.Invocation
	// Check raw bytes before JSON allocates escaped/base64 representations.
	remaining := int64(factoryHostExecRequestLimit) - int64(len(invocation.Stdin))
	for _, values := range [][]string{{invocation.Path, invocation.Dir, request.ResultPath}, invocation.Args, invocation.Env} {
		for _, value := range values {
			remaining -= int64(len(value)) + 3
			if remaining < 0 {
				return nil, errors.New("factory host request exceeds its complete envelope bound")
			}
		}
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode factory host request: %w", err)
	}
	if len(data) > factoryHostExecRequestLimit {
		return nil, errors.New("factory host request exceeds its complete envelope bound")
	}
	return data, nil
}

func validateFactoryHostExecRequest(request factoryHostExecRequest) error {
	invocation := request.Invocation
	if err := validateFactoryJobScope(invocation.Scope); err != nil {
		return err
	}
	if !factoryJobAbsolutePath(invocation.Path) || !factoryJobAbsolutePath(invocation.Dir) ||
		!factoryJobAbsolutePath(request.ResultPath) || filepath.Base(request.ResultPath) != "result.pending.json" {
		return errors.New("factory host invocation requires absolute command, cwd and private result paths")
	}
	if invocation.Deadline.IsZero() || invocation.Deadline.UTC().Year() < 1 || invocation.Deadline.UTC().Year() > 9999 || !time.Now().Before(invocation.Deadline) {
		return errors.New("factory host invocation requires an unexpired representable deadline")
	}
	if invocation.OutputLimit <= 0 || invocation.OutputLimit > (math.MaxInt64-factoryHostExecMetadataLimit)/4 {
		return errors.New("factory host invocation requires a positive nonoverflowing output bound")
	}
	for _, arg := range invocation.Args {
		if strings.ContainsRune(arg, 0) {
			return errors.New("factory host arguments cannot contain NUL")
		}
	}
	return validateFactoryHostEnvironment(invocation.Env)
}

func validateFactoryHostEnvironment(environment []string) error {
	seen := make(map[string]bool, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) || seen[key] {
			return errors.New("factory host environment requires unique nonempty names and no NUL")
		}
		seen[key] = true
	}
	return nil
}

func factoryHostPrivateDirectory(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return errors.New("factory host directory must exist and be canonical without symlinks")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect factory host private directory: %w", err)
	}
	if !info.IsDir() || !factoryHostPrivateOwner(info) {
		return errors.New("factory host directory must be private and owned by the current UID")
	}
	return nil
}

func factoryHostPrivateFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect factory host request: %w", err)
	}
	if !info.Mode().IsRegular() || !factoryHostPrivateOwner(info) {
		return errors.New("factory host request must be a private regular file owned by the current UID")
	}
	return nil
}

func factoryHostPrivateOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0o077 == 0
}

func factoryHostDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func runFactoryHostExec(ctx context.Context, request factoryHostExecRequest, authority factory.ScopeAuthority) error {
	data, err := encodeFactoryHostExecRequest(request)
	if err != nil {
		return err
	}
	if err := factoryHostPrivateDirectory(filepath.Dir(request.ResultPath)); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, request.Invocation.Deadline)
	defer cancel()
	if err := authorizeFactoryHostExec(ctx, request.Invocation, authority); err != nil {
		return err
	}
	file, err := reserveFactoryHostResult(ctx, request.ResultPath, request.Invocation.Deadline)
	if err != nil {
		return err
	}
	defer file.Close()
	result, executionError := executeFactoryHostCommand(ctx, request.Invocation)
	pending := factoryHostExecPending{
		Version: 1, RequestSHA256: factoryHostDigest(data), Scope: request.Invocation.Scope,
		Deadline: request.Invocation.Deadline, OutputLimit: request.Invocation.OutputLimit, Result: result,
	}
	if executionError != nil {
		pending.Error = executionError.Error()
		if len(pending.Error) > factoryHostExecErrorLimit {
			pending.Error = pending.Error[:factoryHostExecErrorLimit]
		}
	}
	return errors.Join(executionError, publishFactoryHostResult(ctx, file, request.ResultPath, pending))
}

func authorizeFactoryHostExec(ctx context.Context, invocation factory.HostInvocation, authority factory.ScopeAuthority) error {
	if authority == nil {
		return errors.New("factory host invocation requires authenticated authority")
	}
	pid := os.Getpid()
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("read factory wrapper process group: %w", err)
	}
	if err := authority.RecordHello(ctx, invocation.Scope, pid, pgid); err != nil {
		return fmt.Errorf("authenticate factory host HELLO: %w", err)
	}
	observation, err := authority.Observe(ctx, invocation.Scope)
	if err != nil {
		return fmt.Errorf("observe factory host authority: %w", err)
	}
	if observation.Phase != "granted" || observation.PID != pid || observation.PGID != pgid || !observation.Deadline.Equal(invocation.Deadline) {
		return errors.New("factory host authority differs from the immutable invocation")
	}
	granted, err := authority.Granted(ctx, invocation.Scope)
	if err != nil {
		return fmt.Errorf("receive factory host GO: %w", err)
	}
	if !granted {
		return errors.New("factory host invocation did not receive authenticated GO")
	}
	return factoryHostDeadline(ctx, invocation.Deadline)
}

func factoryHostDeadline(ctx context.Context, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("factory host execution canceled: %w", err)
	}
	if !time.Now().Before(deadline) {
		return errors.New("factory host immutable deadline elapsed")
	}
	return nil
}

func reserveFactoryHostResult(ctx context.Context, path string, deadline time.Time) (*os.File, error) {
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("factory host pending result already exists or is inaccessible")
	}
	// A durable exclusive inflight artifact prevents replay even after wrapper
	// death or publication. Only scope retirement may remove this barrier.
	if err := factoryHostDeadline(ctx, deadline); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+".inflight", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // Validated private directory and fixed exclusive filename.
	if err != nil {
		return nil, fmt.Errorf("reserve factory host pending result: %w", err)
	}
	return file, nil
}

func executeFactoryHostCommand(ctx context.Context, invocation factory.HostInvocation) (factory.HostResult, error) {
	cmd := exec.CommandContext(ctx, invocation.Path, invocation.Args...) //nolint:gosec // Frozen trusted executable and literal argv, authenticated GO already received.
	cmd.Dir, cmd.Env = invocation.Dir, append([]string{}, invocation.Env...)
	capture, err := newFactoryHostIO(cmd, invocation)
	if err != nil {
		return factory.HostResult{ExitCode: -1}, err
	}
	defer capture.close()
	// No Setpgid: the child and its descendants stay inside the launchd job.
	// Only the newly created direct child is killed here; launchd retires the job.
	if err := factoryHostDeadline(ctx, invocation.Deadline); err != nil {
		return factory.HostResult{ExitCode: -1}, err
	}
	if err := cmd.Start(); err != nil {
		return factory.HostResult{ExitCode: -1}, fmt.Errorf("start factory host command: %w", err)
	}
	startError := capture.start()
	if startError != nil {
		startError = errors.Join(startError, stopFactoryHostChild(cmd))
	}
	err = cmd.Wait()
	// Cmd.Wait prefers ExitError over its own copy errors. All three streams use
	// owned file descriptors, so capture completion is independent of that error.
	drainError := capture.wait(ctx)
	result := factory.HostResult{Stdout: capture.stdout.buffer.Bytes(), Stderr: capture.stderr.buffer.Bytes(), ExitCode: -1}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	// A normal nonzero exit is command data: acceptance may explicitly expect
	// it. Signals, cancellation, output overflow and wait failures remain errors.
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.Exited() {
		err = nil
	}
	return result, errors.Join(err, startError, drainError, ctx.Err())
}

func stopFactoryHostChild(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop owned factory host child: %w", err)
	}
	return nil
}

type factoryHostIO struct {
	stdout, stderr *factoryHostOutput
	pipes          []*factoryHostPipe
}

type factoryHostPipe struct {
	parent, child *os.File
	transfer      func() error
	done          chan error
}

func newFactoryHostIO(cmd *exec.Cmd, invocation factory.HostInvocation) (*factoryHostIO, error) {
	stop := func() error { return stopFactoryHostChild(cmd) }
	capture := &factoryHostIO{
		stdout: &factoryHostOutput{limit: invocation.OutputLimit, stop: stop, stream: "stdout"},
		stderr: &factoryHostOutput{limit: invocation.OutputLimit, stop: stop, stream: "stderr"},
	}
	for _, output := range []*factoryHostOutput{capture.stdout, capture.stderr} {
		pipe, err := openFactoryHostOutput(output)
		if err != nil {
			capture.close()
			return nil, err
		}
		capture.pipes = append(capture.pipes, pipe)
	}
	cmd.Stdout, cmd.Stderr = capture.pipes[0].child, capture.pipes[1].child
	if len(invocation.Stdin) != 0 {
		pipe, err := openFactoryHostInput(invocation.Stdin)
		if err != nil {
			capture.close()
			return nil, err
		}
		capture.pipes = append(capture.pipes, pipe)
		cmd.Stdin = pipe.child
	}
	return capture, nil
}

func openFactoryHostOutput(output *factoryHostOutput) (*factoryHostPipe, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open factory host %s pipe: %w", output.stream, err)
	}
	return &factoryHostPipe{parent: reader, child: writer, done: make(chan error, 1), transfer: func() error {
		if _, err := io.Copy(output, reader); err != nil {
			return fmt.Errorf("drain factory host %s: %w", output.stream, err)
		}
		return nil
	}}, nil
}

func openFactoryHostInput(input []byte) (*factoryHostPipe, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open factory host stdin pipe: %w", err)
	}
	return &factoryHostPipe{parent: writer, child: reader, done: make(chan error, 1), transfer: func() error {
		// A command may intentionally exit without consuming all input. EPIPE
		// completes the transfer; a retained pipe must still finish or time out.
		if _, err := io.Copy(writer, bytes.NewReader(input)); err != nil && !errors.Is(err, syscall.EPIPE) {
			return fmt.Errorf("feed factory host stdin: %w", err)
		}
		return nil
	}}, nil
}

func (capture *factoryHostIO) start() error {
	var closeError error
	for _, pipe := range capture.pipes {
		if err := pipe.child.Close(); err != nil {
			closeError = errors.Join(closeError, fmt.Errorf("close parent copy of factory child pipe: %w", err))
		}
		go pipe.run()
	}
	return closeError
}

func (pipe *factoryHostPipe) run() {
	err := pipe.transfer()
	_ = pipe.parent.Close()
	pipe.done <- err
}

func (capture *factoryHostIO) close() {
	for _, pipe := range capture.pipes {
		_ = pipe.parent.Close()
		_ = pipe.child.Close()
	}
}

func (capture *factoryHostIO) wait(ctx context.Context) error {
	timer := time.NewTimer(factoryHostExecWaitDelay)
	defer timer.Stop()
	var drainError error
	for index, pipe := range capture.pipes {
		select {
		case err := <-pipe.done:
			drainError = errors.Join(drainError, err)
			continue
		case <-ctx.Done():
			drainError = errors.Join(drainError, ctx.Err())
		case <-timer.C:
			drainError = errors.Join(drainError, errors.New("factory host pipe drain exceeded its bounded wait"))
		}
		// Closing owned pollable pipe endpoints interrupts blocked reads/writes.
		// Join every remaining copy before reading buffers or publishing anything.
		capture.close()
		for _, remaining := range capture.pipes[index:] {
			drainError = errors.Join(drainError, <-remaining.done)
		}
		return drainError
	}
	return drainError
}

// Each owned output pipe has one copy goroutine, joined explicitly before its
// buffer is read. The bound is per stream; overflow is always a transport error.
type factoryHostOutput struct {
	buffer bytes.Buffer
	limit  int64
	stop   func() error
	stream string
	err    error
}

func (output *factoryHostOutput) Write(data []byte) (int, error) {
	remaining := output.limit - int64(output.buffer.Len())
	if int64(len(data)) <= remaining {
		n, err := output.buffer.Write(data)
		if err != nil {
			return n, fmt.Errorf("capture factory host %s: %w", output.stream, err)
		}
		return n, nil
	}
	n, _ := output.buffer.Write(data[:remaining])
	output.err = errors.Join(fmt.Errorf("factory host %s exceeded %d-byte output bound", output.stream, output.limit), output.stop())
	return n, output.err
}

func publishFactoryHostResult(ctx context.Context, file *os.File, path string, pending factoryHostExecPending) error {
	return publishFactoryHostResultWithSync(ctx, file, path, pending, syncFactoryHostDirectory)
}

func publishFactoryHostResultWithSync(ctx context.Context, file *os.File, path string, pending factoryHostExecPending, syncDirectory func(string) error) error {
	if err := factoryHostDeadline(ctx, pending.Deadline); err != nil {
		return err
	}
	data, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("encode factory host pending result: %w", err)
	}
	if int64(len(data)) > 4*pending.OutputLimit+factoryHostExecMetadataLimit {
		return errors.New("factory host pending result exceeds reserved serialization bound")
	}
	if err := factoryHostDeadline(ctx, pending.Deadline); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write factory host pending result: %w", err)
	}
	if err := factoryHostDeadline(ctx, pending.Deadline); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync factory host pending result: %w", err)
	}
	// Linking the completed private inode publishes atomically without replacing
	// an earlier receipt. Keep both names until retirement: removing the inflight
	// name would let a delayed reservation replay an already published scope.
	if err := factoryHostDeadline(ctx, pending.Deadline); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("publish factory host pending result without overwrite: %w", err)
	}
	if err := factoryHostDeadline(ctx, pending.Deadline); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return factoryHostDeadline(ctx, pending.Deadline)
}

func syncFactoryHostDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // Previously validated private request directory.
	if err != nil {
		return fmt.Errorf("open factory host result directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync factory host result directory: %w", err)
	}
	return nil
}

// This adapter is intentionally one-shot and cannot retire a scope. Its caller
// owns it until runFactoryHostExec returns; it is not shared between goroutines.
type factoryHostSocketAuthority struct {
	conn      *net.UnixConn
	request   factoryHostExecRequest
	digest    string
	response  factoryHostExecGo
	hello     bool
	consumed  bool
	published bool
}

func openFactoryHostSocket(ctx context.Context, path string, request factoryHostExecRequest, digest string) (*factoryHostSocketAuthority, error) {
	if !factoryJobAbsolutePath(path) || len(path) >= 104 {
		return nil, errors.New("factory authority requires a clean absolute Unix socket path")
	}
	if err := factoryHostPrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || !factoryHostPrivateOwner(info) {
		return nil, errors.New("factory authority socket must be private and owned by the current UID")
	}
	dialer := net.Dialer{Deadline: request.Invocation.Deadline}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("connect factory authority: %w", err)
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("factory authority connection is not a Unix socket")
	}
	return &factoryHostSocketAuthority{conn: conn, request: request, digest: digest}, nil
}

func (authority *factoryHostSocketAuthority) RecordHello(ctx context.Context, scope factory.ScopeHandle, pid, pgid int) error {
	if authority.hello || scope != authority.request.Invocation.Scope || pid != os.Getpid() {
		return errors.New("factory authority HELLO is mismatched or replayed")
	}
	authority.hello = true
	_, uid, err := factoryHostPeer(authority.conn)
	if err != nil {
		return err
	}
	if uid != os.Getuid() {
		return errors.New("factory authority server UID differs from wrapper UID")
	}
	deadline := authority.request.Invocation.Deadline
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	if err := authority.conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("bound factory authority handshake: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = authority.conn.SetDeadline(time.Now()) })
	defer stop()
	hello := factoryHostExecHello{Version: 1, Operation: "HELLO", RequestSHA256: authority.digest, Scope: scope, PID: pid, PGID: pgid}
	if err := json.NewEncoder(authority.conn).Encode(hello); err != nil {
		return fmt.Errorf("send factory host HELLO: %w", err)
	}
	var response factoryHostExecGo
	if err := readFactoryHostGo(authority.conn, &response); err != nil {
		return err
	}
	if !factoryHostGoMatches(response, hello, authority.request.Invocation.Deadline) {
		return errors.New("factory authority did not return an exact authenticated GO")
	}
	authority.response = response
	return nil
}

func factoryHostGoMatches(response factoryHostExecGo, hello factoryHostExecHello, deadline time.Time) bool {
	return response.Version == 1 && response.Operation == "GO" && response.RequestSHA256 == hello.RequestSHA256 &&
		response.Scope == hello.Scope && response.PID == hello.PID && response.PGID == hello.PGID && response.Deadline.Equal(deadline)
}

func readFactoryHostGo(reader io.Reader, response *factoryHostExecGo) error {
	// A newline frame avoids waiting for socket EOF and bounds whitespace too.
	var frame bytes.Buffer
	one := make([]byte, 1)
	for frame.Len() < factoryHostExecWireLimit {
		if _, err := io.ReadFull(reader, one); err != nil {
			return fmt.Errorf("receive factory host GO: %w", err)
		}
		if one[0] == '\n' {
			decoder := json.NewDecoder(bytes.NewReader(frame.Bytes()))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(response); err != nil {
				return fmt.Errorf("decode factory host GO: %w", err)
			}
			canonical, err := json.Marshal(response)
			if err != nil || !bytes.Equal(canonical, frame.Bytes()) {
				return errors.New("factory host GO is not canonical JSON")
			}
			return nil
		}
		_ = frame.WriteByte(one[0])
	}
	return errors.New("factory host GO exceeds its metadata bound")
}

func (authority *factoryHostSocketAuthority) Granted(ctx context.Context, scope factory.ScopeHandle) (bool, error) {
	if !authority.hello || authority.consumed || scope != authority.response.Scope || authority.response.Operation != "GO" {
		return false, errors.New("factory authority GO is missing, mismatched or consumed")
	}
	authority.consumed = true
	if err := factoryHostDeadline(ctx, authority.response.Deadline); err != nil {
		return false, err
	}
	return true, nil
}

func (authority *factoryHostSocketAuthority) Observe(_ context.Context, scope factory.ScopeHandle) (factory.ScopeObservation, error) {
	if !authority.hello || scope != authority.response.Scope || authority.response.Operation != "GO" {
		return factory.ScopeObservation{}, errors.New("factory authority has no authenticated observation")
	}
	return factory.ScopeObservation{Phase: "granted", PID: authority.response.PID, PGID: authority.response.PGID, Deadline: authority.response.Deadline}, nil
}

func (*factoryHostSocketAuthority) MarkQuiescent(context.Context, factory.ScopeHandle) error {
	return errors.New("factory host wrapper cannot certify scope retirement")
}

func (authority *factoryHostSocketAuthority) Close() error {
	if err := authority.conn.Close(); err != nil {
		return fmt.Errorf("close factory host authority: %w", err)
	}
	return nil
}

// Published is private to the hidden CLI, which calls it only after the run
// helper succeeds. It confirms bytes over the original authenticated connection;
// it neither consumes the pending result nor certifies scope retirement.
func (authority *factoryHostSocketAuthority) Published(ctx context.Context) error {
	if !authority.consumed || authority.published {
		return errors.New("factory host publication requires an unconsumed completion opportunity after GO")
	}
	authority.published = true
	digest, err := factoryHostPendingDigest(ctx, authority.request)
	if err != nil {
		return err
	}
	response := authority.response
	message := factoryHostExecPublished{
		Version: 1, Operation: "PUBLISHED", RequestSHA256: authority.digest, Scope: response.Scope,
		PID: response.PID, PGID: response.PGID, Deadline: response.Deadline, PendingSHA256: digest,
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode factory host PUBLISHED: %w", err)
	}
	if len(data)+1 > factoryHostExecWireLimit {
		return errors.New("factory host PUBLISHED exceeds its metadata bound")
	}
	if err := authority.conn.SetDeadline(response.Deadline); err != nil {
		return fmt.Errorf("bound factory host PUBLISHED: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = authority.conn.SetDeadline(time.Now()) })
	defer stop()
	if err := factoryHostDeadline(ctx, response.Deadline); err != nil {
		return err
	}
	if _, err := authority.conn.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("send factory host PUBLISHED: %w", err)
	}
	return factoryHostDeadline(ctx, response.Deadline)
}

func factoryHostPendingDigest(ctx context.Context, request factoryHostExecRequest) (string, error) {
	if err := factoryHostDeadline(ctx, request.Invocation.Deadline); err != nil {
		return "", err
	}
	file, err := os.OpenFile(request.ResultPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // Exact private pending path, reject symlinks and blocking special files.
	if err != nil {
		return "", fmt.Errorf("open factory host pending bytes for confirmation: %w", err)
	}
	defer file.Close()
	if err := factoryHostPrivateFile(file); err != nil {
		return "", err
	}
	limit := 4*request.Invocation.OutputLimit + factoryHostExecMetadataLimit
	return hashFactoryHostPending(ctx, file, request.Invocation.Deadline, limit)
}

func hashFactoryHostPending(ctx context.Context, file *os.File, deadline time.Time, limit int64) (string, error) {
	hash := sha256.New()
	reader := io.LimitReader(file, limit+1)
	buffer := make([]byte, 32<<10)
	var count int64
	for {
		if err := factoryHostDeadline(ctx, deadline); err != nil {
			return "", err
		}
		n, readErr := reader.Read(buffer)
		count += int64(n)
		if count > limit {
			return "", errors.New("factory host pending bytes exceed the admitted serialization bound")
		}
		_, _ = hash.Write(buffer[:n]) // hash.Hash writes cannot fail.
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("hash factory host pending bytes: %w", readErr)
		}
	}
	if err := factoryHostDeadline(ctx, deadline); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
