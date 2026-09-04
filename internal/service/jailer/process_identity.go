package jailer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

type verifiedLaunchIdentity struct {
	identity         instanceActiveIdentity
	destination      *jailDestinationLease
	finalFirecracker processObservation
}

type observedJailDestination struct {
	root       instanceObjectIdentity
	executable processExecutableDescriptorIdentity
	digest     string
}

// activateVerifiedProcess re-observes the retained tasks and pinned objects before consuming launching authority.
func (lease *launchLease) activateVerifiedProcess(ctx context.Context) (*activeLease, error) {
	return activateVerifiedProcessWithDeps(ctx, lease, realProcessIdentityVerifierDeps())
}

func activateVerifiedProcessWithDeps(
	ctx context.Context,
	lease *launchLease,
	deps processIdentityVerifierDeps,
) (_ *activeLease, returnErr error) {
	vmID := processLaunchLeaseVMID(lease)
	if ctx == nil {
		return nil, processVerificationError(vmID, "process identity context is required", nil)
	}
	if err := validateProcessIdentityVerifierDeps(deps); err != nil {
		return nil, processVerificationError(vmID, "process verifier dependencies are incomplete", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, processVerificationError(vmID, "activate verified process", err)
	}
	if lease == nil || lease.state == nil {
		return nil, processVerificationError(vmID, "launch process authority is not active", nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.directories == nil || state.vmLock == nil || state.prepared == nil ||
		state.processes == nil || state.processes.state == nil ||
		state.networkNamespace == nil ||
		(state.preparedManagedVMCgroup == nil) == (state.managedVMCgroup == nil) ||
		(state.preparedJailDestination == nil && state.jailDestination == nil) {
		return nil, processVerificationError(vmID, "launch process authority is not active", nil)
	}
	record := cloneInstanceRecord(state.record)
	vmID = record.vmID
	if err := validateInstanceRecord(record); err != nil {
		return nil, err
	}
	if record.lifecycle != instanceLifecycleLaunching || record.launch == nil {
		return nil, processVerificationError(vmID, "launch process authority is not awaiting activation", nil)
	}
	manifest, err := snapshotLaunchManifest(state)
	if err != nil {
		return nil, err
	}
	processState := state.processes.state
	processState.mu.Lock()
	defer processState.mu.Unlock()
	if err := validateRetainedLaunchProcesses(ctx, processState, record); err != nil {
		return nil, err
	}
	if state.jailDestination == nil {
		destination, bindErr := state.preparedJailDestination.bindAfterHandoff(
			ctx,
			record.ownerUID,
			record.launch.ownerGID,
		)
		if bindErr != nil {
			return nil, bindErr
		}
		state.preparedJailDestination = nil
		state.jailDestination = destination
	}
	verified, err := verifyRetainedLaunchProcesses(
		ctx,
		deps,
		record,
		manifest,
		processState,
		state.jailDestination,
		*state.networkNamespace,
	)
	if err != nil {
		return nil, err
	}
	if err := bindOrVerifyLaunchManagedVMCgroup(ctx, state, deps, record, processState, verified); err != nil {
		return nil, err
	}
	activeRecord, err := activateInstanceRecord(record, verified.identity)
	if err != nil {
		return nil, processVerificationError(vmID, "validate verified active process identity", err)
	}
	writeErr := state.directories.writeRecord(ctx, activeRecord)
	if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
		return nil, writeErr
	}
	target := &activeLease{state: &activeLeaseState{
		directories:      state.directories,
		vmLock:           state.vmLock,
		prepared:         state.prepared,
		processes:        state.processes,
		jailDestination:  verified.destination,
		networkNamespace: state.networkNamespace,
		runtimeDirectory: state.runtimeDirectory,
		managedVMCgroup:  state.managedVMCgroup,
		record:           activeRecord,
	}}
	state.directories = nil
	state.vmLock = nil
	state.prepared = nil
	state.processes = nil
	state.jailDestination = nil
	state.networkNamespace = nil
	state.runtimeDirectory = nil
	state.managedVMCgroup = nil
	state.record = activeRecord
	state.released = true
	if writeErr != nil {
		return target, writeErr
	}
	return target, nil
}

func processLaunchLeaseVMID(lease *launchLease) string {
	if lease == nil || lease.state == nil {
		return ""
	}
	lease.state.mu.Lock()
	defer lease.state.mu.Unlock()
	return lease.state.record.vmID
}

func snapshotLaunchManifest(state *launchLeaseState) (trustedReleaseManifest, error) {
	prepared := state.prepared.snapshot()
	if prepared.status != preparedReleaseOpen || !prepared.hasSlotLease || !prepared.releaseLockActive ||
		!prepared.hasStore || !prepared.hasDirectory || !prepared.hasAdmission || !prepared.hasExecutables {
		return trustedReleaseManifest{}, processVerificationError(
			state.record.vmID,
			"retained prepared release authority is not active",
			nil,
		)
	}
	if err := validateTrustedReleaseManifest(prepared.manifest); err != nil {
		return trustedReleaseManifest{}, processVerificationError(
			state.record.vmID,
			"retained prepared release manifest is invalid",
			err,
		)
	}
	identity, err := prepared.manifest.releaseIdentity()
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	if state.record.launch == nil || identity != prepared.identity || identity != state.record.launch.release ||
		prepared.slot != prepared.manifest.slot || prepared.slot != releaseSlotForIdentity(identity) {
		return trustedReleaseManifest{}, processVerificationError(
			state.record.vmID,
			"prepared release does not match launch authority",
			nil,
		)
	}
	return prepared.manifest, nil
}

func validateRetainedLaunchProcesses(
	ctx context.Context,
	state *blockedLaunchProcessesState,
	record instanceRecord,
) error {
	if state.status != blockedLaunchProcessesReleasedToJailer || state.vmID != record.vmID ||
		state.receiverExecutable == nil ||
		record.launch == nil {
		return processVerificationError(record.vmID, "retained launch process authority is not active", nil)
	}
	if state.firecracker.locator != record.launch.process {
		return processVerificationError(
			record.vmID,
			"retained Firecracker locator does not match launch authority",
			nil,
		)
	}
	if !state.firecracker.gate.isReleased() {
		return processVerificationError(record.vmID, "Firecracker release gate is not released", nil)
	}
	if record.launch.relay.kind == instanceRelayAbsent {
		if state.relay != nil {
			return processVerificationError(record.vmID, "retained relay presence does not match launch authority", nil)
		}
	} else if state.relay == nil || state.relay.locator != record.launch.relay.locator || !state.relay.gate.isReleased() {
		return processVerificationError(record.vmID, "retained relay locator does not match launch authority", nil)
	}
	if err := requireProcessAlive(
		ctx,
		state.deps.pidfdAlive,
		state.firecracker.pidfd,
		record.vmID,
		"Firecracker",
	); err != nil {
		return err
	}
	if state.relay != nil {
		if err := requireProcessAlive(ctx, state.deps.pidfdAlive, state.relay.pidfd, record.vmID, "relay"); err != nil {
			return err
		}
	}
	return nil
}

func verifyRetainedLaunchProcesses(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
	manifest trustedReleaseManifest,
	processes *blockedLaunchProcessesState,
	destination *jailDestinationLease,
	networkNamespace topologySelectedNetworkNamespace,
) (_ verifiedLaunchIdentity, returnErr error) {
	vmID := record.vmID
	bootID, err := deps.readBootID(ctx)
	if err != nil {
		return verifiedLaunchIdentity{}, processVerificationError(vmID, "read current host boot ID", err)
	}
	if !hostBootIDPattern.MatchString(bootID) || bootID != record.launch.hostBootID {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"host boot ID does not match launch authority",
			nil,
		)
	}
	receiverIdentity, err := deps.pinnedIdentity(ctx, processes.receiverExecutable)
	if err != nil {
		return verifiedLaunchIdentity{}, processVerificationError(vmID, "inspect pinned receiver executable", err)
	}
	firstRelay, err := verifyRelayObservation(ctx, deps, record, receiverIdentity)
	if err != nil {
		return verifiedLaunchIdentity{}, err
	}
	destination.state.mu.Lock()
	destinationSealed := destination.state.sealed
	destination.state.mu.Unlock()
	var preSealFirecracker observedProcessIdentity
	var preSealDestination observedJailDestination
	if destinationSealed {
		preSealDestination, err = inspectJailDestination(ctx, deps, destination, manifest.firecracker)
		if err == nil {
			preSealFirecracker, err = verifyFirecrackerObservation(ctx, deps, record, preSealDestination)
		}
	} else {
		preSealFirecracker, preSealDestination, err = observeFirecrackerExecTransition(
			ctx,
			deps,
			record,
			processes,
			destination,
		)
	}
	if err != nil {
		return verifiedLaunchIdentity{}, err
	}
	if preSealFirecracker.networkNamespace != networkNamespace.identity {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"Firecracker network namespace does not match the topology-selected network namespace",
			nil,
		)
	}
	if !destinationSealed {
		if err := destination.seal(ctx, record.ownerUID, record.launch.ownerGID); err != nil {
			return verifiedLaunchIdentity{}, err
		}
	}
	sealedDestination, err := inspectJailDestination(ctx, deps, destination, manifest.firecracker)
	if err != nil {
		return verifiedLaunchIdentity{}, err
	}
	if sealedDestination.root != preSealDestination.root ||
		!sameExecutableObject(sealedDestination.executable, preSealDestination.executable) {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"jail destination changed while sealing",
			nil,
		)
	}
	finalFirecracker, err := verifyFirecrackerObservation(ctx, deps, record, sealedDestination)
	if err != nil {
		return verifiedLaunchIdentity{}, err
	}
	if !processObservationsEqual(preSealFirecracker.process, finalFirecracker.process) ||
		preSealFirecracker.mountNamespace != finalFirecracker.mountNamespace ||
		preSealFirecracker.networkNamespace != finalFirecracker.networkNamespace ||
		preSealFirecracker.root != finalFirecracker.root ||
		!sameExecutableObject(preSealFirecracker.executable, finalFirecracker.executable) {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"Firecracker identity changed while sealing",
			nil,
		)
	}
	if finalFirecracker.networkNamespace != networkNamespace.identity {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"Firecracker network namespace does not match the topology-selected network namespace",
			nil,
		)
	}
	reinspectedReceiver, err := deps.pinnedReinspect(ctx, processes.receiverExecutable)
	if err != nil {
		return verifiedLaunchIdentity{}, processVerificationError(vmID, "reinspect pinned receiver executable", err)
	}
	if receiverIdentity != reinspectedReceiver {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"pinned receiver executable identity changed",
			nil,
		)
	}
	finalRelay, err := verifyRelayObservation(ctx, deps, record, reinspectedReceiver)
	if err != nil {
		return verifiedLaunchIdentity{}, err
	}
	if !observedProcessIdentitiesEqual(firstRelay, finalRelay) {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"relay identity changed during verification",
			nil,
		)
	}
	finalBootID, err := deps.readBootID(ctx)
	if err != nil {
		return verifiedLaunchIdentity{}, processVerificationError(vmID, "recheck current host boot ID", err)
	}
	if finalBootID != bootID {
		return verifiedLaunchIdentity{}, processVerificationError(
			vmID,
			"host boot ID changed during process verification",
			nil,
		)
	}
	if err := requireExactPIDFDLocator(
		ctx,
		deps,
		processes.firecracker.pidfd,
		record.launch.process,
		vmID,
		"Firecracker",
	); err != nil {
		return verifiedLaunchIdentity{}, err
	}
	if processes.relay != nil {
		if err := requireExactPIDFDLocator(
			ctx,
			deps,
			processes.relay.pidfd,
			record.launch.relay.locator,
			vmID,
			"relay",
		); err != nil {
			return verifiedLaunchIdentity{}, err
		}
	}
	identity := firecrackerActiveIdentity(record, finalFirecracker, sealedDestination)
	identity.relay = relayActiveIdentity(record, finalRelay, reinspectedReceiver)
	if err := validateInstanceActiveIdentity(record, identity); err != nil {
		return verifiedLaunchIdentity{}, processVerificationError(vmID, "observed process identity is invalid", err)
	}
	return verifiedLaunchIdentity{
		identity: identity, destination: destination, finalFirecracker: finalFirecracker.process,
	}, nil
}

