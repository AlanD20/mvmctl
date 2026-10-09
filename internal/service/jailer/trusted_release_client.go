package jailer

import (
	"bytes"
	"context"
	"errors"
	"io"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

type trustedReleaseExchange interface {
	Exchange(context.Context, trustedReleaseExchangeRequest) trustedReleaseExchangeOutcome
}

type trustedReleaseExchangeRequest struct {
	action        string
	frame         []byte
	payloadLength uint64
	payload       io.ReadCloser
}

type trustedReleaseExchangeOutcome struct {
	response    []byte
	responseEOF bool
	started     bool
	diagnostic  *errs.DomainError
}

type trustedReleaseControlTransport interface {
	Exchange(context.Context, *system.PrivilegedControlRequest) system.PrivilegedControlOutcome
	ExchangeVMRootfsExport(context.Context, *system.PrivilegedControlRequest) system.PrivilegedControlOutcome
}

type trustedReleaseSystemExchange struct {
	transport trustedReleaseControlTransport
}

func (exchange trustedReleaseSystemExchange) Exchange(
	ctx context.Context,
	request trustedReleaseExchangeRequest,
) trustedReleaseExchangeOutcome {
	if request.action == VMRootfsExportAction && (request.payloadLength != 0 || request.payload != nil) {
		return trustedReleaseExchangeOutcome{diagnostic: trustedReleaseClientPreExchangeError(
			request.payload,
			errs.New(errs.CodeValidationFailed, "VM rootfs export does not accept a payload"),
		)}
	}
	controlRequest, err := system.NewPrivilegedControlRequest(
		request.action,
		request.frame,
		request.payloadLength,
		request.payload,
	)
	if err != nil {
		if request.payload != nil {
			err = errors.Join(err, request.payload.Close())
		}
		return trustedReleaseExchangeOutcome{diagnostic: trustedReleaseClientTransportError(false, err)}
	}
	var outcome system.PrivilegedControlOutcome
	if request.action == VMRootfsExportAction {
		outcome = exchange.transport.ExchangeVMRootfsExport(ctx, controlRequest)
	} else {
		outcome = exchange.transport.Exchange(ctx, controlRequest)
	}
	return trustedReleaseExchangeOutcome{
		response:    outcome.Response(),
		responseEOF: outcome.ResponseEOF(),
		started:     outcome.ProcessStarted(),
		diagnostic:  outcome.Diagnostic(),
	}
}

// TrustedReleaseClient exposes only the named trusted-release capabilities.
// Its zero value is inactive; use NewTrustedReleaseClient for production transport binding.
type TrustedReleaseClient struct {
	exchange trustedReleaseExchange
}

// NewTrustedReleaseClient binds the trusted-release capability to the fixed system privileged transport.
func NewTrustedReleaseClient() *TrustedReleaseClient {
	return newTrustedReleaseClient(trustedReleaseSystemExchange{
		transport: system.PrivilegedControlTransport{},
	})
}

func newTrustedReleaseClient(exchange trustedReleaseExchange) *TrustedReleaseClient {
	return &TrustedReleaseClient{exchange: exchange}
}

// Install requests one exact trusted-release installation. A valid call transfers payload ownership to the client.
// Zero payload selects the receiver-owned root-origin fetch.
func (client *TrustedReleaseClient) Install(
	ctx context.Context,
	request TrustedReleaseInstallRequest,
	payload io.ReadCloser,
	payloadLength uint64,
) (TrustedReleaseInstallResult, error) {
	if ctx == nil {
		return TrustedReleaseInstallResult{}, errs.New(
			errs.CodeValidationFailed,
			"trusted release context is required",
		)
	}
	if err := request.Validate(); err != nil {
		return TrustedReleaseInstallResult{}, err
	}
	if payloadLength > trustedReleaseArchiveMaxBytes || (payloadLength == 0) != (payload == nil) {
		return TrustedReleaseInstallResult{}, errs.New(
			errs.CodeValidationFailed,
			"trusted release install payload and length are inconsistent",
		)
	}
	return exchangeTrustedRelease(
		ctx,
		client,
		TrustedReleaseInstallAction,
		request,
		payload,
		payloadLength,
		func(result TrustedReleaseInstallResult) error { return result.Validate(request) },
	)
}

// Remove requests removal of one exact trusted-release slot.
func (client *TrustedReleaseClient) Remove(
	ctx context.Context,
	request TrustedReleaseRemoveRequest,
) (TrustedReleaseRemoveResult, error) {
	if ctx == nil {
		return TrustedReleaseRemoveResult{}, errs.New(
			errs.CodeValidationFailed,
			"trusted release context is required",
		)
	}
	if err := request.Validate(); err != nil {
		return TrustedReleaseRemoveResult{}, err
	}
	return exchangeTrustedRelease(
		ctx,
		client,
		TrustedReleaseRemoveAction,
		request,
		nil,
		0,
		func(TrustedReleaseRemoveResult) error { return nil },
	)
}

func exchangeTrustedRelease[Request any, Result any](
	ctx context.Context,
	client *TrustedReleaseClient,
	action string,
	body Request,
	payload io.ReadCloser,
	payloadLength uint64,
	validateResult func(Result) error,
) (Result, error) {
	var zero Result
	if client == nil || client.exchange == nil {
		return zero, trustedReleaseClientPreExchangeError(
			payload,
			errs.New(
				errs.CodeProcessError,
				"trusted release client is not active",
				errs.WithClass(errs.ClassInternal),
			),
		)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, action, body, payloadLength); err != nil {
		return zero, trustedReleaseClientPreExchangeError(payload, err)
	}
	outcome := client.exchange.Exchange(ctx, trustedReleaseExchangeRequest{
		action:        action,
		frame:         frame.Bytes(),
		payloadLength: payloadLength,
		payload:       payload,
	})
	if outcome.responseEOF && !outcome.started {
		return zero, trustedReleaseClientTransportError(
			false,
			errors.Join(errors.New("privileged response arrived before process start"), outcome.diagnostic),
		)
	}
	if !outcome.responseEOF {
		return zero, trustedReleaseClientTransportError(
			outcome.started,
			errors.Join(errors.New("privileged response did not reach authoritative EOF"), outcome.diagnostic),
		)
	}
	response, err := privilegedwire.DecodeResponse[Result](
		context.WithoutCancel(ctx),
		bytes.NewReader(outcome.response),
		action,
	)
	if err != nil {
		return zero, trustedReleaseClientTransportError(
			outcome.started,
			errors.Join(err, outcome.diagnostic),
		)
	}
	if operationErr := response.OperationError(); operationErr != nil {
		return zero, operationErr
	}
	result := response.Result()
	if err := validateResult(result); err != nil {
		return zero, trustedReleaseClientTransportError(
			outcome.started,
			errors.Join(err, outcome.diagnostic),
		)
	}
	return result, nil
}

func trustedReleaseClientPreExchangeError(payload io.ReadCloser, cause error) *errs.DomainError {
	var closeErr error
	if payload != nil {
		closeErr = payload.Close()
	}
	return trustedReleaseClientTransportError(false, errors.Join(cause, closeErr))
}

func trustedReleaseClientTransportError(started bool, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeProcessError,
		"trusted release privileged response is not authoritative",
		cause,
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{
			"process_started": started,
			"outcome_unknown": started,
		}),
	)
}
