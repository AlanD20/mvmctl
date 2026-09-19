package network

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/service/firewall"
	"mvmctl/pkg/errs"
)

// ManagedNetworkServer owns bridge creation and exact root network registry cleanup.
type ManagedNetworkServer struct{}

// NewManagedNetworkServer constructs the fixed root-side network service.
func NewManagedNetworkServer() *ManagedNetworkServer { return &ManagedNetworkServer{} }

// managedNetworkTransaction is invocation-local and retains index -> network -> firewall authority.
type managedNetworkTransaction struct {
	key       networkKey
	root      *authorityfs.Network
	index     *authorityfs.Lock
	owner     *authorityfs.NetworkUID
	mutation  *authorityfs.NetworkMutation
	storeRoot *networkStoreRoot
	store     *networkStoreUID
	host      *linkNamespaceLease
	uplink    *uplinkLease
	firewall  *firewall.MutationLease
	route     *directRouteNetlink
	record    networkAuthorityRecord
	found     bool
}

// CreateManagedNetwork creates down, records exact identity, applies firewall state, then raises the bridge.
// CRITICAL: ownerUID is the authenticated control peer; request fields grant no host device authority.
func (server *ManagedNetworkServer) CreateManagedNetwork(
	ctx context.Context, ownerUID uint32, request ManagedNetworkCreateRequest,
) (_ ManagedNetworkResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return ManagedNetworkResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID), true)
	if err != nil {
		return ManagedNetworkResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	l3, err := request.configuration()
	if err != nil {
		return ManagedNetworkResult{}, err
	}
	if err := tx.rejectPrefixOverlap(ctx, l3); err != nil {
		return ManagedNetworkResult{}, err
	}
	if tx.found {
		if tx.record.configuration.l3 != l3 || tx.record.configuration.nat != request.NAT {
			return ManagedNetworkResult{}, errs.New(errs.CodeNetworkAlreadyExists, "managed network intent differs from root authority")
		}
	}
	fd, host, err := tx.host.descriptor()
	if err != nil {
		return ManagedNetworkResult{}, err
	}
	if request.NAT {
		tx.uplink, err = retainUplink(ctx, host)
		if err != nil {
			return ManagedNetworkResult{}, err
		}
		if tx.found && tx.uplink.selected != *tx.record.configuration.uplink {
			return ManagedNetworkResult{}, networkAuthorityCorruptError("managed network NAT uplink changed", nil)
		}
	}
	if !tx.found {
		name := deriveNetworkLinkName("mvb", tx.key, "managed-bridge")
		before, err := tx.route.inspectLink(ctx, fd, name)
		if err != nil {
			return ManagedNetworkResult{}, networkAuthorityEffectError("inspect new managed bridge", err)
		}
		if before != nil {
			return ManagedNetworkResult{}, networkAuthorityCorruptError("derived bridge exists without root authority; clean installation is required", nil)
		}
		createErr := tx.route.createBridgeDown(ctx, fd, name, managedBridgeAlias(name))
		observed, inspectErr := tx.route.inspectLink(context.WithoutCancel(ctx), fd, name)
		if inspectErr != nil || observed == nil {
			return ManagedNetworkResult{}, errs.WrapMsg(errs.CodeNetworkBridgeFailed,
				"managed bridge creation could not establish exact identity", errors.Join(createErr, inspectErr),
				errs.WithDetails(map[string]any{"outcome_unknown": inspectErr != nil, "network_id": request.NetworkID}))
		}
		bridge := linkIdentity{name: name, ifIndex: observed.ifIndex, namespace: host}
		if err := validateExactManagedLink(*observed, bridge, linkKindBridge, managedBridgeAlias(name), 0, 0); err != nil {
			return ManagedNetworkResult{}, networkAuthorityCorruptError("created bridge identity differs", errors.Join(createErr, err))
		}
		if observed.flags&unix.IFF_UP != 0 {
			return ManagedNetworkResult{}, networkAuthorityCorruptError("new managed bridge is unexpectedly up", createErr)
		}
		boot, err := readCurrentBootID(ctx)
		if err != nil {
			return ManagedNetworkResult{}, tx.removeUnpublishedBridge(ctx, bridge, err)
		}
		tx.record = networkAuthorityRecord{schemaVersion: managedNetworkSchemaVersion, ownerUID: ownerUID,
			networkID: tx.key.networkID, bootID: boot, managedBridge: bridge,
			configuration: &managedNetworkConfiguration{l3: l3, nat: request.NAT}}
		if tx.uplink != nil {
			selected := tx.uplink.selected
			tx.record.configuration.uplink = &selected
		}
		// CRITICAL: Publish exact down-link ownership before addressing or exposure. A later failure is retryable by ID.
		writeErr := tx.store.writeRecord(context.WithoutCancel(ctx), tx.record)
		if writeErr != nil {
			if de := errs.AsDomainError(writeErr); de != nil && de.Details["record_created"] == true {
				tx.found = true
				return ManagedNetworkResult{}, writeErr
			}
			return ManagedNetworkResult{}, tx.removeUnpublishedBridge(ctx, bridge, errors.Join(createErr, writeErr))
		}
		tx.found = true
		if createErr != nil {
			return ManagedNetworkResult{}, networkAuthorityEffectError("create down managed bridge", createErr)
		}
	}
	if err := tx.configureBridge(ctx); err != nil {
		return ManagedNetworkResult{}, err
	}
	if tx.uplink != nil {
		if err := tx.uplink.revalidate(ctx); err != nil {
			return ManagedNetworkResult{}, err
		}
	}
	if err := tx.beginFirewall(ctx); err != nil {
		return ManagedNetworkResult{}, err
	}
	if err := tx.firewall.ApplyNetwork(ctx, tx.firewallIntent()); err != nil {
		return ManagedNetworkResult{}, err
	}
	if tx.uplink != nil {
		if err := tx.uplink.revalidate(ctx); err != nil {
			return ManagedNetworkResult{}, err
		}
	}
	if err := tx.setBridgeState(ctx, true); err != nil {
		return ManagedNetworkResult{}, err
	}
	if err := inspectManagedBridgeL3(ctx, tx.route, fd, tx.record); err != nil {
		return ManagedNetworkResult{}, appendNetworkAuthorityError(networkAuthorityEffectError("verify configured managed bridge", err),
			"lower rejected managed bridge", tx.setBridgeState(context.WithoutCancel(ctx), false))
	}
	return ManagedNetworkResult{NetworkID: request.NetworkID, Bridge: tx.record.managedBridge.name.String(),
		Prefix: request.Prefix, Gateway: request.Gateway, NAT: request.NAT}, nil
}

