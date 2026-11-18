package network

import (
	"context"
	"errors"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/system"
)

// cleanupPriorBootFloatingIPVM consumes existing lifecycle proof only for logical retirement.
// It never deletes using old kernel identities and does not require namespace inode numbers to differ across boots.
func cleanupPriorBootFloatingIPVM(
	ctx context.Context, tx *managedNetworkTransaction, topology topologyJournalRecord,
	hold *authorityfs.TopologyHold, proof *system.PriorBootVMAbsence,
) (returnErr error) {
	current, err := proof.Verify(ctx, hold, topology.BootID, topology.LaunchGeneration)
	if err != nil || current == topology.BootID {
		return networkAuthorityCorruptError("prior-boot floating-IP proof differs", err)
	}
	if observed, err := readCurrentBootID(ctx); err != nil || observed != current {
		return networkAuthorityCorruptError("prior-boot floating-IP current boot differs", err)
	}
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store != nil {
		records, err := store.list(ctx)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Retired || record.NetworkID != topology.NetworkID {
				continue
			}
			matches := func(owner floatingIPOwner) bool {
				return owner.VMID == topology.VMID && owner.BootID == topology.BootID &&
					owner.LaunchGeneration == topology.LaunchGeneration && owner.ExecutionUID == topology.Topology.TAPExecutionUID
			}
			if !matches(record.Owner) && !matches(record.BeforeOwner) && !matches(record.TargetOwner) {
				continue
			}
			recovering, err := beginFloatingIPRecovery(ctx, store, record)
			if err != nil {
				return err
			}
			binding := floatingIPFirewallBinding(recovering, floatingIPOwner{})
			if err := tx.firewall.VerifyRetiredFloatingIP(ctx, binding); err != nil {
				return err
			}
			if _, err := completeFloatingIPRecovery(ctx, store, recovering); err != nil {
				return err
			}
		}
	}
	// Resource ownership is already absent. Exact grant revocation now cannot authorize historical effects.
	return retireFloatingIPVMDelegations(ctx, tx, topology.VMID, topology.BootID,
		topology.LaunchGeneration, topology.Topology.TAPExecutionUID)
}
