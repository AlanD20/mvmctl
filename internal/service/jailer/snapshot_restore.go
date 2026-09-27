package jailer

import (
	"context"
	"errors"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
)

// preparedSnapshotRestore is constructed only from a locked immutable root capture.
// No caller-supplied paths, device configuration, or network overrides enter this capability.
type preparedSnapshotRestore struct {
	snapshotID       string
	ownerUID         uint32
	cacheIdentity    managedCacheIdentity
	targetVMID       string
	metadata         snapshotCaptureRecord
	memoryFD         int
	stateFD          int
	rootfs           *protectedBackingLease
	kernel           *protectedBackingLease
	cloudInit        *protectedBackingLease
	volumes          []preparedSnapshotVolume
	artifacts        map[string]int
	backings         []*protectedBackingLease
	backingsPrepared bool
	resume           bool
	registered       bool
	referenceCreated bool
	released         bool
	releaseErr       error
}

type preparedSnapshotVolume struct {
	guestDriveID string
	path         string
	selection    VMLaunchVolume
	backing      *protectedBackingLease
}

func prepareSnapshotRestore(
	ctx context.Context,
	cache *managedCacheLease,
	request VMSnapshotRestoreRequest,
) (_ *preparedSnapshotRestore, returnErr error) {
	store, err := openProtectedSnapshotStore(ctx, cache, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	entry, err := lockSnapshotStoreEntry(ctx, store, request.SnapshotID, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.Release(context.WithoutCancel(ctx))) }()
	metadata, err := entry.readRecord(ctx, cache.ownerUID)
	if err != nil {
		return nil, err
	}
	if protectedCacheIdentity(metadata.Source.Rootfs.cacheIdentity) != protectedCacheIdentity(cache.identity) {
		return nil, instanceValidationError("restore cache differs from immutable capture identity")
	}
	prepared := &preparedSnapshotRestore{
		snapshotID:    request.SnapshotID,
		ownerUID:      cache.ownerUID,
		cacheIdentity: cache.identity,
		targetVMID:    request.VMID,
		metadata:      metadata,
		memoryFD:      -1,
		stateFD:       -1,
		artifacts:     make(map[string]int),
		resume:        request.Resume,
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, prepared.releaseDescriptors(context.WithoutCancel(ctx)))
		}
	}()
	for _, artifact := range metadata.Artifacts {
		fd, err := entry.openArtifact(ctx, artifact)
		if err != nil {
			return nil, err
		}
		prepared.artifacts[artifact.Name] = fd
		if artifact.Name == "memory" {
			prepared.memoryFD = fd
		}
		if artifact.Name == "vmstate" {
			prepared.stateFD = fd
		}
	}
	for _, source := range metadata.Source.Volumes {
		selection := source.Selection
		selection.ID = crypto.VolumeID(request.VMID+"-"+source.GuestDriveID, request.SnapshotID)
		prepared.volumes = append(prepared.volumes, preparedSnapshotVolume{
			guestDriveID: source.GuestDriveID, path: source.Path, selection: selection,
		})
	}
	if err := entry.addRestoreReference(ctx, prepared); err != nil {
		return nil, err
	}
	prepared.referenceCreated = true
	return prepared, nil
}

func snapshotArtifactMode(name string) uint32 {
	if name == "memory" || name == "vmstate" {
		return 0444
	}
	return 0400
}

