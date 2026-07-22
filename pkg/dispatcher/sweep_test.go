package dispatcher //nolint:testpackage // white-box: verifies retired sweep symbols are absent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReviewQueueUnreachable(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for _, name := range []string{"sweep.go", "sweeper_loop.go"} {
		path := filepath.Join(filepath.Dir(filename), name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		if strings.Contains(text, "ExpireReviewQueue"+"SLA") {
			t.Errorf("%s still contains the retired review queue SLA sweep", name)
		}
		if strings.Contains(text, "review_queue_"+"sla_expired") {
			t.Errorf("%s still contains the retired review queue discard reason", name)
		}
	}
}
