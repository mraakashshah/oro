package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"oro/pkg/factory"
)

// Called only after exact-scope retirement. The publication receipt is bound to
// the authenticated socket; existence of this file never authorizes consumption.
func readFactoryRetiredResult(request factoryHostExecRequest, publication factoryHostExecPublished) (factory.HostResult, error) {
	file, err := os.OpenFile(request.ResultPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // Exact derived private receipt after scope quiescence.
	if err != nil {
		return factory.HostResult{}, fmt.Errorf("open retired factory result: %w", err)
	}
	defer file.Close()
	if err := factoryHostPrivateFile(file); err != nil {
		return factory.HostResult{}, err
	}
	limit := 4*request.Invocation.OutputLimit + factoryHostExecMetadataLimit
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return factory.HostResult{}, fmt.Errorf("read retired factory result: %w", err)
	}
	if int64(len(data)) > limit || factoryHostDigest(data) != publication.PendingSHA256 {
		return factory.HostResult{}, errors.New("retired factory result exceeds bound or differs from publication")
	}
	var pending factoryHostExecPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return factory.HostResult{}, fmt.Errorf("decode retired factory result: %w", err)
	}
	canonical, err := json.Marshal(pending)
	if err != nil || !bytes.Equal(data, canonical) {
		return factory.HostResult{}, errors.New("retired factory result is not canonical JSON")
	}
	if err := validateFactoryRetiredResult(request, publication, pending); err != nil {
		return factory.HostResult{}, err
	}
	return pending.Result, nil
}

func validateFactoryRetiredResult(request factoryHostExecRequest, publication factoryHostExecPublished, pending factoryHostExecPending) error {
	invocation := request.Invocation
	if pending.Version != 1 || pending.RequestSHA256 != publication.RequestSHA256 || pending.Scope != invocation.Scope ||
		!pending.Deadline.Equal(invocation.Deadline) || pending.OutputLimit != invocation.OutputLimit || pending.Error != "" {
		return errors.New("retired factory result differs from its authenticated invocation")
	}
	if pending.Result.ExitCode < 0 || pending.Result.ExitCode > 255 || int64(len(pending.Result.Stdout)) > invocation.OutputLimit || int64(len(pending.Result.Stderr)) > invocation.OutputLimit {
		return errors.New("retired factory result has invalid exit status or unbounded output")
	}
	return nil
}
