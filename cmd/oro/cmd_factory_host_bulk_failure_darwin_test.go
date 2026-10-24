//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

type factoryBulkFaultOutcome struct {
	result factory.HostResult
	err    error
}

type factoryBulkFaultEvidence struct {
	Name                                      string
	Scope                                     factory.ScopeHandle
	Observation                               factory.ScopeObservation
	Deadline, HardStop                        time.Time
	Error                                     string
	RequestBytes, InflightBytes, PendingBytes int64
	PendingError                              string
	PendingStdoutBytes                        int
	PendingExitCode                           int
	PendingSHA256                             string
	PendingStdoutSHA256                       string
	Frame                                     string
	FrameReceivedAt                           time.Time
	NewlineHeldAt, NewlineReleasedAt          time.Time
	FramePendingSHA256                        string
	Progress                                  factoryBulkFaultProgress
	EmptyResult, ComponentPublicationPath     bool
}

type factoryBulkFaultProgress struct {
	PID, PGID     int
	ReadBytes     int
	SHA256, Nonce string
}

type factoryBulkFaultReceipt struct {
	Reservation                factory.HostReservation
	OriginalDeadline, HardStop time.Time
	PayloadPath, PayloadSHA256 string
	ProgramBytes               int64
	Failures                   []factoryBulkFaultEvidence
	RetainedBytes              int64
	ElapsedSeconds             float64
	Released                   bool
}

func TestFactoryHostBulkFailureNative(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("native host bulk faults require ORO_FACTORY_INTEGRATION=1")
	}
	f := newFactoryHostBulkFixture(t)
	started := time.Now()
	payload, digest, programBytes := freezeFactoryBulkFaultPayload(t, f)
	receipt := factoryBulkFaultReceipt{
		Reservation: f.reservation, OriginalDeadline: f.deadline,
		HardStop: f.config.HardStop, PayloadPath: payload, PayloadSHA256: digest, ProgramBytes: programBytes,
	}
	data := make([]byte, 32<<20)
	for index := range data {
		data[index] = byte(index)
	}
	seed := f.reserveScope(t, f.deadline)
	seedResult := f.invoke(t, f.invocation(seed, []string{"hash-object", "-w", "--stdin", "--no-filters"}, data, 256))
	oid := strings.TrimSpace(string(seedResult.Stdout))
	if !factoryJobLowerHex(oid, 40) {
		t.Fatal("actual Git did not return its exact blob identity")
	}
	receipt.Failures = append(receipt.Failures, cancelFactoryBulkTransfer(t, f, data, payload, digest))
	receipt.Failures = append(receipt.Failures, overflowFactoryBulkTransfer(t, f, oid))
	for _, mode := range []string{"missing publication", "late publication"} {
		receipt.Failures = append(receipt.Failures, failFactoryBulkPublication(t, f, oid, mode))
	}
	if err := verifyFactoryRuntimeProgram(t.Context(), payload, digest); err != nil {
		t.Fatalf("frozen payload changed: %v", err)
	}
	if len(f.scopes) != 5 || f.reservation != receipt.Reservation || !f.deadline.Equal(receipt.OriginalDeadline) || !f.config.HardStop.Equal(receipt.HardStop) {
		t.Fatal("faults changed original authority, deadlines or counted invocation total")
	}
	f.finish(t)
	receipt.Released, receipt.RetainedBytes = f.receipt.Released, f.receipt.RetainedLogicalBytes
	receipt.ElapsedSeconds = time.Since(started).Seconds()
	writeFactoryBulkFaultReceipt(t, f, receipt)
}

func freezeFactoryBulkFaultPayload(t *testing.T, f *factoryHostBulkFixture) (path, digest string, programBytes int64) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	path = filepath.Join(f.base, "fault-payload")
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	n, copyErr := io.Copy(output, io.LimitReader(input, f.limits.ProgramBytes+1))
	if err := errors.Join(copyErr, output.Sync(), output.Close()); err != nil || n > f.limits.ProgramBytes {
		t.Fatalf("bounded private payload copy: %d %v", n, err)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := syncFactoryHostDirectory(f.base); err != nil {
		t.Fatal(err)
	}
	digest = factoryHostBulkFileHash(t, path, f.limits.ProgramBytes)
	for _, program := range []string{path, f.config.OroPath, f.config.GitPath} {
		info, err := os.Stat(program)
		if err != nil || !info.Mode().IsRegular() || info.Size() > f.limits.ProgramBytes {
			t.Fatalf("fixture program exceeds existing allowance: %s %v", program, err)
		}
		programBytes += info.Size()
	}
	if programBytes > 3*f.limits.ProgramBytes {
		t.Fatal("fixture programs exceed originally reserved program bytes")
	}
	return path, digest, programBytes
}

