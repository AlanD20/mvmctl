package jailer

import "context"

// VMLifecycleClient exposes only named lifecycle operations over the authenticated receiver.
type VMLifecycleClient struct{ transport *TrustedReleaseClient }

// NewVMLifecycleClient constructs the production privileged transport.
func NewVMLifecycleClient() *VMLifecycleClient {
	return &VMLifecycleClient{transport: NewTrustedReleaseClient()}
}

// Launch returns only the display projection of a committed root-owned launch.
func (client *VMLifecycleClient) Launch(ctx context.Context, request VMLaunchRequest) (VMLaunchResult, error) {
	if err := request.Validate(); err != nil {
		return VMLaunchResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMLaunchAction, request, nil, 0,
		func(result VMLaunchResult) error { return result.Validate(request) })
}

// Abort terminates only processes admitted from the exact root-owned instance authority.
func (client *VMLifecycleClient) Abort(ctx context.Context, request VMCleanupRequest) (VMCleanupResult, error) {
	if err := request.Validate(); err != nil {
		return VMCleanupResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMAbortAction, request, nil, 0,
		func(result VMCleanupResult) error { return result.Validate(request) })
}

// Cleanup recovers and completes the fixed cleanup transaction for one owner-bound VM.
func (client *VMLifecycleClient) Cleanup(ctx context.Context, request VMCleanupRequest) (VMCleanupResult, error) {
	if err := request.Validate(); err != nil {
		return VMCleanupResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMCleanupAction, request, nil, 0,
		func(result VMCleanupResult) error { return result.Validate(request) })
}

// Remove also invalidates persistent root-owned endpoint reservations and traffic grants.
func (client *VMLifecycleClient) Remove(ctx context.Context, request VMCleanupRequest) (VMCleanupResult, error) {
	if err := request.Validate(); err != nil {
		return VMCleanupResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMRemoveAction, request, nil, 0,
		func(result VMCleanupResult) error { return result.Validate(request) })
}
