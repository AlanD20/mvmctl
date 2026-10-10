package jailer

import (
	"context"
	"errors"
	"log/slog"
	"math"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// ProtectedVolumeServer resolves storage only under the authenticated owner's import lock.
type ProtectedVolumeServer struct{}

// NewProtectedVolumeServer constructs the named root storage capabilities without effects.
func NewProtectedVolumeServer() *ProtectedVolumeServer { return &ProtectedVolumeServer{} }

type protectedVolumeStorage struct {
	cache     *managedCacheLease
	backing   *protectedBackingLease
	protected bool
}

func openProtectedVolumeStorage(
	ctx context.Context, uid, gid uint32, cacheRoot, volumeID string,
) (_ *protectedVolumeStorage, returnErr error) {
	if ctx == nil {
		return nil, managedVMResourceValidationError("volume storage context is required")
	}
	if err := validateProtectedVolumeSelection(cacheRoot, volumeID); err != nil {
		return nil, err
	}
	caller := instanceCaller{uid: uid, gid: gid}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	locator, err := parseManagedCacheLocator(cacheRoot)
	if err != nil {
		return nil, err
	}
	cache, err := pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(), caller, locator)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx)))
		}
	}()
	backing, found, err := cache.openProtectedVMVolume(ctx, vmVolumeResourceID(volumeID))
	if err != nil {
		return nil, err
	}
	return &protectedVolumeStorage{cache: cache, backing: backing, protected: found}, nil
}

func (storage *protectedVolumeStorage) Release(ctx context.Context) error {
	return errors.Join(storage.backing.Release(ctx), storage.cache.Release(ctx))
}

func (storage *protectedVolumeStorage) requireAvailable(ctx context.Context) error {
	removed, err := storage.backing.IsRemoved(ctx)
	if err != nil {
		return err
	}
	if removed {
		return managedVMResourceValidationError("volume storage has been removed")
	}
	pending, err := storage.backing.IsInitialVolumeRemovalPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		return managedVMResourceValidationError("volume removal is pending; retry volume removal")
	}
	return nil
}

// Inspect never opens a caller raw file after protected backing has been admitted.
func (server *ProtectedVolumeServer) Inspect(
	ctx context.Context, uid, gid uint32, request ProtectedVolumeInspectRequest,
) (_ ProtectedVolumeInspectResult, returnErr error) {
	defer func() { returnErr = protectedVolumeOperationError("inspect", request.VolumeID, returnErr) }()
	storage, err := openProtectedVolumeStorage(ctx, uid, gid, request.CacheRoot, request.VolumeID)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, storage.Release(context.WithoutCancel(ctx))) }()
	if err := storage.requireAvailable(ctx); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if storage.protected {
		return storage.inspectProtected(ctx, request.VolumeID)
	}
	raw, err := storage.openInitialRaw(ctx, request.VolumeID, false)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, raw.Release()) }()
	result, err := inspectProtectedVolumeFD(ctx, request.VolumeID, raw.fd, false)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if err := raw.requireCurrentName(ctx, storage.cache); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	return result, nil
}

