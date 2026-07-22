package configenv_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/testutil/configenv"
)

func TestRunIsolatesAndRestoresAgentConfigEnvironment(t *testing.T) {
	ambientRoot := t.TempDir()
	ambientHome := filepath.Join(ambientRoot, "home")
	ambientOroHome := filepath.Join(ambientRoot, "oro-home")
	t.Setenv("HOME", ambientHome)
	t.Setenv("ORO_HOME", ambientOroHome)

	called := false
	code := configenv.Run(func() int {
		called = true
		if got := os.Getenv("HOME"); got == "" || got == ambientHome {
			t.Errorf("HOME = %q, want isolated temporary home", got)
		}
		if got := os.Getenv("ORO_HOME"); got == "" || got == ambientOroHome {
			t.Errorf("ORO_HOME = %q, want isolated temporary Oro home", got)
		}
		return 23
	})

	if !called {
		t.Fatal("Run did not invoke test runner")
	}
	if code != 23 {
		t.Fatalf("Run exit code = %d, want 23", code)
	}
	if got := os.Getenv("HOME"); got != ambientHome {
		t.Fatalf("HOME after Run = %q, want restored %q", got, ambientHome)
	}
	if got := os.Getenv("ORO_HOME"); got != ambientOroHome {
		t.Fatalf("ORO_HOME after Run = %q, want restored %q", got, ambientOroHome)
	}
}

func TestRunPreservesExternalGoCaches(t *testing.T) {
	ambientRoot := t.TempDir()
	ambientHome := filepath.Join(ambientRoot, "home")
	ambientGoCache := filepath.Join(ambientRoot, "go-build")
	t.Setenv("HOME", ambientHome)
	t.Setenv("GOCACHE", ambientGoCache)
	t.Setenv("GOMODCACHE", "")
	t.Setenv("TEST_TELEMETRY_DIR", filepath.Join(ambientRoot, "telemetry-disabled"))

	code := configenv.Run(func() int {
		isolatedHome := os.Getenv("HOME")
		for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
			value := os.Getenv(key)
			if value == "" {
				t.Errorf("%s is empty", key)
				continue
			}
			if rel, err := filepath.Rel(isolatedHome, value); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				t.Errorf("%s = %q, want path outside temporary HOME %q", key, value, isolatedHome)
			}
		}
		if got := os.Getenv("GOCACHE"); got != ambientGoCache {
			t.Errorf("GOCACHE = %q, want preserved %q", got, ambientGoCache)
		}
		return 0
	})

	if code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	if got := os.Getenv("GOCACHE"); got != ambientGoCache {
		t.Fatalf("GOCACHE after Run = %q, want restored %q", got, ambientGoCache)
	}
	if got := os.Getenv("GOMODCACHE"); got != "" {
		t.Fatalf("GOMODCACHE after Run = %q, want restored empty value", got)
	}
}

func TestRunDisablesTelemetryWhileResolvingMissingGoCache(t *testing.T) {
	ambientRoot := t.TempDir()
	ambientHome := filepath.Join(ambientRoot, "home")
	binDir := t.TempDir()
	goPath := filepath.Join(binDir, "go")
	telemetryPath := filepath.Join(ambientHome, "telemetry")
	goScript := "#!/bin/sh\n" +
		"if [ \"$GOTELEMETRY\" != off ]; then mkdir -p \"$HOME/telemetry\"; fi\n" +
		"printf '%s\\n%s\\n' \"$GOCACHE\" \"/external/go-mod\"\n"
	if err := os.WriteFile(goPath, []byte(goScript), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", ambientHome)
	t.Setenv("GOCACHE", filepath.Join(ambientRoot, "go-build"))
	t.Setenv("GOMODCACHE", "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if code := configenv.Run(func() int { return 0 }); code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
	if _, err := os.Stat(telemetryPath); !os.IsNotExist(err) {
		t.Fatalf("go env telemetry path = %q, err = %v; want no caller-HOME writes", telemetryPath, err)
	}
}

func TestRunSkipsGoLookupWhenBothCachesAreSet(t *testing.T) {
	cacheRoot := t.TempDir()
	binDir := t.TempDir()
	goPath := filepath.Join(binDir, "go")
	if err := os.WriteFile(goPath, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOCACHE", filepath.Join(cacheRoot, "go-build"))
	t.Setenv("GOMODCACHE", filepath.Join(cacheRoot, "go-mod"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if code := configenv.Run(func() int { return 0 }); code != 0 {
		t.Fatalf("Run exit code = %d, want 0", code)
	}
}
