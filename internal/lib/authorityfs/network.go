package authorityfs

import (
	"context"
	"fmt"
	"sync"
)

const networkIndexLockName = "network-index.lock"

// Network pins the fixed root-only managed-network authority directory.
type Network struct {
	state *networkState
}

type networkState struct {
	// mu guards every descriptor and lifecycle field below. Read locking permits independent child and lock acquisition.
	mu         sync.RWMutex
	deps       dependencies
	policy     policy
	authority  int
	retained   []int
	active     bool
	released   bool
	releaseErr error
}

// NetworkUID pins one fixed root-owned managed-network UID directory.
type NetworkUID struct {
	state *networkUIDState
}

type networkUIDState struct {
	// mu guards fd and every lifecycle field below. Read locking preserves resource-scoped lock concurrency.
	mu         sync.RWMutex
	deps       dependencies
	policy     policy
	fd         int
	active     bool
	released   bool
	releaseErr error
}

// OpenNetwork opens or creates the fixed root-owned managed-network authority tree.
func OpenNetwork(ctx context.Context) (*Network, error) {
	return openNetworkWithPolicy(ctx, realDependencies(), productionPolicy())
}

func openNetworkWithPolicy(
	ctx context.Context,
	deps dependencies,
	policy policy,
) (*Network, error) {
	root, err := openAuthorityRoot(ctx, deps, policy)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open network authority: %w", err)
	}
	return &Network{state: &networkState{
		deps: deps, policy: policy, authority: root.authority, retained: root.retained, active: true,
	}}, nil
}

// OpenUID opens or creates the fixed root-owned managed-network directory for uid.
func (authority *Network) OpenUID(ctx context.Context, uid uint32) (*NetworkUID, error) {
	if uid == 0 {
		return nil, fmt.Errorf("authorityfs: network UID must be non-zero")
	}
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: network authority is not active")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: network authority is released")
	}
	fd, err := openManagedDirectory(
		ctx,
		state.deps,
		state.authority,
		formatUID(uid),
		directoryMode,
		resolveManaged,
		state.policy,
	)
	if err != nil {
		return nil, err
	}
	return &NetworkUID{state: &networkUIDState{
		deps: state.deps, policy: state.policy, fd: fd, active: true,
	}}, nil
}

// Release closes the descriptors owned by the Network capability. Children and locks remain valid.
func (authority *Network) Release(ctx context.Context) error {
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
	state.retained = nil
	state.authority = -1
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release network authority: %w", err)
	}
	return state.releaseErr
}

// Release closes the descriptor owned by the Network UID capability. Acquired locks remain valid.
func (uid *NetworkUID) Release(ctx context.Context) error {
	if uid == nil || uid.state == nil {
		return nil
	}
	state := uid.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active {
		return nil
	}
	err := state.deps.close(context.WithoutCancel(ctx), state.fd)
	state.fd = -1
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release network UID authority: %w", err)
	}
	return state.releaseErr
}
