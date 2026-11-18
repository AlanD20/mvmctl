package network

import (
	"context"
	"errors"

	"mvmctl/internal/service/firewall"
)

// projectFloatingIPReservations bootstraps only stable ownerless root reservations after the network
// exists in the sealed graph. Current owned metadata is observed, never fenced or reactivated.
func (tx *managedNetworkTransaction) projectFloatingIPReservations(ctx context.Context) (returnErr error) {
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store == nil {
		return nil
	}
	records, err := store.list(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.NetworkID != string(tx.key.networkID) {
			continue
		}
		binding, project, err := floatingIPReservationProjection(
			record, uint32(tx.key.ownerUID), string(tx.key.networkID), tx.record.bootID,
		)
		if err != nil {
			return err
		}
		if record.Retired {
			continue
		}
		if project {
			if err := tx.firewall.FenceFloatingIP(ctx, binding); err != nil {
				return err
			}
		} else {
			observed, err := tx.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
			if err != nil {
				return err
			}
			if observed.Binding != binding {
				return networkAuthorityCorruptError("owned floating-IP projection differs", nil)
			}
		}
	}
	return nil
}

func floatingIPReservationProjection(
	record floatingIPRecord, ownerUID uint32, networkID, bootID string,
) (firewall.FloatingIPBinding, bool, error) {
	if err := validateFloatingIPRecord(record); err != nil {
		return firewall.FloatingIPBinding{}, false, err
	}
	if record.OwnerUID != ownerUID || record.NetworkID != networkID {
		return firewall.FloatingIPBinding{}, false, networkAuthorityCorruptError("floating-IP projection scope differs", nil)
	}
	if record.Retired {
		return firewall.FloatingIPBinding{}, false, nil
	}
	if record.Phase == floatingIPUnassociated {
		return floatingIPFirewallBinding(record, floatingIPOwner{}), true, nil
	}
	if record.Phase == floatingIPOwned && record.Owner.BootID == bootID {
		return floatingIPFirewallBinding(record, record.Owner), false, nil
	}
	return firewall.FloatingIPBinding{}, false, networkAuthorityCorruptError(
		"floating-IP projection requires ownership recovery", nil)
}
