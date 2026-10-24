//go:build !darwin

package main

import (
	"context"
	"errors"

	"oro/pkg/factory"
)

type factoryLaunchdRuntime struct{}

func openFactoryLaunchdRuntime(context.Context, factoryLaunchdConfig) (*factoryLaunchdRuntime, error) {
	return nil, errors.New("managed factory launchd runtime is unsupported on this platform")
}
func (*factoryLaunchdRuntime) Close() error { return nil }
func (*factoryLaunchdRuntime) Invoke(context.Context, factory.HostInvocation) (factory.HostResult, error) {
	return factory.HostResult{}, errors.New("managed factory launchd runtime is unsupported on this platform")
}

func (*factoryLaunchdRuntime) Retire(context.Context, factory.ScopeHandle) error {
	return errors.New("managed factory launchd runtime is unsupported on this platform")
}
