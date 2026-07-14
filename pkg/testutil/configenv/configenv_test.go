package configenv_test

import (
	"os"
	"path/filepath"
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
