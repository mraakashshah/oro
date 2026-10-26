//go:build darwin

package main

import "context"

func factoryServicePlatformSupported() error { return nil }
func bootstrapFactoryService(ctx context.Context, plistPath string) error {
	return bootstrapFactoryServiceWithRunner(ctx, plistPath, runFactoryLaunchctl)
}
