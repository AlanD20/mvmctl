package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

const vmExecTerminalTimeout = 2 * time.Second

var errVMExecSourceCancelled = errors.New("VM-exec source cancelled")

func (op *Operation) execWithVMRelay(
	ctx context.Context,
	input inputs.ExecInput,
	resolved *inputs.ResolvedExecInput,
	client *vsock.Client,
	user string,
	probeTimeout time.Duration,
) (result *results.ExecResult, resultErr error) {
	audit, err := logging.NewVMExecAuditSink(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, audit.Close(ctx)) }()
	// The opt-in outer command and its one source request share a bounded lifetime.
	relayCtx, cancel := context.WithTimeout(ctx, vmexecwire.MaxExecutionDuration)
	defer cancel()
	conn, err := dialVMExecAgent(relayCtx, resolved.VsockItem, probeTimeout)
	if err != nil {
		return nil, err
	}
	source, err := vsock.NewSourceSession(conn, resolved.VsockItem.Token)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	defer func() { resultErr = errors.Join(resultErr, source.Close()) }()
	sessionID, err := source.Open(relayCtx)
	if err != nil {
		return nil, err
	}

	relayDone := make(chan error, 1)
	var outerDone atomic.Bool
	go func() {
		request, readErr := source.ReadRequest(relayCtx)
		if readErr != nil {
			// An outer command may complete without claiming its optional source session.
			if outerDone.Load() || relayCtx.Err() != nil || errors.Is(readErr, io.EOF) {
				relayDone <- nil
				return
			}
			relayDone <- readErr
			return
		}
		relayDone <- op.relayVMExec(relayCtx, resolved.VM.ID, conn, source, request, audit, probeTimeout)
	}()
	outer, outerErr := client.ExecWithSourceSession(
		relayCtx, input.Command, user, 0, input.Env, input.NoSync, sessionID,
	)
	outerDone.Store(true)
	closeErr := source.Close()
	relayErr := <-relayDone
	cancel()
	if err := errors.Join(outerErr, relayErr, closeErr); err != nil {
		return nil, err
	}
	return &results.ExecResult{Stdout: outer.Stdout, Stderr: outer.Stderr, ExitCode: outer.ExitCode}, nil
}

func (op *Operation) relayVMExec(
	ctx context.Context,
	sourceVMID string,
	sourceConn *net.UnixConn,
	source *vsock.SourceSession,
	request vmexecwire.SourceRequest,
	audit *logging.VMExecAuditSink,
	probeTimeout time.Duration,
) (resultErr error) {
	started := time.Now()
	record := logging.VMExecAuditRecord{
		RequestID: request.RequestID, SourceVMID: sourceVMID,
		TargetUser: request.TargetUser, StartedAt: started,
		Result: logging.VMExecAuditDenied,
	}
	var terminal *vmexecwire.ResultFrame
	var authority *jailer.VMExecSession
	var target atomic.Pointer[vsock.DestinationSession]
	requestCtx, cancel := context.WithCancelCause(ctx)
	finished := make(chan struct{})
	cancelDone := make(chan error, 1)
	waiting := false
	var denial *logging.VMExecDenialRecord
	defer cancel(nil)
	defer func() {
		close(finished)
		if session := target.Load(); session != nil {
			resultErr = errors.Join(resultErr, session.Close())
		}
		if errors.Is(context.Cause(requestCtx), errVMExecSourceCancelled) {
			record.Result, record.ExitCode = logging.VMExecAuditCancelled, nil
			resultErr = errors.Join(resultErr, vmExecRelayFailed())
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			record.Result, record.ExitCode = logging.VMExecAuditTimedOut, nil
		}
		record.Duration = min(time.Since(started), vmexecwire.MaxExecutionDuration)
		terminalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmExecTerminalTimeout)
		defer cancel()
		// CRITICAL: Audit failure cannot become a successful guest-visible terminal result.
		if denial != nil {
			denial.Duration = record.Duration
			resultErr = errors.Join(resultErr, audit.RecordDenial(terminalCtx, *denial))
		} else {
			resultErr = errors.Join(resultErr, audit.Record(terminalCtx, record))
		}
		if resultErr == nil && terminal != nil {
			resultErr = source.WriteResult(terminalCtx, terminal.ExitCode, terminal.Duration)
		} else {
			resultErr = errors.Join(resultErr, source.WriteError(terminalCtx, "VM-to-VM execution failed"))
		}
		resultErr = errors.Join(resultErr, source.Close())
		if waiting {
			resultErr = errors.Join(resultErr, <-cancelDone)
		}
		// CRITICAL: Release shuts down both retained descriptions, so it follows source terminal/EOF.
		if authority != nil {
			cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
			resultErr = errors.Join(resultErr, authority.Release(cleanupCtx))
			stop()
		}
	}()

	destination, err := vm.NewResolver(op.Repos.VM).Resolve(ctx, request.Destination)
	if err != nil {
		denial = vmExecDenialRecord(record, request.Destination, logging.VMExecDestinationUnresolved)
		return vmExecDenied()
	}
	record.DestinationVMID = destination.ID
	if destination.ID == sourceVMID {
		denial = vmExecDenialRecord(record, request.Destination, logging.VMExecDestinationIsSource)
		return vmExecDenied()
	}
	// Exec policy intentionally performs no IP-network or traffic-policy lookup.
	allowed, err := op.Services.ExecPolicy.Allows(ctx, sourceVMID, destination.ID, request.TargetUser)
	if err != nil || !allowed {
		return vmExecDenied()
	}
	destinationConfig, err := op.Repos.Vsock.GetByVMID(ctx, destination.ID)
	if err != nil || destinationConfig == nil || destinationConfig.VmID != destination.ID {
		return vmExecDenied()
	}

	waiting = true
	go func() {
		cancelErr := source.WaitCancel(requestCtx)
		select {
		case <-finished:
			cancelDone <- nil
			return
		default:
		}
		if cancelErr == nil {
			if session := target.Load(); session != nil {
				cancelCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), vmExecTerminalTimeout)
				cancelErr = session.Cancel(cancelCtx)
				stop()
			}
			cancel(errVMExecSourceCancelled)
		} else {
			cancel(cancelErr)
		}
		cancelDone <- cancelErr
	}()

	// CRITICAL: Only the root sidecar proves owner, active generations, Firecracker peers,
	// and global capacity. SQLite status and the caller-selected socket are never authority.
	authority, err = jailer.NewVMExecSessionClient().Open(requestCtx, jailer.VMExecSessionOpenRequest{
		SourceVMID: sourceVMID, DestinationVMID: destination.ID,
	}, sourceConn)
	if err != nil {
		return vmExecDenied()
	}
	record.Result = logging.VMExecAuditRelayFailed
	destinationConn, err := dialVMExecAgent(requestCtx, destinationConfig, probeTimeout)
	if err != nil {
		return vmExecRelayFailed()
	}
	if err := authority.BindDestination(requestCtx, destinationConn); err != nil {
		record.Result = logging.VMExecAuditDenied
		return errors.Join(vmExecDenied(), destinationConn.Close())
	}
	session, err := vsock.NewDestinationSession(destinationConn)
	if err != nil {
		return errors.Join(vmExecRelayFailed(), destinationConn.Close())
	}
	target.Store(session)
	// Both descriptor proofs and readiness precede construction of command/token-bearing payload.
	if err := session.Open(requestCtx, vmexecwire.DestinationRequest{
		RequestID: request.RequestID, AgentToken: destinationConfig.Token,
		TargetUser: request.TargetUser, Command: request.Command,
	}); err != nil {
		return vmExecRelayFailed()
	}
	for {
		frame, err := session.Read(requestCtx)
		if err != nil {
			return vmExecRelayFailed()
		}
		switch frame := frame.(type) {
		case vmexecwire.StdoutFrame:
			if err := source.WriteStdout(requestCtx, frame.Data); err != nil {
				return vmExecRelayFailed()
			}
		case vmexecwire.StderrFrame:
			if err := source.WriteStderr(requestCtx, frame.Data); err != nil {
				return vmExecRelayFailed()
			}
		case vmexecwire.ResultFrame:
			terminal = &frame
			record.ExitCode = &frame.ExitCode
			record.Result = logging.VMExecAuditSucceeded
			if frame.ExitCode != 0 {
				record.Result = logging.VMExecAuditCommandFailed
			}
			return nil
		case vmexecwire.ErrorFrame:
			// The destination's arbitrary message is never copied into the audit or host error.
			return vmExecRelayFailed()
		}
	}
}

