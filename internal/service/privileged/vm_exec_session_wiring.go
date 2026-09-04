package privileged

import (
	"context"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type vmExecSessionServer interface {
	OpenSource(
		context.Context,
		jailer.VMExecSessionCaller,
		jailer.VMExecSessionOpenRequest,
		jailer.VMExecSessionConnection,
	) (*jailer.VMExecSessionServerLease, error)
}

type vmExecSessionServerLeaseAdapter struct {
	lease *jailer.VMExecSessionServerLease
}

type vmExecSessionStopWait interface {
	Pending() bool
	Wait() error
}

var (
	_ jailer.VMExecSessionConnection = vmExecSessionConnection{}
	_ vmExecSessionHandlerLease      = (*vmExecSessionServerLeaseAdapter)(nil)
)

func newVMExecSessionHandlerEffects(server vmExecSessionServer) vmExecSessionHandlerEffects {
	return vmExecSessionHandlerEffects{openSource: func(
		ctx context.Context,
		caller callerIdentity,
		request vmexecsessionwire.OpenSource,
		connection vmExecSessionConnection,
	) (vmExecSessionHandlerLease, error) {
		if server == nil {
			return nil, errs.New(errs.CodeInternal, "retained VM-exec session server is not active")
		}
		lease, err := server.OpenSource(
			ctx,
			jailer.VMExecSessionCaller{UID: caller.uid, GID: caller.gid},
			jailer.VMExecSessionOpenRequest{
				SourceVMID:      request.SourceVMID,
				DestinationVMID: request.DestinationVMID,
			},
			connection,
		)
		if lease == nil {
			return nil, err
		}
		return &vmExecSessionServerLeaseAdapter{lease: lease}, err
	}}
}

// BindVMExecSessionPeer keeps the handler-owned connection live and immutable through the sole synchronous peer bind.
// CRITICAL: The owner mutex prevents cleanup from closing or reusing the descriptor while Jailer proves its peer.
func (connection vmExecSessionConnection) BindVMExecSessionPeer(
	ctx context.Context,
	binder jailer.VMExecSessionPeerBinder,
) error {
	if connection.retained == nil || connection.retained.owner == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec connection is required")
	}
	owner := connection.retained.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if ctx == nil {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec connection context is required")
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "bind retained VM-exec connection was cancelled", err)
	}
	if owner.closed {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec connection is closed")
	}
	if owner.fd < 0 {
		return errs.New(errs.CodeValidationFailed, "retained VM-exec connection descriptor is invalid")
	}
	return binder.Bind(ctx, owner.fd)
}

func (adapter *vmExecSessionServerLeaseAdapter) bindDestination(
	ctx context.Context,
	connection vmExecSessionConnection,
) error {
	if adapter == nil || adapter.lease == nil {
		return errs.New(errs.CodeInternal, "retained VM-exec session lease is not active")
	}
	return adapter.lease.BindDestination(ctx, connection)
}

func (adapter *vmExecSessionServerLeaseAdapter) waitStop(
	ctx context.Context,
	ready vmExecSessionStopWaitReady,
) error {
	if adapter == nil || adapter.lease == nil {
		return errs.New(errs.CodeInternal, "retained VM-exec session lease is not active")
	}
	if ready == nil {
		return errs.New(errs.CodeInternal, "retained VM-exec session stop readiness handler is unavailable")
	}
	wait, err := adapter.lease.StartStopWait(ctx)
	if err != nil {
		return err
	}
	return finishVMExecSessionStopWait(wait, ready)
}

func finishVMExecSessionStopWait(wait vmExecSessionStopWait, ready vmExecSessionStopWaitReady) error {
	if wait == nil {
		return errs.New(errs.CodeInternal, "retained VM-exec session stop wait is unavailable")
	}
	if wait.Pending() {
		if ready == nil {
			return errs.New(errs.CodeInternal, "retained VM-exec session stop readiness handler is unavailable")
		}
		ready()
	}
	return wait.Wait()
}

func (adapter *vmExecSessionServerLeaseAdapter) release(ctx context.Context) error {
	if adapter == nil || adapter.lease == nil {
		return nil
	}
	return adapter.lease.Release(ctx)
}
