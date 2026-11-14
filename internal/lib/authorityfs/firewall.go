package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const firewallLockName = "firewall.lock"

// Firewall pins the fixed root-owned authority directory for the host-global firewall lock.
type Firewall struct {
	state *firewallState
}

type firewallState struct {
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

// OpenFirewall opens or creates the fixed root-owned firewall authority tree.
func OpenFirewall(ctx context.Context) (*Firewall, error) {
	return openFirewallWithPolicy(ctx, realDependencies(), productionPolicy())
}

func openFirewallWithPolicy(
	ctx context.Context,
	deps dependencies,
	policy policy,
) (_ *Firewall, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: open firewall authority: %w", err)
	}
	root, err := openAuthorityRoot(ctx, deps, policy)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open firewall authority: %w", err)
	}
	retained := root.retained
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("authorityfs: close rejected firewall authority: %w", err))
		}
	}()

	return &Firewall{state: &firewallState{
		deps: deps, policy: policy, authority: root.authority, retained: retained, active: true,
	}}, nil
}

// Acquire acquires the never-unlinked host-global firewall lock.
func (authority *Firewall) Acquire(ctx context.Context) (*Lock, error) {
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: firewall authority is not active")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: firewall authority is released")
	}
	return acquireLock(ctx, state.deps, state.policy, state.authority, firewallLockName, "firewall")
}

// Release closes the descriptors owned by the firewall capability. An acquired lock remains valid.
func (authority *Firewall) Release(ctx context.Context) error {
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
		state.releaseErr = fmt.Errorf("authorityfs: release firewall authority: %w", err)
	}
	return state.releaseErr
}
