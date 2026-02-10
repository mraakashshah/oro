package dispatcher

import (
	"crypto/sha256"
	"encoding/hex"
)

// maxStuckCount is the number of consecutive identical QG output hashes before
// the dispatcher considers the worker stuck and escalates instead of re-assigning.
const maxStuckCount = 3

// qgHistory tracks consecutive QG output hashes for a single bead to detect
// when a worker is repeating the same mistake.
type qgHistory struct {
	hashes []string // last N QG output hashes (most recent at end)
}

// hashQGOutput returns a hex-encoded SHA-256 hash of the QG output string.
func hashQGOutput(output string) string {
	h := sha256.Sum256([]byte(output))
	return hex.EncodeToString(h[:])
}

// isQGStuck records the QG output hash for a bead and returns true if the same
// hash has appeared maxStuckCount or more consecutive times. Caller must NOT
// hold d.mu.
func (d *Dispatcher) isQGStuck(beadID, qgOutput string) bool {
	hash := hashQGOutput(qgOutput)

	d.mu.Lock()
	defer d.mu.Unlock()

	hist, ok := d.qgStuckTracker[beadID]
	if !ok {
		hist = &qgHistory{}
		d.qgStuckTracker[beadID] = hist
	}

	hist.hashes = append(hist.hashes, hash)

	// Check if the last maxStuckCount hashes are all identical.
	n := len(hist.hashes)
	if n < maxStuckCount {
		return false
	}

	latest := hist.hashes[n-1]
	for i := n - maxStuckCount; i < n; i++ {
		if hist.hashes[i] != latest {
			return false
		}
	}
	return true
}
