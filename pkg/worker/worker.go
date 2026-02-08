package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"oro/pkg/protocol"
)

// SubprocessSpawner abstracts claude -p invocation for testing.
type SubprocessSpawner interface {
	Spawn(ctx context.Context, prompt string, workdir string) (Process, error)
}

// Process abstracts a running subprocess.
type Process interface {
	Wait() error
	Kill() error
}

// DefaultContextPollInterval controls how often the context watcher polls .oro/context_pct.
const DefaultContextPollInterval = 5 * time.Second

// contextHandoffThreshold is the context percentage above which a handoff is triggered.
const contextHandoffThreshold = 70

// reconnectBaseInterval is the base retry interval for reconnection.
const reconnectBaseInterval = 2 * time.Second

// reconnectJitter is the maximum jitter added to the reconnect interval.
const reconnectJitter = 500 * time.Millisecond

// maxBufferedMessages is the maximum number of messages buffered during reconnection.
const maxBufferedMessages = 100

// Worker is the Oro worker agent. It holds a UDS connection to the Dispatcher,
// manages a claude -p subprocess, and monitors context usage.
type Worker struct {
	ID                  string
	conn                net.Conn
	proc                Process
	beadID              string
	worktree            string
	mu                  sync.Mutex
	spawner             SubprocessSpawner
	socketPath          string // for reconnection
	buffer              *MessageBuffer
	disconnected        bool
	contextPollInterval time.Duration
}

// New creates a Worker that connects to the Dispatcher at socketPath.
func New(id, socketPath string, spawner SubprocessSpawner) (*Worker, error) {
	conn, err := net.Dial("unix", socketPath) //nolint:noctx // UDS connect is instant, no context needed
	if err != nil {
		return nil, fmt.Errorf("connect to dispatcher: %w", err)
	}
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             spawner,
		socketPath:          socketPath,
		buffer:              NewMessageBuffer(maxBufferedMessages),
		contextPollInterval: DefaultContextPollInterval,
	}, nil
}

// NewWithConn creates a Worker with a pre-established connection (for testing).
func NewWithConn(id string, conn net.Conn, spawner SubprocessSpawner) *Worker {
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             spawner,
		buffer:              NewMessageBuffer(maxBufferedMessages),
		contextPollInterval: DefaultContextPollInterval,
	}
}

// SetContextPollInterval overrides the context watcher poll interval (for testing).
func (w *Worker) SetContextPollInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.contextPollInterval = d
}

// Run is the main event loop. It reads messages from the UDS connection and
// dispatches them. It returns nil on clean shutdown or context cancellation.
func (w *Worker) Run(ctx context.Context) error { //nolint:gocognit // event loop — refactor tracked in oro-mak
	scanner := bufio.NewScanner(w.conn)
	msgCh := make(chan protocol.Message)
	errCh := make(chan error, 1)

	// Read messages in a goroutine so we can select on ctx.Done.
	go func() {
		for scanner.Scan() {
			var msg protocol.Message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue // skip malformed messages
			}
			msgCh <- msg
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		} else {
			errCh <- fmt.Errorf("connection closed")
		}
	}()

	for {
		select {
		case <-ctx.Done():
			w.killProc()
			return nil

		case msg := <-msgCh:
			done, err := w.handleMessage(ctx, msg)
			if err != nil {
				return err
			}
			if done {
				return nil
			}

		case err := <-errCh:
			// Connection dropped
			if ctx.Err() != nil {
				return nil //nolint:nilerr // context cancelled = clean shutdown, swallow connection error
			}
			if w.socketPath == "" {
				// No socketPath means we can't reconnect (test with net.Pipe)
				return err
			}
			if reconnErr := w.reconnect(ctx); reconnErr != nil {
				return reconnErr
			}
			// After reconnect, restart the read loop with the new connection
			return w.Run(ctx)
		}
	}
}

// handleMessage processes a single incoming message. Returns (true, nil) on shutdown.
func (w *Worker) handleMessage(ctx context.Context, msg protocol.Message) (bool, error) {
	switch msg.Type {
	case protocol.MsgAssign:
		return false, w.handleAssign(ctx, msg)
	case protocol.MsgShutdown:
		w.killProc()
		return true, nil
	default:
		// Unknown message type, ignore
		return false, nil
	}
}

// handleAssign processes an ASSIGN message: stores state, spawns subprocess,
// starts context watcher.
func (w *Worker) handleAssign(ctx context.Context, msg protocol.Message) error {
	if msg.Assign == nil {
		return fmt.Errorf("ASSIGN message missing payload")
	}

	w.mu.Lock()
	w.beadID = msg.Assign.BeadID
	w.worktree = msg.Assign.Worktree
	w.mu.Unlock()

	prompt := buildPrompt(msg.Assign.BeadID, msg.Assign.Worktree)
	proc, err := w.spawner.Spawn(ctx, prompt, msg.Assign.Worktree)
	if err != nil {
		return fmt.Errorf("spawn claude: %w", err)
	}

	w.mu.Lock()
	w.proc = proc
	w.mu.Unlock()

	// Send STATUS running
	if err := w.SendStatus(ctx, "running", ""); err != nil {
		return err
	}

	// Start context file watcher
	go w.watchContext(ctx)

	// Wait for subprocess completion in background
	go func() {
		_ = proc.Wait()
	}()

	return nil
}

