package network

import (
	"context"
	"errors"
	"fmt"
)

func retainHostLinkNamespace(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
) (_ *linkNamespaceLease, returnErr error) {
	if ctx == nil {
		return nil, errors.New("host link namespace context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNamespaceEffectDeps(deps); err != nil {
		return nil, err
	}
	sourceFD, err := deps.open(ctx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("open host link namespace: %w", err)
	}
	defer func() {
		returnErr = errors.Join(
			returnErr,
			closeConsumedNamespaceFD(context.WithoutCancel(ctx), deps, &sourceFD),
		)
	}()
	identity, err := inspectNetworkNamespaceFD(ctx, deps, policy, sourceFD)
	if err != nil {
		return nil, fmt.Errorf("inspect host link namespace: %w", err)
	}
	fd, err := deps.dupCloexec(ctx, sourceFD, 3)
	if err != nil {
		return nil, fmt.Errorf("duplicate host link namespace: %w", err)
	}
	defer func() {
		if returnErr == nil || fd < 0 {
			return
		}
		returnErr = errors.Join(
			returnErr,
			closeConsumedNamespaceFD(context.WithoutCancel(ctx), deps, &fd),
		)
	}()
	if fd <= 0 {
		return nil, errors.New("duplicated host link namespace descriptor is not explicit")
	}
	duplicated, err := inspectNetworkNamespaceFD(ctx, deps, policy, fd)
	if err != nil {
		return nil, fmt.Errorf("inspect duplicated host link namespace: %w", err)
	}
	if duplicated != identity {
		return nil, errors.New("duplicated host link namespace identity changed")
	}
	return &linkNamespaceLease{state: &linkNamespaceLeaseState{
		deps: deps, fd: fd, identity: identity,
	}}, nil
}
