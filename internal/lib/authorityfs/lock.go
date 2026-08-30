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
	mu         sync.Mutex
	deps       dependencies
	fd         int
	active     bool
	released   bool
	releaseErr error
}

// AcquireIndex acquires the global Jailer instance-index lock.
func (jailer *Jailer) AcquireIndex(ctx context.Context) (*Lock, error) {
	if jailer == nil {
		return nil, fmt.Errorf("authorityfs: Jailer authority is not active")
	}
	jailer.mu.Lock()
	defer jailer.mu.Unlock()
	if !jailer.active {
		return nil, fmt.Errorf("authorityfs: Jailer authority is released")
	}
	return acquireLock(ctx, jailer.deps, jailer.policy, jailer.authority, "index.lock", "Jailer index")
}

// AcquireRelease acquires the Jailer lock for one canonical release-slot digest.
func (jailer *Jailer) AcquireRelease(ctx context.Context, key ReleaseKey) (*Lock, error) {
	if !isLowerHex(key.value, releaseKeyLength) {
		return nil, fmt.Errorf("authorityfs: release key is invalid")
	}
	if jailer == nil {
		return nil, fmt.Errorf("authorityfs: Jailer authority is not active")
	}
	jailer.mu.Lock()
	defer jailer.mu.Unlock()
	if !jailer.active {
		return nil, fmt.Errorf("authorityfs: Jailer authority is released")
	}
	return acquireLock(ctx, jailer.deps, jailer.policy, jailer.releases, key.value+".lock", "Jailer release")
}

// AcquireVM acquires the Jailer lock for one canonical VM identity.
func (uid *JailerUID) AcquireVM(ctx context.Context, key VMKey) (*Lock, error) {
	if !isLowerHex(key.value, vmKeyLength) {
		return nil, fmt.Errorf("authorityfs: VM key is invalid")
	}
	if uid == nil {
		return nil, fmt.Errorf("authorityfs: Jailer UID authority is not active")
	}
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if !uid.active {
		return nil, fmt.Errorf("authorityfs: Jailer UID authority is released")
	}
	return acquireLock(ctx, uid.deps, uid.policy, uid.fd, key.value+".lock", "Jailer VM")
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
		err := deps.flock(ctx, fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{deps: deps, fd: fd, active: true}, nil
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
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return fmt.Errorf("%s lock is not a one-link regular file", description)
	}
	if stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID || stat.Mode&07777 != lockMode {
		return fmt.Errorf("%s lock has unexpected owner or mode", description)
	}
	return nil
}

// Release unlocks and closes the lock exactly once, preserving context values while ignoring cancellation.
func (lock *Lock) Release(ctx context.Context) error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released {
		return lock.releaseErr
	}
	if !lock.active {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	unlockErr := lock.deps.flock(cleanupCtx, lock.fd, unix.LOCK_UN)
	closeErr := lock.deps.close(cleanupCtx, lock.fd)
	lock.fd = -1
	lock.active = false
	lock.released = true
	if err := errors.Join(unlockErr, closeErr); err != nil {
		lock.releaseErr = fmt.Errorf("authorityfs: release lock: %w", err)
	}
	return lock.releaseErr
}
