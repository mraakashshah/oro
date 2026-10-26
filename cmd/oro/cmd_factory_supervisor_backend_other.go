//go:build !darwin

package main

import (
	"context"
	"errors"

	"oro/pkg/factory"
)

type factorySupervisorBackend struct{}

func openFactorySupervisorBackend(context.Context, string, string, factory.ManagedRuntimeReference) (*factorySupervisorBackend, error) {
	return nil, errors.New("managed factory supervisor is unsupported on this platform")
}
func (*factorySupervisorBackend) Close() error { return nil }
func (*factorySupervisorBackend) Invoke(context.Context, factory.HostInvocation) (factory.HostResult, error) {
	return factory.HostResult{}, errors.New("unsupported")
}

func (*factorySupervisorBackend) StartController(context.Context, factory.ScopeHandle) error {
	return errors.New("unsupported")
}

func (*factorySupervisorBackend) Retire(context.Context, factory.ScopeHandle) error {
	return errors.New("unsupported")
}

func (*factorySupervisorBackend) Quiescent(context.Context, factory.ScopeHandle) (bool, error) {
	return false, errors.New("unsupported")
}

func (*factorySupervisorBackend) ReleaseHostReservation(context.Context, string) error {
	return errors.New("unsupported")
}
