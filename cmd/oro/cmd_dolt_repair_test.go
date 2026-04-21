package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func TestDoltRepairCmd_Matrix(t *testing.T) {
	alwaysLock := func(_ string) (func() error, error) {
		return func() error { return nil }, nil
	}

	tests := []struct {
		name     string
		makeDeps func(oroHome string) doltRepairDeps
		wantCode int // 0 means expect nil error
	}{
		{
			name: "probe already passes exits 0",
			makeDeps: func(oroHome string) doltRepairDeps {
				return doltRepairDeps{
					oroHome:     oroHome,
					lockFn:      alwaysLock,
					probeFn:     func() (int, string, error) { return 42, "/good/dolt", nil },
					dbPresentFn: func() bool { return true },
				}
			},
			wantCode: 0,
		},
		{
			name: "flock contended exits 5",
			makeDeps: func(oroHome string) doltRepairDeps {
				return doltRepairDeps{
					oroHome: oroHome,
					lockFn: func(_ string) (func() error, error) {
						return nil, ErrFlockContended
					},
				}
			},
			wantCode: 5,
		},
		{
			name: "cannot identify owner exits 2",
			makeDeps: func(oroHome string) doltRepairDeps {
				return doltRepairDeps{
					oroHome: oroHome,
					lockFn:  alwaysLock,
					probeFn: func() (int, string, error) {
						return 0, "", fmt.Errorf("%w: no pid file, lsof unavailable", ErrCannotIdentify)
					},
				}
			},
			wantCode: 2,
		},
		{
			name: "data dir right but database missing exits 4",
			makeDeps: func(oroHome string) doltRepairDeps {
				return doltRepairDeps{
					oroHome:     oroHome,
					lockFn:      alwaysLock,
					probeFn:     func() (int, string, error) { return 42, "/good/dolt", nil },
					dbPresentFn: func() bool { return false },
				}
			},
			wantCode: 4,
		},
		{
			name: "rogue owned by different UID exits 2",
			makeDeps: func(oroHome string) doltRepairDeps {
				return doltRepairDeps{
					oroHome:    oroHome,
					lockFn:     alwaysLock,
					probeFn:    func() (int, string, error) { return 999, "/wrong/dir", ErrDataDirMismatch },
					ownerFn:    func(_ int) (int, error) { return 0, nil }, // owned by root
					currentUID: 1000,                                       // current user is not root
				}
			},
			wantCode: 2,
		},
		{
			name: "repair attempt still fails exits 3",
			makeDeps: func(oroHome string) doltRepairDeps {
				call := 0
				return doltRepairDeps{
					oroHome: oroHome,
					lockFn:  alwaysLock,
					probeFn: func() (int, string, error) {
						call++
						if call == 1 {
							return 999, "/wrong/dir", ErrDataDirMismatch
						}
						return 0, "", ErrDataDirMismatch // re-probe still fails
					},
					ownerFn:     func(_ int) (int, error) { return os.Getuid(), nil },
					currentUID:  os.Getuid(),
					killFn:      func(_ int) error { return nil },
					kickstartFn: func() bool { return true },
					waitPortFn:  func(_ int, _ time.Duration) bool { return false },
					dbPresentFn: func() bool { return false },
				}
			},
			wantCode: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oroHome := t.TempDir()
			deps := tt.makeDeps(oroHome)
			err := runDoltRepair(deps, io.Discard)
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("runDoltRepair error: %v", err)
				}
				return
			}
			var ee *exitError
			if !errors.As(err, &ee) {
				t.Fatalf("expected *exitError, got %T: %v", err, err)
			}
			if ee.code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", ee.code, tt.wantCode)
			}
		})
	}
}
