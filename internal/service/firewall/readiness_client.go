package firewall

import (
	"bytes"
	"context"
	"errors"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

// ReadinessClient invokes only the fixed root-owned firewall readiness probe.
type ReadinessClient struct{}

// NewReadinessClient binds readiness to the installed privileged dispatcher.
func NewReadinessClient() *ReadinessClient { return &ReadinessClient{} }

// Probe observes actual kernel capability through a disposable root-created network namespace.
func (client *ReadinessClient) Probe(ctx context.Context) (ReadinessResult, error) {
	if client == nil || ctx == nil {
		return ReadinessResult{}, generationAuthorityError(
			"firewall readiness client and context are required",
			nil,
			nil,
		)
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, ReadinessAction, ReadinessRequest{}, 0); err != nil {
		return ReadinessResult{}, err
	}
	request, err := system.NewPrivilegedControlRequest(ReadinessAction, frame.Bytes(), 0, nil)
	if err != nil {
		return ReadinessResult{}, err
	}
	outcome := (system.PrivilegedControlTransport{}).Exchange(ctx, request)
	if !outcome.ProcessStarted() || !outcome.ResponseEOF() {
		return ReadinessResult{}, generationAuthorityError(
			"firewall readiness response is incomplete",
			outcome.Diagnostic(),
			nil,
		)
	}
	response, err := privilegedwire.DecodeResponse[ReadinessResult](
		context.WithoutCancel(ctx),
		bytes.NewReader(outcome.Response()),
		ReadinessAction,
	)
	if err != nil {
		return ReadinessResult{}, generationAuthorityError(
			"decode firewall readiness response",
			errors.Join(err, outcome.Diagnostic()),
			nil,
		)
	}
	if err := response.OperationError(); err != nil {
		return ReadinessResult{}, err
	}
	result := response.Result()
	if err := result.Validate(); err != nil {
		return ReadinessResult{}, err
	}
	return result, nil
}
