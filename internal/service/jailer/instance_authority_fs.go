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
	acquireVM(context.Context, authorityfs.VMKey) (instanceVMAuthority, error)
	Release(context.Context) error
}

type instanceAuthorityLock interface {
	Release(context.Context) error
}

type instanceVMAuthority interface {
	retainTopology(context.Context) (instanceTopologyAuthority, error)
	Release(context.Context) error
}

type instanceTopologyAuthority interface {
	binding() (uint32, authorityfs.VMKey, error)
	Release(context.Context) error
}

type systemInstanceAuthorityTree struct{}

type systemJailerAuthority struct {
	// mu guards value and every lifecycle field below.
	mu         sync.Mutex
	value      *authorityfs.Jailer
	released   bool
	releaseErr error
}

type systemJailerUID struct {
	// mu guards value and every lifecycle field below.
	mu         sync.Mutex
	value      *authorityfs.JailerUID
	released   bool
	releaseErr error
}

type systemAuthorityLock struct {
	// mu guards value and every lifecycle field below.
	mu         sync.Mutex
	value      *authorityfs.Lock
	released   bool
	releaseErr error
}

type systemVMAuthority struct {
	// mu guards value and every lifecycle field below.
	mu         sync.Mutex
	value      *authorityfs.JailerVM
	released   bool
	releaseErr error
}

type systemTopologyAuthority struct {
	state *systemTopologyAuthorityState
}

type systemTopologyAuthorityState struct {
	// mu guards value, binding, and every lifecycle field below.
	mu         sync.Mutex
	value      *authorityfs.TopologyHold
	ownerUID   uint32
	key        authorityfs.VMKey
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
	if authority.released || authority.value == nil {
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
	if authority.released || authority.value == nil {
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
	if authority.released || authority.value == nil {
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
	if authority.value == nil {
		authority.released = true
		authority.releaseErr = instanceAtomicError(
			"release Jailer authority tree",
			errors.New("Jailer authority is not active"),
		)
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
) (instanceVMAuthority, error) {
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if uid.released || uid.value == nil {
		return nil, instanceLockError("acquire VM lock", errors.New("Jailer UID authority is released"))
	}
	value, err := uid.value.AcquireVM(ctx, key)
	if err != nil {
		return nil, mapAuthorityFSLockError("VM", err)
	}
	return &systemVMAuthority{value: value}, nil
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
	if uid.value == nil {
		uid.released = true
		uid.releaseErr = instanceAtomicError(
			"release Jailer UID authority",
			errors.New("Jailer UID authority is not active"),
		)
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
	if lock.value == nil {
		lock.released = true
		lock.releaseErr = instanceLockError(
			"release authority lock",
			errors.New("authority lock is not active"),
		)
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

func (authority *systemVMAuthority) retainTopology(
	ctx context.Context,
) (instanceTopologyAuthority, error) {
	if authority == nil {
		return nil, instanceStateError("", "vm authority is not active")
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released || authority.value == nil {
		return nil, instanceStateError("", "vm authority is not active")
	}
	value, err := authority.value.RetainTopology(ctx)
	if err != nil {
		return nil, instanceLockError("retain VM topology authority", err)
	}
	if value == nil {
		return nil, instanceAtomicError("retained VM topology authority is missing", nil)
	}
	ownerUID, key, err := value.Binding()
	if err != nil {
		return nil, appendInstanceOperationError(
			instanceLockError("inspect VM topology authority binding", err),
			"release rejected VM topology authority",
			value.Release(context.WithoutCancel(ctx)),
		)
	}
	return &systemTopologyAuthority{state: &systemTopologyAuthorityState{
		value: value, ownerUID: ownerUID, key: key,
	}}, nil
}

func (authority *systemVMAuthority) Release(ctx context.Context) error {
	if authority == nil {
		return nil
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return authority.releaseErr
	}
	if authority.value == nil {
		authority.released = true
		authority.releaseErr = instanceStateError("", "vm authority is not active")
		return authority.releaseErr
	}
	err := authority.value.Release(context.WithoutCancel(ctx))
	if errors.Is(err, authorityfs.ErrTopologyRetained) {
		return instanceLockError("release VM authority", err)
	}
	authority.value = nil
	authority.released = true
	if err != nil {
		authority.releaseErr = instanceLockError("release VM authority", err)
	}
	return authority.releaseErr
}

func (authority *systemTopologyAuthority) binding() (uint32, authorityfs.VMKey, error) {
	if authority == nil || authority.state == nil {
		return 0, authorityfs.VMKey{}, instanceStateError("", "vm topology authority is not active")
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.value == nil {
		return 0, authorityfs.VMKey{}, instanceStateError("", "vm topology authority is not active")
	}
	ownerUID, key, err := state.value.Binding()
	if err != nil {
		return 0, authorityfs.VMKey{}, instanceLockError("inspect VM topology authority binding", err)
	}
	if ownerUID != state.ownerUID || key != state.key {
		return 0, authorityfs.VMKey{}, instanceAtomicError(
			"VM topology authority binding changed while retained",
			nil,
		)
	}
	return ownerUID, key, nil
}

func (authority *systemTopologyAuthority) Release(ctx context.Context) error {
	if authority == nil || authority.state == nil {
		return nil
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.value == nil {
		state.releaseErr = instanceStateError("", "vm topology authority is not active")
	} else if err := state.value.Release(context.WithoutCancel(ctx)); err != nil {
		state.releaseErr = instanceLockError("release VM topology authority", err)
	}
	state.value = nil
	state.ownerUID = 0
	state.key = authorityfs.VMKey{}
	state.released = true
	return state.releaseErr
}

func mapAuthorityFSLockError(description string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return instanceLockContextError(description, err)
	}
	return instanceLockError("acquire "+description+" lock", err)
}
