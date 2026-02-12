package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"

	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// newDirectiveCmd creates the "oro directive" subcommand.
func newDirectiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "directive <op> [args]",
		Short: "Send a directive to the dispatcher",
		Long: `Connects to the dispatcher UDS socket, sends a DIRECTIVE message,
waits for ACK, then disconnects.

Supported operations:
  start           - Begin pulling and assigning ready work
  stop            - Finish current work, don't assign new beads
  pause           - Hold new assignments, workers keep running
  resume          - Resume from paused state
  scale N         - Set target worker pool size to N
  focus <epic>    - Prioritize beads from specific epic
  status          - Query dispatcher state`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDirective(cmd.Context(), cmd.OutOrStdout(), args)
		},
	}

	return cmd
}

// runDirective sends a directive to the dispatcher and prints the result.
func runDirective(ctx context.Context, w io.Writer, args []string) error {
	op := args[0]
	var opArgs string
	if len(args) > 1 {
		opArgs = strings.Join(args[1:], " ")
	}

	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		return fmt.Errorf("get socket path: %w", err)
	}

	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		return fmt.Errorf("dial dispatcher: %w", err)
	}
	defer conn.Close()

	if err := sendDirective(conn, op, opArgs); err != nil {
		return fmt.Errorf("send directive: %w", err)
	}

	ack, err := readACK(conn)
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	printDirectiveResult(w, op, ack)
	return nil
}

// dialDispatcher connects to the dispatcher UDS socket.
func dialDispatcher(ctx context.Context, sockPath string) (net.Conn, error) {
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to dispatcher: %w", err)
	}
	return conn, nil
}

// sendDirective marshals and sends a DIRECTIVE message.
func sendDirective(conn net.Conn, op, opArgs string) error {
	msg := protocol.Message{
		Type: protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{
			Op:            op,
			Args:          opArgs,
			HumanApproved: true, // CLI commands are always human-initiated
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal directive: %w", err)
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("send directive: %w", err)
	}
	return nil
}

// readACK reads and parses the ACK response from the dispatcher.
func readACK(conn net.Conn) (*protocol.ACKPayload, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read ack: %w", err)
		}
		return nil, fmt.Errorf("no ack received")
	}

	var ack protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		return nil, fmt.Errorf("unmarshal ack: %w", err)
	}

	if ack.Type != protocol.MsgACK {
		return nil, fmt.Errorf("unexpected response type: %s", ack.Type)
	}

	if ack.ACK == nil {
		return nil, fmt.Errorf("ack payload is nil")
	}

	if !ack.ACK.OK {
		return nil, fmt.Errorf("directive failed: %s", ack.ACK.Detail)
	}

	return ack.ACK, nil
}

// printDirectiveResult prints the directive result to the output.
func printDirectiveResult(w io.Writer, op string, ack *protocol.ACKPayload) {
	if ack.Detail != "" {
		fmt.Fprintln(w, ack.Detail)
	} else {
		fmt.Fprintf(w, "directive %s applied\n", op)
	}
}