func observeFirecrackerExecTransition(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
	processes *blockedLaunchProcessesState,
	destination *jailDestinationLease,
) (observedProcessIdentity, observedJailDestination, error) {
	deadline := deps.now().Add(processExecObservationBudget)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	var lastMismatch error
	for {
		if err := ctx.Err(); err != nil {
			return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
				record.vmID,
				"observe Jailer to Firecracker executable transition",
				err,
			)
		}
		if !deps.now().Before(deadline) {
			return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
				record.vmID,
				"Jailer to Firecracker executable transition exceeded its deadline",
				lastMismatch,
			)
		}
		if err := requireProcessAlive(
			ctx,
			deps.pidfdAlive,
			processes.firecracker.pidfd,
			record.vmID,
			"Firecracker",
		); err != nil {
			return observedProcessIdentity{}, observedJailDestination{}, err
		}
		if err := requireExactPIDFDLocator(
			ctx,
			deps,
			processes.firecracker.pidfd,
			record.launch.process,
			record.vmID,
			"Firecracker",
		); err != nil {
			return observedProcessIdentity{}, observedJailDestination{}, err
		}
		bootID, err := deps.readBootID(ctx)
		if err != nil {
			return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
				record.vmID,
				"recheck host boot ID during executable transition",
				err,
			)
		}
		if bootID != record.launch.hostBootID {
			return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
				record.vmID,
				"host boot ID changed during executable transition",
				nil,
			)
		}
		preSealDestination, err := inspectPreSealJailDestination(
			ctx,
			destination,
			record.ownerUID,
			record.launch.ownerGID,
		)
		if err != nil {
			return observedProcessIdentity{}, observedJailDestination{}, err
		}
		observed, err := observeProcessIdentityWithExecutableOwner(
			ctx,
			deps,
			record.vmID,
			"Firecracker",
			record.launch.process,
			record.ownerUID,
			record.launch.ownerGID,
			true,
		)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			return observedProcessIdentity{}, observedJailDestination{}, err
		}
		if err == nil && observed.root == preSealDestination.root &&
			sameExecutableObject(observed.executable, preSealDestination.executable) {
			if err := requireExpectedProcessCredentials(
				observed.process,
				record.ownerUID,
				record.launch.ownerGID,
				"Firecracker",
			); err != nil {
				return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
					record.vmID,
					err.Error(),
					nil,
				)
			}
			wantCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + record.vmID
			if observed.process.cgroupPath != wantCgroup {
				return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
					record.vmID,
					"Firecracker cgroup does not match launch authority",
					nil,
				)
			}
			return observed, preSealDestination, nil
		}
		lastMismatch = processVerificationError(
			record.vmID,
			"Firecracker root or executable does not yet match retained jail destination",
			err,
		)
		remaining := deadline.Sub(deps.now())
		if remaining < processExecObservationInterval {
			if remaining > 0 {
				if err := deps.wait(ctx, remaining); err != nil {
					return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
						record.vmID,
						"wait for Jailer to Firecracker executable transition",
						err,
					)
				}
			}
			continue
		}
		if err := deps.wait(ctx, processExecObservationInterval); err != nil {
			return observedProcessIdentity{}, observedJailDestination{}, processVerificationError(
				record.vmID,
				"wait for Jailer to Firecracker executable transition",
				err,
			)
		}
	}
}

