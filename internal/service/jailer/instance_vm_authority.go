package jailer

import (
	"context"
	"errors"
	"sync"

	"mvmctl/internal/lib/authorityfs"
)

type vmAuthorityLease struct {
	state *vmAuthorityLeaseState
}

type vmAuthorityLeaseState struct {
	// mu guards value and every lifecycle field below. ownerUID and key are immutable.
	mu         sync.Mutex
	value      instanceVMAuthority
	ownerUID   uint32
	key        authorityfs.VMKey
	active     bool
	released   bool
	releaseErr error
}

type topologyAuthorityLease struct {
	state *topologyAuthorityLeaseState
}

type topologyAuthorityLeaseState struct {
	// mu guards value and every lifecycle field below. ownerUID and key are immutable.
	mu         sync.Mutex
	value      instanceTopologyAuthority
	ownerUID   uint32
	key        authorityfs.VMKey
	released   bool
	releaseErr error
}

func (lease *vmAuthorityLease) retainTopologyAuthority(
	ctx context.Context,
) (*topologyAuthorityLease, error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "vm authority lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active || state.released || state.value == nil {
		return nil, instanceStateError("", "vm authority lease is not active")
	}
	value, err := state.value.retainTopology(ctx)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, instanceAtomicError("retained VM topology authority is missing", nil)
	}
	if value.ownerUID() != state.ownerUID || value.vmKey() != state.key {
		return nil, appendInstanceOperationError(
			instanceAtomicError("retained VM topology authority binding is invalid", nil),
			"release rejected VM topology authority",
			value.Release(context.WithoutCancel(ctx)),
		)
	}
	return &topologyAuthorityLease{state: &topologyAuthorityLeaseState{
		value: value, ownerUID: state.ownerUID, key: state.key,
	}}, nil
}

func (lease *vmAuthorityLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active || state.value == nil {
		state.value = nil
		state.active = false
		state.released = true
		state.releaseErr = instanceStateError("", "vm authority lease is not active")
		return state.releaseErr
	}
	err := state.value.Release(context.WithoutCancel(ctx))
	if errors.Is(err, authorityfs.ErrTopologyRetained) {
		return err
	}
	state.value = nil
	state.active = false
	state.released = true
	state.releaseErr = err
	return state.releaseErr
}

func (lease *vmAuthorityLease) isActive() bool {
	if lease == nil || lease.state == nil {
		return false
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.active
}

func (lease *topologyAuthorityLease) ownerUID() uint32 {
	if lease == nil || lease.state == nil {
		return 0
	}
	return lease.state.ownerUID
}

func (lease *topologyAuthorityLease) vmKey() authorityfs.VMKey {
	if lease == nil || lease.state == nil {
		return authorityfs.VMKey{}
	}
	return lease.state.key
}

func (lease *topologyAuthorityLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.value == nil {
		state.releaseErr = instanceStateError("", "vm topology authority lease is not active")
	} else {
		state.releaseErr = state.value.Release(context.WithoutCancel(ctx))
	}
	state.value = nil
	state.released = true
	return state.releaseErr
}
