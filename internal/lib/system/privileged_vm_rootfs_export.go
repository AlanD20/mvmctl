package system

import (
	"context"

	"mvmctl/pkg/errs"
)

// PrivilegedVMRootfsExportAction identifies the fixed root export operation.
const PrivilegedVMRootfsExportAction = "jailer-vm-rootfs-export"

// ExchangeVMRootfsExport transfers only a payload-free VM rootfs export request.
// CRITICAL: Interruption disconnects the caller without killing the separately
// sessioned root worker, which owns bounded original-state restoration and discard.
func (PrivilegedControlTransport) ExchangeVMRootfsExport(
	ctx context.Context,
	request *PrivilegedControlRequest,
) PrivilegedControlOutcome {
	return exchangePrivilegedVMRootfsExport(
		ctx, request, realPrivilegedControlDeps(), productionPrivilegedControlPolicy(),
	)
}

func exchangePrivilegedVMRootfsExport(
	ctx context.Context,
	request *PrivilegedControlRequest,
	deps privilegedControlDeps,
	policy privilegedControlPolicy,
) PrivilegedControlOutcome {
	if request == nil {
		return privilegedControlFailure(false, false, errs.New(
			errs.CodeValidationFailed, "VM rootfs export request is required",
		))
	}
	// Request metadata is immutable; refusal must leave its payload and single-use
	// ownership intact so another action cannot acquire export interruption semantics.
	if request.action != PrivilegedVMRootfsExportAction || request.payloadLength != 0 || request.payload != nil {
		return privilegedControlFailure(false, false, errs.New(
			errs.CodeValidationFailed, "VM rootfs export requires the fixed action and no payload",
		))
	}
	return exchangePrivilegedControlSession(ctx, request, deps, policy, nil, true)
}
