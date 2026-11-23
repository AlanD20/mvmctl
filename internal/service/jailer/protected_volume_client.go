package jailer

import "context"

// ProtectedVolumeClient exposes named owner-scoped storage operations over the privileged transport.
type ProtectedVolumeClient struct{ transport *TrustedReleaseClient }

// NewProtectedVolumeClient binds storage operations to the authenticated fixed receiver.
func NewProtectedVolumeClient() *ProtectedVolumeClient {
	return &ProtectedVolumeClient{transport: NewTrustedReleaseClient()}
}

// Inspect reads the authoritative backing without importing an untouched caller-owned volume.
func (client *ProtectedVolumeClient) Inspect(
	ctx context.Context, request ProtectedVolumeInspectRequest,
) (ProtectedVolumeInspectResult, error) {
	if err := request.Validate(); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, ProtectedVolumeInspectAction, request, nil, 0,
		func(result ProtectedVolumeInspectResult) error { return result.Validate(request.VolumeID) })
}

// Grow requests an idempotent absolute target on the authoritative backing.
func (client *ProtectedVolumeClient) Grow(
	ctx context.Context, request ProtectedVolumeGrowRequest,
) (ProtectedVolumeInspectResult, error) {
	if err := request.Validate(); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, ProtectedVolumeGrowAction, request, nil, 0,
		func(result ProtectedVolumeInspectResult) error {
			if err := result.Validate(request.VolumeID); err != nil {
				return err
			}
			if result.LogicalBytes != request.TargetBytes {
				return managedVMResourceValidationError("volume growth result differs from the requested target")
			}
			return nil
		})
}

// Remove retains root-side reference checks and durable idempotency across local database failures.
func (client *ProtectedVolumeClient) Remove(
	ctx context.Context, request ProtectedVolumeRemoveRequest,
) (ProtectedVolumeRemoveResult, error) {
	if err := request.Validate(); err != nil {
		return ProtectedVolumeRemoveResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, ProtectedVolumeRemoveAction, request, nil, 0,
		func(result ProtectedVolumeRemoveResult) error { return result.Validate(request.VolumeID) })
}