func requireExactPIDFDLocator(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	pidfd int,
	expected instanceProcessLocator,
	vmID string,
	role string,
) error {
	derived, err := deps.deriveLocator(ctx, pidfd)
	if err != nil {
		return processVerificationError(vmID, "derive "+role+" locator from pidfd", err)
	}
	if derived != expected {
		return processVerificationError(vmID, role+" pidfd locator does not match launch authority", nil)
	}
	return nil
}

func sameExecutableObject(left, right processExecutableDescriptorIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.sizeBytes == right.sizeBytes &&
		left.mode == right.mode && left.links == right.links
}

func verifyFirecrackerObservation(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
	destination observedJailDestination,
) (observedProcessIdentity, error) {
	observed, err := observeProcessIdentity(ctx, deps, record.vmID, "Firecracker", record.launch.process)
	if err != nil {
		return observedProcessIdentity{}, err
	}
	if err := requireExpectedProcessCredentials(
		observed.process,
		record.ownerUID,
		record.launch.ownerGID,
		"Firecracker",
	); err != nil {
		return observedProcessIdentity{}, processVerificationError(record.vmID, err.Error(), nil)
	}
	wantCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + record.vmID
	if observed.process.cgroupPath != wantCgroup {
		return observedProcessIdentity{}, processVerificationError(
			record.vmID,
			"Firecracker cgroup does not match launch authority",
			nil,
		)
	}
	if observed.root != destination.root || !samePersistentExecutable(observed.executable, destination.executable) {
		return observedProcessIdentity{}, processVerificationError(
			record.vmID,
			"Firecracker root or executable does not match retained jail destination",
			nil,
		)
	}
	return observed, nil
}

