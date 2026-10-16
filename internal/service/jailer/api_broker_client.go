package jailer

import "context"

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

// Resume asks the root broker to resume one exact active VM.
func (client *VMAPIBrokerClient) Resume(ctx context.Context, request VMAPIRequest) (VMAPIResult, error) {
	if err := request.Validate(); err != nil {
		return VMAPIResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMResumeAction, request, nil, 0,
		func(result VMAPIResult) error { return result.Validate(request) })
}

// StartInstance starts the already admitted Firecracker instance; it never launches a process.
func (client *VMAPIBrokerClient) StartInstance(ctx context.Context, request VMAPIRequest) (VMAPIResult, error) {
	if err := request.Validate(); err != nil {
		return VMAPIResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMStartInstanceAction, request, nil, 0,
		func(result VMAPIResult) error { return result.Validate(request) })
}
