package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunProcessProbe(t *testing.T) {
	t.Run("extracts --data-dir when flag appears after sql-server", func(t *testing.T) {
		oroHome := t.TempDir()
		expectedDataDir := filepath.Join(oroHome, "dolt")

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 42, nil },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("unused") },
			readPSArgs: func(_ int) (string, error) {
				return "dolt sql-server --host 127.0.0.1 --port 13307 --data-dir " + expectedDataDir, nil
			},
		}

		pid, dataDir, err := runProcessProbeWith(oroHome, probe)
		if err != nil {
			t.Fatalf("runProcessProbeWith error: %v", err)
		}
		if pid != 42 {
			t.Errorf("pid = %d, want 42", pid)
		}
		if dataDir != expectedDataDir {
			t.Errorf("dataDir = %q, want %q", dataDir, expectedDataDir)
		}
	})

	t.Run("extracts --data-dir=VALUE form", func(t *testing.T) {
		oroHome := t.TempDir()
		expectedDataDir := filepath.Join(oroHome, "dolt")

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 7, nil },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("unused") },
			readPSArgs: func(_ int) (string, error) {
				return "dolt sql-server --data-dir=" + expectedDataDir + " --port 13307", nil
			},
		}

		pid, dataDir, err := runProcessProbeWith(oroHome, probe)
		if err != nil {
			t.Fatalf("runProcessProbeWith error: %v", err)
		}
		if pid != 7 {
			t.Errorf("pid = %d, want 7", pid)
		}
		if dataDir != expectedDataDir {
			t.Errorf("dataDir = %q, want %q", dataDir, expectedDataDir)
		}
	})

	t.Run("extracts --data-dir when flag appears before sql-server", func(t *testing.T) {
		oroHome := t.TempDir()
		expectedDataDir := filepath.Join(oroHome, "dolt")

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 99, nil },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("unused") },
			readPSArgs: func(_ int) (string, error) {
				return "dolt --data-dir " + expectedDataDir + " sql-server --port 13307", nil
			},
		}

		_, dataDir, err := runProcessProbeWith(oroHome, probe)
		if err != nil {
			t.Fatalf("runProcessProbeWith error: %v", err)
		}
		if dataDir != expectedDataDir {
			t.Errorf("dataDir = %q, want %q", dataDir, expectedDataDir)
		}
	})

	t.Run("returns ErrDataDirMismatch when data-dir != oroHome/dolt", func(t *testing.T) {
		oroHome := t.TempDir()

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 10, nil },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("unused") },
			readPSArgs: func(_ int) (string, error) {
				return "dolt sql-server --data-dir /some/other/path", nil
			},
		}

		_, _, err := runProcessProbeWith(oroHome, probe)
		if !errors.Is(err, ErrDataDirMismatch) {
			t.Errorf("err = %v, want ErrDataDirMismatch", err)
		}
	})

	t.Run("returns ErrDataDirMismatch when --data-dir missing from args", func(t *testing.T) {
		oroHome := t.TempDir()

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 5, nil },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("unused") },
			readPSArgs: func(_ int) (string, error) {
				return "dolt sql-server --port 13307", nil
			},
		}

		_, _, err := runProcessProbeWith(oroHome, probe)
		if !errors.Is(err, ErrDataDirMismatch) {
			t.Errorf("err = %v, want ErrDataDirMismatch", err)
		}
	})

	t.Run("returns ErrCannotIdentify when both PID file and lsof fail", func(t *testing.T) {
		oroHome := t.TempDir()

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 0, errors.New("no pid file") },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("no listener") },
			readPSArgs:  func(_ int) (string, error) { return "", errors.New("unused") },
		}

		_, _, err := runProcessProbeWith(oroHome, probe)
		if !errors.Is(err, ErrCannotIdentify) {
			t.Errorf("err = %v, want ErrCannotIdentify", err)
		}
	})

	t.Run("returns ErrCannotIdentify with hint when lsof unavailable", func(t *testing.T) {
		oroHome := t.TempDir()

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 0, errors.New("no pid file") },
			discoverPID: func(_ int) (int, error) { return 0, exec.ErrNotFound },
			readPSArgs:  func(_ int) (string, error) { return "", errors.New("unused") },
		}

		_, _, err := runProcessProbeWith(oroHome, probe)
		if !errors.Is(err, ErrCannotIdentify) {
			t.Errorf("err = %v, want ErrCannotIdentify", err)
		}
	})

	t.Run("falls back to discoverPID when PID file missing", func(t *testing.T) {
		oroHome := t.TempDir()
		expectedDataDir := filepath.Join(oroHome, "dolt")

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 0, errors.New("no pid file") },
			discoverPID: func(_ int) (int, error) { return 55, nil },
			readPSArgs: func(pid int) (string, error) {
				if pid != 55 {
					return "", errors.New("unexpected pid")
				}
				return "dolt sql-server --data-dir " + expectedDataDir, nil
			},
		}

		pid, dataDir, err := runProcessProbeWith(oroHome, probe)
		if err != nil {
			t.Fatalf("runProcessProbeWith error: %v", err)
		}
		if pid != 55 {
			t.Errorf("pid = %d, want 55", pid)
		}
		if dataDir != expectedDataDir {
			t.Errorf("dataDir = %q, want %q", dataDir, expectedDataDir)
		}
	})

	t.Run("returns ErrCannotIdentify when ps fails for PID", func(t *testing.T) {
		oroHome := t.TempDir()

		probe := processProbe{
			readPIDFile: func(_ string) (int, error) { return 42, nil },
			discoverPID: func(_ int) (int, error) { return 0, errors.New("unused") },
			readPSArgs:  func(_ int) (string, error) { return "", errors.New("process not found") },
		}

		_, _, err := runProcessProbeWith(oroHome, probe)
		if !errors.Is(err, ErrCannotIdentify) {
			t.Errorf("err = %v, want ErrCannotIdentify", err)
		}
	})
}

