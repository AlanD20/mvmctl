package network

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// CALLER MUST HOLD: the real VM and network locks and the same host-global firewall mutation lease that will apply
// the endpoint generation. A different network lock alone cannot serialize MAC reservations across owners.
func reserveTopologyEndpoint(
	ctx context.Context,
	root *networkStoreRoot,
	key topologyKey,
	endpoint endpointL3,
) error {
	names, err := root.deps.readDirNames(ctx, root.stateFD)
	if err != nil {
		return networkAuthorityEffectError("enumerate topology reservation owners", err)
	}
	for _, name := range names {
		owner, err := strconv.ParseUint(name, 10, 32)
		if err != nil || owner == 0 || strconv.FormatUint(owner, 10) != name {
			return networkAuthorityCorruptError("topology reservation owner index is invalid", err)
		}
		uid, err := root.openUID(ctx, uint32(owner), false)
		if err != nil {
			return err
		}
		reservationErr := inspectOwnerEndpointReservations(ctx, uid, key, endpoint)
		closeErr := uid.Release(context.WithoutCancel(ctx))
		if err := appendNetworkAuthorityError(reservationErr, "release endpoint reservation owner", closeErr); err != nil {
			return err
		}
	}
	return nil
}

func inspectOwnerEndpointReservations(
	ctx context.Context,
	uid *networkStoreUID,
	key topologyKey,
	endpoint endpointL3,
) (returnErr error) {
	fd, err := uid.deps.openAt2(ctx, uid.stateFD, "topologies", &unix.OpenHow{
		Flags: networkStoreDirectoryFlags, Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return networkAuthorityEffectError("open endpoint reservation directory", err)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close endpoint reservations",
			uid.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyNetworkStoreDirectory(ctx, uid.deps, fd, "topologies", uid.policy, networkStoreDirectoryMode, true); err != nil {
		return err
	}
	store := &networkStoreUID{deps: uid.deps, policy: uid.policy, uid: uid.uid, stateFD: fd}
	names, err := uid.deps.readDirNames(ctx, fd)
	if err != nil {
		return err
	}
	for _, name := range names {
		if networkStoreTempNamePattern.MatchString(name) {
			return networkAuthorityCorruptError("unfinished topology publication requires reconciliation", nil)
		}
		vmID := strings.TrimSuffix(name, ".json")
		if name != vmID+".json" || !vmIDPattern.MatchString(vmID) {
			return networkAuthorityCorruptError("endpoint reservation filename is invalid", nil)
		}
		raw, _, found, err := readTopologyJournal(ctx, store, VMID(vmID))
		if err != nil || !found {
			return networkAuthorityCorruptError("endpoint reservation disappeared", err)
		}
		record, err := decodeTopologyJournal(raw)
		if err != nil {
			return err
		}
		if record.OwnerUID != uid.uid || record.VMID != vmID {
			return networkAuthorityCorruptError("endpoint reservation owner identity changed", nil)
		}
		if record.Lifecycle == topologyCleaned {
			continue
		}
		if record.OwnerUID == uint32(key.ownerUID) && record.VMID == string(key.vmID) {
			return networkAuthorityCorruptError("VM already retains topology reservation authority", nil)
		}
		if record.GuestMAC == endpoint.mac.String() ||
			(record.NetworkID == string(key.networkID) && record.GuestIPv4 == endpoint.guestAddress.value.String()) {
			return networkAuthorityValidationError("guest MAC or managed-network address is already reserved")
		}
	}
	return nil
}
