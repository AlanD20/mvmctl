package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const vmExecCapacityLockName = "capacity.lock"

// VMExecCapacity pins the fixed root-owned authority directory for the host-global VM-exec capacity lock.
type VMExecCapacity struct {
	state *vmExecCapacityState
}

type vmExecCapacityState struct {
	// mu guards the authority descriptor and every lifecycle field below.
	mu         sync.RWMutex
	deps       dependencies
	policy     policy
	authority  int
	retained   []int
	active     bool
	released   bool
	releaseErr error
}

// OpenVMExecCapacity opens or creates the fixed root-owned VM-exec capacity authority tree.
func OpenVMExecCapacity(ctx context.Context) (*VMExecCapacity, error) {
	return openVMExecCapacityWithPolicy(ctx, realDependencies(), productionPolicy())
}

func openVMExecCapacityWithPolicy(
	ctx context.Context,
	deps dependencies,
	policy policy,
) (_ *VMExecCapacity, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: open VM-exec capacity authority: %w", err)
	}
	root, err := openAuthorityRoot(ctx, deps, policy)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open VM-exec capacity authority: %w", err)
	}
	retained := root.retained
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("authorityfs: close rejected VM-exec capacity authority: %w", err),
			)
		}
	}()
	return &VMExecCapacity{state: &vmExecCapacityState{
		deps: deps, policy: policy, authority: root.authority, retained: retained, active: true,
	}}, nil
}

// Acquire acquires the never-unlinked host-global VM-exec capacity lock.
func (authority *VMExecCapacity) Acquire(ctx context.Context) (*Lock, error) {
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: VM-exec capacity authority is not active")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: VM-exec capacity authority is released")
	}
	return acquireLock(
		ctx,
		state.deps,
		state.policy,
		state.authority,
		vmExecCapacityLockName,
		"VM-exec capacity",
	)
}

// Release closes the descriptors owned by the VM-exec capacity capability. An acquired lock remains valid.
func (authority *VMExecCapacity) Release(ctx context.Context) error {
	if authority == nil || authority.state == nil {
		return nil
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active {
		return nil
	}
	err := closeFDs(context.WithoutCancel(ctx), state.deps, state.retained)
	state.authority = -1
	state.retained = nil
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release VM-exec capacity authority: %w", err)
	}
	return state.releaseErr
}
