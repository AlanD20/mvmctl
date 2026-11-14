package loopmount

import (
	"context"
	"errors"
	"sync"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

type loopMountAuthorityTree interface {
	openLoopMount(context.Context, uint32) (loopMountLockAuthority, error)
}

type loopMountLockAuthority interface {
	acquireVM(context.Context, authorityfs.VMKey) (loopMountAuthorityLock, error)
	acquireImage(context.Context, authorityfs.ImageKey) (loopMountAuthorityLock, error)
	Release(context.Context) error
}

type loopMountAuthorityLock interface {
	Release(context.Context) error
}

type systemLoopMountAuthorityTree struct{}

type systemLoopMountLockAuthority struct {
	value *authorityfs.LoopMount
}

type systemLoopMountAuthorityLock struct {
	value *authorityfs.Lock
}

type authorityFSRootfsResourceLocks struct {
	tree loopMountAuthorityTree
}

type authorityFSRootfsResourceLockLease struct {
	state *authorityFSRootfsResourceLockLeaseState
}

type authorityFSRootfsResourceLockLeaseState struct {
	// mu guards both capabilities and every lifecycle field below.
	mu         sync.Mutex
	lock       loopMountAuthorityLock
	authority  loopMountLockAuthority
	released   bool
	releaseErr error
}

type rootfsResourceLockFamily uint8

const (
	rootfsResourceLockVM rootfsResourceLockFamily = iota + 1
	rootfsResourceLockImage
)

type rootfsResourceLockKey struct {
	family   rootfsResourceLockFamily
	vmKey    authorityfs.VMKey
	imageKey authorityfs.ImageKey
}

func newSystemRootfsResourceLocks() rootfsResourceLocks {
	return &authorityFSRootfsResourceLocks{tree: systemLoopMountAuthorityTree{}}
}

func (locks *authorityFSRootfsResourceLocks) Acquire(
	ctx context.Context,
	caller Caller,
	selection rootfsSelection,
) (rootfsResourceLockLease, error) {
	if err := validateRootfsCaller(caller); err != nil {
		return nil, err
	}
	key, err := resolveRootfsResourceLockKey(selection)
	if err != nil {
		return nil, err
	}
	if locks == nil || locks.tree == nil {
		return nil, rootfsAtomicError("loopmount authority tree is not configured", nil)
	}
	authority, openErr := locks.tree.openLoopMount(ctx, caller.UID)
	var lease *authorityFSRootfsResourceLockLease
	if authority != nil {
		lease = &authorityFSRootfsResourceLockLease{state: &authorityFSRootfsResourceLockLeaseState{
			authority: authority,
		}}
	}
	if openErr != nil {
		return lease, mapRootfsResourceLockError("open loopmount authority", openErr)
	}
	if authority == nil {
		return nil, rootfsAtomicError("loopmount authority is missing", nil)
	}

	var lock loopMountAuthorityLock
	switch key.family {
	case rootfsResourceLockVM:
		lock, err = authority.acquireVM(ctx, key.vmKey)
	case rootfsResourceLockImage:
		lock, err = authority.acquireImage(ctx, key.imageKey)
	default:
		return lease, rootfsAtomicError("loopmount resource lock family is invalid", nil)
	}
	lease.state.lock = lock
	if err != nil {
		return lease, mapRootfsResourceLockError("acquire loopmount resource lock", err)
	}
	if lock == nil {
		return lease, rootfsAtomicError("loopmount resource lock is missing", nil)
	}
	return lease, nil
}

func resolveRootfsResourceLockKey(selection rootfsSelection) (rootfsResourceLockKey, error) {
	switch selection.kind {
	case rootfsVMProvisionSelection:
		key, err := authorityfs.NewVMKey(selection.resourceID)
		if err != nil {
			return rootfsResourceLockKey{}, rootfsValidationError("managed VM lock identity is invalid")
		}
		return rootfsResourceLockKey{family: rootfsResourceLockVM, vmKey: key}, nil
	case rootfsImageInspectSelection, rootfsImageOptimizeSelection:
		key, err := authorityfs.NewImageKey(selection.resourceID)
		if err != nil {
			return rootfsResourceLockKey{}, rootfsValidationError("managed image lock identity is invalid")
		}
		return rootfsResourceLockKey{family: rootfsResourceLockImage, imageKey: key}, nil
	default:
		return rootfsResourceLockKey{}, rootfsValidationError("managed rootfs lock selection is invalid")
	}
}

func (lease *authorityFSRootfsResourceLockLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	if lease.state == nil {
		return rootfsAtomicError("loopmount resource lock lease is not active", nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.lock == nil && state.authority == nil {
		state.released = true
		state.releaseErr = rootfsAtomicError("loopmount resource lock lease is not active", nil)
		return state.releaseErr
	}

	cleanupCtx := context.WithoutCancel(ctx)
	var lockErr error
	if state.lock != nil {
		lockErr = state.lock.Release(cleanupCtx)
	}
	var authorityErr error
	if state.authority != nil {
		authorityErr = state.authority.Release(cleanupCtx)
	}
	state.lock = nil
	state.authority = nil
	state.released = true
	if err := errors.Join(lockErr, authorityErr); err != nil {
		state.releaseErr = rootfsAtomicError("release loopmount resource lock lease", err)
	}
	return state.releaseErr
}

func (systemLoopMountAuthorityTree) openLoopMount(
	ctx context.Context,
	uid uint32,
) (loopMountLockAuthority, error) {
	value, err := authorityfs.OpenLoopMount(ctx, uid)
	if value == nil {
		return nil, err
	}
	return &systemLoopMountLockAuthority{value: value}, err
}

func (authority *systemLoopMountLockAuthority) acquireVM(
	ctx context.Context,
	key authorityfs.VMKey,
) (loopMountAuthorityLock, error) {
	if authority == nil || authority.value == nil {
		return nil, errors.New("loopmount authority is not active")
	}
	value, err := authority.value.AcquireVM(ctx, key)
	if value == nil {
		return nil, err
	}
	return &systemLoopMountAuthorityLock{value: value}, err
}

func (authority *systemLoopMountLockAuthority) acquireImage(
	ctx context.Context,
	key authorityfs.ImageKey,
) (loopMountAuthorityLock, error) {
	if authority == nil || authority.value == nil {
		return nil, errors.New("loopmount authority is not active")
	}
	value, err := authority.value.AcquireImage(ctx, key)
	if value == nil {
		return nil, err
	}
	return &systemLoopMountAuthorityLock{value: value}, err
}

func (authority *systemLoopMountLockAuthority) Release(ctx context.Context) error {
	if authority == nil || authority.value == nil {
		return errors.New("loopmount authority is not active")
	}
	return authority.value.Release(ctx)
}

func (lock *systemLoopMountAuthorityLock) Release(ctx context.Context) error {
	if lock == nil || lock.value == nil {
		return errors.New("loopmount authority lock is not active")
	}
	return lock.value.Release(ctx)
}

func mapRootfsResourceLockError(message string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return errs.WrapMsg(
			errs.CodeLoopMountError,
			message,
			cause,
			errs.WithClass(errs.ClassRetryable),
		)
	}
	return rootfsAtomicError(message, cause)
}
