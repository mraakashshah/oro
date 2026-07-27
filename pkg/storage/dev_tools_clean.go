package storage

import (
	"context"
	"errors"
	"fmt"
)

// ProviderMaintenanceRunner runs one trusted provider maintenance operation.
type ProviderMaintenanceRunner func(context.Context, ProviderMaintenance) (MaintenanceEvidence, error)

// DevToolsCleanupRequest supplies dependencies for a one-shot dev-tools cleanup.
type DevToolsCleanupRequest struct {
	Providers []CacheProvider
	Run       ProviderMaintenanceRunner
}

// DevToolsCleanupResult contains exact provider evidence and aggregate disk
// summary values for a one-shot developer-tool cache cleanup.
type DevToolsCleanupResult struct {
	Providers  []MaintenanceEvidence `json:"providers"`
	FreedBytes uint64                `json:"freed_bytes"`
	FreeBytes  uint64                `json:"free_bytes"`
}

// RunDevToolsCleanup runs the trusted maintenance operation for every eligible
// developer-tool cache provider. Providers without maintenance authorization
// are skipped, and evidence is retained even when another provider fails.
func RunDevToolsCleanup(ctx context.Context, request DevToolsCleanupRequest) (DevToolsCleanupResult, error) {
	runner := request.Run
	if runner == nil {
		runner = RunProviderMaintenance
	}

	result := DevToolsCleanupResult{Providers: make([]MaintenanceEvidence, 0, len(request.Providers))}
	var runErrs []error
	for _, provider := range request.Providers {
		if provider.Concurrency == NoMaintenance || !provider.Cleaner.present() {
			continue
		}
		evidence, err := runner(ctx, ProviderMaintenance{Provider: provider})
		result.Providers = append(result.Providers, evidence)
		result.FreedBytes += freedBytes(evidence)
		if evidence.After.FreeBytes > result.FreeBytes {
			result.FreeBytes = evidence.After.FreeBytes
		}
		if err != nil {
			runErrs = append(runErrs, fmt.Errorf("run dev-tools provider %q: %w", provider.ID, err))
		}
	}
	return result, errors.Join(runErrs...)
}

func freedBytes(evidence MaintenanceEvidence) uint64 {
	if evidence.After.FreeBytes <= evidence.Before.FreeBytes {
		return 0
	}
	return evidence.After.FreeBytes - evidence.Before.FreeBytes
}