// Grow preserves the current backing identity and refuses shrinking or protected live references.
func (server *ProtectedVolumeServer) Grow(
	ctx context.Context, uid, gid uint32, request ProtectedVolumeGrowRequest,
) (_ ProtectedVolumeInspectResult, returnErr error) {
	defer func() { returnErr = protectedVolumeOperationError("grow", request.VolumeID, returnErr) }()
	if err := request.Validate(); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	storage, err := openProtectedVolumeStorage(ctx, uid, gid, request.CacheRoot, request.VolumeID)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, storage.Release(context.WithoutCancel(ctx))) }()
	if err := storage.requireAvailable(ctx); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if storage.protected {
		if err := storage.backing.Grow(ctx, request.TargetBytes); err != nil {
			return ProtectedVolumeInspectResult{}, err
		}
		return storage.inspectProtected(ctx, request.VolumeID)
	}
	raw, err := storage.openInitialRaw(ctx, request.VolumeID, true)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, raw.Release()) }()
	if request.TargetBytes < raw.identity.size {
		return ProtectedVolumeInspectResult{}, managedVMResourceValidationError("volume storage cannot be shrunk")
	}
	if err := raw.requireCurrentName(ctx, storage.cache); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if request.TargetBytes > raw.identity.size {
		if err := ctx.Err(); err != nil {
			return ProtectedVolumeInspectResult{}, err
		}
		if err := unix.Fallocate(
			raw.fd,
			0,
			int64(raw.identity.size),
			int64(request.TargetBytes-raw.identity.size),
		); err != nil {
			return ProtectedVolumeInspectResult{}, managedVMResourceAtomicError("grow pinned initial raw volume", err)
		}
		if err := unix.Fsync(raw.fd); err != nil {
			return ProtectedVolumeInspectResult{}, err
		}
	}
	result, err := inspectProtectedVolumeFD(ctx, request.VolumeID, raw.fd, false)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if result.LogicalBytes != request.TargetBytes {
		return ProtectedVolumeInspectResult{}, managedVMResourceValidationError(
			"initial raw volume changed during growth",
		)
	}
	if err := raw.requireCurrentName(ctx, storage.cache); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	return result, nil
}

// Remove requires no retained VM references and persists removal independently of caller SQLite.
func (server *ProtectedVolumeServer) Remove(
	ctx context.Context, uid, gid uint32, request ProtectedVolumeRemoveRequest,
) (_ ProtectedVolumeRemoveResult, returnErr error) {
	defer func() { returnErr = protectedVolumeOperationError("remove", request.VolumeID, returnErr) }()
	storage, err := openProtectedVolumeStorage(ctx, uid, gid, request.CacheRoot, request.VolumeID)
	if err != nil {
		return ProtectedVolumeRemoveResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, storage.Release(context.WithoutCancel(ctx))) }()
	removed, err := storage.backing.IsRemoved(ctx)
	if err != nil {
		return ProtectedVolumeRemoveResult{}, err
	}
	managedSource := storage.backing.record.Source.Inode != 0 && !storage.backing.record.SnapshotSource
	if removed {
		if managedSource || storage.backing.record.InitialRemoval != nil {
			if err := storage.removeInitialRaw(ctx, request.VolumeID); err != nil {
				return ProtectedVolumeRemoveResult{}, err
			}
		}
		return ProtectedVolumeRemoveResult{VolumeID: request.VolumeID, Removed: true}, nil
	}
	pending, err := storage.backing.IsInitialVolumeRemovalPending(ctx)
	if err != nil {
		return ProtectedVolumeRemoveResult{}, err
	}
	if storage.protected && !pending {
		if managedSource {
			if _, err := storage.protectedSourcePresent(ctx, request.VolumeID); err != nil {
				return ProtectedVolumeRemoveResult{}, err
			}
		}
		if err := storage.backing.Remove(ctx); err != nil {
			return ProtectedVolumeRemoveResult{}, err
		}
		if managedSource {
			if err := storage.removeInitialRaw(ctx, request.VolumeID); err != nil {
				return ProtectedVolumeRemoveResult{}, err
			}
		}
	} else if err := storage.removeInitialRaw(ctx, request.VolumeID); err != nil {
		return ProtectedVolumeRemoveResult{}, err
	}
	return ProtectedVolumeRemoveResult{VolumeID: request.VolumeID, Removed: true}, nil
}

func protectedVolumeOperationError(operation, id string, cause error) error {
	if cause == nil {
		return nil
	}
	slog.Error("authoritative volume storage operation failed", "operation", operation, "volume_id", id, "error", cause)
	return errs.WrapMsg(errs.CodeVolumeError, operation+" authoritative volume storage", cause, errs.WithEntity(id))
}

