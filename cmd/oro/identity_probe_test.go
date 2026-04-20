package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
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
