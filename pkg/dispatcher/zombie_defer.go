package dispatcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const zombieDeferredUntil = "2099-01-01T00:00:00Z"

type exportedBeadDeferState struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	DeferUntil string `json:"defer_until"`
}

func (d *Dispatcher) detectZombieDeferred(ctx context.Context) (fixed int, err error) {
	out, err := d.beads.Export(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "zombie_defer_check_failed", "dispatcher", "", "", err.Error())
		return 0, fmt.Errorf("export beads for zombie defer check: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var bead exportedBeadDeferState
		if err := json.Unmarshal([]byte(line), &bead); err != nil {
			_ = d.logEvent(ctx, "zombie_defer_parse_failed", "dispatcher", "", "", err.Error())
			continue
		}
		if bead.ID == "" || bead.Status != "open" || bead.DeferUntil == "" {
			continue
		}

		_ = d.logEvent(ctx, "zombie_deferred_detected", "dispatcher", bead.ID, "", bead.DeferUntil)
		if err := d.beads.Defer(ctx, bead.ID, zombieDeferredUntil); err != nil {
			_ = d.logEvent(ctx, "zombie_defer_fix_failed", "dispatcher", bead.ID, "", fmt.Sprintf("defer: %v", err))
			continue
		}
		if err := d.beads.Undefer(ctx, bead.ID); err != nil {
			_ = d.logEvent(ctx, "zombie_defer_fix_failed", "dispatcher", bead.ID, "", fmt.Sprintf("undefer: %v", err))
			continue
		}
		fixed++
	}
	if err := scanner.Err(); err != nil {
		_ = d.logEvent(ctx, "zombie_defer_parse_failed", "dispatcher", "", "", err.Error())
	}
	return fixed, nil
}
