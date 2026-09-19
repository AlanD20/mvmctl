package jailer

import (
	"context"

	"mvmctl/internal/lib/model"
)

// VMSnapshotClient transports only named capture, inspection, and removal operations.
type VMSnapshotClient struct{ transport *TrustedReleaseClient }

// NewVMSnapshotClient binds snapshot operations to the authenticated system receiver.
func NewVMSnapshotClient() *VMSnapshotClient {
	return &VMSnapshotClient{transport: NewTrustedReleaseClient()}
}

// Capture returns root-produced display metadata after immutable publication.
func (client *VMSnapshotClient) Capture(
	ctx context.Context,
	request VMSnapshotCaptureRequest,
) (model.SnapshotCaptureResult, error) {
	if err := request.Validate(); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMSnapshotCaptureAction, request, nil, 0,
		func(result model.SnapshotCaptureResult) error {
			if result.SourceVMID != request.VMID {
				return instanceValidationError("snapshot result source VM differs from request")
			}
			return validateSnapshotProjection(result, request.SnapshotID)
		})
}

// Inspect obtains root-authoritative snapshot metadata without accepting caller disk paths.
func (client *VMSnapshotClient) Inspect(
	ctx context.Context,
	request VMSnapshotRequest,
) (model.SnapshotCaptureResult, error) {
	if err := request.Validate(); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMSnapshotInspectAction, request, nil, 0,
		func(result model.SnapshotCaptureResult) error {
			return validateSnapshotProjection(result, request.SnapshotID)
		})
}

// Remove refuses snapshots retained as immutable memory backing by restored VMs.
func (client *VMSnapshotClient) Remove(ctx context.Context, request VMSnapshotRequest) (VMSnapshotRemoveResult, error) {
	if err := request.Validate(); err != nil {
		return VMSnapshotRemoveResult{}, err
	}
	return exchangeTrustedRelease(ctx, client.transport, VMSnapshotRemoveAction, request, nil, 0,
		func(result VMSnapshotRemoveResult) error {
			if result.SnapshotID != request.SnapshotID || !result.Removed {
				return instanceValidationError("snapshot removal result differs from request")
			}
			return nil
		})
}