// RemoveManagedNetwork requires a childless exact bridge, removes exposure, revokes the global graph, then deletes authority.
// CRITICAL: No prefix scan, SQLite row, or caller-selected interface grants deletion authority.
func (server *ManagedNetworkServer) RemoveManagedNetwork(
	ctx context.Context, ownerUID uint32, request ManagedNetworkRemoveRequest,
) (_ ManagedNetworkRemovalResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID), false)
	if err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if !tx.found {
		return ManagedNetworkRemovalResult{}, unavailableNetworkAuthorityError(tx.key.networkID)
	}
	fd, _, err := tx.host.descriptor()
	if err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	bridge, err := tx.inspectBridge(ctx, true)
	if err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	if bridge != nil {
		if err := tx.requireChildless(ctx, bridge.ifIndex); err != nil {
			return ManagedNetworkRemovalResult{}, err
		}
		if err := tx.setBridgeState(ctx, false); err != nil {
			return ManagedNetworkRemovalResult{}, err
		}
	}
	if err := tx.beginFirewall(ctx); err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	if err := tx.firewall.RemoveNetwork(ctx, tx.firewallIntent()); err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	bridge, err = tx.inspectBridge(ctx, true)
	if err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	if bridge != nil {
		if bridge.flags&unix.IFF_UP != 0 {
			return ManagedNetworkRemovalResult{}, networkAuthorityCorruptError("managed bridge rose during removal", nil)
		}
		if err := tx.requireChildless(ctx, bridge.ifIndex); err != nil {
			return ManagedNetworkRemovalResult{}, err
		}
		deleteErr := tx.route.deleteLink(ctx, fd, bridge.ifIndex)
		after, inspectErr := tx.route.inspectLink(context.WithoutCancel(ctx), fd, tx.record.managedBridge.name)
		if after != nil || deleteErr != nil || inspectErr != nil {
			return ManagedNetworkRemovalResult{}, networkAuthorityEffectError("remove exact managed bridge", errors.Join(deleteErr, inspectErr,
				managedBridgeStillPresent(after)))
		}
	}
	if err := tx.removeRecord(ctx); err != nil {
		return ManagedNetworkRemovalResult{}, err
	}
	return ManagedNetworkRemovalResult{NetworkID: request.NetworkID}, nil
}

