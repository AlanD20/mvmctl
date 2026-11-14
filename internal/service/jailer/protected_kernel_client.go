package jailer

import "context"

// ProtectedKernelClient exposes named authenticated import and retirement operations.
type ProtectedKernelClient struct{ transport *TrustedReleaseClient }

// NewProtectedKernelClient binds kernel operations to the fixed privileged receiver.
func NewProtectedKernelClient() *ProtectedKernelClient {
	return &ProtectedKernelClient{transport: NewTrustedReleaseClient()}
}

// Import is the sole operator-source authority for reopening a retired kernel identity.
func (client *ProtectedKernelClient) Import(
	ctx context.Context, request ProtectedKernelImportRequest,
) (ProtectedKernelImportResult, error) {
	if err := request.Validate(); err != nil {
		return ProtectedKernelImportResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, ProtectedKernelImportAction, request, nil, 0,
		func(result ProtectedKernelImportResult) error { return result.Validate(request.KernelID) })
}

// Remove requires authoritative absence of both VM and snapshot references.
func (client *ProtectedKernelClient) Remove(
	ctx context.Context, request ProtectedKernelRemoveRequest,
) (ProtectedKernelRemoveResult, error) {
	if err := request.Validate(); err != nil {
		return ProtectedKernelRemoveResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, ProtectedKernelRemoveAction, request, nil, 0,
		func(result ProtectedKernelRemoveResult) error { return result.Validate(request.KernelID) })
}
