package jailer

import (
	"context"
	"errors"
	"sync"

	"mvmctl/internal/lib/authorityfs"
)

type instanceAuthorityTree interface {
	openJailer(context.Context) (instanceJailerAuthority, error)
}

type instanceJailerAuthority interface {
	acquireIndex(context.Context) (instanceAuthorityLock, error)
	acquireRelease(context.Context, authorityfs.ReleaseKey) (instanceAuthorityLock, error)
	openUID(context.Context, uint32) (instanceJailerUID, error)
	Release(context.Context) error
}

type instanceJailerUID interface {
	acquireVM(context.Context, authorityfs.VMKey) (instanceAuthorityLock, error)
	Release(context.Context) error
}

type instanceAuthorityLock interface {
	Release(context.Context) error
}

type systemInstanceAuthorityTree struct{}

type systemJailerAuthority struct {
	mu         sync.Mutex
	value      *authorityfs.Jailer
	released   bool
	releaseErr error
}

type systemJailerUID struct {
	mu         sync.Mutex
	value      *authorityfs.JailerUID
	released   bool
	releaseErr error
}

type systemAuthorityLock struct {
	mu         sync.Mutex
	value      *authorityfs.Lock
	released   bool
	releaseErr error
}

func (systemInstanceAuthorityTree) openJailer(ctx context.Context) (instanceJailerAuthority, error) {
	value, err := authorityfs.OpenJailer(ctx)
	if err != nil {
		return nil, instanceAtomicError("open Jailer authority tree", err)
	}
	return &systemJailerAuthority{value: value}, nil
}

func (authority *systemJailerAuthority) acquireIndex(ctx context.Context) (instanceAuthorityLock, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return nil, instanceLockError("acquire global index lock", errors.New("Jailer authority is released"))
	}
	value, err := authority.value.AcquireIndex(ctx)
	if err != nil {
		return nil, mapAuthorityFSLockError("global index", err)
	}
	return &systemAuthorityLock{value: value}, nil
}

func (authority *systemJailerAuthority) acquireRelease(
	ctx context.Context,
	key authorityfs.ReleaseKey,
) (instanceAuthorityLock, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return nil, instanceLockError("acquire release lock", errors.New("Jailer authority is released"))
	}
	value, err := authority.value.AcquireRelease(ctx, key)
	if err != nil {
		return nil, mapAuthorityFSLockError("release", err)
	}
	return &systemAuthorityLock{value: value}, nil
}

func (authority *systemJailerAuthority) openUID(
	ctx context.Context,
	uid uint32,
) (instanceJailerUID, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return nil, instanceAtomicError("open Jailer UID authority", errors.New("Jailer authority is released"))
	}
	value, err := authority.value.OpenUID(ctx, uid)
	if err != nil {
		return nil, instanceAtomicError("open Jailer UID authority", err)
	}
	return &systemJailerUID{value: value}, nil
}

func (authority *systemJailerAuthority) Release(ctx context.Context) error {
	if authority == nil {
		return nil
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return authority.releaseErr
	}
	err := authority.value.Release(ctx)
	authority.value = nil
	authority.released = true
	if err != nil {
		authority.releaseErr = instanceAtomicError("release Jailer authority tree", err)
	}
	return authority.releaseErr
}

func (uid *systemJailerUID) acquireVM(
	ctx context.Context,
	key authorityfs.VMKey,
) (instanceAuthorityLock, error) {
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if uid.released {
		return nil, instanceLockError("acquire VM lock", errors.New("Jailer UID authority is released"))
	}
	value, err := uid.value.AcquireVM(ctx, key)
	if err != nil {
		return nil, mapAuthorityFSLockError("VM", err)
	}
	return &systemAuthorityLock{value: value}, nil
}

func (uid *systemJailerUID) Release(ctx context.Context) error {
	if uid == nil {
		return nil
	}
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if uid.released {
		return uid.releaseErr
	}
	err := uid.value.Release(ctx)
	uid.value = nil
	uid.released = true
	if err != nil {
		uid.releaseErr = instanceAtomicError("release Jailer UID authority", err)
	}
	return uid.releaseErr
}

func (lock *systemAuthorityLock) Release(ctx context.Context) error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released {
		return lock.releaseErr
	}
	err := lock.value.Release(ctx)
	lock.value = nil
	lock.released = true
	if err != nil {
		lock.releaseErr = instanceLockError("release authority lock", err)
	}
	return lock.releaseErr
}

func mapAuthorityFSLockError(description string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return instanceLockContextError(description, err)
	}
	return instanceLockError("acquire "+description+" lock", err)
}