func acquireManagedNetworkTransaction(
	ctx context.Context, ownerUID uint32, networkID NetworkID, create bool,
) (_ *managedNetworkTransaction, returnErr error) {
	if ctx == nil || ownerUID == 0 {
		return nil, networkAuthorityValidationError("authenticated non-root network owner and context are required")
	}
	tx := &managedNetworkTransaction{key: networkKey{ownerUID: OwnerUID(ownerUID), networkID: networkID}, route: newDirectRouteNetlink()}
	if err := validateNetworkKey(tx.key); err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = tx.release(ctx, returnErr)
		}
	}()
	var err error
	tx.root, err = authorityfs.OpenNetwork(ctx)
	if err != nil {
		return nil, err
	}
	tx.index, err = tx.root.AcquireIndex(ctx)
	if err != nil {
		return nil, err
	}
	tx.storeRoot, err = openNetworkStoreRoot(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
	if err != nil {
		return nil, err
	}
	claim, err := tx.storeRoot.findClaim(ctx, networkID)
	if err != nil {
		return nil, err
	}
	if claim != nil && claim.ownerUID != ownerUID {
		return nil, unavailableNetworkAuthorityError(networkID)
	}
	if claim == nil && !create {
		return nil, unavailableNetworkAuthorityError(networkID)
	}
	tx.store, err = tx.storeRoot.openUID(ctx, ownerUID, create)
	if err != nil {
		return nil, err
	}
	tx.owner, err = tx.root.OpenUID(ctx, ownerUID)
	if err != nil {
		return nil, err
	}
	key, err := authorityfs.NewNetworkKey(string(networkID))
	if err != nil {
		return nil, err
	}
	tx.mutation, err = tx.owner.AcquireMutation(ctx, key)
	if err != nil {
		return nil, err
	}
	tx.record, tx.found, err = tx.store.readRecord(ctx, networkID)
	if err != nil {
		return nil, err
	}
	if (claim != nil) != tx.found {
		return nil, networkAuthorityCorruptError("network claim changed under ownership index", nil)
	}
	if tx.found {
		if tx.record.schemaVersion != managedNetworkSchemaVersion || tx.record.configuration == nil {
			return nil, networkAuthorityCorruptError("legacy network authority requires a clean installation", nil)
		}
		boot, err := readCurrentBootID(ctx)
		if err != nil {
			return nil, err
		}
		if boot != tx.record.bootID {
			return nil, staleNetworkAuthorityError(networkID)
		}
	}
	ns := newNamespaceEffect()
	tx.host, err = retainHostLinkNamespace(ctx, ns.deps, ns.policy)
	if err != nil {
		return nil, err
	}
	_, identity, err := tx.host.descriptor()
	if err != nil {
		return nil, err
	}
	if tx.found && tx.record.managedBridge.namespace != identity {
		return nil, networkAuthorityCorruptError("managed bridge namespace changed", nil)
	}
	return tx, nil
}

func (tx *managedNetworkTransaction) inspectBridge(ctx context.Context, absentOK bool) (*routeLinkObservation, error) {
	fd, identity, err := tx.host.descriptor()
	if err != nil || identity != tx.record.managedBridge.namespace {
		return nil, networkAuthorityCorruptError("managed bridge namespace changed", err)
	}
	link, err := tx.route.inspectLink(ctx, fd, tx.record.managedBridge.name)
	if err != nil {
		return nil, networkAuthorityEffectError("inspect exact managed bridge", err)
	}
	if link == nil {
		if absentOK {
			return nil, nil
		}
		return nil, networkAuthorityCorruptError("managed bridge disappeared; explicit recovery is required", nil)
	}
	if err := validateExactManagedLink(*link, tx.record.managedBridge, linkKindBridge,
		managedBridgeAlias(tx.record.managedBridge.name), 0, 0); err != nil {
		return nil, networkAuthorityCorruptError("managed bridge was replaced", err)
	}
	return link, nil
}

