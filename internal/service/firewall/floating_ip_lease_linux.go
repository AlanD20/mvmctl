package firewall

import (
	"context"
	"errors"
	"time"

	"github.com/google/nftables"
)

// FenceFloatingIP records ownerless authority and revokes old timed elements before cleanup.
// binding must be resolved from root reservation authority, never decoded from caller input.
// Same-generation fencing is permitted for recovery; generation rollback and identity rebound refuse.
func (lease *MutationLease) FenceFloatingIP(ctx context.Context, binding FloatingIPBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP mutation authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target, err := state.floatingIPOwnershipIntent(binding, false)
	if err != nil {
		return err
	}
	if err := state.applyIntent(ctx, target); err != nil {
		return err
	}
	return state.verifyFloatingIPLeaseAbsent(ctx, binding)
}

// PublishFloatingIPOwner publishes root-resolved ownership metadata only. It never installs a timed
// key. The coordinator must freshly prove the actual incarnation, delegation and running state.
func (lease *MutationLease) PublishFloatingIPOwner(ctx context.Context, binding FloatingIPBinding) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP mutation authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	target, err := state.floatingIPOwnershipIntent(binding, true)
	if err != nil {
		return err
	}
	if err := state.verifyFloatingIPLeaseAbsent(ctx, binding); err != nil {
		return err
	}
	if err := state.applyIntent(ctx, target); err != nil {
		return err
	}
	return state.verifyFloatingIPLeaseAbsent(ctx, binding)
}

// floatingIPOwnershipIntent preserves unrelated root intent and validates the active owner projection.
// CALLER MUST HOLD: state.mu and a ready mutation lease.
func (state *mutationLeaseState) floatingIPOwnershipIntent(binding FloatingIPBinding, publish bool) (Intent, error) {
	if binding.OwnerUID != state.binding.OwnerUID || binding.NetworkID != state.binding.NetworkID {
		return Intent{}, generationAuthorityError("floating-IP ownership scope differs", nil, nil)
	}
	if publish {
		if binding.OwnerVMID == "" || binding.OwnerLaunchGeneration == 0 ||
			binding.LeaseDuration <= 0 || binding.LeaseDuration%time.Second != 0 {
			return Intent{}, generationAuthorityError("floating-IP owner or lease policy is invalid", nil, nil)
		}
	} else if binding.OwnerVMID != "" || binding.OwnerLaunchGeneration != 0 || binding.LeaseDuration != 0 {
		return Intent{}, generationAuthorityError("floating-IP fence must be ownerless", nil, nil)
	}
	target := cloneIntent(state.intent)
	index := -1
	for i, resource := range target.FloatingIPs {
		if resource.ID != binding.ID {
			continue
		}
		if resource.OwnerUID != binding.OwnerUID || resource.NetworkID != binding.NetworkID ||
			resource.IPv4 != binding.IPv4 || binding.Generation < resource.Generation {
			return Intent{}, generationAuthorityError("floating-IP identity rebound or generation rollback", nil, nil)
		}
		if publish && (resource.Generation != binding.Generation || resource.OwnerVMID != "" ||
			resource.OwnerLaunchGeneration != 0 || resource.LeaseDuration != 0) {
			return Intent{}, generationAuthorityError(
				"floating-IP owner publication requires the exact fence", nil, nil)
		}
		index = i
		break
	}
	if index < 0 {
		if publish {
			return Intent{}, generationAuthorityError(
				"floating-IP owner publication has no reservation fence", nil, nil)
		}
		target.FloatingIPs = append(target.FloatingIPs, binding)
	} else {
		target.FloatingIPs[index] = binding
	}
	compiled, err := target.compile()
	if err != nil {
		return Intent{}, err
	}
	if publish {
		if _, active := compiled.bridge.secondaryOwners[binding.ID]; !active {
			return Intent{}, generationAuthorityError("floating-IP owner endpoint is not active", nil, nil)
		}
	}
	return target, nil
}

// ActivateFloatingIPLease installs one finite timed key after exact absent-authority proof.
// An unknown insert outcome is never retried here, by loading intent, or by compiling metadata.
func (lease *MutationLease) ActivateFloatingIPLease(ctx context.Context, resourceID string, generation uint64) error {
	return lease.changeFloatingIPLease(ctx, resourceID, generation, false)
}

// RenewFloatingIPLease deletes the exact old key, proves absence and inserts once. Repeated Add with
// the same timeout does not renew on the supported kernel/library protocol. This permits a fenced gap.
func (lease *MutationLease) RenewFloatingIPLease(ctx context.Context, resourceID string, generation uint64) error {
	return lease.changeFloatingIPLease(ctx, resourceID, generation, true)
}

