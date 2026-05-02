package dispatcher //nolint:testpackage // white-box test for mock command runner

import (
	"context"
	"errors"
	"testing"
)

// mockCommandRunner records calls and returns pre-configured output or errors.
type mockCommandRunner struct {
	calls  []mockCall
	output []byte
	err    error
	// callFn, if set, overrides output/err based on the call.
	callFn func(ctx context.Context, name string, args ...string) ([]byte, error)
}

type mockCall struct {
	Name string
	Args []string
}

func (m *mockCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	if m.callFn != nil {
		return m.callFn(ctx, name, args...)
	}
	return m.output, m.err
}

func TestCommandRunnerNeutralOwner(t *testing.T) {
	t.Run("records calls and returns configured output", func(t *testing.T) {
		runner := &mockCommandRunner{output: []byte("hello")}
		out, err := runner.Run(context.Background(), "git", "status")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(out) != "hello" {
			t.Fatalf("output: got %q, want %q", out, "hello")
		}
		if len(runner.calls) != 1 || runner.calls[0].Name != "git" {
			t.Fatalf("calls not recorded correctly: %v", runner.calls)
		}
	})

	t.Run("callFn overrides static output", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		runner := &mockCommandRunner{
			callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return nil, sentinel
			},
		}
		_, err := runner.Run(context.Background(), "bd", "ready")
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	})
}
