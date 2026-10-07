package jailer

import "context"

// VMRootfsExportClient transports only the named VM-to-image disk-copy operation.
type VMRootfsExportClient struct{ transport *TrustedReleaseClient }

// NewVMRootfsExportClient binds image imports to the authenticated installed receiver.
func NewVMRootfsExportClient() *VMRootfsExportClient {
	return &VMRootfsExportClient{transport: NewTrustedReleaseClient()}
}

// Export returns an independent copy only after the original VM state is restored.
func (client *VMRootfsExportClient) Export(
	ctx context.Context, request VMRootfsExportRequest,
) (VMRootfsExportResult, error) {
	if err := request.Validate(); err != nil {
		return VMRootfsExportResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMRootfsExportAction, request, nil, 0,
		func(result VMRootfsExportResult) error { return result.Validate(request) })
}

// Release is idempotent and cannot select caller files or mutate source state.
func (client *VMRootfsExportClient) Release(
	ctx context.Context,
	request VMRootfsExportReleaseRequest,
) (VMRootfsExportReleaseResult, error) {
	if err := request.Validate(); err != nil {
		return VMRootfsExportReleaseResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMRootfsExportReleaseAction, request, nil, 0,
		func(result VMRootfsExportReleaseResult) error { return result.Validate(request) })
}
