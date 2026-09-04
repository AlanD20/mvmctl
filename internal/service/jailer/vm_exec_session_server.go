package jailer

import (
	"context"
	"sync"

	"mvmctl/pkg/errs"
)

// VMExecSessionCaller is the authenticated non-root identity requesting one retained VM-exec session.
// CRITICAL: UID and GID must come exclusively from authenticated peer credentials, never request payload fields.
type VMExecSessionCaller struct {
	UID uint32
	GID uint32
}

// VMExecSessionConnection is a borrowed, structurally admitted connection capability.
// CRITICAL: Implementations must keep the connection descriptor live for the complete synchronous binder call.
// The interface deliberately exposes no descriptor, file, close operation, or generic callback.
type VMExecSessionConnection interface {
	BindVMExecSessionPeer(context.Context, VMExecSessionPeerBinder) error
}

// VMExecSessionStopWaitReady publishes that an armed stop has no immediate result and its pending result cannot be
// missed. The callback is invoked exactly once for a pending wait and is omitted for every immediate result.
type VMExecSessionStopWaitReady func()

// VMExecSessionPeerBinder performs one server-selected source or destination peer binding.
// Copies share one single-use invocation and its result.
type VMExecSessionPeerBinder struct {
	state *vmExecSessionPeerBinderState
}

type vmExecSessionPeerBinderState struct {
	// mu guards single-use invocation, completion, sealing, and cached evidence.
	mu        sync.Mutex
	bind      func(context.Context, int) error
	invoked   bool
	finished  bool
	sealed    bool
	result    error
	violation error
	done      chan struct{}
}

// Bind applies the server-selected peer authority to one borrowed connection descriptor.
// CRITICAL: This is the sole action-specific raw-descriptor bridge. It may be invoked synchronously exactly once.
func (binder VMExecSessionPeerBinder) Bind(ctx context.Context, descriptor int) error {
	if binder.state == nil {
		return vmExecSessionError("vm-exec session peer binder is unavailable", nil)
	}
	state := binder.state
	state.mu.Lock()
	if state.invoked {
		if state.violation == nil {
			state.violation = vmExecSessionError("vm-exec session peer binder was invoked more than once", nil)
		}
		violation := state.violation
		state.mu.Unlock()
		return violation
	}
	if state.sealed {
		state.mu.Unlock()
		return vmExecSessionError("vm-exec session peer binder is already sealed", nil)
	}
	state.invoked = true
	bind := state.bind
	state.mu.Unlock()

	result := bind(ctx, descriptor)
	state.mu.Lock()
	state.result = result
	state.finished = true
	close(state.done)
	state.mu.Unlock()
	return result
}

func newVMExecSessionPeerBinder(bind func(context.Context, int) error) VMExecSessionPeerBinder {
	return VMExecSessionPeerBinder{state: &vmExecSessionPeerBinderState{
		bind: bind,
		done: make(chan struct{}),
	}}
}

