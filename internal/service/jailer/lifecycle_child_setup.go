package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
)

func (lease *launchLease) sendChildSetup(
	ctx context.Context,
	session *launchChildSession,
	resources *vmLaunchResourceLease,
	console *launchConsoleSession,
) error {
	if lease == nil || lease.state == nil || session == nil || session.state == nil || resources == nil {
		return instanceStateError("", "launch child setup capabilities are incomplete")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.registrationComplete || state.released || state.record.launch == nil ||
		state.record.lifecycle != instanceLifecycleLaunching || state.preparedJailDestination == nil ||
		state.preparedManagedVMCgroup == nil || state.runtimeDirectory == nil || state.persistentOutputs == nil ||
		state.networkNamespace == nil {
		return instanceStateError(state.record.vmID, "launch is not ready for child resource setup")
	}
	if resources.ownerUID != state.record.ownerUID || resources.cacheIdentity != state.record.launch.cacheIdentity ||
		string(resources.vmID) != state.record.vmID || resources.preparedConfig == nil {
		return instanceStateError(state.record.vmID, "launch child resources do not match durable authority")
	}
	if (len(resources.volumes) != 0 || resources.pciEnabled || resources.nestedVirt) &&
		(resources.deviceProfile == nil ||
			resources.deviceProfile.release != state.record.launch.release) {
		return instanceStateError(state.record.vmID, "launch volume device budget differs from admitted release")
	}
	child := session.state
	child.mu.Lock()
	defer child.mu.Unlock()
	if child.released || child.sent || child.vmID != state.record.vmID || child.pid != state.record.launch.process.pid {
		return instanceStateError(state.record.vmID, "launch child setup session does not match durable authority")
	}
	destination := state.preparedJailDestination.state
	destination.mu.Lock()
	defer destination.mu.Unlock()
	if err := recheckPreparedJailDestinationAbsentLocked(ctx, destination); err != nil {
		return err
	}
	cgroup := state.preparedManagedVMCgroup.state
	cgroup.mu.Lock()
	defer cgroup.mu.Unlock()
	if cgroup.released || !cgroup.hasLimitIntent || cgroup.vmID != state.record.vmID {
		return instanceStateError(state.record.vmID, "launch child cgroup intent is unavailable")
	}
	if state.privateRuntimeDirectory == nil {
		return instanceStateError(state.record.vmID, "private VMM runtime is unavailable")
	}
	runtime := state.privateRuntimeDirectory.state
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.released || runtime.removed || !runtime.leafCreated || runtime.vmID != state.record.vmID {
		return instanceStateError(state.record.vmID, "launch child runtime directory is unavailable")
	}
	outputs := state.persistentOutputs.state
	outputs.mu.Lock()
	defer outputs.mu.Unlock()
	if outputs.released || !outputs.ready {
		return instanceStateError(state.record.vmID, "launch child persistent outputs are unavailable")
	}
	config := resources.preparedConfig.state
	config.mu.Lock()
	defer config.mu.Unlock()
	if config.released || config.fd < 0 {
		return instanceStateError(state.record.vmID, "launch child sealed configuration is unavailable")
	}
	logFD, consoleFD, metricsFD := -1, -1, -1
	for _, output := range outputs.outputs {
		switch output.name {
		case managedPersistentLog:
			logFD = output.fd
		case managedPersistentConsole:
			consoleFD = output.fd
		case managedPersistentMetrics:
			metricsFD = output.fd
		}
	}
	if logFD < 0 || consoleFD < 0 {
		return instanceStateError(state.record.vmID, "launch child fixed output descriptors are missing")
	}
	if resources.protectedBackings == nil {
		return instanceStateError(state.record.vmID, "protected backing handoff is absent")
	}
	rootfsBinding, err := resources.protectedBackings.rootfs.Binding(ctx)
	if err != nil {
		return err
	}
	kernelBinding, err := resources.protectedBackings.kernel.Binding(ctx)
	if err != nil {
		return err
	}
	plan := launchChildPlan{
		FirecrackerSHA256: state.record.launch.release.firecrackerSHA256,
		JailerSHA256:      state.record.launch.release.jailerSHA256,
		RootfsBacking:     rootfsBinding,
		KernelID:          string(resources.kernelID),
		KernelBacking:     kernelBinding,
		NetworkPlacement:  state.record.launch.networkPlacement,
		HostBootID:        state.record.launch.hostBootID,
		LaunchGeneration:  state.record.launchGeneration,
		PCIEnabled:        resources.pciEnabled,
		NestedVirt:        resources.nestedVirt,
		Schema:            1,
		VMID:              state.record.vmID,
		OwnerUID:          state.record.ownerUID,
		UID:               state.record.launch.executionIdentity.uid,
		GID:               state.record.launch.executionIdentity.gid,
		Release: ReleaseSlot{
			Version:      state.record.launch.release.version,
			Architecture: state.record.launch.release.architecture,
		},
		Limits:    cgroup.limits,
		CloudInit: resources.cloudInitFD >= 0,
		Metrics:   metricsFD >= 0,
	}
	switch state.record.networkPlacement {
	case model.VMNetworkPlacementManaged:
		if state.topology == nil || state.networkNamespace.production == nil ||
			state.networkNamespace.networkless != nil || state.networkless != nil {
			return instanceStateError(state.record.vmID, "managed child namespace authority is inconsistent")
		}
		topology, err := state.networkNamespace.production.Binding(ctx)
		if err != nil {
			return err
		}
		if topology.OwnerUID != state.record.ownerUID || topology.VMID != state.record.vmID ||
			(instanceObjectIdentity{device: topology.NamespaceDevice, inode: topology.NamespaceInode}) !=
				state.networkNamespace.identity {
			return instanceStateError(state.record.vmID, "managed child namespace binding differs")
		}
		plan.NetworkID = topology.NetworkID
		plan.GuestIPv4 = topology.GuestIPv4.String()
		plan.GuestMAC = topology.GuestMAC
	case model.VMNetworkPlacementNetworkless:
		if state.networkNamespace.networkless == nil || state.networkNamespace.production != nil ||
			state.networkless == nil || state.topology != nil {
			return instanceStateError(state.record.vmID, "networkless child namespace authority is inconsistent")
		}
		binding, err := state.networkNamespace.networkless.Binding(ctx)
		if err != nil {
			return err
		}
		if binding.OwnerUID != state.record.ownerUID || binding.VMID != state.record.vmID ||
			(instanceObjectIdentity{device: binding.NamespaceDevice, inode: binding.NamespaceInode}) !=
				state.networkNamespace.identity {
			return instanceStateError(state.record.vmID, "networkless child namespace binding differs")
		}
	default:
		return instanceStateError(state.record.vmID, "child network placement is invalid")
	}
	if resources.protectedBackings.cloudInit != nil {
		binding, err := resources.protectedBackings.cloudInit.Binding(ctx)
		if err != nil {
			return err
		}
		plan.CloudInitBacking = &binding
	}
	fds := []int{destination.parent.fd, resources.rootfsFD, resources.kernelFD, config.fd,
		runtime.vmDirectoryFD, logFD, consoleFD}
	if plan.CloudInit {
		fds = append(fds, resources.cloudInitFD)
	}
	if plan.Metrics {
		fds = append(fds, metricsFD)
	}
	for index, volume := range resources.volumes {
		guestID := string(volume.spec.id)
		if resources.restoredSnapshot != nil {
			guestID = resources.restoredSnapshot.volumes[index].guestDriveID
		}
		plan.GuestDriveIDs = append(plan.GuestDriveIDs, guestID)
		binding, err := resources.protectedBackings.volumes[volume.spec.id].Binding(ctx)
		if err != nil {
			return err
		}
		plan.VolumeBackings = append(plan.VolumeBackings, binding)
		identity, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), volume.fd)
		if err != nil {
			return err
		}
		if identity != volume.identity {
			return instanceStateError(state.record.vmID, "pinned volume changed before child handoff")
		}
		plan.Volumes = append(plan.Volumes, VMLaunchVolume{
			ID: string(volume.spec.id), Format: model.VolumeFormatRaw, ExpectedBytes: volume.spec.expectedBytes,
			ReadOnly: volume.spec.access == vmVolumeReadOnly, CacheType: volume.spec.cacheType,
		})
		fds = append(fds, volume.fd)
	}
	if resources.restoredSnapshot != nil && resources.restoredSnapshot.metadata.Mode == "full" {
		if state.record.launch.snapshotID != resources.restoredSnapshot.snapshotID ||
			resources.restoredSnapshot.stateFD < 0 ||
			resources.restoredSnapshot.memoryFD < 0 {
			return instanceStateError(state.record.vmID, "snapshot restore descriptor authority is incomplete")
		}
		plan.RestoreMemory = true
		fds = append(fds, resources.restoredSnapshot.stateFD, resources.restoredSnapshot.memoryFD)
	}
	if console != nil {
		if console.slave == nil || state.record.launch.relay.kind != instanceRelayPresent {
			return instanceStateError(state.record.vmID, "console slave does not match durable relay authority")
		}
		plan.Console = true
		fds = append(fds, int(console.slave.Fd()))
	}
	if err := validateLaunchChildPlan(plan, fds); err != nil {
		return err
	}
	packet, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(packet) > launchChildPacketLimit {
		return instanceStateError(state.record.vmID, "launch child volume packet exceeds protocol bound")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := unix.SendmsgN(child.fd, packet, unix.UnixRights(fds...), nil,
			unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitBlockedLaunchGate(ctx, child.fd, unix.POLLOUT); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return processAuthorityError(plan.VMID, "send fixed child resource packet", err)
		}
		if count != len(packet) {
			return processAuthorityError(plan.VMID, "incomplete fixed child resource packet", nil)
		}
		child.sent = true
		return nil
	}
}

func (lease *launchLease) childNamespaceTarget(
	ctx context.Context,
	session *launchChildSession,
) (_ *system.JailerChildNamespaceTarget, returnErr error) {
	if lease == nil || lease.state == nil || session == nil || session.state == nil {
		return nil, instanceStateError("", "child namespace capabilities are incomplete")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.processes == nil || state.record.launch == nil {
		return nil, instanceStateError(state.record.vmID, "child namespace launch authority is unavailable")
	}
	child := session.state
	child.mu.Lock()
	defer child.mu.Unlock()
	if child.released || !child.sent || child.vmID != state.record.vmID ||
		child.pid != state.record.launch.process.pid {
		return nil, instanceStateError(state.record.vmID, "child namespace setup phase is invalid")
	}
	processes := state.processes.state
	processes.mu.Lock()
	defer processes.mu.Unlock()
	if processes.status != blockedLaunchProcessesReleasedToJailer {
		return nil, instanceStateError(state.record.vmID, "child namespace transfer requires released launch gate")
	}
	fd, err := unix.FcntlInt(uintptr(child.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "jailer-child-namespace-target")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	return system.NewJailerChildNamespaceTarget(ctx, file, processes.firecracker.pidfd)
}