func cancelFactoryBulkTransfer(t *testing.T, f *factoryHostBulkFixture, data []byte, payload, digest string) factoryBulkFaultEvidence {
	t.Helper()
	if err := verifyFactoryRuntimeProgram(t.Context(), payload, digest); err != nil {
		t.Fatal(err)
	}
	scope := f.reserveScope(t, f.deadline)
	marker := filepath.Join(f.base, "partial-"+scope.ID+".json")
	invocation := f.invocation(scope, nil, data, 256)
	invocation.Path = payload
	invocation.Args = []string{"-test.run=^TestFactoryHostBulkFailurePayload$", "--", "bulk-cancel", marker, scope.Nonce, invocation.Deadline.Format(time.RFC3339Nano)}
	invocation.Env = []string{"LC_ALL=C", "LANG=C", "GORACE=atexit_sleep_ms=0"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	completed := make(chan factoryBulkFaultOutcome, 1)
	go func() {
		result, err := f.runtime.Invoke(ctx, invocation)
		completed <- factoryBulkFaultOutcome{result, err}
	}()
	progress, progressErr := awaitFactoryBulkProgress(ctx, f, scope, marker, data)
	cancel() // Only after positive evidence that a strict prefix was transferred.
	outcome := awaitFactoryBulkOutcome(t, f, completed)
	if progressErr != nil {
		t.Fatalf("actual partial-transfer barrier: %v; invocation error=%v", progressErr, outcome.err)
	}
	evidence := assertFactoryBulkFailure(t, f, invocation, outcome, "cancellation during stdin")
	evidence.Progress = progress
	return evidence
}

func awaitFactoryBulkProgress(ctx context.Context, f *factoryHostBulkFixture, scope factory.ScopeHandle, path string, data []byte) (factoryBulkFaultProgress, error) {
	ctx, cancel := context.WithDeadline(ctx, f.deadline)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		progress, err := readFactoryBulkProgress(path)
		if err == nil {
			observation, observeErr := f.runtime.authority.Observe(ctx, scope)
			group, groupErr := syscall.Getpgid(progress.PID)
			wantHash := fmt.Sprintf("%x", sha256.Sum256(data[:128<<10]))
			if observeErr != nil || groupErr != nil || observation.Phase != "granted" || observation.PID <= 1 || observation.PID != observation.PGID ||
				progress.PID <= 1 || progress.PID == observation.PID || progress.PGID != observation.PGID || group != observation.PGID ||
				progress.Nonce != scope.Nonce || progress.ReadBytes != 128<<10 || progress.ReadBytes >= len(data) || progress.SHA256 != wantHash {
				return progress, errors.New("partial transfer does not bind the actual authenticated job group and input prefix")
			}
			return progress, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return progress, err
		}
		select {
		case <-ctx.Done():
			return progress, ctx.Err()
		case <-ticker.C:
		}
	}
}

func readFactoryBulkProgress(path string) (factoryBulkFaultProgress, error) {
	var progress factoryBulkFaultProgress
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return progress, err
	}
	defer file.Close()
	if err := factoryHostPrivateFile(file); err != nil {
		return progress, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return progress, errors.New("partial-transfer marker exceeded its bound")
	}
	if err := json.Unmarshal(data, &progress); err != nil {
		return progress, err
	}
	return progress, nil
}

// The only extra executable is a bounded frozen copy of this test binary. Its
// fixed literal command runs under the actual Oro wrapper and launchd group.
func TestFactoryHostBulkFailurePayload(t *testing.T) {
	if len(os.Args) != 7 || os.Args[3] != "bulk-cancel" {
		return
	}
	deadline, err := time.Parse(time.RFC3339Nano, os.Args[6])
	if err != nil || !time.Now().Before(deadline) {
		t.Fatalf("payload requires its original deadline: %v", err)
	}
	data := make([]byte, 128<<10)
	if _, err := io.ReadFull(os.Stdin, data); err != nil {
		t.Fatal(err)
	}
	progress := factoryBulkFaultProgress{PID: os.Getpid(), PGID: syscall.Getpgrp(), ReadBytes: len(data), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Nonce: os.Args[5]}
	encoded, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	// A complete bounded marker is linked atomically; partial bytes are no barrier.
	path := os.Args[4]
	file, err := os.OpenFile(path+".inflight", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(encoded)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path+".inflight", path); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C // Remaining stdin exceeds pipe capacity; the owned feeder is active.
	t.Fatal("controller did not cancel the partial-transfer payload by its original deadline")
}

