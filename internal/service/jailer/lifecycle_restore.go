package jailer

import (
	"context"
	"encoding/json"
	"errors"
)

// launchSnapshot is not a privileged wire operation. Its producer is the root
// immutable-capture reader; no caller-supplied machine or backing data survives.
func (server *VMLifecycleServer) launchSnapshot(
	ctx context.Context,
	caller instanceCaller,
	request VMLaunchRequest,
	prepared *preparedSnapshotRestore,
) (VMLaunchResult, error) {
	if prepared == nil || prepared.released || prepared.ownerUID != caller.uid || prepared.targetVMID != request.VMID {
		return VMLaunchResult{}, instanceValidationError("snapshot launch capability is not admitted")
	}
	return server.launch(ctx, caller, prepared.launchRequest(request.CacheRoot), prepared)
}

// prepareRestoredVMLaunchResources consumes independent protected copies only
// after durable target registration. Guest drive IDs remain those in vmstate;
// protected logical IDs name independent restore objects and are never mounted
// by a caller-selected path.
func prepareRestoredVMLaunchResources(
	ctx context.Context,
	cache *managedCacheLease,
	spec vmLaunchResourceSpec,
	record instanceRecord,
	prepared *preparedSnapshotRestore,
) (_ *vmLaunchResourceLease, returnErr error) {
	if record.lifecycle != instanceLifecycleLaunching || record.launch == nil || record.vmID != prepared.targetVMID ||
		record.launch.snapshotID != prepared.snapshotID ||
		record.ownerUID != cache.ownerUID ||
		record.launch.cacheIdentity != cache.identity {
		return nil, instanceStateError(record.vmID, "restore resources require retained registered target authority")
	}
	if err := validateVMLaunchResourceSpec(spec); err != nil {
		return nil, err
	}
	if err := prepared.prepareBackings(ctx, cache); err != nil {
		return nil, err
	}
	backings := &isolatedLaunchBackings{
		rootfs:    prepared.rootfs,
		kernel:    prepared.kernel,
		cloudInit: prepared.cloudInit,
		volumes:   make(map[vmVolumeResourceID]*protectedBackingLease),
	}
	if backings.rootfs == nil || backings.kernel == nil || len(prepared.volumes) != len(spec.firecracker.volumes) {
		return nil, instanceValidationError("restored backing graph is incomplete")
	}
	uid := record.launch.executionIdentity.uid
	if err := backings.rootfs.Grant(ctx, record.vmID, uid, vmVolumeReadWrite); err != nil {
		return nil, err
	}
	if err := backings.kernel.Grant(ctx, record.vmID, uid, vmVolumeReadOnly); err != nil {
		return nil, err
	}
	cloudInitFD := -1
	if backings.cloudInit != nil {
		if err := backings.cloudInit.Grant(ctx, record.vmID, uid, vmVolumeReadOnly); err != nil {
			return nil, err
		}
		cloudInitFD = backings.cloudInit.fd
	}
	volumes := make([]pinnedVMVolumeResource, 0, len(prepared.volumes))
	for index, volume := range prepared.volumes {
		selection := spec.firecracker.volumes[index]
		if volume.backing == nil || volume.selection.ID != string(selection.id) ||
			!isCanonicalManagedResourceID(volume.guestDriveID, 64) ||
			volume.path != "/volumes/"+volume.guestDriveID+".raw" {
			return nil, instanceValidationError("restored guest drive mapping differs from trusted capture")
		}
		if err := volume.backing.Grant(ctx, record.vmID, uid, selection.access); err != nil {
			return nil, err
		}
		binding, err := volume.backing.Binding(ctx)
		if err != nil {
			return nil, err
		}
		if binding.ownerUID != cache.ownerUID || binding.cacheIdentity != cache.identity ||
			binding.logicalID != string(selection.id) ||
			binding.bytes != selection.expectedBytes {
			return nil, instanceValidationError("restored protected volume differs from target selection")
		}
		backings.volumes[selection.id] = volume.backing
		volumes = append(
			volumes,
			pinnedVMVolumeResource{spec: selection, fd: volume.backing.fd, identity: binding.identity},
		)
	}
	canonical, err := marshalVMFirecrackerLaunchIntent(spec.firecracker)
	if err != nil {
		return nil, err
	}
	config, err := decodeManagedFirecrackerConfig(canonical)
	if err != nil {
		return nil, err
	}
	start := 1
	if spec.firecracker.cloudInit == cloudInitPresent {
		start++
	}
	if len(config.Drives) != start+len(prepared.volumes) {
		return nil, instanceValidationError("restored drive graph is incomplete")
	}
	for index, volume := range prepared.volumes {
		config.Drives[start+index].DriveID = volume.guestDriveID
		config.Drives[start+index].PathOnHost = volume.path
	}
	canonical, err = json.Marshal(config)
	if err != nil {
		return nil, err
	}
	sealed, err := createPreparedFirecrackerConfig(ctx, realManagedFirecrackerConfigDeps(), canonical)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, sealed.Release(context.WithoutCancel(ctx)))
		}
	}()
	root, err := backings.rootfs.Binding(ctx)
	if err != nil {
		return nil, err
	}
	if root.ownerUID != record.ownerUID || root.logicalID != record.vmID || root.bytes != spec.expectedRootfsBytes ||
		root.cacheIdentity != cache.identity {
		return nil, instanceValidationError("restored rootfs differs from registered target")
	}
	return &vmLaunchResourceLease{
		deps:              cache.deps,
		vmID:              spec.vmID,
		kernelID:          spec.kernelID,
		cacheIdentity:     cache.identity,
		ownerUID:          cache.ownerUID,
		rootfsFD:          backings.rootfs.fd,
		kernelFD:          backings.kernel.fd,
		cloudInitFD:       cloudInitFD,
		preparedConfig:    sealed,
		protectedBackings: backings,
		restoredSnapshot:  prepared,
		volumes:           volumes,
		deviceProfile:     spec.firecracker.deviceProfile,
		pciEnabled:        spec.firecracker.pciEnabled,
		nestedVirt:        spec.firecracker.nestedVirt,
	}, nil
}
