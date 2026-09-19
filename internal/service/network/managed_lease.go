package network

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

type managedNetworkLocker struct{}

type managedNetworkLease struct {
	// mu guards retained capabilities and cached release state.
	mu         sync.Mutex
	key        networkKey
	record     networkAuthorityRecord
	root       networkLockRoot
	owner      networkLockUID
	lock       networkAuthorityLock
	mutation   *authorityfs.NetworkMutation
	storeRoot  *networkStoreRoot
	store      *networkStoreUID
	host       *linkNamespaceLease
	uplink     *uplinkLease
	released   bool
	releaseErr error
}

// LockNetwork acquires only the network suffix; VM launch must never re-enter ownership registration.
func (managedNetworkLocker) LockNetwork(ctx context.Context, key networkKey) (_ NetworkLock, returnErr error) {
	if err := validateNetworkKey(key); err != nil {
		return nil, err
	}
	lease := &managedNetworkLease{key: key}
	defer func() {
		if returnErr != nil {
			returnErr = appendNetworkAuthorityError(returnErr, "release rejected managed network", lease.Release(ctx))
		}
	}()
	var err error
	lease.root, err = (systemNetworkAuthorityTree{}).openNetwork(ctx)
	if err != nil {
		return nil, err
	}
	lease.owner, err = lease.root.openUID(ctx, uint32(key.ownerUID))
	if err != nil {
		return nil, err
	}
	lockKey, err := authorityfs.NewNetworkKey(string(key.networkID))
	if err != nil {
		return nil, networkAuthorityValidationError(err.Error())
	}
	owner, ok := lease.owner.(*systemNetworkLockUID)
	if !ok || owner.value == nil {
		return nil, networkAuthorityCorruptError("managed network has no concrete lock owner", nil)
	}
	lease.mutation, err = owner.value.AcquireMutation(ctx, lockKey)
	lease.lock = lease.mutation
	if err != nil {
		return nil, err
	}
	lease.storeRoot, err = openNetworkStoreRoot(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
	if err != nil {
		return nil, err
	}
	lease.store, err = lease.storeRoot.openUID(ctx, uint32(key.ownerUID), false)
	if err != nil {
		return nil, err
	}
	record, found, err := lease.store.readRecord(ctx, key.networkID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, unavailableNetworkAuthorityError(key.networkID)
	}
	// CRITICAL: Schema 1 proves only a bridge name; it cannot authorize addressing, NAT, or a VM topology.
	if record.schemaVersion != managedNetworkSchemaVersion || record.configuration == nil {
		return nil, networkAuthorityCorruptError("network requires canonical schema-2 authority; recreate it", nil)
	}
	bootID, err := readCurrentBootID(ctx)
	if err != nil {
		return nil, err
	}
	if record.bootID != bootID {
		return nil, staleNetworkAuthorityError(key.networkID)
	}
	lease.record = record
	namespace := newNamespaceEffect()
	lease.host, err = retainHostLinkNamespace(ctx, namespace.deps, namespace.policy)
	if err != nil {
		return nil, err
	}
	fd, identity, err := lease.host.descriptor()
	if err != nil || identity != record.managedBridge.namespace {
		return nil, networkAuthorityCorruptError("managed bridge host namespace changed", err)
	}
	if err := inspectManagedBridgeL3(ctx, newDirectRouteNetlink(), fd, record); err != nil {
		return nil, networkAuthorityEffectError("admit managed bridge and L3 authority", err)
	}
	if record.configuration.nat {
		lease.uplink, err = retainUplink(ctx, identity)
		if err != nil {
			return nil, err
		}
		if lease.uplink.selected != *record.configuration.uplink {
			return nil, networkAuthorityCorruptError("root-selected NAT uplink changed since registration", nil)
		}
	}
	return lease, nil
}

func (lease *managedNetworkLease) Key() networkKey { return lease.key }

func (lease *managedNetworkLease) ManagedBridge() linkIdentity { return lease.record.managedBridge }

func (lease *managedNetworkLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return lease.releaseErr
	}
	cleanup := context.WithoutCancel(ctx)
	var result error
	if lease.uplink != nil {
		result = errors.Join(result, lease.uplink.close(cleanup))
	}
	if lease.host != nil {
		result = errors.Join(result, lease.host.close(cleanup))
	}
	if lease.store != nil {
		result = errors.Join(result, lease.store.Release(cleanup))
	}
	if lease.storeRoot != nil {
		result = errors.Join(result, lease.storeRoot.Release(cleanup))
	}
	if lease.lock != nil {
		result = errors.Join(result, lease.lock.Release(cleanup))
	}
	if lease.owner != nil {
		result = errors.Join(result, lease.owner.Release(cleanup))
	}
	if lease.root != nil {
		result = errors.Join(result, lease.root.Release(cleanup))
	}
	lease.released, lease.releaseErr = true, result
	return result
}

func inspectManagedBridgeL3(
	ctx context.Context,
	route routeNetlink,
	fd int,
	record networkAuthorityRecord,
) error {
	link, err := route.inspectLink(ctx, fd, record.managedBridge.name)
	if err != nil || link == nil {
		return errors.Join(err, errors.New("managed bridge is unavailable"))
	}
	if err := validateExactManagedLink(*link, record.managedBridge, linkKindBridge,
		managedBridgeAlias(record.managedBridge.name), 0, 0); err != nil {
		return err
	}
	if link.flags&unix.IFF_UP == 0 {
		return errors.New("managed bridge is not administratively up")
	}
	addresses, addressErr := route.listIPv4Addresses(ctx, fd, link.ifIndex)
	routes, routeErr := route.listIPv4Routes(ctx, fd, link.ifIndex)
	if err := errors.Join(addressErr, routeErr); err != nil {
		return err
	}
	l3 := record.configuration.l3
	bookkeeping, err := deriveBridgeKernelBookkeeping(l3)
	if err != nil {
		return err
	}
	return validateExactIPv4Authority(addresses, routes, l3.bridgeAddress,
		[]routeExpectation{l3.bridgeConnectedRoute, bookkeeping.local, bookkeeping.subnetBroadcast}, link.ifIndex)
}