func awaitFactoryBulkOutcome(t *testing.T, f *factoryHostBulkFixture, completed <-chan factoryBulkFaultOutcome) factoryBulkFaultOutcome {
	t.Helper()
	timer := time.NewTimer(time.Until(f.config.HardStop))
	defer timer.Stop()
	select {
	case result := <-completed:
		return result
	case <-timer.C:
		t.Fatal("owned native call did not join by original hard stop; evidence and charge retained")
		return factoryBulkFaultOutcome{}
	}
}

func overflowFactoryBulkTransfer(t *testing.T, f *factoryHostBulkFixture, oid string) factoryBulkFaultEvidence {
	t.Helper()
	scope := f.reserveScope(t, f.deadline)
	invocation := f.invocation(scope, []string{"cat-file", "blob", oid}, nil, (32<<20)-1)
	result, err := f.runtime.Invoke(t.Context(), invocation)
	evidence := assertFactoryBulkFailure(t, f, invocation, factoryBulkFaultOutcome{result, err}, "raw output cap plus one")
	if !strings.Contains(evidence.PendingError, "exceeded 33554431-byte output bound") || evidence.PendingStdoutBytes != (32<<20)-1 {
		t.Fatalf("failure was not the actual raw cap+1: %+v", evidence)
	}
	return evidence
}

func failFactoryBulkPublication(t *testing.T, f *factoryHostBulkFixture, oid, mode string) factoryBulkFaultEvidence {
	t.Helper()
	deadline := minFactoryBulkDeadline(f.deadline, time.Now().UTC().Add(8*time.Second))
	scope := f.reserveScope(t, deadline)
	invocation := f.invocation(scope, []string{"cat-file", "blob", oid}, nil, 32<<20)
	invocation.Deadline = deadline // Exactly the pre-reserved shorter deadline.
	evidence := factoryBulkFaultEvidence{Name: mode, Scope: scope, ComponentPublicationPath: true}
	outcome := driveFactoryBulkPublication(f, invocation, &evidence)
	checked := assertFactoryBulkFailure(t, f, invocation, outcome, mode)
	checked.ComponentPublicationPath = true
	checked.Frame, checked.FrameReceivedAt, checked.FramePendingSHA256 = evidence.Frame, evidence.FrameReceivedAt, evidence.FramePendingSHA256
	checked.NewlineHeldAt, checked.NewlineReleasedAt = evidence.NewlineHeldAt, evidence.NewlineReleasedAt
	if checked.PendingError != "" || checked.PendingExitCode != 0 || checked.PendingStdoutBytes != 32<<20 {
		t.Fatalf("publication fault lacked a complete successful pending file: %+v", checked)
	}
	if mode == "late publication" && (evidence.Frame == "" || !evidence.FrameReceivedAt.Before(deadline) || evidence.NewlineHeldAt.IsZero() ||
		!evidence.NewlineHeldAt.Before(deadline) || evidence.NewlineReleasedAt.Before(deadline) || checked.PendingSHA256 != evidence.FramePendingSHA256 || !strings.Contains(checked.Error, "deadline")) {
		t.Fatalf("late case did not hold an actual timely authenticated frame: %+v", checked)
	}
	if mode == "missing publication" && !strings.Contains(checked.Error, "receive factory runtime frame") {
		t.Fatalf("missing case did not fail in the production publication receiver: %+v", checked)
	}
	return checked
}

// This driver exposes the actual accepted socket for a test-owned loss/delay.
// It calls unchanged production components, but is not full Invoke coverage.
func driveFactoryBulkPublication(f *factoryHostBulkFixture, invocation factory.HostInvocation, evidence *factoryBulkFaultEvidence) factoryBulkFaultOutcome {
	ctx, cancel := context.WithDeadline(context.Background(), invocation.Deadline)
	defer cancel()
	if err := f.runtime.validateInvocation(ctx, invocation); err != nil {
		return factoryBulkFaultOutcome{err: err}
	}
	call := &factoryLaunchdCall{cancel: cancel, done: make(chan struct{})}
	if err := f.runtime.register(invocation.Scope, call); err != nil {
		return factoryBulkFaultOutcome{err: err}
	}
	defer f.runtime.unregister(invocation.Scope)
	err := performFactoryBulkPublication(ctx, f, invocation, call, evidence)
	close(call.done)
	retireErr := f.runtime.Retire(ctx, invocation.Scope)
	// No readFactoryRetiredResult after an absent or late PUBLISHED message.
	return factoryBulkFaultOutcome{err: errors.Join(err, retireErr)}
}