func TestServerIdentityCookie(t *testing.T) {
	t.Run("writeServerIdentity writes JSON with all required fields", func(t *testing.T) {
		oroHome := t.TempDir()

		ident := serverIdentity{
			PID:        1234,
			StartTime:  "Mon Apr 20 10:30:45 2026",
			DataDir:    filepath.Join(oroHome, "dolt"),
			ObservedAt: time.Now(),
		}

		err := writeServerIdentity(oroHome, ident)
		if err != nil {
			t.Fatalf("writeServerIdentity error: %v", err)
		}

		cookiePath := filepath.Join(oroHome, "dolt", ".server-identity.json")
		data, err := os.ReadFile(cookiePath)
		if err != nil {
			t.Fatalf("failed to read cookie file: %v", err)
		}

		var parsed serverIdentity
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}

		if parsed.PID != 1234 {
			t.Errorf("PID = %d, want 1234", parsed.PID)
		}
		if parsed.StartTime != "Mon Apr 20 10:30:45 2026" {
			t.Errorf("StartTime = %q, want %q", parsed.StartTime, "Mon Apr 20 10:30:45 2026")
		}
		if parsed.DataDir != filepath.Join(oroHome, "dolt") {
			t.Errorf("DataDir = %q, want %q", parsed.DataDir, filepath.Join(oroHome, "dolt"))
		}
	})

	t.Run("readServerIdentity parses valid fresh cookie", func(t *testing.T) {
		oroHome := t.TempDir()
		currentStartTime, err := getProcessStartTime(os.Getpid())
		if err != nil {
			t.Fatalf("failed to get process start time: %v", err)
		}

		ident := serverIdentity{
			PID:        os.Getpid(),
			StartTime:  currentStartTime,
			DataDir:    filepath.Join(oroHome, "dolt"),
			ObservedAt: time.Now(),
		}

		if err := writeServerIdentity(oroHome, ident); err != nil {
			t.Fatalf("writeServerIdentity error: %v", err)
		}

		read, err := readServerIdentity(oroHome)
		if err != nil {
			t.Fatalf("readServerIdentity error: %v", err)
		}

		if read.PID != os.Getpid() {
			t.Errorf("PID = %d, want %d", read.PID, os.Getpid())
		}
		if read.StartTime != currentStartTime {
			t.Errorf("StartTime mismatch")
		}
	})

	t.Run("readServerIdentity returns ErrNoCookie when file missing", func(t *testing.T) {
		oroHome := t.TempDir()

		_, err := readServerIdentity(oroHome)
		if !errors.Is(err, ErrNoCookie) {
			t.Errorf("err = %v, want ErrNoCookie", err)
		}
	})

	t.Run("readServerIdentity returns ErrInvalidCookie for corrupt JSON", func(t *testing.T) {
		oroHome := t.TempDir()

		doltDir := filepath.Join(oroHome, "dolt")
		if err := os.MkdirAll(doltDir, 0o750); err != nil {
			t.Fatalf("mkdir error: %v", err)
		}

		cookiePath := filepath.Join(doltDir, ".server-identity.json")
		if err := os.WriteFile(cookiePath, []byte("not valid json"), 0o600); err != nil {
			t.Fatalf("write error: %v", err)
		}

		_, err := readServerIdentity(oroHome)
		if !errors.Is(err, ErrInvalidCookie) {
			t.Errorf("err = %v, want ErrInvalidCookie", err)
		}
	})

	t.Run("readServerIdentity returns ErrStaleCookie when start_time mismatches", func(t *testing.T) {
		oroHome := t.TempDir()

		ident := serverIdentity{
			PID:        os.Getpid(),
			StartTime:  "Mon Apr 20 10:00:00 2026",
			DataDir:    filepath.Join(oroHome, "dolt"),
			ObservedAt: time.Now(),
		}

		if err := writeServerIdentity(oroHome, ident); err != nil {
			t.Fatalf("writeServerIdentity error: %v", err)
		}

		_, err := readServerIdentity(oroHome)
		if !errors.Is(err, ErrStaleCookie) {
			t.Errorf("err = %v, want ErrStaleCookie for start_time mismatch", err)
		}
	})

	t.Run("readServerIdentity returns ErrStaleCookie when cookie age >60s", func(t *testing.T) {
		oroHome := t.TempDir()
		currentStartTime, err := getProcessStartTime(os.Getpid())
		if err != nil {
			t.Fatalf("failed to get process start time: %v", err)
		}

		ident := serverIdentity{
			PID:        os.Getpid(),
			StartTime:  currentStartTime,
			DataDir:    filepath.Join(oroHome, "dolt"),
			ObservedAt: time.Now().Add(-61 * time.Second),
		}

		if err := writeServerIdentity(oroHome, ident); err != nil {
			t.Fatalf("writeServerIdentity error: %v", err)
		}

		_, err = readServerIdentity(oroHome)
		if !errors.Is(err, ErrStaleCookie) {
			t.Errorf("err = %v, want ErrStaleCookie for age >60s", err)
		}
	})

	t.Run("readServerIdentity returns ErrStaleCookie when PID not found", func(t *testing.T) {
		oroHome := t.TempDir()

		ident := serverIdentity{
			PID:        999999,
			StartTime:  "Mon Apr 20 10:00:00 2026",
			DataDir:    filepath.Join(oroHome, "dolt"),
			ObservedAt: time.Now(),
		}

		if err := writeServerIdentity(oroHome, ident); err != nil {
			t.Fatalf("writeServerIdentity error: %v", err)
		}

		_, err := readServerIdentity(oroHome)
		if !errors.Is(err, ErrStaleCookie) {
			t.Errorf("err = %v, want ErrStaleCookie when PID not found", err)
		}
	})
}

