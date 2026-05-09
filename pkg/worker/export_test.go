package worker

import "oro/pkg/protocol"

// Test-only exports for internal symbols. Compiled only in test mode.

// ModelFamilyFn exposes modelFamily for package-external tests.
var ModelFamilyFn = modelFamily

// EffectiveThresholdKeyFn exposes effectiveThresholdKey for package-external tests.
var EffectiveThresholdKeyFn = effectiveThresholdKey

// ForThresholdKey builds a thresholds from a map and returns For(key).
func ForThresholdKey(models map[string]int, key string) int {
	return thresholds{models: models}.For(key)
}

// SetWorkerTier sets the tier on a Worker (for integration tests).
func SetWorkerTier(w *Worker, tier protocol.Tier) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tier = tier
}