func performFactoryBulkPublication(ctx context.Context, f *factoryHostBulkFixture, invocation factory.HostInvocation, call *factoryLaunchdCall, evidence *factoryBulkFaultEvidence) error {
	prepared, err := f.runtime.prepare(ctx, invocation)
	if err != nil {
		return err
	}
	defer prepared.listener.Close()
	code, err := f.runtime.launchctl(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), prepared.plist)
	if err != nil || code != 0 {
		return fmt.Errorf("actual native bootstrap exit=%d: %w", code, err)
	}
	connection, err := prepared.listener.AcceptUnix()
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(invocation.Deadline); err != nil {
		return err
	}
	granted, err := f.runtime.grant(ctx, connection, prepared, call)
	if err != nil {
		return err
	}
	expected := factoryHostExecPublished{
		Version: 1, Operation: "PUBLISHED", RequestSHA256: prepared.digest,
		Scope: invocation.Scope, PID: granted.PID, PGID: granted.PGID, Deadline: invocation.Deadline,
	}
	if evidence.Name == "missing publication" {
		if err := waitFactoryBulkPending(ctx, prepared.request.ResultPath); err != nil {
			return err
		}
		if err := connection.Close(); err != nil {
			return err
		}
		_, err = receiveFactoryPublication(ctx, connection, expected)
		return err
	}
	return delayFactoryBulkPublication(ctx, connection, expected, evidence)
}

