package jailer

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// ConsoleClient sends only the two closed console operations to the installed receiver.
type ConsoleClient struct {
	transport system.PrivilegedControlTransport
}

func NewConsoleClient() *ConsoleClient { return &ConsoleClient{} }

func (client *ConsoleClient) Observe(ctx context.Context, request ConsoleObserveRequest) (ConsoleObserveResult, error) {
	if err := request.Validate(); err != nil {
		return ConsoleObserveResult{}, err
	}
	if ctx == nil || client == nil {
		return ConsoleObserveResult{}, consoleAuthorityError(request.VMID, "console client is unavailable", nil)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, ConsoleObserveAction, request, 0); err != nil {
		return ConsoleObserveResult{}, err
	}
	control, err := system.NewPrivilegedControlRequest(ConsoleObserveAction, frame.Bytes(), 0, nil)
	if err != nil {
		return ConsoleObserveResult{}, err
	}
	outcome := client.transport.Exchange(ctx, control)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return ConsoleObserveResult{}, consoleAuthorityError(request.VMID,
			"console observation did not reach authoritative EOF", outcome.Diagnostic())
	}
	response, err := privilegedwire.DecodeResponse[ConsoleObserveResult](context.WithoutCancel(ctx),
		bytes.NewReader(outcome.Response()), ConsoleObserveAction)
	if err != nil {
		return ConsoleObserveResult{}, consoleAuthorityError(request.VMID,
			"console response is not authoritative", errors.Join(err, outcome.Diagnostic()))
	}
	if err := response.OperationError(); err != nil {
		return ConsoleObserveResult{}, err
	}
	result := response.Result()
	if err := result.Validate(request); err != nil {
		return ConsoleObserveResult{}, err
	}
	return result, nil
}

func (client *ConsoleClient) Stop(ctx context.Context, request ConsoleStopRequest) (ConsoleStopResult, error) {
	if err := request.Validate(); err != nil {
		return ConsoleStopResult{}, err
	}
	if ctx == nil || client == nil {
		return ConsoleStopResult{}, consoleAuthorityError(request.VMID, "console client is unavailable", nil)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, ConsoleStopAction, request, 0); err != nil {
		return ConsoleStopResult{}, err
	}
	control, err := system.NewPrivilegedControlRequest(ConsoleStopAction, frame.Bytes(), 0, nil)
	if err != nil {
		return ConsoleStopResult{}, err
	}
	outcome := client.transport.Exchange(ctx, control)
	return decodeConsoleStopResponse(ctx, request, consoleStopResponse{
		response:    outcome.Response(),
		responseEOF: outcome.ResponseEOF(),
		started:     outcome.ProcessStarted(),
		diagnostic:  outcome.Diagnostic(),
	})
}

type consoleStopResponse struct {
	response    []byte
	responseEOF bool
	started     bool
	diagnostic  *errs.DomainError
}

func decodeConsoleStopResponse(
	ctx context.Context,
	request ConsoleStopRequest,
	outcome consoleStopResponse,
) (ConsoleStopResult, error) {
	if outcome.responseEOF && !outcome.started {
		return ConsoleStopResult{}, consoleStopClientTransportError(false,
			errors.Join(errors.New("privileged response arrived before process start"), outcome.diagnostic))
	}
	if !outcome.responseEOF {
		return ConsoleStopResult{}, consoleStopClientTransportError(outcome.started,
			errors.Join(errors.New("privileged response did not reach authoritative EOF"), outcome.diagnostic))
	}
	response, err := privilegedwire.DecodeResponse[ConsoleStopResult](context.WithoutCancel(ctx),
		bytes.NewReader(outcome.response), ConsoleStopAction)
	if err != nil {
		return ConsoleStopResult{}, consoleStopClientTransportError(outcome.started,
			errors.Join(err, outcome.diagnostic))
	}
	if err := response.OperationError(); err != nil {
		return ConsoleStopResult{}, err
	}
	result := response.Result()
	if err := result.Validate(request); err != nil {
		return ConsoleStopResult{}, consoleStopClientTransportError(outcome.started,
			errors.Join(err, outcome.diagnostic))
	}
	return result, nil
}

func consoleStopClientTransportError(started bool, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeProcessError,
		"console stop privileged response is not authoritative",
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{
			"process_started": started,
			"outcome_unknown": started,
		}),
	)
}