func (binder VMExecSessionPeerBinder) seal(connectionErr error) error {
	connectionErr = preserveVMExecSessionServerError(
		"bind vm-exec session peer connection",
		connectionErr,
	)
	if binder.state == nil {
		return joinVMExecSessionErrors(
			connectionErr,
			vmExecSessionError("vm-exec session peer binder is unavailable", nil),
		)
	}
	state := binder.state
	state.mu.Lock()
	if !state.sealed {
		state.sealed = true
	}
	if !state.invoked {
		state.mu.Unlock()
		return joinVMExecSessionErrors(
			connectionErr,
			vmExecSessionError("vm-exec session peer binder was not invoked", nil),
		)
	}
	finished := state.finished
	done := state.done
	state.mu.Unlock()

	if !finished {
		<-done
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	return joinVMExecSessionErrors(connectionErr, state.result, state.violation)
}

func preserveVMExecSessionServerError(message string, cause error) error {
	if cause == nil || errs.AsDomainError(cause) != nil {
		return cause
	}
	return vmExecSessionError(message, cause)
}

type vmExecSessionServerAuthorityLease interface {
	bindSource(context.Context, int) error
	bindDestination(context.Context, int) error
	admission() (vmExecSessionAdmission, error)
	Release(context.Context) error
}

type vmExecSessionServerStop interface {
	waitReady(context.Context, VMExecSessionStopWaitReady) error
	Release(context.Context) error
}

type vmExecSessionServerDeps struct {
	reserve func(
		context.Context,
		instanceCaller,
		string,
		string,
	) (vmExecSessionServerAuthorityLease, error)
	armStop func(context.Context, uint64) (vmExecSessionServerStop, error)
}

// VMExecSessionServer composes exact active-pair, global-capacity, peer, and absolute-stop authority.
// CRITICAL: It accepts only typed VM identities and borrowed connection capabilities. It performs no relay, policy,
// audit, token, user, command, path, or payload operation.
type VMExecSessionServer struct {
	deps vmExecSessionServerDeps
}

// NewVMExecSessionServer constructs the production retained VM-exec session authority.
func NewVMExecSessionServer() (*VMExecSessionServer, error) {
	authority, err := newVMExecSessionAuthority()
	if err != nil {
		return nil, err
	}
	stopFactory, err := newVMExecRelayStopFactory()
	if err != nil {
		return nil, err
	}
	return newVMExecSessionServerWithDeps(vmExecSessionServerDeps{
		reserve: func(
			ctx context.Context,
			caller instanceCaller,
			sourceVMID string,
			destinationVMID string,
		) (vmExecSessionServerAuthorityLease, error) {
			return authority.reserve(ctx, caller, sourceVMID, destinationVMID)
		},
		armStop: func(ctx context.Context, deadline uint64) (vmExecSessionServerStop, error) {
			return stopFactory.arm(ctx, deadline)
		},
	})
}

func newVMExecSessionServerWithDeps(deps vmExecSessionServerDeps) (*VMExecSessionServer, error) {
	if deps.reserve == nil || deps.armStop == nil {
		return nil, vmExecSessionError("vm-exec session server dependencies are incomplete", nil)
	}
	return &VMExecSessionServer{deps: deps}, nil
}

// OpenSource reserves the exact active pair and capacity, binds the source peer, then arms the admitted absolute stop.
// A failure after reservation returns a non-nil releasable lease so the caller retains cleanup authority.
// CRITICAL: caller must be derived from the authenticated connection's peer credentials before this method is called.
func (server *VMExecSessionServer) OpenSource(
	ctx context.Context,
	caller VMExecSessionCaller,
	request VMExecSessionOpenRequest,
	connection VMExecSessionConnection,
) (*VMExecSessionServerLease, error) {
	if server == nil || server.deps.reserve == nil || server.deps.armStop == nil {
		return nil, vmExecSessionError("vm-exec session server is unavailable", nil)
	}
	if ctx == nil {
		return nil, vmExecSessionError("vm-exec session server context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, vmExecSessionError("open vm-exec session was cancelled", err)
	}
	privateCaller := instanceCaller{uid: caller.UID, gid: caller.GID}
	if err := validateInstanceCaller(privateCaller); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, vmExecSessionError("vm-exec source connection is required", nil)
	}

	authority, reserveErr := server.deps.reserve(
		ctx,
		privateCaller,
		request.SourceVMID,
		request.DestinationVMID,
	)
	reserveErr = preserveVMExecSessionServerError("reserve vm-exec session authority", reserveErr)
	if authority == nil {
		if reserveErr != nil {
			return nil, reserveErr
		}
		return nil, vmExecSessionError("vm-exec session authority returned no lease", nil)
	}
	lease := &VMExecSessionServerLease{state: &vmExecSessionServerLeaseState{authority: authority}}
	if reserveErr != nil {
		return lease, reserveErr
	}

	binder := newVMExecSessionPeerBinder(authority.bindSource)
	if err := binder.seal(connection.BindVMExecSessionPeer(ctx, binder)); err != nil {
		return lease, err
	}
	admission, err := authority.admission()
	if err != nil {
		return lease, preserveVMExecSessionServerError("read vm-exec session admission", err)
	}
	stop, armErr := server.deps.armStop(ctx, admission.relayStopBoottimeNS)
	lease.state.stop = stop
	if armErr != nil {
		return lease, preserveVMExecSessionServerError("arm vm-exec session stop authority", armErr)
	}
	if stop == nil {
		return lease, vmExecSessionError("vm-exec session stop authority returned no lease", nil)
	}
	return lease, nil
}

// VMExecSessionServerLease retains one admitted pair, its peer authorities, capacity, and absolute stop.
// Copies share the same close-once lifecycle and cached release result.
type VMExecSessionServerLease struct {
	state *vmExecSessionServerLeaseState
}

type vmExecSessionServerLeaseState struct {
	// mu prevents binding from racing release and guards close-once cleanup and its cached result.
	mu         sync.Mutex
	authority  vmExecSessionServerAuthorityLease
	stop       vmExecSessionServerStop
	released   bool
	releaseErr error
}

// BindDestination binds one borrowed destination connection to the retained destination runtime authority.
func (lease *VMExecSessionServerLease) BindDestination(
	ctx context.Context,
	connection VMExecSessionConnection,
) error {
	if lease == nil || lease.state == nil {
		return vmExecSessionError("vm-exec session server lease is unavailable", nil)
	}
	if ctx == nil {
		return vmExecSessionError("vm-exec destination binding context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return vmExecSessionError("bind vm-exec destination was cancelled", err)
	}
	if connection == nil {
		return vmExecSessionError("vm-exec destination connection is required", nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.authority == nil {
		return vmExecSessionError("vm-exec session server lease is released or incomplete", nil)
	}
	binder := newVMExecSessionPeerBinder(state.authority.bindDestination)
	return binder.seal(connection.BindVMExecSessionPeer(ctx, binder))
}

// WaitStop blocks until the authority's absolute CLOCK_BOOTTIME stop expires or waiting fails. For a pending wait,
// ready is called exactly once only after an immediate result has been excluded and the later result cannot be missed.
func (lease *VMExecSessionServerLease) WaitStop(ctx context.Context, ready VMExecSessionStopWaitReady) error {
	if lease == nil || lease.state == nil {
		return vmExecSessionError("vm-exec session server lease is unavailable", nil)
	}
	if ctx == nil {
		return vmExecSessionError("vm-exec session stop wait context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return vmExecSessionError("wait for vm-exec session stop was cancelled", err)
	}
	if ready == nil {
		return vmExecSessionError("vm-exec session stop readiness signal is required", nil)
	}
	state := lease.state
	state.mu.Lock()
	if state.released || state.stop == nil {
		state.mu.Unlock()
		return vmExecSessionError("vm-exec session stop authority is unavailable", nil)
	}
	stop := state.stop
	state.mu.Unlock()
	return preserveVMExecSessionServerError("wait for vm-exec session stop", stop.waitReady(ctx, ready))
}

// Release closes the stop first and then the retained peer/pair/capacity authority exactly once.
// Cancellation never shortens cleanup; every caller observes the same cached result.
func (lease *VMExecSessionServerLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	if ctx == nil {
		state.releaseErr = vmExecSessionError("vm-exec session server release context is required", nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	if state.stop != nil {
		stopErr := state.stop.Release(cleanupCtx)
		if stopErr != nil {
			state.releaseErr = appendVMExecSessionError(
				state.releaseErr,
				"release vm-exec session stop authority",
				stopErr,
			)
		}
	}
	if state.authority != nil {
		authorityErr := state.authority.Release(cleanupCtx)
		if authorityErr != nil {
			state.releaseErr = appendVMExecSessionError(
				state.releaseErr,
				"release vm-exec retained authority",
				authorityErr,
			)
		}
	}
	state.stop = nil
	state.authority = nil
	state.released = true
	return state.releaseErr
}
