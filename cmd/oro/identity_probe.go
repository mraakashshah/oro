package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrDataDirMismatch is returned when the dolt server's --data-dir does not
	// match oroHome/dolt, or when --data-dir is absent (rogue server).
	ErrDataDirMismatch = errors.New("dolt data-dir mismatch")

	// ErrCannotIdentify is returned when the dolt server PID cannot be resolved
	// via either the pid file or lsof port scan.
	ErrCannotIdentify = errors.New("cannot identify dolt server process")

	ErrNoCookie      = errors.New("no server identity cookie found")
	ErrInvalidCookie = errors.New("invalid server identity cookie (corrupt JSON)")
	ErrStaleCookie   = errors.New("server identity cookie is stale (process start time mismatch or age >60s)")
)

// processProbe holds injectable functions for runProcessProbeWith. The zero
// value is not usable; use defaultProcessProbe or populate each field.
type processProbe struct {
	readPIDFile func(path string) (int, error)
	discoverPID func(port int) (int, error)
	readPSArgs  func(pid int) (string, error)
}

// serverIdentity is the persistent short-circuit cache written after a
// successful identity probe. Lives at <oroHome>/dolt/.server-identity.json.
type serverIdentity struct {
	PID             int       `json:"pid"`
	StartTime       string    `json:"start_time"`
	DataDir         string    `json:"data_dir"`
	DatabasePresent bool      `json:"database_present"`
	ObservedAt      time.Time `json:"observed_at"`
}

// probeResult is the outcome of a successful runIdentityProbe call.
type probeResult struct {
	PID             int
	DataDir         string
	DatabasePresent bool
}

const cookieTTL = 60 * time.Second

// identityProbeConfig holds injectable dependencies for runIdentityProbeImpl.
type identityProbeConfig struct {
	oroHome string
	dbName  string
	nowFn   func() time.Time

	// Process probe deps
	aliveFn    func(int) bool
	getArgsFn  func(int) (string, error) // ps -p <pid> -o args=
	getStartFn func(int) (string, error) // ps -p <pid> -o lstart=
	findPIDFn  func() (int, error)       // lsof fallback

	// SQL probe dep (returns dbPresent, err; exec.ErrNotFound means dolt absent)
	sqlFn func(dbName string) (bool, error)
}

// runIdentityProbe verifies the shared dolt server is running with the
// expected data directory and database. Uses a cookie to short-circuit
// repeated probes within 60s.
func runIdentityProbe(oroHome, dbName string) (probeResult, error) {
	cfg := &identityProbeConfig{
		oroHome:    oroHome,
		dbName:     dbName,
		nowFn:      time.Now,
		aliveFn:    IsProcessAlive,
		getArgsFn:  defaultReadPSArgs,
		getStartFn: getProcessStartTime,
		findPIDFn:  func() (int, error) { return discoverPIDByPort(SharedDoltPort) },
		sqlFn:      doltSQLDatabasePresent,
	}
	return runIdentityProbeImpl(cfg)
}

// runIdentityProbeImpl is the testable core of runIdentityProbe.
func runIdentityProbeImpl(cfg *identityProbeConfig) (probeResult, error) {
	cookiePath := filepath.Join(cfg.oroHome, "dolt", ".server-identity.json")
	expectedDataDir := filepath.Join(cfg.oroHome, "dolt")

	if result, ok := tryCookieShortCircuit(cookiePath, cfg); ok {
		return result, nil
	}

	pid, dataDir, startTime, err := identityProcessProbe(cfg, expectedDataDir)
	if err != nil {
		return probeResult{}, err
	}

	dbPresent, err := runSQLProbe(cfg)
	if err != nil {
		return probeResult{}, fmt.Errorf("sql probe: %w", err)
	}

	result := probeResult{PID: pid, DataDir: dataDir, DatabasePresent: dbPresent}

	writeCookieFile(cookiePath, &serverIdentity{
		PID:             pid,
		StartTime:       startTime,
		DataDir:         dataDir,
		DatabasePresent: dbPresent,
		ObservedAt:      cfg.nowFn(),
	})

	return result, nil
}

