package network

import (
	"context"
	"errors"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/firewall"
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
			history, binding, err := priorBootFloatingIPBindings(record, current)
			if err != nil {
				return err
			}
			if err := tx.firewall.VerifyDormantFloatingIP(ctx, history, binding); err != nil {
				return err
			}
			if observed, err := proof.Verify(ctx, hold, topology.BootID, topology.LaunchGeneration); err != nil ||
				observed != current {
				return networkAuthorityCorruptError("prior-boot floating-IP proof changed", err)
			}
			recovering, err := beginFloatingIPRecovery(ctx, store, record)
			if err != nil {
				return err
			}
			if err := tx.firewall.RetirePriorBootFloatingIP(ctx, history, binding); err != nil {
				return err
			}
			if observed, err := proof.Verify(ctx, hold, topology.BootID, topology.LaunchGeneration); err != nil ||
				observed != current {
				return networkAuthorityCorruptError("prior-boot floating-IP proof changed", err)
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

// priorBootFloatingIPBindings enumerates only projections reachable from the closed
// journal phases. Recovery preserves both possible owners; it never guesses an older
// generation from a permissive range. The fenced binding is also an idempotent retry.
func priorBootFloatingIPBindings(
	record floatingIPRecord, currentBoot string,
) ([]firewall.FloatingIPBinding, firewall.FloatingIPBinding, error) {
	invalid := func() ([]firewall.FloatingIPBinding, firewall.FloatingIPBinding, error) {
		return nil, firewall.FloatingIPBinding{}, networkAuthorityCorruptError(
			"floating-IP dormant history requires exact prior-boot ownership", nil)
	}
	if err := validateFloatingIPRecord(record); err != nil {
		return nil, firewall.FloatingIPBinding{}, err
	}
	if !bootIDPattern.MatchString(currentBoot) || record.Retired || record.Phase == floatingIPUnassociated {
		return invalid()
	}
	oldBoot := ""
	for _, owner := range []floatingIPOwner{record.Owner, record.BeforeOwner, record.TargetOwner} {
		if owner == (floatingIPOwner{}) {
			continue
		}
		if owner.BootID == currentBoot || oldBoot != "" && owner.BootID != oldBoot {
			return invalid()
		}
		oldBoot = owner.BootID
	}
	if oldBoot == "" || record.Generation < 2 {
		return invalid()
	}
	fenced := floatingIPFirewallBinding(record, floatingIPOwner{})
	if record.Phase == floatingIPOwned || record.Phase == floatingIPRenewing {
		if record.Generation == ^uint64(0) {
			return invalid()
		}
		fenced.Generation++
	}
	var history []firewall.FloatingIPBinding
	add := func(owner floatingIPOwner, generation uint64) {
		binding := floatingIPFirewallBinding(record, owner)
		binding.Generation = generation
		for _, existing := range history {
			if existing == binding {
				return
			}
		}
		history = append(history, binding)
	}
	before := func() {
		add(record.BeforeOwner, record.Generation-1)
	}
	zero := floatingIPOwner{}
	switch record.Phase {
	case floatingIPOwned:
		add(record.Owner, record.Generation)
	case floatingIPRenewing:
		// Renewal keeps its generation until recovery advances it. Its fence
		// may have committed before either owner metadata publication or activation.
		add(record.BeforeOwner, record.Generation)
		add(zero, record.Generation)
	case floatingIPFencing:
		switch record.Operation {
		case floatingIPAssociate, floatingIPRelease:
			before()
		case floatingIPRecover:
			// Recovery may have replaced owned, fencing, granting or renewing.
			// Only renewal has matching before/target incarnations; its prior
			// ownerless fence carries the predecessor generation too.
			before()
			if record.BeforeOwner != zero && record.TargetOwner != zero &&
				sameFloatingIPIncarnation(record.BeforeOwner, record.TargetOwner) {
				add(zero, record.Generation-1)
			} else if record.TargetOwner != zero {
				add(record.TargetOwner, record.Generation)
			}
		default:
			return invalid()
		}
	case floatingIPFenced:
		// Acknowledged fencing can only retain its exact ownerless projection.
	case floatingIPGranting:
		add(record.TargetOwner, record.Generation)
	default:
		return invalid()
	}
	// Fencing/granting can be interrupted after ownerless publication; a retry
	// after beginRecovery can also observe the already completed recovery fence.
	if record.Phase != floatingIPOwned {
		add(zero, record.Generation)
	}
	return history, fenced, nil
}
