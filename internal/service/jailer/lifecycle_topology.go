package jailer

import (
	"context"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/network"
)

// retainProductionTopologyHold transfers a real child hold to the network transaction. The
// parent VM lock cannot be released until that transaction releases this exact capability.
func (lease *launchLease) retainProductionTopologyHold(ctx context.Context) (*authorityfs.TopologyHold, error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.registrationComplete || state.vmLock == nil ||
		state.record.lifecycle != instanceLifecycleLaunching {
		return nil, instanceStateError(state.record.vmID, "durable launch authority is required for topology")
	}
	if ctx == nil {
		return nil, instanceStateError(state.record.vmID, "topology transfer context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("retain production launch topology", err)
	}
	retained, err := state.vmLock.retainTopologyAuthority(ctx)
	if err != nil {
		return nil, err
	}
	hold, err := retained.transferProductionHold(ctx)
	if err != nil {
		return nil, appendInstanceOperationError(err, "release rejected production topology hold",
			retained.Release(context.WithoutCancel(ctx)))
	}
	return hold, nil
}

func (lease *cleanupLease) recoverProductionTopology(ctx context.Context) error {
	if lease == nil || lease.state == nil || ctx == nil {
		return instanceStateError("", "cleanup topology authority and context are required")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || state.record.lifecycle != instanceLifecycleCleaning {
		return instanceStateError(state.record.vmID, "cleanup topology authority is not active")
	}
	if state.record.launch == nil {
		return instanceStateError(state.record.vmID, "cleanup network placement is absent")
	}
	retained, err := retainedNetworkPlacementRecovery(state)
	if err != nil || retained {
		return err
	}
	owner, err := state.vmLock.retainTopologyAuthority(ctx)
	if err != nil {
		return err
	}
	hold, err := owner.transferProductionHold(ctx)
	if err != nil {
		return appendInstanceOperationError(err, "release rejected recovery topology hold",
			owner.Release(context.WithoutCancel(ctx)))
	}
	switch state.record.launch.networkPlacement {
	case model.VMNetworkPlacementManaged:
		if state.priorBootAbsence != nil {
			state.topology, err = network.RecoverWithPriorBootAbsence(
				ctx, hold, state.record.launchGeneration, state.priorBootAbsence,
			)
		} else {
			state.topology, err = network.Recover(ctx, hold, state.record.launchGeneration)
		}
	case model.VMNetworkPlacementNetworkless:
		var outcome network.NetworklessRecoveryOutcome
		state.networkless, outcome, err = network.RecoverNetworklessForJailer(
			ctx, hold, state.record.launchGeneration, state.priorBootAbsence,
		)
		if err == nil {
			state.networklessCleaned, err = admitNetworklessRecoveryOutcome(state.record, state.networkless, outcome)
		}
	}
	if err != nil {
		return appendInstanceOperationError(err, "release unconsumed recovery topology hold",
			hold.Release(context.WithoutCancel(ctx)))
	}
	return nil
}

func retainedNetworkPlacementRecovery(state *cleanupLeaseState) (bool, error) {
	switch state.record.launch.networkPlacement {
	case model.VMNetworkPlacementManaged:
		if state.networkless != nil || state.networklessCleaned {
			return false, instanceStateError(state.record.vmID, "network placement recovery authority conflicts")
		}
		return state.topology != nil, nil
	case model.VMNetworkPlacementNetworkless:
		if state.topology != nil {
			return false, instanceStateError(state.record.vmID, "network placement recovery authority conflicts")
		}
		return state.networkless != nil || state.networklessCleaned, nil
	default:
		return false, instanceStateError(state.record.vmID, "cleanup network placement is invalid")
	}
}

func admitNetworklessRecoveryOutcome(
	record instanceRecord,
	lease *network.NetworklessNamespaceLease,
	outcome network.NetworklessRecoveryOutcome,
) (bool, error) {
	if lease != nil {
		if outcome != network.NetworklessRecoveryActive {
			return false, instanceStateError(record.vmID, "networkless recovery lease has inconsistent journal state")
		}
		return false, nil
	}
	if outcome == network.NetworklessRecoveryCleaned {
		return true, nil
	}
	if outcome == network.NetworklessRecoveryAbsent && record.active == nil {
		return false, nil
	}
	return false, instanceStateError(record.vmID, "active networkless namespace authority is absent")
}

func (lease *launchLease) prepareProductionNetworkless(ctx context.Context) error {
	hold, err := lease.retainProductionTopologyHold(ctx)
	if err != nil {
		return err
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.topology != nil || state.networkless != nil || state.networkNamespace != nil ||
		state.record.lifecycle != instanceLifecycleLaunching || state.record.launch == nil ||
		state.record.launch.networkPlacement != model.VMNetworkPlacementNetworkless {
		return appendInstanceOperationError(
			instanceStateError(state.record.vmID, "networkless preparation is not available"),
			"release unused networkless hold", hold.Release(context.WithoutCancel(ctx)))
	}
	state.networkless, err = network.PrepareNetworkless(ctx, hold, state.record.launchGeneration)
	if err != nil {
		return appendInstanceOperationError(err, "release unconsumed networkless hold",
			hold.Release(context.WithoutCancel(ctx)))
	}
	namespace, err := state.networkless.JailerNamespace(ctx)
	if err != nil {
		return err
	}
	projection, err := namespace.Binding(ctx)
	if err != nil {
		return err
	}
	if projection.OwnerUID != state.record.ownerUID || projection.VMID != state.record.vmID ||
		projection.NamespaceDevice == 0 || projection.NamespaceInode == 0 {
		return instanceStateError(state.record.vmID, "networkless namespace owner binding differs")
	}
	state.networkNamespace = &topologySelectedNetworkNamespace{
		identity:    instanceObjectIdentity{device: projection.NamespaceDevice, inode: projection.NamespaceInode},
		networkless: namespace,
	}
	return nil
}

func (lease *launchLease) prepareProductionTopology(
	ctx context.Context,
	intent network.VMIntent,
) (network.JailerNetworkConfig, error) {
	hold, err := lease.retainProductionTopologyHold(ctx)
	if err != nil {
		return network.JailerNetworkConfig{}, err
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.topology != nil || state.networkless != nil || state.networkNamespace != nil ||
		state.record.lifecycle != instanceLifecycleLaunching || state.record.launch == nil ||
		state.record.launch.networkPlacement != model.VMNetworkPlacementManaged {
		return network.JailerNetworkConfig{}, appendInstanceOperationError(
			instanceStateError(state.record.vmID, "launch topology preparation is not available"),
			"release unused topology hold", hold.Release(context.WithoutCancel(ctx)))
	}
	intent.LaunchGeneration = state.record.launchGeneration
	topology, err := network.Prepare(ctx, hold, intent)
	if topology != nil {
		state.topology = topology
	}
	if err != nil {
		return network.JailerNetworkConfig{}, appendInstanceOperationError(err, "release unconsumed topology hold",
			hold.Release(context.WithoutCancel(ctx)))
	}
	namespace, err := topology.JailerNamespace(ctx)
	if err != nil {
		return network.JailerNetworkConfig{}, err
	}
	projection, err := namespace.Binding(ctx)
	if err != nil {
		return network.JailerNetworkConfig{}, err
	}
	if projection.OwnerUID != state.record.ownerUID || projection.VMID != state.record.vmID {
		return network.JailerNetworkConfig{}, instanceStateError(
			state.record.vmID,
			"topology namespace owner binding differs",
		)
	}
	state.networkNamespace = &topologySelectedNetworkNamespace{
		identity:   instanceObjectIdentity{device: projection.NamespaceDevice, inode: projection.NamespaceInode},
		production: namespace,
	}
	return projection, nil
}

func (lease *topologyAuthorityLease) transferProductionHold(ctx context.Context) (*authorityfs.TopologyHold, error) {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.value == nil {
		return nil, instanceStateError("", "topology authority is not active")
	}
	// CRITICAL: A private test implementation or an owner/VM tuple cannot mint production
	// topology authority. Only the concrete retained authorityfs hold crosses this boundary.
	system, ok := state.value.(*systemTopologyAuthority)
	if !ok || system == nil || system.state == nil {
		return nil, instanceStateError("", "production topology authority is unavailable")
	}
	concrete := system.state
	concrete.mu.Lock()
	defer concrete.mu.Unlock()
	if concrete.released || concrete.value == nil {
		return nil, instanceStateError("", "production topology authority is not active")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("transfer production topology authority", err)
	}
	owner, key, err := concrete.value.Binding()
	if err != nil {
		return nil, instanceLockError("inspect production topology binding", err)
	}
	if owner != state.ownerUID || key != state.key || owner != concrete.ownerUID || key != concrete.key {
		return nil, instanceAtomicError("production topology authority binding changed", nil)
	}
	hold := concrete.value
	concrete.value = nil
	concrete.ownerUID = 0
	concrete.key = authorityfs.VMKey{}
	concrete.released = true
	state.value = nil
	state.ownerUID = 0
	state.key = authorityfs.VMKey{}
	state.released = true
	return hold, nil
}