func (tx *managedNetworkTransaction) configureBridge(ctx context.Context) error {
	link, err := tx.inspectBridge(ctx, false)
	if err != nil {
		return err
	}
	fd, _, err := tx.host.descriptor()
	if err != nil {
		return err
	}
	if link.flags&unix.IFF_UP != 0 {
		return inspectManagedBridgeL3(ctx, tx.route, fd, tx.record)
	}
	if err := tx.requireChildless(ctx, link.ifIndex); err != nil {
		return err
	}
	addresses, addressErr := tx.route.listIPv4Addresses(ctx, fd, link.ifIndex)
	routes, routeErr := tx.route.listIPv4Routes(ctx, fd, link.ifIndex)
	if err := errors.Join(addressErr, routeErr); err != nil {
		return networkAuthorityEffectError("inspect down managed bridge addressing", err)
	}
	if len(addresses) == 0 && len(routes) == 0 {
		if _, err := tx.inspectBridge(ctx, false); err != nil {
			return err
		}
		if err := tx.route.addIPv4Address(ctx, fd, link.ifIndex, tx.record.configuration.l3.bridgeAddress); err != nil {
			return networkAuthorityEffectError("configure managed bridge gateway", err)
		}
		addresses, addressErr = tx.route.listIPv4Addresses(ctx, fd, link.ifIndex)
		routes, routeErr = tx.route.listIPv4Routes(ctx, fd, link.ifIndex)
		if err := errors.Join(addressErr, routeErr); err != nil {
			return networkAuthorityEffectError("verify down managed bridge addressing", err)
		}
	}
	l3 := tx.record.configuration.l3
	if len(addresses) != 1 || exactAddressObservation(addresses[0], l3.bridgeAddress, link.ifIndex) != nil {
		return networkAuthorityCorruptError("down managed bridge has unexpected IPv4 addresses", nil)
	}
	bookkeeping, err := deriveBridgeKernelBookkeeping(l3)
	if err != nil {
		return err
	}
	expected := []routeExpectation{l3.bridgeConnectedRoute, bookkeeping.local, bookkeeping.subnetBroadcast}
	seen := make(map[routeIPv4Observation]bool, len(routes))
	for _, observed := range routes {
		matched := false
		for _, entry := range expected {
			want, err := routeExpectationToObservation(entry, link.ifIndex)
			if err != nil {
				return err
			}
			if observed == want {
				matched = true
			}
		}
		if !matched || seen[observed] {
			return networkAuthorityCorruptError("down managed bridge has unexpected IPv4 routes", nil)
		}
		seen[observed] = true
	}
	return nil
}

func (tx *managedNetworkTransaction) setBridgeState(ctx context.Context, up bool) error {
	link, err := tx.inspectBridge(ctx, false)
	if err != nil {
		return err
	}
	fd, _, err := tx.host.descriptor()
	if err != nil {
		return err
	}
	if (link.flags&unix.IFF_UP != 0) != up {
		mutationErr := tx.route.setLinkUp(ctx, fd, link.ifIndex, up)
		after, inspectErr := tx.inspectBridge(context.WithoutCancel(ctx), false)
		if mutationErr != nil || inspectErr != nil {
			return networkAuthorityEffectError("change managed bridge state", errors.Join(mutationErr, inspectErr))
		}
		if (after.flags&unix.IFF_UP != 0) != up {
			return networkAuthorityCorruptError("managed bridge state did not converge", nil)
		}
	}
	return nil
}

func (tx *managedNetworkTransaction) requireChildless(ctx context.Context, ifIndex int) error {
	fd, _, err := tx.host.descriptor()
	if err != nil {
		return err
	}
	links, err := tx.route.listLinks(ctx, fd)
	if err != nil {
		return networkAuthorityEffectError("inspect managed bridge dependents", err)
	}
	for _, link := range links {
		if link.master == ifIndex {
			return errs.New(errs.CodeNetworkError, "managed bridge still has attached endpoints", errs.WithClass(errs.ClassConflict))
		}
	}
	return nil
}