func verifyRelayObservation(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
	receiver system.PinnedExecutableIdentity,
) (_ observedProcessIdentity, returnErr error) {
	if record.launch.relay.kind == instanceRelayAbsent {
		return observedProcessIdentity{}, nil
	}
	observed, err := observeProcessIdentity(ctx, deps, record.vmID, "relay", record.launch.relay.locator)
	if err != nil {
		return observedProcessIdentity{}, err
	}
	if err := requireExpectedProcessCredentials(
		observed.process,
		record.ownerUID,
		record.launch.ownerGID,
		"relay",
	); err != nil {
		return observedProcessIdentity{}, processVerificationError(record.vmID, err.Error(), nil)
	}
	descriptors := processVerificationDescriptors{deps: deps, vmID: record.vmID}
	defer func() { returnErr = descriptors.closeAll(ctx, returnErr) }()
	hostNamespaceFD, err := deps.pinHostMountNamespace(ctx)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(record.vmID, "pin host mount namespace", err)
	}
	descriptors.add(hostNamespaceFD)
	hostNamespace, err := inspectProcessObject(ctx, deps, hostNamespaceFD, false)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(record.vmID, "inspect host mount namespace", err)
	}
	hostRootFD, err := deps.pinHostRoot(ctx)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(record.vmID, "pin host root", err)
	}
	descriptors.add(hostRootFD)
	hostRoot, err := inspectProcessObject(ctx, deps, hostRootFD, true)
	if err != nil {
		return observedProcessIdentity{}, processVerificationError(record.vmID, "inspect host root", err)
	}
	if observed.mountNamespace != hostNamespace || observed.root != hostRoot ||
		!processExecutableMatchesPinned(observed.executable, receiver) {
		return observedProcessIdentity{}, processVerificationError(
			record.vmID,
			"relay namespace, root, or executable does not match receiver authority",
			nil,
		)
	}
	return observed, nil
}

