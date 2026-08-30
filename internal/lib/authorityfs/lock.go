package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	lockFlags  = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	lockCreate = lockFlags | unix.O_CREAT | unix.O_EXCL
)

// Lock owns one acquired root-only advisory lock.
type Lock struct {
	state *lockState
}

type lockState struct {
	// mu guards fd and every lifecycle field below.
	mu         sync.Mutex
	deps       dependencies
	fd         int
	active     bool
	released   bool
	releaseErr error
}

// AcquireIndex acquires the global Jailer instance-index lock.
func (jailer *Jailer) AcquireIndex(ctx context.Context) (*Lock, error) {
	if jailer == nil || jailer.state == nil {
		return nil, fmt.Errorf("authorityfs: Jailer authority is not active")
	}
	state := jailer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: Jailer authority is released")
	}
	return acquireLock(ctx, state.deps, state.policy, state.authority, "index.lock", "Jailer index")
}

// AcquireIndex acquires the global managed-network ownership-index lock.
func (authority *Network) AcquireIndex(ctx context.Context) (*Lock, error) {
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: network authority is not active")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: network authority is released")
	}
	return acquireLock(
		ctx,
		state.deps,
		state.policy,
		state.authority,
		networkIndexLockName,
		"network index",
	)
}

// AcquireRelease acquires the Jailer lock for one canonical release-slot digest.
func (jailer *Jailer) AcquireRelease(ctx context.Context, key ReleaseKey) (*Lock, error) {
	if !isLowerHex(key.value, releaseKeyLength) {
		return nil, fmt.Errorf("authorityfs: release key is invalid")
	}
	if jailer == nil || jailer.state == nil {
		return nil, fmt.Errorf("authorityfs: Jailer authority is not active")
	}
	state := jailer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: Jailer authority is released")
	}
	return acquireLock(ctx, state.deps, state.policy, state.releases, key.value+".lock", "Jailer release")
}

// AcquireVM acquires the Jailer lock for one canonical VM identity.
func (uid *JailerUID) AcquireVM(ctx context.Context, key VMKey) (*Lock, error) {
	if !isLowerHex(key.value, vmKeyLength) {
		return nil, fmt.Errorf("authorityfs: VM key is invalid")
	}
	if uid == nil || uid.state == nil {
		return nil, fmt.Errorf("authorityfs: Jailer UID authority is not active")
	}
	state := uid.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: Jailer UID authority is released")
	}
	return acquireLock(ctx, state.deps, state.policy, state.fd, key.value+".lock", "Jailer VM")
}

// AcquireNetwork acquires the final authority class for one owner-bound managed network.
func (uid *NetworkUID) AcquireNetwork(ctx context.Context, key NetworkKey) (*Lock, error) {
	if !isLowerHex(key.value, networkKeyLength) {
		return nil, fmt.Errorf("authorityfs: network key is invalid")
	}
	if uid == nil || uid.state == nil {
		return nil, fmt.Errorf("authorityfs: network UID authority is not active")
	}
	state := uid.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: network UID authority is released")
	}
	return acquireLock(
		ctx,
		state.deps,
		state.policy,
		state.fd,
		"network-"+key.value+".lock",
		"managed network",
	)
}

// AcquireVM acquires the LoopMount lock for one canonical VM identity.
func (authority *LoopMount) AcquireVM(ctx context.Context, key VMKey) (*Lock, error) {
	if !isLowerHex(key.value, vmKeyLength) {
		return nil, fmt.Errorf("authorityfs: VM key is invalid")
	}
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: LoopMount authority is not active")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: LoopMount authority is released")
	}
	return acquireLock(ctx, state.deps, state.policy, state.fd, "vm-"+key.value+".lock", "LoopMount VM")
}

// AcquireImage acquires the LoopMount lock for one canonical image identity.
func (authority *LoopMount) AcquireImage(ctx context.Context, key ImageKey) (*Lock, error) {
	if !isLowerHex(key.value, imageKeyLength) {
		return nil, fmt.Errorf("authorityfs: image key is invalid")
	}
	if authority == nil || authority.state == nil {
		return nil, fmt.Errorf("authorityfs: LoopMount authority is not active")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return nil, fmt.Errorf("authorityfs: LoopMount authority is released")
	}
	return acquireLock(ctx, state.deps, state.policy, state.fd, "image-"+key.value+".lock", "LoopMount image")
}

func acquireLock(
	ctx context.Context,
	deps dependencies,
	policy policy,
	parentFD int,
	name string,
	description string,
) (*Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("authorityfs: acquire %s lock: %w", description, err)
	}
	createHow := &unix.OpenHow{Flags: lockCreate, Mode: uint64(lockMode), Resolve: resolveManaged}
	fd, err := deps.openAt2(ctx, parentFD, name, createHow)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = deps.openAt2(
			ctx,
			parentFD,
			name,
			&unix.OpenHow{Flags: lockFlags, Resolve: resolveManaged},
		)
	}
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open %s lock: %w", description, err)
	}
	abort := func(primary error) (*Lock, error) {
		if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
			primary = errors.Join(primary, fmt.Errorf("close rejected lock: %w", closeErr))
		}
		return nil, fmt.Errorf("authorityfs: prepare %s lock: %w", description, primary)
	}
	if created {
		if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
			return abort(err)
		}
		if err := deps.fchmod(ctx, fd, lockMode); err != nil {
			return abort(err)
		}
	}
	if err := verifyLock(ctx, deps, fd, description, policy); err != nil {
		return abort(err)
	}
	if created {
		if err := deps.fsync(ctx, fd); err != nil {
			return abort(err)
		}
		if err := deps.fsync(ctx, parentFD); err != nil {
			return abort(err)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		err := deps.flock(ctx, fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{state: &lockState{deps: deps, fd: fd, active: true}}, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return abort(err)
		}
		if err := deps.waitLock(ctx); err != nil {
			return abort(err)
		}
	}
}

func verifyLock(
	ctx context.Context,
	deps dependencies,
	fd int,
	description string,
	policy policy,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return fmt.Errorf("inspect %s lock: %w", description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size != 0 {
		return fmt.Errorf("%s lock is not an empty one-link regular file", description)
	}
	if stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID || stat.Mode&07777 != lockMode {
		return fmt.Errorf("%s lock has unexpected owner or mode", description)
	}
	return nil
}

// Release unlocks and closes the lock exactly once, preserving context values while ignoring cancellation.
func (lock *Lock) Release(ctx context.Context) error {
	if lock == nil || lock.state == nil {
		return nil
	}
	state := lock.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	unlockErr := state.deps.flock(cleanupCtx, state.fd, unix.LOCK_UN)
	closeErr := state.deps.close(cleanupCtx, state.fd)
	state.fd = -1
	state.active = false
	state.released = true
	if err := errors.Join(unlockErr, closeErr); err != nil {
		state.releaseErr = fmt.Errorf("authorityfs: release lock: %w", err)
	}
	return state.releaseErr
}