func (tx *managedNetworkTransaction) beginFirewall(ctx context.Context) error {
	var err error
	tx.firewall, err = firewall.BeginPolicyChange(ctx, tx.mutation, firewall.PolicyBinding{
		OwnerUID: tx.record.ownerUID, NetworkID: string(tx.record.networkID), BootID: tx.record.bootID,
		HostNamespace: firewall.NamespaceIdentity{Device: tx.record.managedBridge.namespace.device, Inode: tx.record.managedBridge.namespace.inode},
	})
	return err
}

func (tx *managedNetworkTransaction) firewallIntent() firewall.NetworkIntent {
	record := tx.record
	intent := firewall.NetworkIntent{OwnerUID: record.ownerUID, NetworkID: string(record.networkID),
		HostBridge: record.managedBridge.name.String(), Subnet: record.configuration.l3.prefix.value,
		HostIPv4: record.configuration.l3.gateway.value, Masquerade: record.configuration.nat}
	if record.configuration.uplink != nil {
		intent.UplinkInterface = record.configuration.uplink.link.name.String()
	}
	return intent
}

func (tx *managedNetworkTransaction) rejectPrefixOverlap(ctx context.Context, l3 managedNetworkL3) error {
	owners, err := tx.storeRoot.deps.readDirNames(ctx, tx.storeRoot.stateFD)
	if err != nil {
		return networkAuthorityEffectError("enumerate managed network prefixes", err)
	}
	for _, owner := range owners {
		uid, err := strconv.ParseUint(owner, 10, 32)
		if err != nil || uid == 0 || strconv.FormatUint(uid, 10) != owner {
			return networkAuthorityCorruptError("network owner index is invalid", err)
		}
		store, err := tx.storeRoot.openUID(ctx, uint32(uid), false)
		if err != nil {
			return err
		}
		err = tx.checkOwnerPrefixes(ctx, store, l3)
		if err = appendNetworkAuthorityError(err, "close indexed network owner", store.Release(context.WithoutCancel(ctx))); err != nil {
			return err
		}
	}
	return nil
}

