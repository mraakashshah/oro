package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestOpsCommands(t *testing.T) {
	t.Run("list renders human and json output from ops-runs directive", func(t *testing.T) {
		detail := `[{"id":7,"type":"decompose","bead_id":"oro-a","worker_id":"w-a","status":"failed","error":"missing children"}]`
		humanOut, got := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
			args:   []string{"ops", "list"},
			detail: detail,
		})
		if got.Op != string(protocol.DirectiveOpsRuns) || got.Args != "" {
			t.Fatalf("directive = %s %q, want ops-runs with empty args", got.Op, got.Args)
		}
		if !strings.Contains(humanOut, "7") || !strings.Contains(humanOut, "decompose") || !strings.Contains(humanOut, "oro-a") || !strings.Contains(humanOut, "failed") {
			t.Fatalf("human ops list output missing run fields:\n%s", humanOut)
		}

		jsonOut, got := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
			args:   []string{"ops", "list", "--json"},
			detail: detail,
		})
		if got.Op != string(protocol.DirectiveOpsRuns) || got.Args != "" {
			t.Fatalf("json directive = %s %q, want ops-runs with empty args", got.Op, got.Args)
		}
		var runs []struct {
			ID     int64  `json:"id"`
			BeadID string `json:"bead_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &runs); err != nil {
			t.Fatalf("ops list --json emitted invalid JSON: %v\n%s", err, jsonOut)
		}
		if len(runs) != 1 || runs[0].ID != 7 || runs[0].BeadID != "oro-a" || runs[0].Status != "failed" {
			t.Fatalf("ops list --json = %+v, want dispatcher detail", runs)
		}
	})

	t.Run("retry requires run id and calls ops-retry directive", func(t *testing.T) {
		_, _, err := executeOpsCommand(t, "ops", "retry")
		if err == nil || !strings.Contains(err.Error(), "run id") {
			t.Fatalf("ops retry without run id error = %v, want run id validation", err)
		}

		detail := `{"id":42,"retried":true,"status":"superseded","new_ops_run_id":43,"routed":true}`
		humanOut, got := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
			args:   []string{"ops", "retry", "42"},
			detail: detail,
		})
		if got.Op != string(protocol.DirectiveOpsRetry) || got.Args != "42" {
			t.Fatalf("directive = %s %q, want ops-retry 42", got.Op, got.Args)
		}
		if !strings.Contains(humanOut, "42") || !strings.Contains(humanOut, "43") || !strings.Contains(humanOut, "superseded") {
			t.Fatalf("ops retry human output missing retry fields:\n%s", humanOut)
		}

		jsonOut, got := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
			args:   []string{"ops", "retry", "42", "--json"},
			detail: detail,
		})
		if got.Op != string(protocol.DirectiveOpsRetry) || got.Args != "42" {
			t.Fatalf("json directive = %s %q, want ops-retry 42", got.Op, got.Args)
		}
		var resp struct {
			ID          int64  `json:"id"`
			Retried     bool   `json:"retried"`
			Status      string `json:"status"`
			NewOpsRunID int64  `json:"new_ops_run_id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &resp); err != nil {
			t.Fatalf("ops retry --json emitted invalid JSON: %v\n%s", err, jsonOut)
		}
		if resp.ID != 42 || !resp.Retried || resp.Status != "superseded" || resp.NewOpsRunID != 43 {
			t.Fatalf("ops retry --json = %+v, want dispatcher retry response", resp)
		}
	})

	t.Run("resolve requires run id and reason then calls ops-resolve directive", func(t *testing.T) {
		_, _, err := executeOpsCommand(t, "ops", "resolve")
		if err == nil || !strings.Contains(err.Error(), "run id") {
			t.Fatalf("ops resolve without run id error = %v, want run id validation", err)
		}
		_, _, err = executeOpsCommand(t, "ops", "resolve", "42")
		if err == nil || !strings.Contains(err.Error(), "--reason") {
			t.Fatalf("ops resolve without reason error = %v, want reason validation", err)
		}

		detail := `{"id":42,"resolved":true,"status":"resolved","reason":"operator checked"}`
		humanOut, got := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
			args:   []string{"ops", "resolve", "42", "--reason", "operator checked"},
			detail: detail,
		})
		if got.Op != string(protocol.DirectiveOpsResolve) || got.Args != "42 operator checked" {
			t.Fatalf("directive = %s %q, want ops-resolve with id and reason", got.Op, got.Args)
		}
		if !strings.Contains(humanOut, "42") || !strings.Contains(humanOut, "resolved") || !strings.Contains(humanOut, "operator checked") {
			t.Fatalf("ops resolve human output missing resolved fields:\n%s", humanOut)
		}
	})

	t.Run("resolve validation failure is surfaced without success output", func(t *testing.T) {
		stdout, _, err := executeOpsCommandWithMock(t, mockOpsDirectiveResponse{
			args:   []string{"ops", "resolve", "42", "--reason", "operator checked"},
			fail:   true,
			detail: "decompose validation failed: expected child task before ack",
		})
		if err == nil {
			t.Fatal("ops resolve validation failure error = nil, want error")
		}
		if !strings.Contains(err.Error(), "decompose validation failed") {
			t.Fatalf("ops resolve validation error = %v, want dispatcher validation detail", err)
		}
		if stdout != "" {
			t.Fatalf("ops resolve validation failure stdout = %q, want empty", stdout)
		}
	})

	t.Run("daemon unavailable error is actionable", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "missing.sock"))

		_, _, err := executeOpsCommand(t, "ops", "list")
		if err == nil {
			t.Fatal("ops list with missing daemon error = nil, want error")
		}
		if !strings.Contains(err.Error(), "daemon unavailable") || !strings.Contains(err.Error(), "oro status") {
			t.Fatalf("ops list missing daemon error = %v, want actionable daemon guidance", err)
		}
	})
}

