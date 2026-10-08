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
	accounts   executionIdentityAccountDeps
	policy     instanceAuthorityPolicy
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
	// mu guards value, binding, and every lifecycle field below.
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
	ownerUID, key, err := value.binding()
	if err != nil {
		return nil, appendInstanceOperationError(
			err,
			"release rejected VM topology authority",
			value.Release(context.WithoutCancel(ctx)),
		)
	}
	if ownerUID != state.ownerUID {
		return nil, appendInstanceOperationError(
			instanceAtomicError("retained VM topology authority owner binding is invalid", nil),
			"release rejected VM topology authority",
			value.Release(context.WithoutCancel(ctx)),
		)
	}
	if key != state.key {
		return nil, appendInstanceOperationError(
			instanceAtomicError("retained VM topology authority VM binding is invalid", nil),
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

func (lease *topologyAuthorityLease) binding() (uint32, authorityfs.VMKey, error) {
	if lease == nil || lease.state == nil {
		return 0, authorityfs.VMKey{}, instanceStateError("", "vm topology authority lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.value == nil {
		return 0, authorityfs.VMKey{}, instanceStateError("", "vm topology authority lease is not active")
	}
	ownerUID, key, err := state.value.binding()
	if err != nil {
		return 0, authorityfs.VMKey{}, err
	}
	if ownerUID != state.ownerUID || key != state.key {
		return 0, authorityfs.VMKey{}, instanceAtomicError(
			"VM topology authority binding changed while retained",
			nil,
		)
	}
	return ownerUID, key, nil
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
	state.ownerUID = 0
	state.key = authorityfs.VMKey{}
	state.released = true
	return state.releaseErr
}