func vmExecDenialRecord(
	record logging.VMExecAuditRecord,
	selector string,
	reason logging.VMExecDenialReason,
) *logging.VMExecDenialRecord {
	return &logging.VMExecDenialRecord{
		RequestID: record.RequestID, SourceVMID: record.SourceVMID,
		DestinationSelector: selector, TargetUser: record.TargetUser,
		StartedAt: record.StartedAt, Reason: reason,
	}
}

func dialVMExecAgent(ctx context.Context, config *model.VsockConfigItem, timeout time.Duration) (*net.UnixConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, min(timeout, 5*time.Second))
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(dialCtx, "unix", config.UDSPath)
	if err != nil {
		return nil, vmExecRelayFailed()
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, errors.Join(vmExecRelayFailed(), connection.Close())
	}
	stop := context.AfterFunc(dialCtx, func() {
		// Closing is required to interrupt a partial CONNECT exchange on cancellation.
		_ = conn.Close()
	})
	defer stop()
	deadline, _ := dialCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, errors.Join(vmExecRelayFailed(), conn.Close())
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", config.Port); err != nil {
		return nil, errors.Join(vmExecRelayFailed(), conn.Close())
	}
	// Read only the bounded acknowledgement, without buffering later protocol bytes.
	var response strings.Builder
	for range 32 {
		var value [1]byte
		if _, err := io.ReadFull(conn, value[:]); err != nil {
			return nil, errors.Join(vmExecRelayFailed(), conn.Close())
		}
		if value[0] == '\n' {
			text := response.String()
			if !strings.HasPrefix(text, "OK ") {
				break
			}
			port, err := strconv.ParseUint(strings.TrimPrefix(text, "OK "), 10, 32)
			if err != nil || port == 0 {
				break
			}
			if err := conn.SetDeadline(time.Time{}); err != nil {
				return nil, errors.Join(vmExecRelayFailed(), conn.Close())
			}
			return conn, nil
		}
		response.WriteByte(value[0])
	}
	return nil, errors.Join(vmExecRelayFailed(), conn.Close())
}

func vmExecDenied() error {
	return errs.New(errs.CodePolicyExecAuthorizeFailed, "VM-to-VM execution is not authorized")
}

func vmExecRelayFailed() error {
	return errs.New(errs.CodeVsockExecFailed, "VM-to-VM execution relay failed")
}