func inspectPreSealJailDestination(
	ctx context.Context,
	destination *jailDestinationLease,
	ownerUID uint32,
	ownerGID uint32,
) (observedJailDestination, error) {
	if destination == nil || destination.state == nil {
		return observedJailDestination{}, processVerificationError("", "jail destination authority is not active", nil)
	}
	state := destination.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.sealed || len(state.bindings) != 4 {
		return observedJailDestination{}, processVerificationError(
			state.vmID,
			"jail destination is not awaiting verified sealing",
			nil,
		)
	}
	var result observedJailDestination
	for _, binding := range state.bindings {
		var stat unix.Stat_t
		if err := state.deps.fstat(ctx, binding.fd, &stat); err != nil {
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"reinspect pre-seal jail destination binding",
				err,
			)
		}
		if uint64(stat.Dev) != binding.identity.device || stat.Ino != binding.identity.inode {
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"pre-seal jail destination binding changed",
				nil,
			)
		}
		mount, err := inspectJailDestinationMount(ctx, state.deps, binding.fd)
		if err != nil {
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"reinspect pre-seal jail destination mount",
				err,
			)
		}
		if mount != binding.mount {
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"pre-seal jail destination mount changed",
				nil,
			)
		}
		switch binding.kind {
		case jailDestinationFirecracker, jailDestinationVM:
			if stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 {
				return observedJailDestination{}, processVerificationError(
					state.vmID,
					"pre-seal Jailer ancestor metadata changed",
					nil,
				)
			}
		case jailDestinationRoot:
			if stat.Mode != unix.S_IFDIR|0755 || stat.Uid != 0 || stat.Gid != 0 {
				return observedJailDestination{}, processVerificationError(
					state.vmID,
					"pre-seal jail root metadata changed",
					nil,
				)
			}
			result.root = binding.identity
		case jailDestinationExecutable:
			executable, err := processExecutableIdentityFromStat(stat, ownerUID, ownerGID, true)
			if err != nil {
				return observedJailDestination{}, processVerificationError(
					state.vmID,
					"inspect pre-seal copied Firecracker",
					err,
				)
			}
			result.executable = executable
		default:
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"pre-seal jail destination binding kind is invalid",
				nil,
			)
		}
	}
	return result, nil
}

