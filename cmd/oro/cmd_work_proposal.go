package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"

	"github.com/spf13/cobra"
)

const defaultWorkProposalTimeout = 2 * time.Minute

type workProposalIdentity struct {
	assignmentID int64
	workerID     string
	beadID       string
	socketPath   string
	capability   protocol.WorkRequestCapability
}

type evidenceRunCLIResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
}

type workRequestError struct {
	Operation string `json:"operation"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
}

func (e workRequestError) Error() string {
	encoded, err := json.Marshal(e)
	if err != nil {
		return e.Detail
	}
	return string(encoded)
}

func newEvidenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evidence",
		Short: "Record assignment-scoped diagnostic evidence",
	}
	cmd.AddCommand(newEvidenceRunCmd())
	return cmd
}

func newEvidenceRunCmd() *cobra.Command {
	var kind string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run --kind diagnostic --timeout 2m -- <argv...>",
		Short: "Run and record diagnostic evidence",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("evidence run requires an argv after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEvidenceCommand(cmd.Context(), cmd.OutOrStdout(), kind, timeout, args)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "evidence kind (diagnostic)")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultWorkProposalTimeout, "command and dispatcher response timeout")
	return cmd
}

func runEvidenceCommand(ctx context.Context, output io.Writer, kind string, timeout time.Duration, argv []string) error {
	if kind != "diagnostic" {
		return workRequestError{Operation: "evidence.run", Code: "invalid_kind", Detail: "evidence kind must be diagnostic"}
	}
	identity, err := readWorkProposalIdentity()
	if err != nil {
		return err
	}
	ctx, cancel := workProposalTimeoutContext(ctx, timeout)
	defer cancel()

	result, err := submitEvidenceRun(ctx, identity.socketPath, protocol.EvidenceExecutionRequest{
		AssignmentID: identity.assignmentID,
		WorkerID:     identity.workerID,
		BeadID:       identity.beadID,
		Kind:         kind,
		Argv:         append([]string(nil), argv...),
		TimeoutMS:    timeout.Milliseconds(),
		Capability:   workRequestCapability(identity.capability, "evidence", kind, strings.Join(argv, "\x00")),
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(evidenceRunCLIResult(result)); err != nil {
		return fmt.Errorf("encode evidence result: %w", err)
	}
	return nil
}

func newTaskProposeBlockerCmd() *cobra.Command {
	var evidenceRunID string
	var fingerprint string
	var summary string
	var kind string
	var clientID string
	var scopeHint string
	var title string
	var proposalType string
	var priority int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "propose-blocker --evidence-run <run-id> --fingerprint <fingerprint> --summary <summary>",
		Short: "Propose a blocker backed by recorded evidence",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProposeBlockerCommand(cmd.Context(), cmd.OutOrStdout(), workProposalInput{
				evidenceRunID: evidenceRunID,
				fingerprint:   fingerprint,
				summary:       summary,
				kind:          kind,
				clientID:      clientID,
				scopeHint:     scopeHint,
				title:         title,
				proposalType:  proposalType,
				priority:      priority,
				timeout:       timeout,
			})
		},
	}
	cmd.Flags().StringVar(&evidenceRunID, "evidence-run", "", "recorded evidence run ID")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "stable blocker fingerprint")
	cmd.Flags().StringVar(&summary, "summary", "", "concise blocker summary")
	cmd.Flags().StringVar(&kind, "kind", "", "proposal kind")
	cmd.Flags().StringVar(&clientID, "client-id", "", "stable client proposal ID for retries")
	cmd.Flags().StringVar(&clientID, "client-proposal-id", "", "stable client proposal ID for retries")
	cmd.Flags().StringVar(&scopeHint, "scope", "", "optional provisional scope hint")
	cmd.Flags().StringVar(&title, "title", "", "optional suggested task title")
	cmd.Flags().StringVar(&proposalType, "type", "bug", "suggested task type")
	cmd.Flags().IntVar(&priority, "priority", 0, "suggested task priority")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultWorkProposalTimeout, "dispatcher response timeout")
	return cmd
}

type workProposalInput struct {
	evidenceRunID string
	fingerprint   string
	summary       string
	kind          string
	clientID      string
	scopeHint     string
	title         string
	proposalType  string
	priority      int
	timeout       time.Duration
}

func runProposeBlockerCommand(ctx context.Context, output io.Writer, input workProposalInput) error {
	identity, err := readWorkProposalIdentity()
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.clientID) == "" || strings.TrimSpace(input.evidenceRunID) == "" || strings.TrimSpace(input.fingerprint) == "" || strings.TrimSpace(input.kind) == "" || strings.TrimSpace(input.summary) == "" {
		return workRequestError{Operation: "task.propose-blocker", Code: "invalid_request", Detail: "client-id, evidence-run, fingerprint, kind, and summary are required"}
	}
	ctx, cancel := workProposalTimeoutContext(ctx, input.timeout)
	defer cancel()
	response, err := submitWorkProposal(ctx, identity.socketPath, protocol.WorkProposalPayload{
		ClientProposalID:  input.clientID,
		AssignmentID:      identity.assignmentID,
		WorkerID:          identity.workerID,
		BeadID:            identity.beadID,
		EvidenceRunID:     input.evidenceRunID,
		Fingerprint:       input.fingerprint,
		ScopeHint:         input.scopeHint,
		Kind:              input.kind,
		Summary:           input.summary,
		SuggestedTitle:    input.title,
		SuggestedType:     input.proposalType,
		SuggestedPriority: input.priority,
	}, workRequestCapability(identity.capability, "proposal", input.clientID))
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return fmt.Errorf("encode blocker proposal result: %w", err)
	}
	return nil
}

func readWorkProposalIdentity() (workProposalIdentity, error) {
	credentialPath := strings.TrimSpace(os.Getenv("ORO_CAPABILITY_FILE"))
	if credentialPath == "" {
		return workProposalIdentity{}, workRequestError{Operation: "work request", Code: "missing_credential", Detail: "ORO_CAPABILITY_FILE is required"}
	}
	credential, err := worker.ReadCapabilityFile(credentialPath)
	if err != nil {
		return workProposalIdentity{}, workRequestError{Operation: "work request", Code: "credential_unavailable", Detail: err.Error()}
	}
	if credential.AssignmentID <= 0 || credential.Generation <= 0 || credential.CapabilityID == "" || credential.Token == "" {
		return workProposalIdentity{}, workRequestError{Operation: "work request", Code: "invalid_credential", Detail: "assignment credential is incomplete"}
	}
	if !credential.ExpiresAt.IsZero() && !credential.ExpiresAt.After(time.Now()) {
		return workProposalIdentity{}, workRequestError{Operation: "work request", Code: "expired_credential", Detail: "assignment credential has expired"}
	}
	identity := workProposalIdentity{
		assignmentID: credential.AssignmentID,
		workerID:     strings.TrimSpace(os.Getenv("ORO_WORKER_ID")),
		beadID:       strings.TrimSpace(os.Getenv("ORO_WORKER_BEAD_ID")),
		socketPath:   strings.TrimSpace(os.Getenv("ORO_SOCKET_PATH")),
		capability: protocol.WorkRequestCapability{
			CapabilityID: credential.CapabilityID,
			Token:        credential.Token,
			Generation:   credential.Generation,
		},
	}
	if identity.workerID == "" || identity.beadID == "" || identity.socketPath == "" {
		return workProposalIdentity{}, workRequestError{Operation: "work request", Code: "missing_assignment_context", Detail: "ORO_WORKER_ID, ORO_WORKER_BEAD_ID, and ORO_SOCKET_PATH are required"}
	}
	return identity, nil
}

func submitEvidenceRun(ctx context.Context, socketPath string, execution protocol.EvidenceExecutionRequest) (protocol.EvidenceRunResult, error) {
	var response protocol.Message
	if err := submitWorkRequest(ctx, socketPath, protocol.Message{Type: protocol.MsgEvidenceRequest, EvidenceRequest: &protocol.EvidenceRequest{Execution: &execution}}, &response); err != nil {
		return protocol.EvidenceRunResult{}, err
	}
	if response.Type != protocol.MsgEvidenceResponse || response.EvidenceResponse == nil {
		return protocol.EvidenceRunResult{}, workRequestError{Operation: "evidence.run", Code: "invalid_response", Detail: "dispatcher returned an unexpected evidence response"}
	}
	if response.EvidenceResponse.Error != "" {
		return protocol.EvidenceRunResult{}, workRequestError{Operation: "evidence.run", Code: "server_error", Detail: response.EvidenceResponse.Error}
	}
	return response.EvidenceResponse.Result, nil
}

func submitWorkProposal(ctx context.Context, socketPath string, proposal protocol.WorkProposalPayload, capability protocol.WorkRequestCapability) (protocol.WorkProposalResult, error) {
	var response protocol.Message
	if err := submitWorkRequest(ctx, socketPath, protocol.Message{Type: protocol.MsgWorkProposalRequest, WorkProposalRequest: &protocol.WorkProposalRequest{Proposal: proposal, Capability: capability}}, &response); err != nil {
		return protocol.WorkProposalResult{}, err
	}
	if response.Type != protocol.MsgWorkProposalResponse || response.WorkProposalResponse == nil {
		return protocol.WorkProposalResult{}, workRequestError{Operation: "task.propose-blocker", Code: "invalid_response", Detail: "dispatcher returned an unexpected proposal response"}
	}
	if response.WorkProposalResponse.Error != "" {
		return protocol.WorkProposalResult{}, workRequestError{Operation: "task.propose-blocker", Code: "server_error", Detail: response.WorkProposalResponse.Error}
	}
	return response.WorkProposalResponse.Result, nil
}

func submitWorkRequest(ctx context.Context, socketPath string, request protocol.Message, response *protocol.Message) error {
	conn, err := dialDispatcher(ctx, socketPath)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return timeoutWorkRequestError("work request")
		}
		return workRequestError{Operation: "work request", Code: "socket_unavailable", Detail: err.Error()}
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return workRequestError{Operation: "work request", Code: "socket_deadline", Detail: err.Error()}
		}
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return workRequestTransportError(ctx, "send", err)
	}
	if err := json.NewDecoder(conn).Decode(response); err != nil {
		return workRequestTransportError(ctx, "read", err)
	}
	return nil
}

func workProposalTimeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultWorkProposalTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func workRequestTransportError(ctx context.Context, stage string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeoutWorkRequestError("work request")
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return timeoutWorkRequestError("work request")
	}
	return workRequestError{Operation: "work request", Code: "socket_" + stage + "_failed", Detail: err.Error()}
}

func timeoutWorkRequestError(operation string) error {
	return workRequestError{Operation: operation, Code: "timeout", Detail: "dispatcher request timed out"}
}

func workRequestCapability(capability protocol.WorkRequestCapability, parts ...string) protocol.WorkRequestCapability {
	material := append([]string{capability.CapabilityID}, parts...)
	sum := sha256.Sum256([]byte(strings.Join(material, "\x00")))
	capability.Nonce = hex.EncodeToString(sum[:16])
	return capability
}