func (storage *protectedVolumeStorage) inspectProtected(
	ctx context.Context,
	id string,
) (ProtectedVolumeInspectResult, error) {
	binding, err := storage.backing.Binding(ctx)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	result, err := inspectProtectedVolumeFD(ctx, id, storage.backing.fd, true)
	if err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if result.LogicalBytes != binding.bytes {
		return ProtectedVolumeInspectResult{}, managedVMResourceValidationError(
			"protected volume length differs from root authority",
		)
	}
	return result, nil
}

func inspectProtectedVolumeFD(
	ctx context.Context, volumeID string, fd int, protected bool,
) (ProtectedVolumeInspectResult, error) {
	if err := ctx.Err(); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return ProtectedVolumeInspectResult{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size <= 0 || stat.Blocks < 0 ||
		uint64(stat.Blocks) > math.MaxUint64/512 {
		return ProtectedVolumeInspectResult{}, managedVMResourceValidationError("volume backing accounting is invalid")
	}
	return ProtectedVolumeInspectResult{
		VolumeID:       volumeID,
		LogicalBytes:   uint64(stat.Size),
		AllocatedBytes: uint64(stat.Blocks) * 512,
		Protected:      protected,
	}, nil
}

type initialRawVolume struct {
	fd        int
	directory int
	id        string
	identity  managedVMConfigStat
	mountID   uint64
}

func (storage *protectedVolumeStorage) openInitialRaw(
	ctx context.Context, id string, writable bool,
) (_ *initialRawVolume, returnErr error) {
	directory, err := openManagedVMResourceDirectory(ctx, storage.cache.deps, storage.cache.cacheFD,
		"volumes", "volume store", storage.cache.ownerUID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(directory))
		}
	}()
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if writable {
		flags = unix.O_RDWR | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
	}
	fd, err := openManagedVMResourceFileWithFlags(ctx, storage.cache.deps, directory, id+".raw", flags,
		managedVMRegularFilePolicy{name: "initial raw volume", minimumBytes: 1,
			maximumBytes: math.MaxInt64, requiredOwnerMode: 0600}, storage.cache.ownerUID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	identity, err := stableManagedVMConfigStat(ctx, storage.cache.configDeps, fd)
	if err != nil {
		return nil, err
	}
	mountID, err := initialVolumeMountID(fd)
	if err != nil {
		return nil, err
	}
	return &initialRawVolume{fd: fd, directory: directory, id: id, identity: identity, mountID: mountID}, nil
}

func initialVolumeMountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", managedVMResourceStatxFlags, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, managedVMResourceValidationError("initial volume mount identity is unavailable")
	}
	return stat.Mnt_id, nil
}

func (raw *initialRawVolume) requireCurrentName(ctx context.Context, cache *managedCacheLease) (returnErr error) {
	directory, err := openManagedVMResourceDirectory(ctx, cache.deps, cache.cacheFD,
		"volumes", "volume store", cache.ownerUID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	fd, err := openManagedVMResourceFile(ctx, cache.deps, directory, raw.id+".raw",
		managedVMRegularFilePolicy{name: "initial raw volume", minimumBytes: 1,
			maximumBytes: math.MaxInt64, requiredOwnerMode: 0600}, cache.ownerUID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	current, err := stableManagedVMConfigStat(ctx, cache.configDeps, fd)
	if err != nil {
		return err
	}
	mountID, err := initialVolumeMountID(fd)
	if err != nil {
		return err
	}
	if !sameInitialVolumeObject(raw.identity, current) || mountID != raw.mountID {
		return managedVMResourceValidationError("initial raw volume pathname changed while admitted")
	}
	return nil
}

func sameInitialVolumeObject(left, right managedVMConfigStat) bool {
	return left.deviceMajor == right.deviceMajor && left.deviceMinor == right.deviceMinor &&
		left.inode == right.inode && left.uid == right.uid && left.gid == right.gid &&
		left.mode == right.mode && left.nlink == 1 && right.nlink == 1
}

func (raw *initialRawVolume) Release() error {
	return errors.Join(unix.Close(raw.fd), unix.Close(raw.directory))
}
