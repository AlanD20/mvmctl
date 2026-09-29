package network

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type hostResetNetworkInventory struct {
	links      map[linkName]struct{}
	removed    map[NetworkID]networkAuthorityRecord
	cancelled  map[NetworkID]uint32
	topologies map[VMID]topologyJournalRecord
}

func (inventory hostResetNetworkInventory) ownsName(name linkName) bool {
	_, found := inventory.links[name]
	return found
}

func hostResetReservedLink(link routeLinkObservation) bool {
	if strings.HasPrefix(link.alias, "mvmctl:") {
		return true
	}
	name := string(link.name)
	if len(name) != linuxInterfaceNameMax {
		return false
	}
	switch name[:3] {
	case "mvb", "mvh", "mvn", "mvl", "mvt":
		_, err := hex.DecodeString(name[3:])
		return err == nil
	default:
		return false
	}
}

// openHostResetNetworkStore admits an existing hierarchy or its exact absence. Unlike the ordinary
// mutation opener, it creates no persistent directories while proving that no inventory exists.
func openHostResetNetworkStore(ctx context.Context) (_ *networkStoreRoot, returnErr error) {
	return openHostResetNetworkStoreWithDeps(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
}

func openHostResetNetworkStoreWithDeps(
	ctx context.Context,
	deps networkStoreDeps,
	policy networkStorePolicy,
) (_ *networkStoreRoot, returnErr error) {
	retained := make([]int, 0, 5)
	transferred := false
	defer func() {
		if !transferred {
			returnErr = appendNetworkAuthorityError(returnErr, "close host reset network hierarchy",
				closeNetworkStoreFDs(context.WithoutCancel(ctx), deps, retained))
		}
	}()
	fd, err := deps.open(ctx, policy.rootPath, networkStoreDirectoryFlags, 0)
	if err != nil {
		return nil, networkAuthorityEffectError("open host reset filesystem root", err)
	}
	retained = append(retained, fd)
	if err := verifyNetworkStoreDirectory(ctx, deps, fd, "filesystem root", policy, 0, false); err != nil {
		return nil, err
	}
	names, err := networkStorePathComponents(policy.stateRoot)
	if err != nil {
		return nil, err
	}
	for index, name := range names {
		child, err := deps.openAt2(ctx, fd, name, &unix.OpenHow{
			Flags: networkStoreDirectoryFlags, Resolve: networkStoreResolve,
		})
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		if err != nil {
			return nil, networkAuthorityEffectError("open host reset network hierarchy", err)
		}
		retained = append(retained, child)
		if err := verifyNetworkStoreDirectory(
			ctx,
			deps,
			child,
			name,
			policy,
			networkStoreDirectoryMode,
			networkStorePathDirectoryClass(index) == networkStorePrivateDirectory,
		); err != nil {
			return nil, err
		}
		fd = child
	}
	transferred = true
	return &networkStoreRoot{deps: deps, policy: policy, stateFD: fd, retained: retained}, nil
}

func readHostResetNetworkInventory(ctx context.Context, root *networkStoreRoot) (hostResetNetworkInventory, error) {
	inventory := hostResetNetworkInventory{
		links: make(map[linkName]struct{}), removed: make(map[NetworkID]networkAuthorityRecord),
		cancelled: make(map[NetworkID]uint32), topologies: make(map[VMID]topologyJournalRecord),
	}
	if root == nil {
		return inventory, nil
	}
	names, err := root.deps.readDirNames(ctx, root.stateFD)
	if err != nil {
		return inventory, networkAuthorityEffectError("enumerate host reset network owners", err)
	}
	for _, name := range names {
		uid, err := strconv.ParseUint(name, 10, 32)
		if err != nil || uid == 0 || strconv.FormatUint(uid, 10) != name {
			return inventory, networkAuthorityCorruptError("host reset network owner is not canonical", err)
		}
		owner, err := root.openUID(ctx, uint32(uid), false)
		if err != nil {
			return inventory, err
		}
		err = inventory.readOwner(ctx, owner)
		err = appendNetworkAuthorityError(
			err,
			"close host reset network owner",
			owner.Release(context.WithoutCancel(ctx)),
		)
		if err != nil {
			return inventory, err
		}
	}
	for _, topology := range inventory.topologies {
		id := NetworkID(topology.NetworkID)
		if _, found := inventory.cancelled[id]; found {
			return inventory, networkAuthorityCorruptError("cancelled network retains a topology journal", nil)
		}
		if removed, found := inventory.removed[id]; found && (removed.ownerUID != topology.OwnerUID ||
			removed.configuration.l3.prefix.value.String() != topology.Prefix ||
			removed.configuration.l3.gateway.value.String() != topology.Gateway) {
			return inventory, networkAuthorityCorruptError("cleaned topology differs from network removal history", nil)
		}
	}
	return inventory, nil
}

func (inventory *hostResetNetworkInventory) readOwner(ctx context.Context, owner *networkStoreUID) error {
	names, err := owner.deps.readDirNames(ctx, owner.stateFD)
	if err != nil {
		return networkAuthorityEffectError("enumerate host reset network records", err)
	}
	for _, name := range names {
		switch name {
		case "topologies", "removed", "cancelled":
			if err := inventory.readHistory(ctx, owner, name); err != nil {
				return err
			}
		default:
			id := strings.TrimSuffix(name, ".json")
			if name != id+".json" || !networkIDPattern.MatchString(id) {
				return networkAuthorityCorruptError(
					"host reset network inventory contains pending or unknown state",
					nil,
				)
			}
			_, found, err := owner.readRecord(ctx, NetworkID(id))
			if err != nil || !found {
				return networkAuthorityCorruptError("host reset live network record is unavailable", err)
			}
			return networkAuthorityCorruptError("host reset requires all managed networks to be removed", nil)
		}
	}
	return nil
}

func (inventory *hostResetNetworkInventory) readHistory(
	ctx context.Context, owner *networkStoreUID, name string,
) (returnErr error) {
	fd, err := owner.deps.openAt2(ctx, owner.stateFD, name, &unix.OpenHow{
		Flags: networkStoreDirectoryFlags, Resolve: networkStoreResolve,
	})
	if err != nil {
		return networkAuthorityEffectError("open host reset network history", err)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close host reset network history",
			owner.deps.close(context.WithoutCancel(ctx), fd))
	}()
	if err := verifyNetworkStoreDirectory(
		ctx,
		owner.deps,
		fd,
		name,
		owner.policy,
		networkStoreDirectoryMode,
		true,
	); err != nil {
		return err
	}
	names, err := owner.deps.readDirNames(ctx, fd)
	if err != nil {
		return networkAuthorityEffectError("enumerate host reset network history", err)
	}
	store := &networkStoreUID{deps: owner.deps, policy: owner.policy, uid: owner.uid, stateFD: fd}
	for _, leaf := range names {
		id := strings.TrimSuffix(leaf, ".json")
		validID := networkIDPattern.MatchString(id)
		if name == "topologies" {
			validID = vmIDPattern.MatchString(id)
		}
		if leaf != id+".json" || !validID {
			return networkAuthorityCorruptError("host reset network history contains incomplete or unknown state", nil)
		}
		if name == "topologies" {
			if err := inventory.readTopology(ctx, store, VMID(id)); err != nil {
				return err
			}
			continue
		}
		if _, found := inventory.removed[NetworkID(id)]; found {
			return networkAuthorityCorruptError("host reset network removal identity is duplicated", nil)
		}
		if _, found := inventory.cancelled[NetworkID(id)]; found {
			return networkAuthorityCorruptError("host reset network cancellation identity is duplicated", nil)
		}
		if name == "cancelled" {
			found, err := readManagedCancellationReceipt(ctx, owner, NetworkID(id))
			if err != nil || !found {
				return networkAuthorityCorruptError("host reset network cancellation receipt is unavailable", err)
			}
			inventory.cancelled[NetworkID(id)] = owner.uid
			key := networkKey{ownerUID: OwnerUID(owner.uid), networkID: NetworkID(id)}
			inventory.links[deriveNetworkLinkName("mvb", key, "managed-bridge")] = struct{}{}
			continue
		}
		record, found, err := store.readRecord(ctx, NetworkID(id))
		if err != nil || !found || record.schemaVersion != managedNetworkSchemaVersion || record.configuration == nil {
			return networkAuthorityCorruptError("host reset network removal receipt is incompatible", err)
		}
		inventory.removed[NetworkID(id)] = record
		inventory.links[record.managedBridge.name] = struct{}{}
	}
	return nil
}

