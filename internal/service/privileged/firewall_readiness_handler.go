package privileged

import (
	"context"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/firewall"
)

func handleFirewallReadiness(ctx context.Context, endpoint controlEndpoint, deps controlDeps) error {
	deadline := boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout)
	reader := newRequestReader(ctx, int(endpoint.Fd()), deadline, deps)
	if _, err := privilegedwire.DecodeRequest[firewall.ReadinessRequest](
		ctx,
		reader,
		firewall.ReadinessAction,
		0,
	); err != nil {
		return deliverOperationError(ctx, endpoint, firewall.ReadinessAction, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, firewall.ReadinessAction, err, deps)
	}
	result, err := firewall.ProbeReadiness(ctx)
	if err != nil {
		return deliverOperationError(ctx, endpoint, firewall.ReadinessAction, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, firewall.ReadinessAction, result, deps)
}