func (tx *managedNetworkTransaction) checkOwnerPrefixes(ctx context.Context, store *networkStoreUID, l3 managedNetworkL3) error {
	names, err := store.deps.readDirNames(ctx, store.stateFD)
	if err != nil {
		return networkAuthorityEffectError("enumerate managed network records", err)
	}
	for _, name := range names {
		if name == "topologies" {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || !networkIDPattern.MatchString(id) {
			return networkAuthorityCorruptError("managed network inventory contains incomplete or unknown state", nil)
		}
		record, found, err := store.readRecord(ctx, NetworkID(id))
		if err != nil {
			return err
		}
		if !found || record.schemaVersion != managedNetworkSchemaVersion || record.configuration == nil {
			return networkAuthorityCorruptError("network inventory contains incompatible authority", nil)
		}
		if record.networkID == tx.key.networkID {
			continue
		}
		if record.configuration.l3.prefix.value.Overlaps(l3.prefix.value) {
			return errs.New(errs.CodeNetworkSubnetOverlap, "managed network prefix overlaps root-owned network authority", errs.WithClass(errs.ClassConflict))
		}
	}
	return nil
}

func (tx *managedNetworkTransaction) removeUnpublishedBridge(ctx context.Context, bridge linkIdentity, primary error) error {
	cleanup := context.WithoutCancel(ctx)
	tx.record.managedBridge = bridge
	link, err := tx.inspectBridge(cleanup, true)
	if err == nil && link != nil {
		err = tx.requireChildless(cleanup, bridge.ifIndex)
		if err == nil && link.flags&unix.IFF_UP != 0 {
			err = errors.New("unpublished bridge is unexpectedly up")
		}
		if err == nil {
			fd, _, descriptorErr := tx.host.descriptor()
			if descriptorErr != nil {
				err = descriptorErr
			} else {
				err = tx.route.deleteLink(cleanup, fd, bridge.ifIndex)
				after, inspectErr := tx.route.inspectLink(cleanup, fd, bridge.name)
				err = errors.Join(err, inspectErr, managedBridgeStillPresent(after))
			}
		}
	}
	return appendNetworkAuthorityError(primary, "remove unpublished managed bridge", err)
}

func managedBridgeStillPresent(link *routeLinkObservation) error {
	if link != nil {
		return errors.New("managed bridge remains present")
	}
	return nil
}

func (tx *managedNetworkTransaction) removeRecord(ctx context.Context) (returnErr error) {
	// CRITICAL: The exact record stays until both kernel bridge and firewall state are proven absent.
	name := string(tx.record.networkID) + ".json"
	fd, err := tx.store.deps.openAt2(ctx, tx.store.stateFD, name, &unix.OpenHow{Flags: networkStoreReadFlags, Resolve: networkStoreResolve})
	if err != nil {
		return networkAuthorityEffectError("open managed network removal record", err)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close removed network record", tx.store.deps.close(context.WithoutCancel(ctx), fd))
	}()
	before, err := inspectNetworkAuthorityRecordFile(ctx, tx.store.deps, fd, tx.store.policy)
	if err != nil {
		return err
	}
	raw, err := readAllNetworkAuthorityRecord(ctx, tx.store.deps, fd)
	if err != nil {
		return networkAuthorityEffectError("read managed network removal record", err)
	}
	want, err := encodeManagedNetworkRecord(tx.record)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, want) {
		return networkAuthorityCorruptError("managed network record changed before deletion", nil)
	}
	current, err := inspectNetworkAuthorityRecordFile(ctx, tx.store.deps, fd, tx.store.policy)
	if err != nil || !sameNetworkAuthorityRecordStat(before, current) {
		return networkAuthorityCorruptError("managed network removal record is unstable", err)
	}
	checkFD, err := tx.store.deps.openAt2(ctx, tx.store.stateFD, name, &unix.OpenHow{Flags: networkStoreReadFlags, Resolve: networkStoreResolve})
	if err != nil {
		return networkAuthorityEffectError("readmit named network removal record", err)
	}
	named, statErr := inspectNetworkAuthorityRecordFile(ctx, tx.store.deps, checkFD, tx.store.policy)
	closeErr := tx.store.deps.close(context.WithoutCancel(ctx), checkFD)
	if err := errors.Join(statErr, closeErr); err != nil {
		return networkAuthorityEffectError("readmit network removal record", err)
	}
	if !sameNetworkAuthorityRecordStat(current, named) {
		return networkAuthorityCorruptError("managed network removal record was replaced", nil)
	}
	if err := tx.store.deps.unlinkAt(ctx, tx.store.stateFD, name, 0); err != nil {
		return networkAuthorityEffectError("unlink exact managed network record", err)
	}
	if err := tx.store.deps.fsync(context.WithoutCancel(ctx), tx.store.stateFD); err != nil {
		return errs.WrapMsg(errs.CodeNetworkRemoveFailed, "sync managed network removal", err,
			errs.WithDetails(map[string]any{"record_removed": true, "durability_uncertain": true}))
	}
	return nil
}

func (tx *managedNetworkTransaction) release(ctx context.Context, primary error) error {
	cleanup := context.WithoutCancel(ctx)
	if tx.firewall != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network firewall", tx.firewall.Release(cleanup))
	}
	if tx.uplink != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network uplink", tx.uplink.close(cleanup))
	}
	if tx.host != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network namespace", tx.host.close(cleanup))
	}
	if tx.mutation != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network lock", tx.mutation.Release(cleanup))
	}
	if tx.owner != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network lock owner", tx.owner.Release(cleanup))
	}
	if tx.store != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network store owner", tx.store.Release(cleanup))
	}
	if tx.storeRoot != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network store", tx.storeRoot.Release(cleanup))
	}
	if tx.index != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network index", tx.index.Release(cleanup))
	}
	if tx.root != nil {
		primary = appendNetworkAuthorityError(primary, "release managed network root", tx.root.Release(cleanup))
	}
	return primary
}