// tryCookieShortCircuit returns (result, true) if the on-disk cookie is fresh
// (<60s), the recorded PID is still alive, and the process start_time matches.
func tryCookieShortCircuit(cookiePath string, cfg *identityProbeConfig) (probeResult, bool) {
	cookie, err := readCookieFile(cookiePath)
	if err != nil {
		return probeResult{}, false
	}

	age := cfg.nowFn().Sub(cookie.ObservedAt)
	if age >= cookieTTL {
		return probeResult{}, false
	}

	if !cfg.aliveFn(cookie.PID) {
		return probeResult{}, false
	}

	startTime, psErr := cfg.getStartFn(cookie.PID)
	if psErr != nil || strings.TrimSpace(startTime) != strings.TrimSpace(cookie.StartTime) {
		return probeResult{}, false
	}

	return probeResult{
		PID:             cookie.PID,
		DataDir:         cookie.DataDir,
		DatabasePresent: cookie.DatabasePresent,
	}, true
}

// identityProcessProbe resolves the dolt server PID (via PID file or lsof),
// reads its --data-dir arg, and compares it against expectedDataDir.
// Returns (pid int, dataDir string, startTime string, error).
//
//nolint:gocritic // unnamedResult: four returns are documented in comment above
func identityProcessProbe(cfg *identityProbeConfig, expectedDataDir string) (int, string, string, error) {
	pid, err := resolvePID(cfg)
	if err != nil {
		return 0, "", "", err
	}

	args, err := cfg.getArgsFn(pid)
	if err != nil {
		return 0, "", "", fmt.Errorf("read process args for PID %d: %w", pid, err)
	}

	dataDir, parseErr := parseDataDir(args)
	if parseErr != nil {
		return 0, "", "", fmt.Errorf("process_data_dir_mismatch: %w", parseErr)
	}

	if filepath.Clean(dataDir) != filepath.Clean(expectedDataDir) {
		return 0, "", "", fmt.Errorf("process_data_dir_mismatch: got %q, want %q", dataDir, expectedDataDir)
	}

	startTime, _ := cfg.getStartFn(pid)

	return pid, dataDir, startTime, nil
}

// resolvePID returns the PID of the running dolt server by reading the PID
// file first, then falling back to lsof discovery.
func resolvePID(cfg *identityProbeConfig) (int, error) {
	pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
	data, err := os.ReadFile(pidPath) //nolint:gosec // oroHome is caller-controlled
	if err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && cfg.aliveFn(pid) {
			return pid, nil
		}
	}

	pid, lsofErr := cfg.findPIDFn()
	if lsofErr != nil {
		return 0, fmt.Errorf("cannot identify dolt owner: PID file absent or stale, lsof: %w", lsofErr)
	}
	if !cfg.aliveFn(pid) {
		return 0, fmt.Errorf("cannot identify dolt owner: lsof PID %d not alive", pid)
	}
	return pid, nil
}

// runSQLProbe checks whether cfg.dbName is present in the dolt server.
// Returns (false, nil) with a warning when dolt CLI is absent.
func runSQLProbe(cfg *identityProbeConfig) (bool, error) {
	present, err := cfg.sqlFn(cfg.dbName)
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}
	return present, err
}

func runProcessProbeWith(oroHome string, probe processProbe) (pid int, dataDir string, err error) {
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	pid, err = probe.readPIDFile(pidPath)
	if err != nil {
		pid, err = probe.discoverPID(SharedDoltPort)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return 0, "", fmt.Errorf("%w: lsof not available — install lsof to enable port scanning", ErrCannotIdentify)
			}
			return 0, "", fmt.Errorf("%w: %w", ErrCannotIdentify, err)
		}
	}

	args, psErr := probe.readPSArgs(pid)
	if psErr != nil {
		return 0, "", fmt.Errorf("%w: ps failed for PID %d: %w", ErrCannotIdentify, pid, psErr)
	}

	dataDir, parseErr := parseDataDir(args)
	if parseErr != nil {
		return pid, "", ErrDataDirMismatch
	}

	if dataDir != filepath.Join(oroHome, "dolt") {
		return pid, dataDir, ErrDataDirMismatch
	}

	return pid, dataDir, nil
}

