package network

import (
	"context"
	"errors"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

type networkAuthorityTree interface {
	openNetwork(context.Context) (networkLockRoot, error)
}

type networkLockRoot interface {
	acquireIndex(context.Context) (networkAuthorityLock, error)
	openUID(context.Context, uint32) (networkLockUID, error)
	Release(context.Context) error
}

type networkLockUID interface {
	acquireNetwork(context.Context, authorityfs.NetworkKey) (networkAuthorityLock, error)
	createNamespaceHandle(context.Context, authorityfs.VMKey) (namespaceAuthorityTarget, error)
	Release(context.Context) error
}

type networkAuthorityLock interface {
	Release(context.Context) error
}

type systemNetworkAuthorityTree struct{}

type systemNetworkLockRoot struct {
	value *authorityfs.Network
}

type systemNetworkLockUID struct {
	value *authorityfs.NetworkUID
}

func (systemNetworkAuthorityTree) openNetwork(ctx context.Context) (networkLockRoot, error) {
	value, err := authorityfs.OpenNetwork(ctx)
	if err != nil {
		return nil, networkAuthorityEffectError("open network lock authority", err)
	}
	return &systemNetworkLockRoot{value: value}, nil
}

func (root *systemNetworkLockRoot) acquireIndex(ctx context.Context) (networkAuthorityLock, error) {
	if root == nil || root.value == nil {
		return nil, networkAuthorityCorruptError("network index lock authority is released", nil)
	}
	value, err := root.value.AcquireIndex(ctx)
	if err != nil {
		return nil, mapNetworkAuthorityLockError("network index", err)
	}
	return value, nil
}

func (root *systemNetworkLockRoot) openUID(ctx context.Context, uid uint32) (networkLockUID, error) {
	if root == nil || root.value == nil {
		return nil, networkAuthorityCorruptError("network lock authority is released", nil)
	}
	value, err := root.value.OpenUID(ctx, uid)
	if err != nil {
		return nil, networkAuthorityEffectError("open network lock owner", err)
	}
	return &systemNetworkLockUID{value: value}, nil
}

func (root *systemNetworkLockRoot) Release(ctx context.Context) error {
	if root == nil || root.value == nil {
		return nil
	}
	value := root.value
	root.value = nil
	if err := value.Release(context.WithoutCancel(ctx)); err != nil {
		return networkAuthorityEffectError("release network lock authority", err)
	}
	return nil
}

func (uid *systemNetworkLockUID) acquireNetwork(
	ctx context.Context,
	key authorityfs.NetworkKey,
) (networkAuthorityLock, error) {
	if uid == nil || uid.value == nil {
		return nil, networkAuthorityCorruptError("network lock owner authority is released", nil)
	}
	value, err := uid.value.AcquireNetwork(ctx, key)
	if err != nil {
		return nil, mapNetworkAuthorityLockError("managed network", err)
	}
	return value, nil
}

func (uid *systemNetworkLockUID) createNamespaceHandle(
	ctx context.Context,
	key authorityfs.VMKey,
) (namespaceAuthorityTarget, error) {
	if uid == nil || uid.value == nil {
		return nil, networkAuthorityCorruptError("network namespace authority is released", nil)
	}
	value, err := uid.value.CreateNamespaceHandle(ctx, key)
	if value == nil {
		return nil, err
	}
	return &systemNamespaceAuthorityTarget{value: value}, err
}

func (uid *systemNetworkLockUID) Release(ctx context.Context) error {
	if uid == nil || uid.value == nil {
		return nil
	}
	value := uid.value
	uid.value = nil
	if err := value.Release(context.WithoutCancel(ctx)); err != nil {
		return networkAuthorityEffectError("release network lock owner", err)
	}
	return nil
}

func mapNetworkAuthorityLockError(description string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errs.WrapMsg(
			errs.CodeNetworkError,
			"wait for "+description+" lock",
			err,
			errs.WithClass(errs.ClassRetryable),
		)
	}
	return networkAuthorityEffectError("acquire "+description+" lock", err)
}