// buildPrompt constructs the prompt string for claude -p.
func buildPrompt(beadID, worktree string) string {
	return fmt.Sprintf("Execute bead %s in worktree %s", beadID, worktree)
}

// watchContext polls .oro/context_pct in the current worktree and triggers
// handoff if context usage exceeds the threshold.
func (w *Worker) watchContext(ctx context.Context) {
	w.mu.Lock()
	interval := w.contextPollInterval
	w.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.Lock()
			wt := w.worktree
			w.mu.Unlock()

			if wt == "" {
				continue
			}

			pctPath := filepath.Join(wt, ".oro", "context_pct")
			data, err := os.ReadFile(pctPath) //nolint:gosec // path is constructed internally, not user input
			if err != nil {
				// File doesn't exist or unreadable — not an error
				continue
			}

			pct, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				continue
			}

			if pct > contextHandoffThreshold {
				_ = w.SendHandoff(ctx)
				w.killProc()
				return
			}
		}
	}
}

// killProc kills the current subprocess if one is running.
func (w *Worker) killProc() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.proc != nil {
		_ = w.proc.Kill()
		w.proc = nil
	}
}

// reconnect attempts to re-establish the UDS connection to the Dispatcher.
// It retries every 2s with ±500ms jitter until success or context cancellation.
// The subprocess is NOT killed during reconnection.
func (w *Worker) reconnect(ctx context.Context) error {
	w.mu.Lock()
	w.disconnected = true
	w.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("worker reconnect: %w", ctx.Err())
		default:
		}

		jitter := time.Duration(rand.Int64N(int64(2*reconnectJitter))) - reconnectJitter //nolint:gosec // jitter doesn't need crypto rand
		wait := reconnectBaseInterval + jitter

		select {
		case <-ctx.Done():
			return fmt.Errorf("worker reconnect: %w", ctx.Err())
		case <-time.After(wait):
		}

		conn, err := net.Dial("unix", w.socketPath) //nolint:noctx // UDS reconnect is instant
		if err != nil {
			continue
		}

		w.mu.Lock()
		w.conn = conn
		w.disconnected = false
		beadID := w.beadID
		state := "running"
		if w.proc == nil {
			state = "idle"
		}
		w.mu.Unlock()

		// Send RECONNECT with buffered events
		buffered := w.buffer.Drain()
		reconnMsg := protocol.Message{
			Type: protocol.MsgReconnect,
			Reconnect: &protocol.ReconnectPayload{
				WorkerID:       w.ID,
				BeadID:         beadID,
				State:          state,
				BufferedEvents: buffered,
			},
		}
		if err := w.sendMessage(reconnMsg); err != nil {
			continue
		}

		return nil
	}
}

// sendMessage encodes and writes a protocol.Message as line-delimited JSON.
// If disconnected, the message is buffered instead.
func (w *Worker) sendMessage(msg protocol.Message) error {
	w.mu.Lock()
	disconnected := w.disconnected
	w.mu.Unlock()

	if disconnected {
		w.buffer.Add(msg)
		return nil
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	conn := w.conn
	w.mu.Unlock()

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// SendHeartbeat sends a HEARTBEAT message to the Dispatcher.
func (w *Worker) SendHeartbeat(_ context.Context, contextPct int) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			BeadID:     beadID,
			WorkerID:   w.ID,
			ContextPct: contextPct,
		},
	})
}

// SendStatus sends a STATUS message to the Dispatcher.
func (w *Worker) SendStatus(_ context.Context, state, result string) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
			State:    state,
			Result:   result,
		},
	})
}

// SendDone sends a DONE message to the Dispatcher.
func (w *Worker) SendDone(_ context.Context) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:   beadID,
			WorkerID: w.ID,
		},
	})
}

// SendHandoff sends a HANDOFF message to the Dispatcher.
func (w *Worker) SendHandoff(_ context.Context) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
		},
	})
}

// SendReadyForReview sends a READY_FOR_REVIEW message to the Dispatcher.
func (w *Worker) SendReadyForReview(_ context.Context) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
		},
	})
}

// ClaudeSpawner is the production SubprocessSpawner that invokes `claude -p`.
type ClaudeSpawner struct{}

// Spawn starts a `claude -p` subprocess with the given prompt and working directory.
func (s *ClaudeSpawner) Spawn(ctx context.Context, prompt, workdir string) (Process, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	return &cmdProcess{cmd: cmd}, nil
}

// cmdProcess wraps *exec.Cmd to implement the Process interface.
type cmdProcess struct {
	cmd *exec.Cmd
}

// Wait blocks until the subprocess exits.
func (p *cmdProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("claude process wait: %w", err)
	}
	return nil
}

// Kill terminates the subprocess immediately.
func (p *cmdProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill claude process: %w", err)
	}
	return nil
}
