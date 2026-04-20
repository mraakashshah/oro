package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
