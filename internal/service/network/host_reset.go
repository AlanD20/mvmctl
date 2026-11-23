package network

import (
	"context"
	"errors"
	"sync"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/service/firewall"
)

// HostResetGuard retains the network ownership index and empty firewall authority through host reset.
// Copies share release-once state. It grants no authority to delete networks or historical records.
type HostResetGuard struct{ state *hostResetGuardState }

type hostResetGuardState struct {
	// mu guards the retained capabilities and cached release result.
	mu       sync.Mutex
	root     *authorityfs.Network
	index    *authorityfs.Lock
	store    *networkStoreRoot
	host     *linkNamespaceLease
	firewall *firewall.HostResetGuard
	released bool
	closeErr error
}

// AcquireHostResetGuard proves network quiescence without adopting or removing live state.
// CALLER MUST HOLD: exclusive host admission, then the Jailer global index. The caller retains
// those guards until this guard is released; queued operations must recheck host admission afterward.
func AcquireHostResetGuard(ctx context.Context) (_ *HostResetGuard, returnErr error) {
	if ctx == nil {
		return nil, networkAuthorityValidationError("host reset network context is required")
	}
	guard := &HostResetGuard{state: &hostResetGuardState{}}
	state := guard.state
	defer func() {
		if returnErr != nil {
			returnErr = appendNetworkAuthorityError(
				returnErr,
				"release rejected host reset network guard",
				guard.Release(ctx),
			)
		}
	}()
	var err error
	state.root, err = authorityfs.OpenNetwork(ctx)
	if err != nil {
		return nil, err
	}
	state.index, err = state.root.AcquireIndex(ctx)
	if err != nil {
		return nil, err
	}
	state.store, err = openHostResetNetworkStore(ctx)
	if err != nil {
		return nil, err
	}
	namespace := newNamespaceEffect()
	state.host, err = retainHostLinkNamespace(ctx, namespace.deps, namespace.policy)
	if err != nil {
		return nil, err
	}
	if err := state.requireQuiescent(ctx); err != nil {
		return nil, err
	}
	state.firewall, err = firewall.AcquireHostResetGuard(ctx)
	if err != nil {
		return nil, err
	}
	// Reobserve local authority after acquiring the complete suffix. No boot mismatch or empty
	// user database is treated as evidence that a root-owned resource disappeared.
	if err := state.requireQuiescent(ctx); err != nil {
		return nil, err
	}
	return guard, nil
}

func (state *hostResetGuardState) requireQuiescent(ctx context.Context) error {
	inventory, err := readHostResetNetworkInventory(ctx, state.store)
	if err != nil {
		return err
	}
	if err := state.root.RequireHostResetNamespacesAbsent(ctx); err != nil {
		return networkAuthorityEffectError("admit host reset namespace absence", err)
	}
	fd, _, err := state.host.descriptor()
	if err != nil {
		return err
	}
	links, err := newDirectRouteNetlink().listLinks(ctx, fd)
	if err != nil {
		return networkAuthorityEffectError("inspect host reset network links", err)
	}
	for _, link := range links {
		if inventory.ownsName(link.name) || hostResetReservedLink(link) {
			return networkAuthorityCorruptError("host reset requires managed network links to be absent", nil)
		}
	}
	return nil
}

// Release closes the firewall suffix before releasing the network index. It never deletes state.
func (guard *HostResetGuard) Release(ctx context.Context) error {
	if guard == nil || guard.state == nil {
		return nil
	}
	if ctx == nil {
		return networkAuthorityValidationError("host reset network release context is required")
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.closeErr
	}
	cleanup := context.WithoutCancel(ctx)
	var closeErr error
	if state.firewall != nil {
		closeErr = errors.Join(closeErr, state.firewall.Release(cleanup))
	}
	if state.host != nil {
		closeErr = errors.Join(closeErr, state.host.close(cleanup))
	}
	if state.store != nil {
		closeErr = errors.Join(closeErr, state.store.Release(cleanup))
	}
	if state.index != nil {
		closeErr = errors.Join(closeErr, state.index.Release(cleanup))
	}
	if state.root != nil {
		closeErr = errors.Join(closeErr, state.root.Release(cleanup))
	}
	state.released, state.closeErr = true, closeErr
	return closeErr
}