func TestOpsListHandlesLargeFeedback(t *testing.T) {
	largeFeedback := strings.Repeat("feedback ", 140000)
	detail := fmt.Sprintf(`[
		{"id":7,"type":"review","bead_id":"oro-a","worker_id":"w-a","status":"failed","feedback":%q},
		{"id":8,"type":"diagnosis","bead_id":"oro-b","worker_id":"w-b","status":"running","error":"still running"}
	]`, largeFeedback)

	jsonOut, got := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
		args:   []string{"ops", "list", "--json"},
		detail: detail,
	})
	if got.Op != string(protocol.DirectiveOpsRuns) || got.Args != "" {
		t.Fatalf("directive = %s %q, want ops-runs with empty args", got.Op, got.Args)
	}
	var runs []struct {
		ID       int64  `json:"id"`
		Type     string `json:"type"`
		BeadID   string `json:"bead_id"`
		Status   string `json:"status"`
		Feedback string `json:"feedback"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &runs); err != nil {
		t.Fatalf("ops list --json emitted invalid JSON: %v\n%s", err, jsonOut)
	}
	if len(runs) != 2 {
		t.Fatalf("ops list --json emitted %d runs, want 2", len(runs))
	}
	if runs[0].ID != 7 || runs[0].Type != "review" || runs[0].BeadID != "oro-a" || runs[0].Status != "failed" {
		t.Fatalf("failed run fields = %+v, want id/type/bead/status preserved", runs[0])
	}
	if runs[0].Feedback != largeFeedback {
		t.Fatalf("feedback length = %d, want %d", len(runs[0].Feedback), len(largeFeedback))
	}
	if runs[1].ID != 8 || runs[1].Type != "diagnosis" || runs[1].BeadID != "oro-b" || runs[1].Status != "running" || runs[1].Error != "still running" {
		t.Fatalf("running run fields = %+v, want id/type/bead/status/error preserved", runs[1])
	}

	humanOut, _ := runOpsCommandWithMock(t, mockOpsDirectiveResponse{
		args:   []string{"ops", "list"},
		detail: detail,
	})
	if !strings.Contains(humanOut, "7\treview\toro-a\tfailed") || !strings.Contains(humanOut, "8\tdiagnosis\toro-b\trunning") {
		t.Fatalf("human ops list output missing failed/running rows:\n%s", humanOut)
	}
	if strings.Contains(humanOut, largeFeedback) {
		t.Fatalf("human ops list output was not truncated")
	}
	if !strings.Contains(humanOut, "truncated") {
		t.Fatalf("human ops list output should mark truncated detail:\n%s", humanOut)
	}
}

type mockOpsDirectiveResponse struct {
	args   []string
	fail   bool
	detail string
}

func runOpsCommandWithMock(t *testing.T, resp mockOpsDirectiveResponse) (string, protocol.DirectivePayload) {
	t.Helper()
	stdout, got, err := executeOpsCommandWithMock(t, resp)
	if err != nil {
		t.Fatalf("%v failed: %v", resp.args, err)
	}
	return stdout, got
}

func executeOpsCommandWithMock(t *testing.T, resp mockOpsDirectiveResponse) (string, protocol.DirectivePayload, error) {
	t.Helper()
	if resp.fail && resp.detail == "" {
		resp.detail = "directive failed"
	}

	sockPath := fmt.Sprintf("/tmp/oro-ops-test-%d.sock", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := make(chan protocol.DirectivePayload, 1)
	go runMockOpsDispatcher(ctx, t, sockPath, received, !resp.fail, resp.detail)
	waitForSocket(t, sockPath, 2*time.Second)
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	stdout, _, err := executeOpsCommand(t, resp.args...)
	var got protocol.DirectivePayload
	select {
	case got = <-received:
	case <-ctx.Done():
		t.Fatal("timeout waiting for ops directive")
	}
	return stdout, got, err
}

func executeOpsCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func runMockOpsDispatcher(ctx context.Context, t *testing.T, sockPath string, received chan<- protocol.DirectivePayload, ok bool, detail string) {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock ops dispatcher listen: %v", err)
		return
	}
	defer os.Remove(sockPath)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Error("mock ops dispatcher failed to read directive")
		return
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Errorf("mock ops dispatcher unmarshal: %v", err)
		return
	}
	if msg.Type != protocol.MsgDirective || msg.Directive == nil {
		t.Errorf("mock ops dispatcher got message type %s with directive %+v", msg.Type, msg.Directive)
		return
	}
	received <- *msg.Directive

	data, _ := json.Marshal(protocol.Message{
		Type: protocol.MsgACK,
		ACK:  &protocol.ACKPayload{OK: ok, Detail: detail},
	})
	_, _ = conn.Write(append(data, '\n'))
}