func waitFactoryBulkPending(ctx context.Context, path string) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > (128<<20)+factoryHostExecMetadataLimit {
				return errors.New("actual pending publication has invalid type or size")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func delayFactoryBulkPublication(ctx context.Context, connection *net.UnixConn, expected factoryHostExecPublished, evidence *factoryBulkFaultEvidence) error {
	var wire bytes.Buffer
	var actual factoryHostExecPublished
	// Capture actual bytes through the original authenticated socket. This does
	// not manufacture or re-encode a frame and does not consume the pending file.
	if err := readFactoryRuntimeFrame(io.TeeReader(connection, &wire), &actual); err != nil {
		return err
	}
	evidence.FrameReceivedAt = time.Now().UTC()
	expected.PendingSHA256 = actual.PendingSHA256
	if actual != expected || !factoryJobLowerHex(actual.PendingSHA256, 64) || !evidence.FrameReceivedAt.Before(expected.Deadline) {
		return errors.New("real authenticated frame differs from the original invocation or arrived too late")
	}
	evidence.Frame, evidence.FramePendingSHA256 = wire.String(), actual.PendingSHA256
	reader := &factoryBulkLateFrameReader{prefix: bytes.NewReader(wire.Bytes()[:wire.Len()-1]), newline: wire.Bytes()[wire.Len()-1:], ctx: ctx}
	_, err := receiveFactoryPublication(ctx, reader, expected)
	evidence.NewlineHeldAt, evidence.NewlineReleasedAt = reader.heldAt, reader.releasedAt
	return err
}

type factoryBulkLateFrameReader struct {
	prefix             *bytes.Reader
	newline            []byte
	ctx                context.Context
	heldAt, releasedAt time.Time
}

func (reader *factoryBulkLateFrameReader) Read(data []byte) (int, error) {
	if reader.prefix.Len() != 0 {
		return reader.prefix.Read(data)
	}
	if len(reader.newline) == 0 {
		return 0, io.EOF
	}
	reader.heldAt = time.Now().UTC()
	<-reader.ctx.Done()
	reader.releasedAt = time.Now().UTC()
	n := copy(data, reader.newline)
	reader.newline = reader.newline[n:]
	return n, nil
}

func assertFactoryBulkFailure(t *testing.T, f *factoryHostBulkFixture, invocation factory.HostInvocation, outcome factoryBulkFaultOutcome, name string) factoryBulkFaultEvidence {
	t.Helper()
	if outcome.err == nil || outcome.result.ExitCode != 0 || len(outcome.result.Stdout) != 0 || len(outcome.result.Stderr) != 0 {
		t.Fatalf("%s returned a consumable result: exit=%d stdout=%d stderr=%d error=%v", name, outcome.result.ExitCode, len(outcome.result.Stdout), len(outcome.result.Stderr), outcome.err)
	}
	observation := f.retire(t, invocation.Scope)
	if !observation.Deadline.Equal(invocation.Deadline) || !time.Now().Before(f.config.HardStop) {
		t.Fatal("fault retirement changed deadline or exceeded original hard stop")
	}
	f.assertSpent(t)
	if err := releaseFactoryHostReservation(t.Context(), f.config.StateRoot, f.reservation.ID); err == nil {
		t.Fatal("failed but still running objective released capacity")
	}
	evidence := factoryBulkFaultEvidence{
		Name: name, Scope: invocation.Scope, Observation: observation,
		Deadline: invocation.Deadline, HardStop: f.config.HardStop, Error: outcome.err.Error(), EmptyResult: true,
	}
	inspectFactoryBulkFaultArtifacts(t, f, invocation, &evidence)
	t.Logf("%s: authenticated group %d retired; spent H=%d; error=%s", name, observation.PGID, len(f.scopes), outcome.err)
	return evidence
}

func inspectFactoryBulkFaultArtifacts(t *testing.T, f *factoryHostBulkFixture, invocation factory.HostInvocation, evidence *factoryBulkFaultEvidence) {
	t.Helper()
	dir := filepath.Join(f.config.StateRoot, f.spec.ID, "host-scopes", invocation.Scope.ID+"."+invocation.Scope.Nonce)
	for _, artifact := range []struct {
		name  string
		limit int64
		size  *int64
	}{
		{"request.json", f.limits.RequestBytes, &evidence.RequestBytes},
		{"result.pending.json.inflight", f.limits.PendingResultBytes, &evidence.InflightBytes},
		{"result.pending.json", f.limits.PendingResultBytes, &evidence.PendingBytes},
	} {
		info, err := os.Lstat(filepath.Join(dir, artifact.name))
		if errors.Is(err, os.ErrNotExist) && artifact.name != "request.json" {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > artifact.limit {
			t.Fatalf("fault artifact outside derived bound: %s %v", artifact.name, err)
		}
		*artifact.size = info.Size()
	}
	if evidence.PendingBytes == 0 {
		return
	}
	file, err := os.Open(filepath.Join(dir, "result.pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, f.limits.PendingResultBytes+1))
	if err != nil || int64(len(data)) > f.limits.PendingResultBytes {
		t.Fatalf("bounded retired pending read: %v", err)
	}
	var pending factoryHostExecPending
	if err := json.Unmarshal(data, &pending); err != nil || pending.Version != 1 || pending.Scope != invocation.Scope || !pending.Deadline.Equal(invocation.Deadline) || pending.OutputLimit != invocation.OutputLimit {
		t.Fatalf("retired pending identity changed: %v", err)
	}
	canonical, err := json.Marshal(pending)
	if err != nil || !bytes.Equal(canonical, data) || pending.RequestSHA256 != factoryHostBulkFileHash(t, filepath.Join(dir, "request.json"), f.limits.RequestBytes) {
		t.Fatalf("retired pending is not canonical and request-bound: %v", err)
	}
	if int64(len(pending.Result.Stdout)) > invocation.OutputLimit || int64(len(pending.Result.Stderr)) > invocation.OutputLimit {
		t.Fatal("failed pending contains over-limit raw output")
	}
	evidence.PendingError, evidence.PendingStdoutBytes = pending.Error, len(pending.Result.Stdout)
	evidence.PendingExitCode, evidence.PendingSHA256 = pending.Result.ExitCode, factoryHostDigest(data)
	evidence.PendingStdoutSHA256 = fmt.Sprintf("%x", sha256.Sum256(pending.Result.Stdout))
}

func minFactoryBulkDeadline(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func writeFactoryBulkFaultReceipt(t *testing.T, f *factoryHostBulkFixture, receipt factoryBulkFaultReceipt) {
	t.Helper()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil || len(data) > 1<<20 {
		t.Fatalf("fault receipt exceeds metadata bound: %v", err)
	}
	path := filepath.Join(f.base, "fault-receipt.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("fault evidence=%s; exact spent H=%d; retained=%d; original tuple released=%v", path, len(f.scopes), receipt.RetainedBytes, receipt.Released)
}