func (entry *snapshotStoreEntry) openArtifact(ctx context.Context, artifact snapshotArtifact) (_ int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	fd, err := unix.Openat2(entry.directoryFD, artifact.Name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err == nil && (stat.Mode != unix.S_IFREG|snapshotArtifactMode(artifact.Name) || stat.Uid != 0 || stat.Gid != 0 ||
		stat.Nlink != 1 || stat.Size < 1 || uint64(stat.Size) != artifact.Bytes || uint64(stat.Dev) != artifact.Device || stat.Ino != artifact.Inode) {
		err = instanceValidationError("immutable snapshot artifact differs from captured identity")
	}
	if err != nil {
		return -1, errors.Join(err, unix.Close(fd))
	}
	return fd, nil
}

// CALLER MUST HOLD: the newly registered target VM authority, before child release.
func (prepared *preparedSnapshotRestore) prepareBackings(
	ctx context.Context,
	cache *managedCacheLease,
) (returnErr error) {
	if prepared.released || !prepared.registered || cache.ownerUID != prepared.ownerUID ||
		cache.identity != prepared.cacheIdentity ||
		prepared.backingsPrepared {
		return instanceValidationError("restore backing preparation is outside the registered transaction")
	}
	prepared.backingsPrepared = true
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, prepared.releaseBackings(context.WithoutCancel(ctx)))
		}
	}()
	type importSelection struct {
		kind, id, artifact string
		volumeIndex        int
	}
	items := []importSelection{
		{kind: protectedBackingRootfs, id: prepared.targetVMID, artifact: "rootfs.img"},
		{kind: protectedBackingKernel, id: prepared.metadata.Source.KernelID, artifact: "kernel"},
	}
	if prepared.metadata.Source.CloudInit {
		items = append(
			items,
			importSelection{kind: protectedBackingCloudInit, id: prepared.targetVMID, artifact: "cloud-init.iso"},
		)
	}
	for index, volume := range prepared.volumes {
		items = append(
			items,
			importSelection{
				kind:        protectedBackingVolume,
				id:          volume.selection.ID,
				artifact:    "volume-" + volume.guestDriveID + ".raw",
				volumeIndex: index,
			},
		)
	}
	slices.SortFunc(items, func(a, b importSelection) int { return strings.Compare(a.kind+"/"+a.id, b.kind+"/"+b.id) })
	for _, item := range items {
		source, found := prepared.artifacts[item.artifact]
		if !found {
			return instanceValidationError("restore backing artifact is absent")
		}
		var size uint64
		for _, artifact := range prepared.metadata.Artifacts {
			if artifact.Name == item.artifact {
				size = artifact.Bytes
				break
			}
		}
		var lease *protectedBackingLease
		var err error
		switch item.kind {
		case protectedBackingRootfs:
			lease, err = cache.restoreProtectedVMRootfs(ctx, vmResourceID(prepared.targetVMID), source, size)
			prepared.rootfs = lease
		case protectedBackingKernel:
			lease, err = cache.restoreProtectedVMKernel(ctx, kernelResourceID(item.id), source, size)
			prepared.kernel = lease
		case protectedBackingCloudInit:
			lease, err = cache.restoreProtectedVMCloudInit(ctx, vmResourceID(prepared.targetVMID), source, size)
			prepared.cloudInit = lease
		case protectedBackingVolume:
			lease, err = cache.restoreProtectedVMVolume(
				ctx,
				prepared.volumes[item.volumeIndex].selection.private(),
				source,
			)
			prepared.volumes[item.volumeIndex].backing = lease
		}
		if lease != nil {
			prepared.backings = append(prepared.backings, lease)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (prepared *preparedSnapshotRestore) launchRequest(cacheRoot string) VMLaunchRequest {
	source := prepared.metadata.Source
	config := source.Config
	request := VMLaunchRequest{
		VMID:             prepared.targetVMID,
		CacheRoot:        cacheRoot,
		KernelID:         source.KernelID,
		RootfsBytes:      source.Rootfs.bytes,
		Release:          source.Release,
		NetworkPlacement: source.NetworkPlacement,
		NetworkID:        source.NetworkID,
		GuestIPv4:        source.GuestIPv4,
		GuestMAC:         source.GuestMAC,
		BootArgs:         config.BootSource.BootArgs,
		VCPUs:            config.MachineConfig.VCPUCount,
		MemoryMiB:        config.MachineConfig.MemSizeMiB,
		SMT:              config.MachineConfig.SMT,
		TrackDirtyPages:  config.MachineConfig.TrackDirtyPages,
		RootfsCacheType:  config.Drives[0].CacheType,
		CloudInit:        source.CloudInit,
		Logging:          config.Logger != nil,
		Metrics:          config.Metrics != nil,
		CPUConfig:        config.CPUConfig,
		CgroupLimits:     source.CgroupLimits,
		PCIEnabled:       source.PCIEnabled,
		NestedVirt:       source.NestedVirt,
	}
	if config.Logger != nil {
		request.LogLevel = config.Logger.Level
	}
	if config.Vsock != nil {
		cid := config.Vsock.GuestCID
		request.VsockCID = &cid
	}
	for _, volume := range prepared.volumes {
		request.Volumes = append(request.Volumes, volume.selection)
	}
	return request
}

func (prepared *preparedSnapshotRestore) releaseDescriptors(ctx context.Context) error {
	result := prepared.releaseBackings(ctx)
	for name, fd := range prepared.artifacts {
		result = errors.Join(result, unix.Close(fd))
		delete(prepared.artifacts, name)
	}
	prepared.memoryFD, prepared.stateFD = -1, -1
	return result
}

func (prepared *preparedSnapshotRestore) releaseBackings(ctx context.Context) error {
	var result error
	for i := len(prepared.backings) - 1; i >= 0; i-- {
		result = errors.Join(result, prepared.backings[i].Release(ctx))
	}
	prepared.backings = nil
	return result
}

func (prepared *preparedSnapshotRestore) Release(ctx context.Context) error {
	if prepared == nil {
		return nil
	}
	if prepared.released {
		return prepared.releaseErr
	}
	prepared.releaseErr = prepared.releaseDescriptors(context.WithoutCancel(ctx))
	if prepared.referenceCreated && !prepared.registered {
		prepared.releaseErr = errors.Join(
			prepared.releaseErr,
			removePreparedSnapshotReference(context.WithoutCancel(ctx), prepared),
		)
	}
	prepared.released = true
	return prepared.releaseErr
}

// Restore consumes only root-captured configuration and independent target backing.
func (server *VMSnapshotServer) Restore(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMSnapshotRestoreRequest,
) (_ model.SnapshotRestoreResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return model.SnapshotRestoreResult{}, err
	}
	caller := instanceCaller{uid: ownerUID, gid: ownerGID}
	cache, err := pinSnapshotCache(ctx, caller, request.CacheRoot)
	if err != nil {
		return model.SnapshotRestoreResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	prepared, err := prepareSnapshotRestore(ctx, cache, request)
	if err != nil {
		return model.SnapshotRestoreResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, prepared.Release(context.WithoutCancel(ctx))) }()
	launch, err := NewVMLifecycleServer().launchSnapshot(ctx, caller, prepared.launchRequest(request.CacheRoot), prepared)
	if err != nil {
		return model.SnapshotRestoreResult{}, err
	}
	result := model.SnapshotRestoreResult{VMID: launch.VMID, PID: launch.PID, StartTimeTicks: launch.StartTimeTicks,
		LaunchGeneration: launch.LaunchGeneration, Configuration: prepared.metadata.projection(!request.Resume),
		Volumes: make([]model.SnapshotRestoredVolume, 0, len(prepared.volumes))}
	for _, volume := range prepared.volumes {
		result.Volumes = append(result.Volumes, model.SnapshotRestoredVolume{
			ID: volume.selection.ID, GuestDriveID: volume.guestDriveID, Bytes: volume.selection.ExpectedBytes,
			ReadOnly: volume.selection.ReadOnly, CacheType: volume.selection.CacheType})
	}
	return result, nil
}
