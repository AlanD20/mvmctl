package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

type authorityLock struct {
	// mu guards value and every lifecycle field below.
	mu         sync.Mutex
	value      instanceAuthorityLock
	active     bool
	released   bool
	releaseErr error
}

func (roots *instanceAuthorityRoots) acquireIndexLock(ctx context.Context) (*authorityLock, error) {
	value, err := roots.authority.acquireIndex(ctx)
	if err != nil {
		return nil, err
	}
	return &authorityLock{value: value, active: true}, nil
}

func (roots *instanceAuthorityRoots) acquireReleaseSlotLock(
	ctx context.Context,
	slot releaseSlot,
) (*authorityLock, error) {
	if err := validateReleaseSlotValue(slot); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	key, err := authorityfs.NewReleaseKey(releaseSlotDigest(slot))
	if err != nil {
		return nil, instanceValidationError(err.Error())
	}
	value, err := roots.authority.acquireRelease(ctx, key)
	if err != nil {
		return nil, err
	}
	return &authorityLock{value: value, active: true}, nil
}

func (directories *instanceUIDDirectories) acquireVMLock(
	ctx context.Context,
	vmID string,
) (*authorityLock, error) {
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("vm ID is invalid")
	}
	key, err := authorityfs.NewVMKey(vmID)
	if err != nil {
		return nil, instanceValidationError(err.Error())
	}
	value, err := directories.authority.acquireVM(ctx, key)
	if err != nil {
		return nil, err
	}
	return &authorityLock{value: value, active: true}, nil
}

func (lock *authorityLock) Release(ctx context.Context) error {
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
	if lock.value == nil {
		return instanceStateError("", "authority lock is not active")
	}
	err := lock.value.Release(context.WithoutCancel(ctx))
	lock.active = false
	lock.value = nil
	lock.released = true
	lock.releaseErr = err
	return lock.releaseErr
}

func (lock *authorityLock) isActive() bool {
	if lock == nil {
		return false
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return lock.active
}

func releaseLockName(slot releaseSlot) string {
	return releaseSlotDigest(slot) + ".lock"
}

func releaseSlotDigest(slot releaseSlot) string {
	canonical := slot.version + "\x00" + slot.architecture
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func instanceLockError(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeProcessError, message+": "+cause.Error(), cause)
}

func instanceLockContextError(description string, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeProcessError,
		fmt.Sprintf("wait for %s lock: %v", description, cause),
		cause,
		errs.WithClass(errs.ClassRetryable),
	)
}
