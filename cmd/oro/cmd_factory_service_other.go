//go:build !darwin

package main

import (
	"context"
	"errors"
)

func factoryServicePlatformSupported() error {
	return errors.New("factory service registration requires Darwin")
}
func bootstrapFactoryService(context.Context, string) error { return factoryServicePlatformSupported() }
