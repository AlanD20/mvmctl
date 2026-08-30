package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrTopologyRetained identifies a rejected Jailer VM release while topology authority is retained.
var ErrTopologyRetained = errors.New("jailer VM topology authority is retained")

// JailerVM owns one owner-bound Jailer VM lock and may retain closed topology authority from it.
type JailerVM struct {
	state *jailerVMState
}

type jailerVMState struct {
	// mu guards lock, topologyHolds, and every lifecycle field below. ownerUID and key are immutable.
	mu            sync.Mutex
	lock          *Lock
	ownerUID      uint32
	key           VMKey
	topologyHolds uint64
	released      bool
	releaseErr    error
}

// TopologyHold retains one owner-bound VM topology authority without exposing its lock or filesystem representation.
type TopologyHold struct {
	state *topologyHoldState
}

type topologyHoldState struct {
	// mu guards released and releaseErr. parent, ownerUID, and key are immutable.
	mu         sync.Mutex
	parent     *jailerVMState
	ownerUID   uint32
	key        VMKey
	released   bool
	releaseErr error
}

// RetainTopology retains one child topology authority while the Jailer VM authority remains active.
func (authority *JailerVM) RetainTopology(ctx context.Context) (*TopologyHold, error) {
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: Jailer VM authority is not active")
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.lock == nil {
		return nil, fmt.Errorf("authorityfs: Jailer VM authority is released")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: retain Jailer VM topology authority: %w", err)
	}
	if state.topologyHolds == ^uint64(0) {
		return nil, fmt.Errorf("authorityfs: retain Jailer VM topology authority: hold limit reached")
	}
	state.topologyHolds++
	return &TopologyHold{state: &topologyHoldState{
		parent: state, ownerUID: state.ownerUID, key: state.key,
	}}, nil
}

// OwnerUID returns the authenticated non-root owner bound to this topology authority.
func (hold *TopologyHold) OwnerUID() uint32 {
	if hold == nil || hold.state == nil {
		return 0
	}
	return hold.state.ownerUID
}

// VMKey returns the opaque canonical VM key bound to this topology authority.
func (hold *TopologyHold) VMKey() VMKey {
	if hold == nil || hold.state == nil {
		return VMKey{}
	}
	return hold.state.key
}

// Release drops this topology hold exactly once across copies and concurrent callers.
func (hold *TopologyHold) Release(ctx context.Context) error {
	if hold == nil || hold.state == nil {
		return nil
	}
	state := hold.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	parent := state.parent
	if parent == nil {
		state.releaseErr = fmt.Errorf("authorityfs: release Jailer VM topology authority: parent is not active")
	} else {
		parent.mu.Lock()
		if parent.topologyHolds == 0 {
			state.releaseErr = fmt.Errorf("authorityfs: release Jailer VM topology authority: hold counter underflow")
		} else {
			parent.topologyHolds--
		}
		parent.mu.Unlock()
	}
	state.parent = nil
	state.released = true
	return state.releaseErr
}

// Release unlocks and closes the Jailer VM authority exactly once after all topology holds are released.
func (authority *JailerVM) Release(ctx context.Context) error {
	if authority == nil || authority.state == nil {
		return nil
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.lock == nil {
		state.released = true
		state.releaseErr = fmt.Errorf("authorityfs: release Jailer VM authority: lock is not active")
		return state.releaseErr
	}
	if state.topologyHolds != 0 {
		return fmt.Errorf(
			"authorityfs: release Jailer VM authority with %d topology holds: %w",
			state.topologyHolds,
			ErrTopologyRetained,
		)
	}
	err := state.lock.Release(context.WithoutCancel(ctx))
	state.lock = nil
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release Jailer VM authority: %w", err)
	}
	return state.releaseErr
}
