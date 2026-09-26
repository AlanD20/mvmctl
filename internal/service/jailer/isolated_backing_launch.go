package jailer

import (
	"context"
	"errors"
	"sort"
)

type isolatedLaunchBackings struct {
	rootfs    *protectedBackingLease
	kernel    *protectedBackingLease
	cloudInit *protectedBackingLease
	volumes   map[vmVolumeResourceID]*protectedBackingLease
	ordered   []*protectedBackingLease
}

// prepareIsolatedLaunchBackings runs under the VM lock after durable registration.
// Object locks are acquired by logical identity, never caller guest-drive order.
func prepareIsolatedLaunchBackings(
	ctx context.Context,
	cache *managedCacheLease,
	spec vmLaunchResourceSpec,
	record instanceRecord,
) (_ *isolatedLaunchBackings, returnErr error) {
	if record.launch == nil || record.lifecycle != instanceLifecycleLaunching || record.vmID != string(spec.vmID) ||
		record.ownerUID != cache.ownerUID ||
		record.launch.cacheIdentity != cache.identity {
		return nil, instanceStateError(record.vmID, "protected backing launch binding is inconsistent")
	}
	result := &isolatedLaunchBackings{volumes: make(map[vmVolumeResourceID]*protectedBackingLease)}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, result.Release(context.WithoutCancel(ctx)))
		}
	}()
	if spec.firecracker.cloudInit == cloudInitPresent {
		seed, err := cache.protectVMCloudInit(ctx, spec.vmID)
		if err != nil {
			return nil, err
		}
		result.cloudInit = seed
		result.ordered = append(result.ordered, seed)
	}
	kernel, err := cache.protectVMKernel(ctx, spec.kernelID)
	if err != nil {
		return nil, err
	}
	result.kernel = kernel
	result.ordered = append(result.ordered, kernel)
	rootfs, err := cache.protectVMRootfs(ctx, spec.vmID, spec.expectedRootfsBytes)
	if err != nil {
		return nil, err
	}
	result.rootfs = rootfs
	result.ordered = append(result.ordered, rootfs)
	volumes := append([]vmVolumeResourceSpec(nil), spec.firecracker.volumes...)
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].id < volumes[j].id })
	for _, volume := range volumes {
		backing, err := cache.protectVMVolume(ctx, volume)
		if err != nil {
			return nil, err
		}
		result.volumes[volume.id] = backing
		result.ordered = append(result.ordered, backing)
	}
	if err := rootfs.Grant(ctx, record.vmID, record.launch.executionIdentity.uid, vmVolumeReadWrite); err != nil {
		return nil, err
	}
	if err := kernel.Grant(ctx, record.vmID, record.launch.executionIdentity.uid, vmVolumeReadOnly); err != nil {
		return nil, err
	}
	if result.cloudInit != nil {
		if err := result.cloudInit.Grant(ctx, record.vmID, record.launch.executionIdentity.uid, vmVolumeReadOnly); err != nil {
			return nil, err
		}
	}
	for _, volume := range volumes {
		if err := result.volumes[volume.id].Grant(ctx, record.vmID, record.launch.executionIdentity.uid, volume.access); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (backings *isolatedLaunchBackings) Release(ctx context.Context) error {
	if backings == nil {
		return nil
	}
	var result error
	for index := len(backings.ordered) - 1; index >= 0; index-- {
		result = errors.Join(result, backings.ordered[index].Release(ctx))
	}
	backings.ordered = nil
	return result
}