func (lease *MutationLease) changeFloatingIPLease(
	ctx context.Context, resourceID string, generation uint64, renew bool,
) error {
	if lease == nil || lease.state == nil {
		return generationAuthorityError("floating-IP mutation authority is unavailable", nil, nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, kernelApplicationSocketTimeout)
	defer cancel()
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	resource, present, err := state.floatingIPLeaseAuthority(resourceID, generation, observed)
	if err != nil {
		return err
	}
	if present {
		if !renew {
			return generationAuthorityError("floating-IP lease activation requires absent authority", nil, nil)
		}
		if err := state.mutateFloatingIPLeaseElement(ctx, resource, false); err != nil {
			return err
		}
	}
	if err := state.verifyFloatingIPLeaseAbsent(ctx, resource); err != nil {
		return err
	}
	if err := state.mutateFloatingIPLeaseElement(ctx, resource, true); err != nil {
		return err
	}
	observed, err = state.network.kernel.observeGeneration(ctx)
	if err != nil {
		return err
	}
	if err := state.verifyFloatingIPLeaseInstalled(resourceID, generation, observed); err != nil {
		return err
	}
	state.uncertain = false
	return nil
}

// floatingIPLeaseAuthority checks the complete graph and derives one exact configured owner key.
// Present includes a zero-remaining element: only absence proves revocation.
func (state *mutationLeaseState) floatingIPLeaseAuthority(
	resourceID string, generation uint64, observed kernelObservation,
) (FloatingIPBinding, bool, error) {
	if err := compareKernelObservation(observed, state.compiled); err != nil {
		return FloatingIPBinding{}, false, err
	}
	for _, resource := range state.intent.FloatingIPs {
		if resource.ID != resourceID {
			continue
		}
		key, active := state.compiled.bridge.secondaryOwners[resourceID]
		if !active || resource.OwnerUID != state.binding.OwnerUID || resource.NetworkID != state.binding.NetworkID ||
			resource.Generation != generation || generation == 0 || resource.OwnerVMID == "" ||
			resource.OwnerLaunchGeneration == 0 || resource.LeaseDuration <= 0 ||
			resource.LeaseDuration%time.Second != 0 {
			return FloatingIPBinding{}, false, generationAuthorityError("floating-IP lease ownership differs", nil, nil)
		}
		for _, element := range observed.tables[0].secondaryElements {
			if element.key == key {
				return resource, true, nil
			}
		}
		return resource, false, nil
	}
	return FloatingIPBinding{}, false, generationAuthorityError("floating-IP lease resource is missing", nil, nil)
}

func (state *mutationLeaseState) verifyFloatingIPLeaseInstalled(
	resourceID string, generation uint64, observed kernelObservation,
) error {
	_, present, err := state.floatingIPLeaseAuthority(resourceID, generation, observed)
	if err != nil {
		return err
	}
	if !present {
		return generationAuthorityError("floating-IP lease is absent after insertion", nil, nil)
	}
	for _, element := range observed.tables[0].secondaryElements {
		if element.key == state.compiled.bridge.secondaryOwners[resourceID] && element.remaining <= 0 {
			return generationAuthorityError("floating-IP lease has no positive remaining lifetime", nil, nil)
		}
	}
	return nil
}

func (state *mutationLeaseState) verifyFloatingIPLeaseAbsent(ctx context.Context, resource FloatingIPBinding) error {
	observed, err := state.network.kernel.observeGeneration(ctx)
	if err != nil {
		state.uncertain = true
		return err
	}
	_, err = state.floatingIPFlowAddress(resource.ID, resource.Generation, observed)
	if err != nil {
		state.uncertain = true
	}
	return err
}

// mutateFloatingIPLeaseElement queues only the admitted resource's key. Each effect batch commits
// once; no full generation replacement or unrelated timeout is queued. CALLER MUST HOLD: state.mu.
func (state *mutationLeaseState) mutateFloatingIPLeaseElement(
	ctx context.Context, resource FloatingIPBinding, insert bool,
) error {
	admitted := false
	for _, current := range state.intent.FloatingIPs {
		if current == resource && current.OwnerUID == state.binding.OwnerUID &&
			current.NetworkID == state.binding.NetworkID {
			admitted = true
			break
		}
	}
	key, active := state.compiled.bridge.secondaryOwners[resource.ID]
	if !admitted || !active || state.compiled.bridge.secondarySet == nil {
		return generationAuthorityError("floating-IP lease element has no admitted owner", nil, nil)
	}
	connection, err := state.network.kernel.newApplicationConnection(ctx)
	if err != nil {
		return err
	}
	// ID 1 belongs to the compiler's initial creation batch. Direct operations address the
	// retained named set without carrying that transaction-local ID into another batch.
	set := *state.compiled.bridge.secondarySet
	set.ID = 0
	element := nftables.SetElement{Key: key[:]}
	if insert {
		element.Timeout = resource.LeaseDuration
		err = connection.addSecondaryElements(&set, []nftables.SetElement{element})
	} else {
		err = connection.deleteSecondaryElements(&set, []nftables.SetElement{element})
	}
	if err != nil {
		closeErr := connection.close()
		if closeErr != nil {
			state.uncertain = true
		}
		return generationAuthorityError("queue exact floating-IP lease element", errors.Join(err, closeErr), nil)
	}
	if err := ctx.Err(); err != nil {
		closeErr := connection.close()
		if closeErr != nil {
			state.uncertain = true
		}
		return generationAuthorityError("commit floating-IP lease element", errors.Join(err, closeErr), nil)
	}
	state.uncertain = true
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { interrupted <- connection.interrupt() })
	flushErr := connection.flush()
	var interruptErr error
	if !stop() {
		interruptErr = <-interrupted
	}
	if err := errors.Join(ctx.Err(), flushErr, interruptErr, connection.close()); err != nil {
		return generationAuthorityError("commit exact floating-IP lease element", err, nil)
	}
	return nil
}