func inspectJailDestination(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	destination *jailDestinationLease,
	expected trustedReleaseExecutable,
) (observedJailDestination, error) {
	if destination == nil || destination.state == nil {
		return observedJailDestination{}, processVerificationError("", "jail destination authority is not active", nil)
	}
	state := destination.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.sealed || len(state.bindings) != 4 {
		return observedJailDestination{}, processVerificationError(
			state.vmID,
			"jail destination authority is not active",
			nil,
		)
	}
	var root jailDestinationBinding
	var executable jailDestinationBinding
	for _, binding := range state.bindings {
		var stat unix.Stat_t
		if err := deps.fstat(ctx, binding.fd, &stat); err != nil {
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"reinspect jail destination binding",
				err,
			)
		}
		if uint64(stat.Dev) != binding.identity.device || stat.Ino != binding.identity.inode {
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"jail destination binding changed",
				nil,
			)
		}
		switch binding.kind {
		case jailDestinationFirecracker, jailDestinationVM:
			if stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 {
				return observedJailDestination{}, processVerificationError(
					state.vmID,
					"Jailer ancestor metadata changed",
					nil,
				)
			}
		case jailDestinationRoot:
			if stat.Mode != unix.S_IFDIR|0755 || stat.Uid != 0 || stat.Gid != 0 {
				return observedJailDestination{}, processVerificationError(
					state.vmID,
					"jail root metadata changed",
					nil,
				)
			}
			root = binding
		case jailDestinationExecutable:
			executable = binding
		default:
			return observedJailDestination{}, processVerificationError(
				state.vmID,
				"jail destination binding kind is invalid",
				nil,
			)
		}
	}
	identity, err := inspectProcessExecutable(ctx, deps, executable.fd)
	if err != nil {
		return observedJailDestination{}, processVerificationError(state.vmID, "inspect copied Firecracker", err)
	}
	digest, finalIdentity, err := hashProcessExecutable(ctx, deps, executable.fd, identity)
	if err != nil {
		return observedJailDestination{}, processVerificationError(state.vmID, "hash copied Firecracker", err)
	}
	if finalIdentity.sizeBytes != expected.sizeBytes || digest != hex.EncodeToString(expected.digest[:]) {
		return observedJailDestination{}, processVerificationError(
			state.vmID,
			"copied Firecracker does not match release manifest",
			nil,
		)
	}
	return observedJailDestination{root: root.identity, executable: finalIdentity, digest: digest}, nil
}

