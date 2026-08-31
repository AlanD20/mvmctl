package agent

import (
	"context"
	"crypto/subtle"
	"io"
	"net"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecwire"
)

const (
	destinationUnauthorizedMessage   = "VM-exec request is unauthorized"
	destinationExecutionErrorMessage = "VM-exec execution failed"
	destinationCanceledMessage       = "VM-exec execution canceled"
	destinationCancelErrorMessage    = "VM-exec cancellation frame is invalid"
	destinationTimeoutMessage        = "VM-exec execution timed out"
)

type destinationExecutionEffect interface {
	Execute(
		ctx context.Context,
		targetUser string,
		command string,
		stdout io.Writer,
		stderr io.Writer,
	) (int, error)
}

func (a *Agent) handleDestinationConnection(ctx context.Context, reader io.Reader, conn net.Conn) {
	request, err := vmexecwire.DecodeDestinationRequest(ctx, reader)
	if err != nil {
		return
	}
	response, err := vmexecwire.NewResponseWriter(conn, request.RequestID)
	if err != nil {
		return
	}
	if a.token == "" || subtle.ConstantTimeCompare([]byte(request.AgentToken), []byte(a.token)) != 1 {
		_ = response.WriteError(ctx, destinationUnauthorizedMessage)
		return
	}

	executionCtx, cancelExecution := context.WithTimeout(ctx, vmexecwire.MaxExecutionDuration)
	defer cancelExecution()
	cancelResult := make(chan error, 1)
	go func() {
		cancelErr := vmexecwire.DecodeCancel(executionCtx, conn, request.RequestID)
		cancelResult <- cancelErr
		cancelExecution()
	}()

	startedAt := time.Now()
	stdout := &destinationStdoutWriter{ctx: executionCtx, response: response}
	stderr := &destinationStderrWriter{ctx: executionCtx, response: response}
	exitCode, executionErr := a.destinationExecution.Execute(
		executionCtx,
		request.TargetUser,
		request.Command,
		stdout,
		stderr,
	)
	duration := min(time.Since(startedAt), vmexecwire.MaxExecutionDuration)

	select {
	case cancelErr := <-cancelResult:
		if cancelErr != nil {
			_ = response.WriteError(context.WithoutCancel(ctx), destinationCancelErrorMessage)
			return
		}
		_ = response.WriteError(context.WithoutCancel(ctx), destinationCanceledMessage)
		return
	default:
	}
	if executionCtx.Err() != nil {
		message := destinationCanceledMessage
		if executionCtx.Err() == context.DeadlineExceeded {
			message = destinationTimeoutMessage
		}
		_ = response.WriteError(context.WithoutCancel(ctx), message)
		return
	}
	if executionErr != nil || stdout.Err() != nil || stderr.Err() != nil || exitCode < 0 || exitCode > 255 {
		_ = response.WriteError(context.WithoutCancel(ctx), destinationExecutionErrorMessage)
		return
	}
	_ = response.WriteResult(context.WithoutCancel(ctx), exitCode, duration)
}

type destinationStdoutWriter struct {
	mu       sync.Mutex
	ctx      context.Context
	response *vmexecwire.ResponseWriter
	err      error
}

func (writer *destinationStdoutWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if err := writer.response.WriteStdout(writer.ctx, data); err != nil {
		writer.mu.Lock()
		if writer.err == nil {
			writer.err = err
		}
		writer.mu.Unlock()
		return 0, err
	}
	return len(data), nil
}

func (writer *destinationStdoutWriter) Err() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.err
}

type destinationStderrWriter struct {
	mu       sync.Mutex
	ctx      context.Context
	response *vmexecwire.ResponseWriter
	err      error
}

func (writer *destinationStderrWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if err := writer.response.WriteStderr(writer.ctx, data); err != nil {
		writer.mu.Lock()
		if writer.err == nil {
			writer.err = err
		}
		writer.mu.Unlock()
		return 0, err
	}
	return len(data), nil
}

func (writer *destinationStderrWriter) Err() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.err
}
