package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// LoopMount pins the fixed root-owned authority directory for one authenticated UID.
type LoopMount struct {
	state *loopMountState
}

type loopMountState struct {
	// mu guards fd and every lifecycle field below.
	mu         sync.RWMutex
	deps       dependencies
	policy     policy
	fd         int
	retained   []int
	active     bool
	released   bool
	releaseErr error
}

// OpenLoopMount opens or creates the fixed LoopMount authority tree for an authenticated UID.
func OpenLoopMount(ctx context.Context, uid uint32) (*LoopMount, error) {
	return openLoopMountWithPolicy(ctx, realDependencies(), productionPolicy(), uid)
}

func openLoopMountWithPolicy(
	ctx context.Context,
	deps dependencies,
	policy policy,
	uid uint32,
) (_ *LoopMount, returnErr error) {
	if uid == 0 {
		return nil, fmt.Errorf("authorityfs: LoopMount UID must be non-zero")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: open LoopMount authority: %w", err)
	}
	root, err := openAuthorityRoot(ctx, deps, policy)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open LoopMount authority: %w", err)
	}
	retained := root.retained
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("authorityfs: close rejected LoopMount authority: %w", err))
		}
	}()

	uidFD, err := openManagedDirectory(
		ctx,
		deps,
		root.authority,
		formatUID(uid),
		directoryMode,
		resolveManaged,
		policy,
	)
	if err != nil {
		return nil, err
	}
	retained = append(retained, uidFD)
	loopmountFD, err := openManagedDirectory(
		ctx,
		deps,
		uidFD,
		"loopmount",
		directoryMode,
		resolveManaged,
		policy,
	)
	if err != nil {
		return nil, err
	}
	retained = append(retained, loopmountFD)
	return &LoopMount{state: &loopMountState{
		deps: deps, policy: policy, fd: loopmountFD, retained: retained, active: true,
	}}, nil
}

// Release closes the descriptors owned by the LoopMount capability. Acquired locks remain valid.
func (authority *LoopMount) Release(ctx context.Context) error {
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
	state.fd = -1
	state.retained = nil
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release LoopMount authority: %w", err)
	}
	return state.releaseErr
}
