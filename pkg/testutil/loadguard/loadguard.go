package loadguard

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const defaultLoadPerCPUThreshold = 1.5

// SkipIfLoaded skips timing-sensitive tests when the host is already heavily loaded.
func SkipIfLoaded(t testing.TB) {
	t.Helper()

	if os.Getenv("ORO_LOADGUARD_DISABLE") == "1" {
		return
	}
	if os.Getenv("ORO_LOADGUARD_FORCE_SKIP") == "1" {
		t.Skip("host load guard forced by ORO_LOADGUARD_FORCE_SKIP")
	}

	load, ok := oneMinuteLoad()
	if !ok {
		return
	}
	cpus := runtime.NumCPU()
	if shouldSkip(load, cpus, defaultLoadPerCPUThreshold) {
		t.Skipf("host load is %.2f across %d CPUs; skipping timing-sensitive test", load, cpus)
	}
}

func shouldSkip(load float64, cpus int, threshold float64) bool {
	if cpus <= 0 || threshold <= 0 {
		return false
	}
	return load/float64(cpus) >= threshold
}

func oneMinuteLoad() (float64, bool) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if v, parseErr := strconv.ParseFloat(fields[0], 64); parseErr == nil {
				return v, true
			}
		}
	}

	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(strings.Trim(string(out), "{} \n\t"))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}
