package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

// VMSnapshotServer composes root-owned producer, backing, and immutable-store authority.
type VMSnapshotServer struct{ instances *instanceAuthority }

// NewVMSnapshotServer constructs the fixed privileged snapshot receiver.
func NewVMSnapshotServer() *VMSnapshotServer {
	return &VMSnapshotServer{instances: newInstanceAuthority()}
}

func pinSnapshotCache(ctx context.Context, caller instanceCaller, locator string) (*managedCacheLease, error) {
	selection, err := parseManagedCacheLocator(locator)
	if err != nil {
		return nil, err
	}
	return pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(), caller, selection)
}

// Inspect reads only immutable root metadata; user SQLite cannot select restore inputs.
func (server *VMSnapshotServer) Inspect(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMSnapshotRequest,
) (_ model.SnapshotCaptureResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	cache, err := pinSnapshotCache(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.CacheRoot)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	store, err := openProtectedSnapshotStore(ctx, cache, false)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	entry, err := lockSnapshotStoreEntry(ctx, store, request.SnapshotID, false)
	if err != nil {
		return model.SnapshotCaptureResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.Release(context.WithoutCancel(ctx))) }()
	record, err := entry.readRecord(ctx, ownerUID)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			progress, progressErr := entry.readProgress(ctx)
			if progressErr != nil {
				return model.SnapshotCaptureResult{}, progressErr
			}
			if progress.OwnerUID != ownerUID {
				return model.SnapshotCaptureResult{}, instanceValidationError("snapshot progress owner differs")
			}
			progress.VMState = "unknown" // A historical pause must not project current state.
			return model.SnapshotCaptureResult{}, snapshotCaptureOutcomeError(progress, err)
		}
		return model.SnapshotCaptureResult{}, err
	}
	return record.projection(false), nil
}

// Remove refuses retained memory references and removes only exact protected snapshot leaves.
func (server *VMSnapshotServer) Remove(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMSnapshotRequest,
) (_ VMSnapshotRemoveResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return VMSnapshotRemoveResult{}, err
	}
	cache, err := pinSnapshotCache(ctx, instanceCaller{uid: ownerUID, gid: ownerGID}, request.CacheRoot)
	if err != nil {
		return VMSnapshotRemoveResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	store, err := openProtectedSnapshotStore(ctx, cache, false)
	if err != nil {
		return VMSnapshotRemoveResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	entry, err := lockSnapshotStoreEntry(ctx, store, request.SnapshotID, false)
	if errors.Is(err, unix.ENOENT) {
		return VMSnapshotRemoveResult{SnapshotID: request.SnapshotID, Removed: true}, nil
	}
	if err != nil {
		return VMSnapshotRemoveResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.Release(context.WithoutCancel(ctx))) }()
	if err := entry.remove(ctx); err != nil {
		return VMSnapshotRemoveResult{}, err
	}
	return VMSnapshotRemoveResult{SnapshotID: request.SnapshotID, Removed: true}, nil
}

func (record snapshotCaptureRecord) projection(paused bool) model.SnapshotCaptureResult {
	projection := model.SnapshotCaptureResult{
		SnapshotID:          record.SnapshotID,
		SourceVMID:          record.Source.VMID,
		Mode:                record.Mode,
		Paused:              paused,
		VCPUCount:           record.Source.Config.MachineConfig.VCPUCount,
		MemSizeMiB:          record.Source.Config.MachineConfig.MemSizeMiB,
		RootfsBytes:         record.Source.Rootfs.bytes,
		NetworkID:           record.Source.NetworkID,
		GuestIPv4:           record.Source.GuestIPv4,
		GuestMAC:            record.Source.GuestMAC,
		ReleaseVersion:      record.Source.Release.Version,
		ReleaseArchitecture: record.Source.Release.Architecture,
		FirecrackerSHA256:   record.Source.FirecrackerSHA256, JailerSHA256: record.Source.JailerSHA256,
		KernelID: record.Source.KernelID, PCIEnabled: record.Source.PCIEnabled, NestedVirt: record.Source.NestedVirt,
		CloudInit: record.Source.CloudInit, BootArgs: record.Source.Config.BootSource.BootArgs,
		CgroupLimits: record.Source.CgroupLimits, CPUConfig: record.Source.Config.CPUConfig,
		Logging: record.Source.Config.Logger != nil, Metrics: record.Source.Config.Metrics != nil,
	}
	if record.Source.Config.Vsock != nil {
		cid := record.Source.Config.Vsock.GuestCID
		projection.VsockCID = &cid
	}
	if record.Source.Config.Logger != nil {
		projection.LogLevel = record.Source.Config.Logger.Level
	}
	return projection
}
