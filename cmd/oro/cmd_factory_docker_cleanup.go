package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"oro/pkg/factory"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func newFactoryDockerCleanupCmd() *cobra.Command {
	var path, digest string
	command := &cobra.Command{
		Use: "_docker-cleanup", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := readFactoryRuntimeConfig(cmd.Context(), path, digest)
			if err != nil {
				return err
			}
			input, ok := cmd.InOrStdin().(*os.File)
			if !ok || input == nil {
				return errors.New("factory cleanup requires its inherited stdin pipe")
			}
			permit, err := readFactoryDockerCleanupPermit(cmd.Context(), input, config.HardStop)
			if err != nil {
				return err
			}
			return runFactoryDockerCleanup(cmd.Context(), config, permit, cmd.OutOrStdout())
		},
	}
	command.Flags().StringVar(&path, "runtime-config", "", "exact frozen runtime artifact")
	command.Flags().StringVar(&digest, "runtime-config-sha256", "", "admitted runtime artifact SHA-256")
	_ = command.MarkFlagRequired("runtime-config")
	_ = command.MarkFlagRequired("runtime-config-sha256")
	return command
}

func readFactoryDockerCleanupPermit(ctx context.Context, input *os.File, hardStop time.Time) (factory.DockerCleanupPermit, error) {
	if err := factoryHostDeadline(ctx, hardStop); err != nil {
		return factory.DockerCleanupPermit{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, hardStop)
	defer cancel()
	owned, err := openFactoryCleanupInput(input)
	if err != nil {
		return factory.DockerCleanupPermit{}, err
	}
	if err := owned.file.SetReadDeadline(hardStop); err != nil {
		return factory.DockerCleanupPermit{}, errors.Join(fmt.Errorf("bound factory cleanup stdin: %w", err), owned.close())
	}
	// Only the deadline callback runs asynchronously. The read stays on this
	// goroutine; cancellation cannot leave a detached reader behind.
	done := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { done <- owned.file.SetReadDeadline(time.Now()) })
	data, readErr := io.ReadAll(io.LimitReader(owned.file, factory.ManagedCleanupPermitBytes+1))
	var callbackErr error
	if !stop() {
		callbackErr = <-done
	}
	if err := errors.Join(readErr, callbackErr, owned.close(), ctx.Err()); err != nil {
		return factory.DockerCleanupPermit{}, fmt.Errorf("read bounded factory cleanup stdin: %w", err)
	}
	return decodeFactoryDockerCleanupPermit(data)
}

type factoryCleanupInputPipe struct {
	file     *os.File
	original syscall.RawConn
	flags    int
}

// Inherited os.Stdin is not runtime-pollable on Darwin. A close-on-exec owned
// duplicate, made nonblocking before os.NewFile, supports genuine deadlines.
// Dup shares status flags, so close restores the original read description.
func openFactoryCleanupInput(input *os.File) (factoryCleanupInputPipe, error) {
	var result factoryCleanupInputPipe
	info, err := input.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return result, errors.New("factory cleanup stdin must be a pipe")
	}
	raw, err := input.SyscallConn()
	if err != nil {
		return result, fmt.Errorf("access factory cleanup stdin: %w", err)
	}
	duplicate, flags := -1, 0
	var controlErr error
	err = raw.Control(func(fd uintptr) {
		flags, controlErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if controlErr != nil {
			return
		}
		duplicate, controlErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
		if controlErr == nil {
			controlErr = unix.SetNonblock(duplicate, true)
		}
	})
	if err != nil || controlErr != nil {
		if duplicate >= 0 {
			_ = unix.Close(duplicate)
		}
		return result, errors.Join(errors.New("duplicate factory cleanup stdin"), err, controlErr)
	}
	return factoryCleanupInputPipe{file: os.NewFile(uintptr(duplicate), "factory-cleanup-stdin"), original: raw, flags: flags}, nil
}

func (input factoryCleanupInputPipe) close() error {
	closeErr := input.file.Close()
	var restoreErr error
	controlErr := input.original.Control(func(fd uintptr) {
		_, restoreErr = unix.FcntlInt(fd, unix.F_SETFL, input.flags)
	})
	if err := errors.Join(closeErr, controlErr, restoreErr); err != nil {
		return fmt.Errorf("close factory cleanup stdin duplicate and restore flags: %w", err)
	}
	return nil
}

func decodeFactoryDockerCleanupPermit(data []byte) (factory.DockerCleanupPermit, error) {
	var permit factory.DockerCleanupPermit
	if len(data) > factory.ManagedCleanupPermitBytes {
		return permit, errors.New("factory cleanup stdin exceeds its 16 KiB bound")
	}
	if err := json.Unmarshal(data, &permit); err != nil {
		return permit, fmt.Errorf("decode factory cleanup permit: %w", err)
	}
	if _, err := factoryDockerCleanupInspectArgs(permit); err != nil {
		return factory.DockerCleanupPermit{}, err
	}
	canonical, err := json.Marshal(permit)
	if err != nil || !bytes.Equal(data, canonical) {
		return factory.DockerCleanupPermit{}, errors.Join(errors.New("factory cleanup permit requires exact canonical JSON"), err)
	}
	return permit, nil
}
