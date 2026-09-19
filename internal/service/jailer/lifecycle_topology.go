package jailer

import (
	"context"

	"mvmctl/internal/lib/authorityfs"
)

// retainProductionTopologyHold transfers a real child hold to the network transaction. The
// parent VM lock cannot be released until that transaction releases this exact capability.
func (lease *launchLease) retainProductionTopologyHold(ctx context.Context) (*authorityfs.TopologyHold, error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.registrationComplete || state.vmLock == nil ||
		state.record.lifecycle != instanceLifecycleLaunching {
		return nil, instanceStateError(state.record.vmID, "durable launch authority is required for topology")
	}
	if ctx == nil {
		return nil, instanceStateError(state.record.vmID, "topology transfer context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("retain production launch topology", err)
	}
	retained, err := state.vmLock.retainTopologyAuthority(ctx)
	if err != nil {
		return nil, err
	}
	hold, err := retained.transferProductionHold(ctx)
	if err != nil {
		return nil, appendInstanceOperationError(err, "release rejected production topology hold",
			retained.Release(context.WithoutCancel(ctx)))
	}
	return hold, nil
}

func (lease *topologyAuthorityLease) transferProductionHold(ctx context.Context) (*authorityfs.TopologyHold, error) {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.value == nil {
		return nil, instanceStateError("", "topology authority is not active")
	}
	// CRITICAL: A private test implementation or an owner/VM tuple cannot mint production
	// topology authority. Only the concrete retained authorityfs hold crosses this boundary.
	system, ok := state.value.(*systemTopologyAuthority)
	if !ok || system == nil || system.state == nil {
		return nil, instanceStateError("", "production topology authority is unavailable")
	}
	concrete := system.state
	concrete.mu.Lock()
	defer concrete.mu.Unlock()
	if concrete.released || concrete.value == nil {
		return nil, instanceStateError("", "production topology authority is not active")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("transfer production topology authority", err)
	}
	owner, key, err := concrete.value.Binding()
	if err != nil {
		return nil, instanceLockError("inspect production topology binding", err)
	}
	if owner != state.ownerUID || key != state.key || owner != concrete.ownerUID || key != concrete.key {
		return nil, instanceAtomicError("production topology authority binding changed", nil)
	}
	hold := concrete.value
	concrete.value = nil
	concrete.ownerUID = 0
	concrete.key = authorityfs.VMKey{}
	concrete.released = true
	state.value = nil
	state.ownerUID = 0
	state.key = authorityfs.VMKey{}
	state.released = true
	return hold, nil
}