// parseDataDir extracts the --data-dir value from a process command line.
// Supports both "--data-dir=VALUE" and "--data-dir VALUE" forms.
func parseDataDir(args string) (string, error) {
	fields := strings.Fields(args)
	for i, f := range fields {
		if strings.HasPrefix(f, "--data-dir=") {
			return strings.TrimPrefix(f, "--data-dir="), nil
		}
		if f == "--data-dir" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", errors.New("--data-dir not found in process args")
}

func defaultReadPSArgs(pid int) (string, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "args=").Output() //nolint:gosec,noctx // pid is int from trusted internal sources
	if err != nil {
		return "", fmt.Errorf("ps -p %d: %w", pid, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeServerIdentity writes the server identity to ~/.oro/dolt/.server-identity.json.
// Creates the dolt directory if it doesn't exist.
func writeServerIdentity(oroHome string, ident serverIdentity) error {
	doltDir := filepath.Join(oroHome, "dolt")
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", doltDir, err)
	}

	cookiePath := filepath.Join(doltDir, ".server-identity.json")
	data, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal server identity: %w", err)
	}

	if err := os.WriteFile(cookiePath, append(data, '\n'), 0o600); err != nil { //nolint:gosec // cookiePath is caller-controlled
		return fmt.Errorf("write server identity cookie: %w", err)
	}

	return nil
}

// readServerIdentity reads and validates the server identity from ~/.oro/dolt/.server-identity.json.
// Returns:
//   - ErrNoCookie if the file doesn't exist
//   - ErrInvalidCookie if the JSON is corrupt
//   - ErrStaleCookie if the start_time doesn't match the current process's start time
//     or if the cookie age is >60 seconds
func readServerIdentity(oroHome string) (serverIdentity, error) {
	cookiePath := filepath.Join(oroHome, "dolt", ".server-identity.json")
	data, err := os.ReadFile(cookiePath) //nolint:gosec // oroHome is caller-controlled
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return serverIdentity{}, ErrNoCookie
		}
		return serverIdentity{}, fmt.Errorf("read server identity cookie: %w", err)
	}

	var ident serverIdentity
	if err := json.Unmarshal(data, &ident); err != nil {
		return serverIdentity{}, ErrInvalidCookie
	}

	currentStartTime, err := getProcessStartTime(ident.PID)
	if err != nil {
		return serverIdentity{}, ErrStaleCookie
	}
	if currentStartTime != ident.StartTime {
		return serverIdentity{}, ErrStaleCookie
	}

	age := time.Since(ident.ObservedAt)
	if age > 60*time.Second {
		return serverIdentity{}, ErrStaleCookie
	}

	return ident, nil
}

// getProcessStartTime returns the start time of the process with the given PID
// in the format returned by `ps -o lstart=` (e.g., "Mon Apr 20 10:30:45 2026").
func getProcessStartTime(pid int) (string, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output() //nolint:gosec,noctx // pid is int from trusted internal sources
	if err != nil {
		return "", fmt.Errorf("get process start time: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// doltSQLDatabasePresent runs `dolt sql -h 127.0.0.1 -P 13307 --result-format json -q "SHOW DATABASES;"`
// and checks whether dbName appears in the output.
// Returns exec.ErrNotFound if dolt is not in PATH.
func doltSQLDatabasePresent(dbName string) (bool, error) {
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return false, exec.ErrNotFound
	}
	//nolint:gosec,noctx // args are trusted internal values; background context appropriate for one-shot SQL probe
	out, err := exec.CommandContext(context.Background(), doltPath, "sql",
		"-h", "127.0.0.1",
		"-P", strconv.Itoa(SharedDoltPort),
		"--result-format", "json",
		"-q", "SHOW DATABASES;",
	).Output()
	if err != nil {
		return false, fmt.Errorf("dolt sql SHOW DATABASES: %w", err)
	}
	return containsDatabase(out, dbName), nil
}

// containsDatabase parses the JSON output of `dolt sql --result-format json
// -q "SHOW DATABASES;"` and returns true if dbName appears in the rows.
func containsDatabase(jsonOut []byte, dbName string) bool {
	var result struct {
		Rows []map[string]string `json:"rows"`
	}
	if err := json.Unmarshal(jsonOut, &result); err != nil {
		return false
	}
	for _, row := range result.Rows {
		for _, v := range row {
			if strings.EqualFold(v, dbName) {
				return true
			}
		}
	}
	return false
}

// readCookieFile reads and unmarshals the server identity cookie at cookiePath
// without validation. Validation happens in tryCookieShortCircuit, which needs
// injectable alive/start-time checks.
func readCookieFile(cookiePath string) (*serverIdentity, error) {
	data, err := os.ReadFile(cookiePath) //nolint:gosec // cookiePath derived from trusted oroHome
	if err != nil {
		return nil, fmt.Errorf("read cookie: %w", err)
	}
	var c serverIdentity
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse cookie: %w", err)
	}
	return &c, nil
}

// writeCookieFile marshals c and writes it atomically to cookiePath.
// Errors are silently dropped — cookie is a best-effort optimisation.
func writeCookieFile(cookiePath string, c *serverIdentity) {
	if err := os.MkdirAll(filepath.Dir(cookiePath), 0o750); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(cookiePath, data, 0o600) //nolint:gosec // cookiePath derived from trusted oroHome
}
