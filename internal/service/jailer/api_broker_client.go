package jailer

import (
	"context"

	"mvmctl/pkg/errs"
)

// VMAPIBrokerClient exposes only the named VM control operations supported by ordinary callers.
type VMAPIBrokerClient struct{ transport *TrustedReleaseClient }

// NewVMAPIBrokerClient binds the authenticated privileged transport.
func NewVMAPIBrokerClient() *VMAPIBrokerClient {
	return &VMAPIBrokerClient{transport: NewTrustedReleaseClient()}
}

// WaitReady confirms the isolated Firecracker API peer without exposing its socket.
func (client *VMAPIBrokerClient) WaitReady(ctx context.Context, request VMAPIRequest) (VMAPIReadyResult, error) {
	if err := request.Validate(); err != nil {
		return VMAPIReadyResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMAPIReadyAction, request, nil, 0,
		func(result VMAPIReadyResult) error { return result.Validate(request) })
}

// Pause asks the root broker to pause one exact active VM.
func (client *VMAPIBrokerClient) Pause(ctx context.Context, request VMAPIRequest) (VMAPIResult, error) {
	if err := request.Validate(); err != nil {
		return VMAPIResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMPauseAction, request, nil, 0,
		func(result VMAPIResult) error { return result.Validate(request) })
}

// Resume asks the root broker to resume one exact active VM. A confirmed result
// may accompany a Guest Control reconciliation error.
func (client *VMAPIBrokerClient) Resume(ctx context.Context, request VMAPIRequest) (VMAPIResult, error) {
	if err := request.Validate(); err != nil {
		return VMAPIResult{}, err
	}
	result, err := exchangeTrustedRelease(ctx, client.transport, VMResumeAction, request, nil, 0,
		func(result VMAPIResult) error { return result.Validate(request) })
	// The dispatcher sets this action-specific error only after confirming
	// the Firecracker transition. Transport errors never carry that proof.
	if failure := errs.AsDomainError(err); failure != nil &&
		failure.Code == errs.CodeVMResumeGuestControlFailed &&
		failure.Entity == request.VMID && failure.Details["vm_state"] == "running" {
		result = VMAPIResult{VMID: request.VMID, Applied: true}
	}
	return result, err
}

// StartInstance starts the already admitted Firecracker instance; it never launches a process.
func (client *VMAPIBrokerClient) StartInstance(ctx context.Context, request VMAPIRequest) (VMAPIResult, error) {
	if err := request.Validate(); err != nil {
		return VMAPIResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMStartInstanceAction, request, nil, 0,
		func(result VMAPIResult) error { return result.Validate(request) })
}