func (inventory *hostResetNetworkInventory) readTopology(ctx context.Context, store *networkStoreUID, id VMID) error {
	if !vmIDPattern.MatchString(string(id)) {
		return networkAuthorityCorruptError("host reset topology filename is invalid", nil)
	}
	raw, _, found, err := readTopologyJournal(ctx, store, id)
	if err != nil || !found {
		return networkAuthorityCorruptError("host reset topology journal is unavailable", err)
	}
	record, err := decodeTopologyJournal(raw)
	if err != nil {
		return err
	}
	if record.OwnerUID != store.uid || record.VMID != string(id) || record.Lifecycle != topologyCleaned ||
		record.Pending != "" || record.Progress != (topologyProgress{}) {
		return networkAuthorityCorruptError("host reset requires an exact terminal-cleaned topology", nil)
	}
	if _, found := inventory.topologies[id]; found {
		return networkAuthorityCorruptError("host reset topology identity is duplicated", nil)
	}
	key, err := newTopologyKey(store.uid, string(id), record.NetworkID)
	if err != nil {
		return err
	}
	inventory.topologies[id] = record
	names := deriveTopologyNames(key)
	for _, name := range []linkName{names.managedBridge, names.hostVeth, names.namespaceVeth, names.localBridge, names.tap} {
		inventory.links[name] = struct{}{}
	}
	return nil
}
