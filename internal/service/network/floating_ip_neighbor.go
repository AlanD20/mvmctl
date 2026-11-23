package network

import (
	"bytes"
	"context"
	"errors"
	"net/netip"

	"mvmctl/internal/lib/authorityfs"
)

// invalidateFloatingIPNetworkState runs only inside an ownership transition that has already
// fenced the old owner. Failure or an unknown outcome must never admit the next owner.
// The transaction retains index -> network -> global firewall authority through the final effect.
// No VM namespace or host-veth state is required: the old VM may still run or may have stopped.
func (tx *managedNetworkTransaction) invalidateFloatingIPNetworkState(
	ctx context.Context, reservation floatingIPRecord,
) error {
	if tx == nil {
		return networkAuthorityCorruptError("floating-IP network transaction is unavailable", nil)
	}
	if err := tx.mutation.Verify(ctx); err != nil {
		return networkAuthorityCorruptError("floating-IP network mutation authority is unavailable", err)
	}
	expectedKey, err := authorityfs.NewNetworkKey(string(tx.key.networkID))
	if err != nil {
		return networkAuthorityCorruptError("floating-IP network key is invalid", err)
	}
	owner, key, err := tx.mutation.Binding()
	if err != nil || owner != uint32(tx.key.ownerUID) || key != expectedKey {
		return networkAuthorityCorruptError("floating-IP network mutation binding differs", err)
	}
	if tx.firewall == nil || tx.store == nil || tx.host == nil || tx.route == nil {
		return networkAuthorityCorruptError("floating-IP cleanup authority is incomplete", nil)
	}
	address, err := tx.floatingIPInvalidationAddress(ctx, reservation)
	if err != nil {
		return err
	}
	current, found, err := tx.store.readRecord(ctx, tx.key.networkID)
	if err != nil {
		return err
	}
	actual, actualErr := encodeNetworkAuthorityRecord(current)
	expected, expectedErr := encodeNetworkAuthorityRecord(tx.record)
	if !found || actualErr != nil || expectedErr != nil || !bytes.Equal(actual, expected) {
		return networkAuthorityCorruptError("floating-IP managed network root record differs",
			errors.Join(actualErr, expectedErr))
	}
	if _, _, err := tx.inspectFloatingIPInvalidationBridge(ctx); err != nil {
		return err
	}
	// This freshly proves all secondary keys for this address absent before deleting exact flows.
	if err := tx.firewall.InvalidateFloatingIPFlows(ctx, reservation.ID, reservation.Generation); err != nil {
		return networkAuthorityEffectError("invalidate exact floating-IP flows", err)
	}
	// Reinspect after the flow operation; never use a stale bridge ifindex for neighbor deletion.
	fd, bridge, err := tx.inspectFloatingIPInvalidationBridge(ctx)
	if err != nil {
		return err
	}
	if err := tx.route.invalidateIPv4Neighbor(ctx, fd, bridge.ifIndex, address); err != nil {
		return networkAuthorityEffectError("invalidate exact floating-IP neighbor", err)
	}
	return nil
}

// floatingIPInvalidationAddress derives the deletion address from the exact retained root reservation.
// CALLER MUST HOLD: matching network and global firewall mutation authority.
func (tx *managedNetworkTransaction) floatingIPInvalidationAddress(
	ctx context.Context, reservation floatingIPRecord,
) (_ netip.Addr, returnErr error) {
	if err := validateFloatingIPRecord(reservation); err != nil {
		return netip.Addr{}, err
	}
	if !tx.found || tx.removed || tx.cancelled || tx.priorBoot != nil ||
		tx.record.schemaVersion != managedNetworkSchemaVersion || tx.record.configuration == nil ||
		tx.record.ownerUID != uint32(tx.key.ownerUID) || tx.record.networkID != tx.key.networkID ||
		reservation.Retired || reservation.OwnerUID != uint32(tx.key.ownerUID) ||
		reservation.NetworkID != string(tx.key.networkID) {
		return netip.Addr{}, networkAuthorityCorruptError("floating-IP cleanup reservation scope is invalid", nil)
	}
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return netip.Addr{}, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "release floating-IP cleanup store",
			store.release(context.WithoutCancel(ctx)))
	}()
	if store == nil {
		return netip.Addr{}, networkAuthorityCorruptError("floating-IP cleanup reservation is missing", nil)
	}
	observed, _, _, found, err := store.read(ctx, reservation.ID)
	if err != nil {
		return netip.Addr{}, err
	}
	if !found || observed != reservation {
		return netip.Addr{}, networkAuthorityCorruptError("floating-IP cleanup reservation changed", nil)
	}
	address, err := selectFloatingIPv4(tx.record.configuration.l3, observed.IPv4, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	return address.value, nil
}

func (tx *managedNetworkTransaction) inspectFloatingIPInvalidationBridge(
	ctx context.Context,
) (int, *routeLinkObservation, error) {
	bridge, err := tx.inspectBridge(ctx, false)
	if err != nil {
		return 0, nil, err
	}
	fd, _, err := tx.host.descriptor()
	if err != nil {
		return 0, nil, err
	}
	if err := inspectManagedBridgeL3(ctx, tx.route, fd, tx.record); err != nil {
		return 0, nil, networkAuthorityEffectError("inspect floating-IP managed bridge L3", err)
	}
	// L3 inspection can involve multiple kernel exchanges. Check identity once more before use.
	bridge, err = tx.inspectBridge(ctx, false)
	return fd, bridge, err
}