// TestRunIdentityProbe_Matrix exercises the orchestration logic of runIdentityProbeImpl.
// Each case controls injected dependencies to avoid spawning real processes or
// connecting to a real dolt server.
func TestRunIdentityProbe_Matrix(t *testing.T) {
	const testDBName = "beads"
	const testStartTime = "Mon Apr 20 12:00:00 2026"
	const testStartTimeAlt = "Mon Apr 20 13:00:00 2026"

	type tc struct {
		name string

		// --- Cookie setup ---
		// cookieAge == 0 means no cookie file.
		cookieAge       time.Duration
		cookiePID       int
		cookieStart     string
		cookieDataDir   string // empty → use oroHome+"/dolt"
		cookieDBPresent bool

		// --- PID file (0 = no file written) ---
		pidInFile int

		// --- Injected probe fns ---
		aliveFn    func(pid int) bool // nil → always false
		getArgsFn  func(pid int) (string, error)
		getStartFn func(pid int) (string, error)
		findPIDFn  func() (int, error)
		sqlFn      func(dbName string) (bool, error) // nil → exec.ErrNotFound (dolt absent)

		// --- Expected outcomes ---
		wantErr         bool
		wantErrContains string
		wantPID         int    // 0 = don't assert
		wantDataDirSufx string // expected DataDir suffix (relative to oroHome)
		wantDBPresent   bool
	}

	cases := []tc{
		{
			name:            "cookie_fresh_pid_alive_start_matches",
			cookieAge:       30 * time.Second,
			cookiePID:       100,
			cookieStart:     testStartTime,
			cookieDBPresent: true,
			aliveFn:         func(pid int) bool { return pid == 100 },
			getStartFn: func(pid int) (string, error) {
				if pid == 100 {
					return testStartTime, nil
				}
				return "", fmt.Errorf("unexpected pid %d", pid)
			},
			getArgsFn: func(_ int) (string, error) {
				return "", errors.New("getArgsFn should not be called on cookie short-circuit")
			},
			findPIDFn: func() (int, error) {
				return 0, errors.New("findPIDFn should not be called on cookie short-circuit")
			},
			wantErr:         false,
			wantPID:         100,
			wantDataDirSufx: "dolt",
			wantDBPresent:   true,
		},
		{
			name:        "cookie_fresh_pid_dead_falls_through_to_probes",
			cookieAge:   30 * time.Second,
			cookiePID:   100,
			cookieStart: testStartTime,
			pidInFile:   200,
			aliveFn:     func(pid int) bool { return pid == 200 },
			getStartFn: func(_ int) (string, error) {
				return testStartTime, nil
			},
			findPIDFn:     func() (int, error) { return 0, errors.New("lsof: no listener") },
			sqlFn:         func(_ string) (bool, error) { return true, nil },
			wantErr:       false,
			wantPID:       200,
			wantDBPresent: true,
		},
		{
			name:          "cookie_stale_runs_probes",
			cookieAge:     90 * time.Second,
			cookiePID:     100,
			cookieStart:   testStartTime,
			pidInFile:     100,
			aliveFn:       func(pid int) bool { return pid == 100 },
			getStartFn:    func(_ int) (string, error) { return testStartTimeAlt, nil },
			findPIDFn:     func() (int, error) { return 0, errors.New("lsof: no listener") },
			sqlFn:         func(_ string) (bool, error) { return true, nil },
			wantErr:       false,
			wantPID:       100,
			wantDBPresent: true,
		},
		{
			name:       "no_cookie_no_pid_no_lsof",
			aliveFn:    func(_ int) bool { return false },
			getArgsFn:  func(_ int) (string, error) { return "", nil },
			getStartFn: func(_ int) (string, error) { return "", nil },
			findPIDFn: func() (int, error) {
				return 0, fmt.Errorf("no process found listening on port %d", SharedDoltPort)
			},
			sqlFn:           func(_ string) (bool, error) { return false, nil },
			wantErr:         true,
			wantErrContains: "cannot identify dolt owner",
		},
		{
			name:      "data_dir_mismatch",
			pidInFile: 100,
			aliveFn:   func(pid int) bool { return pid == 100 },
			getArgsFn: func(_ int) (string, error) {
				return "dolt sql-server --host 127.0.0.1 --port 13307 --data-dir /wrong/path", nil
			},
			getStartFn:      func(_ int) (string, error) { return testStartTime, nil },
			findPIDFn:       func() (int, error) { return 0, errors.New("unused") },
			sqlFn:           func(_ string) (bool, error) { return false, nil },
			wantErr:         true,
			wantErrContains: "process_data_dir_mismatch",
		},
		{
			name:          "process_ok_sql_ok",
			pidInFile:     300,
			aliveFn:       func(pid int) bool { return pid == 300 },
			getStartFn:    func(_ int) (string, error) { return testStartTime, nil },
			findPIDFn:     func() (int, error) { return 0, errors.New("unused") },
			sqlFn:         func(_ string) (bool, error) { return true, nil },
			wantErr:       false,
			wantPID:       300,
			wantDBPresent: true,
		},
		{
			name:          "process_ok_dolt_absent_warn_no_error",
			pidInFile:     400,
			aliveFn:       func(pid int) bool { return pid == 400 },
			getStartFn:    func(_ int) (string, error) { return testStartTime, nil },
			findPIDFn:     func() (int, error) { return 0, errors.New("unused") },
			sqlFn:         func(_ string) (bool, error) { return false, exec.ErrNotFound },
			wantErr:       false,
			wantPID:       400,
			wantDBPresent: false,
		},
		{
			name:       "process_ok_sql_fail_dolt_present",
			pidInFile:  500,
			aliveFn:    func(pid int) bool { return pid == 500 },
			getStartFn: func(_ int) (string, error) { return testStartTime, nil },
			findPIDFn:  func() (int, error) { return 0, errors.New("unused") },
			sqlFn: func(_ string) (bool, error) {
				return false, fmt.Errorf("dial tcp 127.0.0.1:13307: connection refused")
			},
			wantErr:         true,
			wantErrContains: "sql probe",
		},
		{
			name:          "lsof_fallback_when_pid_file_missing",
			aliveFn:       func(pid int) bool { return pid == 600 },
			getStartFn:    func(_ int) (string, error) { return testStartTime, nil },
			findPIDFn:     func() (int, error) { return 600, nil },
			sqlFn:         func(_ string) (bool, error) { return true, nil },
			wantErr:       false,
			wantPID:       600,
			wantDBPresent: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oroHome := t.TempDir()
			doltDir := filepath.Join(oroHome, "dolt")
			if err := os.MkdirAll(doltDir, 0o750); err != nil {
				t.Fatalf("mkdir doltDir: %v", err)
			}

			if c.cookieAge > 0 {
				cookiePath := filepath.Join(doltDir, ".server-identity.json")
				dataDir := c.cookieDataDir
				if dataDir == "" {
					dataDir = doltDir
				}
				cookie := serverIdentity{
					PID:             c.cookiePID,
					StartTime:       c.cookieStart,
					DataDir:         dataDir,
					DatabasePresent: c.cookieDBPresent,
					ObservedAt:      time.Now().Add(-c.cookieAge),
				}
				data, err := json.Marshal(cookie)
				if err != nil {
					t.Fatalf("marshal cookie: %v", err)
				}
				if err := os.WriteFile(cookiePath, data, 0o600); err != nil {
					t.Fatalf("write cookie: %v", err)
				}
			}

			if c.pidInFile != 0 {
				pidPath := filepath.Join(oroHome, "dolt-server.pid")
				if err := os.WriteFile(pidPath, []byte(strconv.Itoa(c.pidInFile)), 0o600); err != nil {
					t.Fatalf("write pid file: %v", err)
				}
			}

			getArgsFn := c.getArgsFn
			if getArgsFn == nil {
				getArgsFn = func(_ int) (string, error) {
					return fmt.Sprintf("dolt sql-server --host 127.0.0.1 --port 13307 --data-dir %s", doltDir), nil
				}
			}
			if c.name == "cookie_fresh_pid_dead_falls_through_to_probes" ||
				c.name == "cookie_stale_runs_probes" ||
				c.name == "process_ok_sql_ok" ||
				c.name == "process_ok_dolt_absent_warn_no_error" ||
				c.name == "process_ok_sql_fail_dolt_present" ||
				c.name == "lsof_fallback_when_pid_file_missing" {
				getArgsFn = func(_ int) (string, error) {
					return fmt.Sprintf("dolt sql-server --host 127.0.0.1 --port 13307 --data-dir %s", doltDir), nil
				}
			}

			aliveFn := c.aliveFn
			if aliveFn == nil {
				aliveFn = func(_ int) bool { return false }
			}

			sqlFn := c.sqlFn
			if sqlFn == nil {
				sqlFn = func(_ string) (bool, error) { return false, exec.ErrNotFound }
			}

			cfg := &identityProbeConfig{
				oroHome:    oroHome,
				dbName:     testDBName,
				nowFn:      time.Now,
				aliveFn:    aliveFn,
				getArgsFn:  getArgsFn,
				getStartFn: c.getStartFn,
				findPIDFn:  c.findPIDFn,
				sqlFn:      sqlFn,
			}

			result, err := runIdentityProbeImpl(cfg)

			if c.wantErr {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (result=%+v)", c.wantErrContains, result)
				}
				if c.wantErrContains != "" && !strings.Contains(err.Error(), c.wantErrContains) {
					t.Errorf("error %q does not contain %q", err.Error(), c.wantErrContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if c.wantPID != 0 && result.PID != c.wantPID {
				t.Errorf("PID = %d, want %d", result.PID, c.wantPID)
			}

			if c.wantDataDirSufx != "" {
				expected := filepath.Join(oroHome, c.wantDataDirSufx)
				if filepath.Clean(result.DataDir) != filepath.Clean(expected) {
					t.Errorf("DataDir = %q, want suffix %q (full: %q)", result.DataDir, c.wantDataDirSufx, expected)
				}
			}

			if result.DatabasePresent != c.wantDBPresent {
				t.Errorf("DatabasePresent = %v, want %v", result.DatabasePresent, c.wantDBPresent)
			}
		})
	}
}
