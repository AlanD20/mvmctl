package loopmount

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

type privilegedExchangeOutcome interface {
	Response() []byte
	ResponseEOF() bool
	ProcessStarted() bool
	Diagnostic() *errs.DomainError
}

type privilegedExchanger interface {
	Exchange(context.Context, *system.PrivilegedControlRequest) privilegedExchangeOutcome
}

type systemPrivilegedExchanger struct {
	transport system.PrivilegedControlTransport
}

func (exchanger systemPrivilegedExchanger) Exchange(
	ctx context.Context,
	request *system.PrivilegedControlRequest,
) privilegedExchangeOutcome {
	return exchanger.transport.Exchange(ctx, request)
}

// Client invokes only the three named loopmount capabilities.
// It exposes no raw privileged action, request frame, command, argv, path, or decoder seam.
type Client struct {
	exchanger privilegedExchanger
}

// NewClient constructs a client for the fixed root-owned privileged transport.
func NewClient() *Client {
	return &Client{exchanger: systemPrivilegedExchanger{}}
}

func newClientWithExchanger(exchanger privilegedExchanger) *Client {
	return &Client{exchanger: exchanger}
}

// ProvisionVM applies one closed plan to the receiver-derived VM rootfs.
func (client *Client) ProvisionVM(
	ctx context.Context,
	request VMProvisionRequest,
) (VMProvisionResult, error) {
	return exchangeLoopMount(
		ctx,
		client,
		VMProvisionAction,
		request,
		request.Validate,
		func(result VMProvisionResult) error { return result.Validate(request) },
	)
}

// InspectImage identifies the operating system in one receiver-derived staging image.
func (client *Client) InspectImage(
	ctx context.Context,
	request ImageInspectRequest,
) (ImageInspectResult, error) {
	return exchangeLoopMount(
		ctx,
		client,
		ImageInspectAction,
		request,
		request.Validate,
		func(result ImageInspectResult) error { return result.Validate(request) },
	)
}

// OptimizeImage applies one receiver-ordered plan to a receiver-derived staging image.
func (client *Client) OptimizeImage(
	ctx context.Context,
	request ImageOptimizeRequest,
) (ImageOptimizeResult, error) {
	return exchangeLoopMount(
		ctx,
		client,
		ImageOptimizeAction,
		request,
		request.Validate,
		func(result ImageOptimizeResult) error { return result.Validate(request) },
	)
}

func exchangeLoopMount[Request any, Result any](
	ctx context.Context,
	client *Client,
	action string,
	request Request,
	validateRequest func() error,
	validateResult func(Result) error,
) (Result, error) {
	var zero Result
	if ctx == nil {
		return zero, validationError("privileged client context is required")
	}
	if err := ctx.Err(); err != nil {
		return zero, errs.WrapMsg(errs.CodeProcessError, "start loopmount privileged operation", err)
	}
	if client == nil || client.exchanger == nil {
		return zero, errs.New(
			errs.CodeProcessError,
			"loopmount privileged client is not active",
			errs.WithClass(errs.ClassInternal),
			errs.WithDetails(map[string]any{"process_started": false, "outcome_unknown": false}),
		)
	}
	if validateRequest == nil || validateResult == nil {
		return zero, errs.New(
			errs.CodeInternal,
			"loopmount privileged client contract is incomplete",
			errs.WithClass(errs.ClassInternal),
		)
	}
	if err := validateRequest(); err != nil {
		return zero, err
	}

	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, action, request, 0); err != nil {
		return zero, err
	}
	controlRequest, err := system.NewPrivilegedControlRequest(action, frame.Bytes(), 0, nil)
	if err != nil {
		return zero, err
	}
	outcome := client.exchanger.Exchange(ctx, controlRequest)
	if outcome == nil {
		return zero, unknownLoopMountOutcome(action, false, errors.New("privileged exchange returned no outcome"))
	}

	if outcome.ResponseEOF() {
		response, decodeErr := privilegedwire.DecodeResponse[Result](
			context.WithoutCancel(ctx),
			bytes.NewReader(outcome.Response()),
			action,
		)
		if decodeErr == nil {
			if operationErr := response.OperationError(); operationErr != nil {
				return zero, operationErr
			}
			result := response.Result()
			if err := validateResult(result); err != nil {
				return zero, unknownLoopMountOutcome(
					action,
					outcome.ProcessStarted(),
					joinLoopMountOutcomeErrors(err, outcome.Diagnostic()),
				)
			}
			return result, nil
		}
		return zero, unknownLoopMountOutcome(
			action,
			outcome.ProcessStarted(),
			joinLoopMountOutcomeErrors(decodeErr, outcome.Diagnostic()),
		)
	}

	return zero, unknownLoopMountOutcome(
		action,
		outcome.ProcessStarted(),
		joinLoopMountOutcomeErrors(
			errors.New("privileged response did not reach authenticated socket EOF"),
			outcome.Diagnostic(),
		),
	)
}

func unknownLoopMountOutcome(action string, processStarted bool, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeProcessError,
		"loopmount privileged operation returned no authoritative response",
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{
			"action":          action,
			"process_started": processStarted,
			"outcome_unknown": processStarted,
		}),
	)
}

func joinLoopMountOutcomeErrors(primary error, diagnostic *errs.DomainError) error {
	if diagnostic == nil {
		return primary
	}
	return errors.Join(primary, diagnostic)
}
