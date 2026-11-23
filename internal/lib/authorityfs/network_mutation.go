package authorityfs

import (
	"context"
	"fmt"
	"sync"
)

// NetworkMutation owns the exact earlier network lock required before firewall mutation.
// THREAD-SAFE: copied handles share transfer and close-once state.
type NetworkMutation struct {
	state *networkMutationState
}

type networkMutationState struct {
	// mu guards the lock, immutable binding transfer, and cached release outcome.
	mu         sync.Mutex
	lock       *Lock
	ownerUID   uint32
	key        NetworkKey
	released   bool
	releaseErr error
}

// AcquireMutation acquires the named owner-bound network mutation capability.
func (uid *NetworkUID) AcquireMutation(ctx context.Context, key NetworkKey) (*NetworkMutation, error) {
	if uid == nil || uid.state == nil || !isLowerHex(key.value, networkKeyLength) {
		return nil, fmt.Errorf("authorityfs: network mutation binding is invalid")
	}
	state := uid.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: network UID authority is released")
	}
	lock, err := acquireLock(ctx, state.deps, state.policy, state.fd,
		"network-"+key.value+".lock", "managed network")
	if err != nil {
		return nil, err
	}
	return &NetworkMutation{state: &networkMutationState{lock: lock, ownerUID: state.ownerUID, key: key}}, nil
}

// Binding returns the immutable owner and key only while the exact network lock is retained.
func (mutation *NetworkMutation) Binding() (uint32, NetworkKey, error) {
	if mutation == nil || mutation.state == nil {
		return 0, NetworkKey{}, fmt.Errorf("authorityfs: network mutation is not active")
	}
	state := mutation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.verify(); err != nil {
		return 0, NetworkKey{}, err
	}
	return state.ownerUID, state.key, nil
}

// Verify proves the named network mutation still owns its earlier lock.
func (mutation *NetworkMutation) Verify(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("authorityfs: network mutation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, err := mutation.Binding()
	return err
}

func (state *networkMutationState) verify() error {
	if state.released || state.lock == nil || state.lock.state == nil {
		return fmt.Errorf("authorityfs: network mutation is not active")
	}
	lock := state.lock.state
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if !lock.active || lock.released || lock.fd < 0 {
		return fmt.Errorf("authorityfs: network mutation lock is not active")
	}
	return nil
}

// Transfer moves the exact lock into a new owner and invalidates every source copy.
// A failed transfer preserves source ownership.
func (mutation *NetworkMutation) Transfer(ctx context.Context) (*NetworkMutation, error) {
	if ctx == nil {
		return nil, fmt.Errorf("authorityfs: network mutation context is required")
	}
	if mutation == nil || mutation.state == nil {
		return nil, fmt.Errorf("authorityfs: network mutation is not active")
	}
	state := mutation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.verify(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target := &NetworkMutation{state: &networkMutationState{
		lock: state.lock, ownerUID: state.ownerUID, key: state.key,
	}}
	state.lock = nil
	state.ownerUID = 0
	state.key = NetworkKey{}
	state.released = true
	return target, nil
}

// Release unlocks exactly once, including after cancellation, and caches the first outcome.
func (mutation *NetworkMutation) Release(ctx context.Context) error {
	if mutation == nil || mutation.state == nil {
		return nil
	}
	state := mutation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if ctx == nil {
		return fmt.Errorf("authorityfs: network mutation context is required")
	}
	if state.lock == nil {
		return fmt.Errorf("authorityfs: network mutation lock is absent")
	}
	state.releaseErr = state.lock.Release(context.WithoutCancel(ctx))
	state.lock = nil
	state.ownerUID = 0
	state.key = NetworkKey{}
	state.released = true
	return state.releaseErr
}
