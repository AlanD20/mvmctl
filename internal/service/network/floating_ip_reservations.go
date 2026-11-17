package network

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"golang.org/x/sys/unix"
)

// primaryIPv4Reservations reads exact root topology journals while mutation authorities are retained.
// Stopped and interrupted topologies retain their primary reservation until terminal cleanup.
func primaryIPv4Reservations(
	ctx context.Context, owner *networkStoreUID, networkID NetworkID,
) (_ []netip.Addr, returnErr error) {
	fd, err := owner.deps.openAt2(ctx, owner.stateFD, "topologies", &unix.OpenHow{
		Flags: networkStoreDirectoryFlags, Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, networkAuthorityEffectError("open primary address reservations", err)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close primary address reservations",
			owner.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyNetworkStoreDirectory(
		ctx,
		owner.deps,
		fd,
		"topologies",
		owner.policy,
		networkStoreDirectoryMode,
		true,
	); err != nil {
		return nil, err
	}
	names, err := owner.deps.readDirNames(ctx, fd)
	if err != nil {
		return nil, networkAuthorityEffectError("enumerate primary address reservations", err)
	}
	store := &networkStoreUID{deps: owner.deps, policy: owner.policy, uid: owner.uid, stateFD: fd}
	var reserved []netip.Addr
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || !vmIDPattern.MatchString(id) {
			return nil, networkAuthorityCorruptError("primary address inventory contains pending or unknown state", nil)
		}
		raw, _, found, err := readTopologyJournal(ctx, store, VMID(id))
		if err != nil || !found {
			return nil, networkAuthorityCorruptError("primary address reservation disappeared", err)
		}
		record, err := decodeTopologyJournal(raw)
		if err != nil || record.OwnerUID != owner.uid || record.VMID != id {
			return nil, networkAuthorityCorruptError("primary address reservation identity differs", err)
		}
		if record.Lifecycle != topologyCleaned && record.NetworkID == string(networkID) {
			reserved = append(reserved, netip.MustParseAddr(record.GuestIPv4))
		}
	}
	return reserved, nil
}

func floatingIPv4Reservations(
	ctx context.Context, owner *networkStoreUID, networkID NetworkID,
) (_ []netip.Addr, returnErr error) {
	store, err := openFloatingIPStore(ctx, owner, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close floating-IP reservations",
			store.release(context.WithoutCancel(ctx)),
		)
	}()
	records, err := store.list(ctx)
	if err != nil {
		return nil, err
	}
	var reserved []netip.Addr
	for _, record := range records {
		if !record.Retired && record.NetworkID == string(networkID) {
			reserved = append(reserved, netip.MustParseAddr(record.IPv4))
		}
	}
	return reserved, nil
}

// Called only after reset enumerated the directory; disappearance is a conflicting observation.
func requireRetiredFloatingIPs(ctx context.Context, owner *networkStoreUID) (returnErr error) {
	store, err := openFloatingIPStore(ctx, owner, false)
	if err != nil {
		return err
	}
	if store == nil {
		return networkAuthorityCorruptError("reset floating-IP inventory disappeared", nil)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close reset floating-IP inventory",
			store.release(context.WithoutCancel(ctx)),
		)
	}()
	records, err := store.list(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if !record.Retired {
			return networkAuthorityCorruptError("host reset requires all floating IPs to be removed", nil)
		}
	}
	return nil
}