func firecrackerActiveIdentity(
	record instanceRecord,
	observed observedProcessIdentity,
	destination observedJailDestination,
) instanceActiveIdentity {
	return instanceActiveIdentity{
		userIDs: observed.process.userIDs, groupIDs: observed.process.groupIDs,
		supplementaryGroups: append([]uint32{}, observed.process.supplementaryGroups...),
		mountNamespace:      observed.mountNamespace,
		jailRoot:            destination.root,
		executable: instanceFirecrackerExecutableIdentity{
			device: destination.executable.device, inode: destination.executable.inode,
			sizeBytes: destination.executable.sizeBytes, mode: destination.executable.mode,
			uid: destination.executable.uid, gid: destination.executable.gid, links: destination.executable.links,
			manifestSHA256: destination.digest,
		},
		relay: instanceActiveRelay{kind: instanceRelayAbsent},
	}
}

func relayActiveIdentity(
	record instanceRecord,
	observed observedProcessIdentity,
	receiver system.PinnedExecutableIdentity,
) instanceActiveRelay {
	if record.launch.relay.kind == instanceRelayAbsent {
		return instanceActiveRelay{kind: instanceRelayAbsent}
	}
	return instanceActiveRelay{
		kind: instanceRelayPresent, userIDs: observed.process.userIDs, groupIDs: observed.process.groupIDs,
		supplementaryGroups: append([]uint32{}, observed.process.supplementaryGroups...),
		mountNamespace:      observed.mountNamespace,
		root:                observed.root,
		executable: instanceRelayExecutableIdentity{
			device: receiver.Device, inode: receiver.Inode, sizeBytes: receiver.SizeBytes, mode: receiver.Mode,
			uid: receiver.UID, gid: receiver.GID, links: receiver.Links, sha256: receiver.SHA256,
		},
	}
}

func samePersistentExecutable(left, right processExecutableDescriptorIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.sizeBytes == right.sizeBytes &&
		left.mode == right.mode && left.uid == right.uid && left.gid == right.gid && left.links == right.links
}

func observedProcessIdentitiesEqual(left, right observedProcessIdentity) bool {
	return processObservationsEqual(left.process, right.process) && left.mountNamespace == right.mountNamespace &&
		left.networkNamespace == right.networkNamespace && left.root == right.root && left.executable == right.executable
}

func processExecutableMatchesPinned(
	observed processExecutableDescriptorIdentity,
	pinned system.PinnedExecutableIdentity,
) bool {
	return observed.device == pinned.Device && observed.inode == pinned.Inode &&
		observed.sizeBytes == pinned.SizeBytes && observed.mode == pinned.Mode && observed.uid == pinned.UID &&
		observed.gid == pinned.GID && observed.links == pinned.Links
}

func requireProcessAlive(
	ctx context.Context,
	check func(context.Context, int) (bool, error),
	pidfd int,
	vmID string,
	role string,
) error {
	alive, err := check(ctx, pidfd)
	if err != nil {
		return processVerificationError(vmID, "inspect "+role+" pidfd liveness", err)
	}
	if !alive {
		return processVerificationError(
			vmID,
			role+" process exited during identity verification",
			errors.New("pidfd is readable"),
		)
	}
	return nil
}

func validateProcessIdentityVerifierDeps(deps processIdentityVerifierDeps) error {
	if deps.readBootID == nil || deps.pidfdAlive == nil || deps.deriveLocator == nil || deps.now == nil ||
		deps.wait == nil || deps.openProcessDirectory == nil ||
		deps.readProcessMetadata == nil || deps.pinProcessObject == nil || deps.pinHostMountNamespace == nil ||
		deps.pinHostRoot == nil || deps.descriptorFlags == nil || deps.fstat == nil || deps.pread == nil ||
		deps.close == nil || deps.pinnedIdentity == nil || deps.pinnedReinspect == nil {
		return fmt.Errorf("one or more required process verifier effects are unavailable")
	}
	return nil
}
