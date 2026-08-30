package loopmount

import (
	"context"
	"errors"
	"io"
	"maps"
	"runtime"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

const (
	rootfsInspectRuntimeDirectory      = "/run/mvmctl"
	rootfsInspectRuntimeMode           = 0711
	rootfsInspectMountName             = "rootfs"
	rootfsInspectMountPath             = rootfsInspectRuntimeDirectory + "/" + rootfsInspectMountName
	rootfsInspectMountMode             = 0700
	rootfsInspectMountFlags            = unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
	rootfsInspectPrivateFlags          = unix.MS_REC | unix.MS_PRIVATE
	rootfsInspectOpenFlags             = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsInspectFileOpenFlags         = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsInspectResolveFlags          = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	rootfsInspectLeafResolveFlags      = rootfsInspectResolveFlags | unix.RESOLVE_NO_XDEV
	rootfsInspectSnapshotMask          = unix.STATX_BASIC_STATS | unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
	maxRootfsInspectCleanupDetailBytes = 512

	rootfsInspectCleanupFailedKey = "loopmount_image_inspect_cleanup_failed"
	rootfsInspectCleanupDetailKey = "loopmount_image_inspect_cleanup_detail"
)

var rootfsInspectOSReleaseLeaves = [...]string{"etc/os-release", "usr/lib/os-release"}

type rootfsImageInspectAdapter struct {
	deps rootfsImageInspectDeps
}

type rootfsImageInspectDeps struct {
	lockOSThread       func(context.Context) error
	unlockOSThread     func(context.Context) error
	unshare            func(context.Context, int) error
	mount              func(context.Context, string, string, string, uintptr, string) error
	unmount            func(context.Context, string, int) error
	open               func(context.Context, string, int, uint32) (int, error)
	openAt2            func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt            func(context.Context, int, string, uint32) error
	fstat              func(context.Context, int, *unix.Stat_t) error
	fstatfs            func(context.Context, int, *unix.Statfs_t) error
	statx              func(context.Context, int, int, int, *unix.Statx_t) error
	read               func(context.Context, int, []byte) (int, error)
	close              func(context.Context, int) error
	attachLoop         func(context.Context, *rootfsLease) (*rootfsLoopDevice, error)
	discoverPartitions func(context.Context, *rootfsLoopDevice) (*rootfsPartitionSet, error)
	probeFilesystem    func(
		context.Context,
		Filesystem,
		*rootfsLoopDevice,
		*rootfsPartitionSet,
	) (*rootfsSelectedSource, error)
}

type rootfsInspectMountpoint struct {
	parentFD int
	rootFD   int
	identity rootfsInspectFileIdentity
}

type rootfsInspectFileIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	uid    uint32
	gid    uint32
	nlink  uint64
	size   int64
	mtime  unix.Timespec
	ctime  unix.Timespec
}

type rootfsInspectBackingSnapshot struct {
	mask           uint32
	attributes     uint64
	attributesMask uint64
	deviceMajor    uint32
	deviceMinor    uint32
	inode          uint64
	mode           uint16
	uid            uint32
	gid            uint32
	nlink          uint32
	size           uint64
	blocks         uint64
	atime          unix.StatxTimestamp
	mtime          unix.StatxTimestamp
	ctime          unix.StatxTimestamp
	mountID        uint64
}

type completedImageInspectCleanup struct{}

func (completedImageInspectCleanup) CleanupInspectImage(context.Context) error { return nil }

// newSystemRootfsImageInspectAdapter constructs the production read-only inspection effect. Once
// unshare(CLONE_NEWNS) succeeds, the locked thread must terminate with the short-lived receiver process; it must never
// be unlocked or returned to Go's thread pool.
func newSystemRootfsImageInspectAdapter() *rootfsImageInspectAdapter {
	loopDeps := systemRootfsLoopDeps()
	partitionDeps := systemRootfsPartitionDiscoveryDeps()
	probeDeps := rootfsFilesystemProbeDeps{
		run: system.DefaultRunner.Run,
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
	}
	return &rootfsImageInspectAdapter{deps: rootfsImageInspectDeps{
		lockOSThread: func(context.Context) error {
			runtime.LockOSThread()
			return nil
		},
		unlockOSThread: func(context.Context) error {
			runtime.UnlockOSThread()
			return nil
		},
		unshare: func(_ context.Context, flags int) error { return unix.Unshare(flags) },
		mount: func(_ context.Context, source, target, filesystem string, flags uintptr, data string) error {
			return unix.Mount(source, target, filesystem, flags, data)
		},
		unmount: func(_ context.Context, target string, flags int) error { return unix.Unmount(target, flags) },
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		openAt2: func(_ context.Context, fd int, path string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(fd, path, how)
		},
		mkdirAt: func(_ context.Context, fd int, path string, mode uint32) error {
			return unix.Mkdirat(fd, path, mode)
		},
		fstat:   func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		fstatfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error { return unix.Fstatfs(fd, stat) },
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		read: func(_ context.Context, fd int, buffer []byte) (int, error) {
			count, err := unix.Read(fd, buffer)
			if count == 0 && err == nil {
				return 0, io.EOF
			}
			return count, err
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		attachLoop: func(ctx context.Context, lease *rootfsLease) (*rootfsLoopDevice, error) {
			return attachRootfsLoopDevice(ctx, loopDeps, lease)
		},
		discoverPartitions: func(ctx context.Context, loop *rootfsLoopDevice) (*rootfsPartitionSet, error) {
			return discoverRootfsPartitionDevices(ctx, partitionDeps, loop)
		},
		probeFilesystem: func(
			ctx context.Context,
			filesystem Filesystem,
			loop *rootfsLoopDevice,
			partitions *rootfsPartitionSet,
		) (*rootfsSelectedSource, error) {
			return probeRootfsMountSource(ctx, probeDeps, filesystem, loop, partitions)
		},
	}}
}

func (adapter *rootfsImageInspectAdapter) InspectImage(
	ctx context.Context,
	lease *rootfsLease,
	filesystem Filesystem,
) (outcome imageInspectEffectOutcome, returnErr error) {
	outcome = imageInspectEffectOutcome{state: imageInspectEffectStateUnchanged, lease: completedImageInspectCleanup{}}
	if ctx == nil {
		return outcome, rootfsAtomicError("image inspection context is required", nil)
	}
	if adapter == nil || validateRootfsImageInspectDeps(adapter.deps) != nil {
		return outcome, rootfsAtomicError("image inspection dependencies are incomplete", nil)
	}
	if err := filesystem.Validate(); err != nil {
		return outcome, rootfsAtomicError("image inspection filesystem is invalid", err)
	}
	if lease == nil {
		return outcome, rootfsAtomicError("managed image rootfs authority is missing", nil)
	}
	before, err := inspectRootfsBackingSnapshot(ctx, adapter.deps, lease)
	if err != nil {
		return outcome, err
	}
	if err := adapter.deps.lockOSThread(ctx); err != nil {
		return outcome, rootfsAtomicError("lock image inspection OS thread", err)
	}
	// CRITICAL: Cancellation must be observed after LockOSThread and before unshare. Only a thread that still shares
	// the process mount namespace may be unlocked and returned to Go's thread pool.
	if err := ctx.Err(); err != nil {
		return outcome, appendRootfsInspectCleanupError(
			rootfsAtomicError("image inspection context ended before mount namespace creation", err),
			"unlock image inspection OS thread",
			adapter.deps.unlockOSThread(context.WithoutCancel(ctx)),
		)
	}
	if err := adapter.deps.unshare(ctx, unix.CLONE_NEWNS); err != nil {
		return outcome, appendRootfsInspectCleanupError(
			rootfsAtomicError("create image inspection mount namespace", err),
			"unlock image inspection OS thread",
			adapter.deps.unlockOSThread(context.WithoutCancel(ctx)),
		)
	}

	// A successful unshare is the uncertainty boundary. This function deliberately never calls UnlockOSThread.
	outcome.state = imageInspectEffectStateOutcomeUnknown
	cleanupCtx := context.WithoutCancel(ctx)
	var mountpoint *rootfsInspectMountpoint
	var loop *rootfsLoopDevice
	var partitions *rootfsPartitionSet
	var source *rootfsSelectedSource
	mounted := false
	mountedRootFD := -1
	defer func() {
		if mountedRootFD >= 0 {
			returnErr = appendRootfsInspectCleanupError(
				returnErr,
				"close pinned inspected root",
				adapter.deps.close(cleanupCtx, mountedRootFD),
			)
		}
		if mounted {
			returnErr = appendRootfsInspectCleanupError(
				returnErr,
				"unmount inspected rootfs",
				adapter.deps.unmount(cleanupCtx, rootfsInspectMountPath, 0),
			)
		}
		returnErr = appendRootfsInspectCleanupError(
			returnErr,
			"release selected rootfs source",
			source.Release(cleanupCtx),
		)
		returnErr = appendRootfsInspectCleanupError(
			returnErr,
			"release rootfs partitions",
			partitions.Release(cleanupCtx),
		)
		returnErr = appendRootfsInspectCleanupError(returnErr, "release rootfs loop device", loop.Release(cleanupCtx))
		returnErr = appendRootfsInspectCleanupError(
			returnErr,
			"close image inspection mountpoint",
			closeRootfsInspectMountpoint(cleanupCtx, adapter.deps, mountpoint),
		)
		after, snapshotErr := inspectRootfsBackingSnapshot(cleanupCtx, adapter.deps, lease)
		returnErr = appendRootfsInspectCleanupError(returnErr, "revalidate inspected backing image", snapshotErr)
		if snapshotErr == nil && after != before {
			returnErr = appendRootfsInspectCleanupError(
				returnErr,
				"revalidate inspected backing image",
				errors.New("backing image metadata or size changed"),
			)
		}
		if returnErr == nil {
			outcome.state = imageInspectEffectStateUnchanged
		} else {
			outcome.osType = ""
		}
	}()

	if err := adapter.deps.mount(ctx, "", "/", "", rootfsInspectPrivateFlags, ""); err != nil {
		return outcome, rootfsAtomicError("make image inspection mounts recursively private", err)
	}
	mountpoint, err = prepareRootfsInspectMountpoint(ctx, adapter.deps)
	if err != nil {
		return outcome, err
	}
	loop, err = adapter.deps.attachLoop(ctx, lease)
	if err != nil {
		return outcome, err
	}
	partitions, err = adapter.deps.discoverPartitions(ctx, loop)
	if err != nil {
		return outcome, err
	}
	source, err = adapter.deps.probeFilesystem(ctx, filesystem, loop, partitions)
	if err != nil {
		return outcome, err
	}
	sourcePath, err := activeRootfsInspectSource(ctx, adapter.deps, source, filesystem)
	if err != nil {
		return outcome, err
	}
	if err := revalidateRootfsInspectMountpoint(ctx, adapter.deps, mountpoint); err != nil {
		return outcome, err
	}
	if err := adapter.deps.mount(
		ctx,
		sourcePath,
		rootfsInspectMountPath,
		string(filesystem),
		rootfsInspectMountFlags,
		"",
	); err != nil {
		return outcome, rootfsAtomicError("mount selected rootfs read-only", err)
	}
	mounted = true
	mountedRootFD, err = pinRootfsInspectMountedRoot(ctx, adapter.deps, mountpoint, source, filesystem)
	if err != nil {
		return outcome, err
	}
	outcome.osType, err = readRootfsOSRelease(ctx, adapter.deps, mountedRootFD)
	if err != nil {
		return outcome, err
	}
	return outcome, nil
}

func validateRootfsImageInspectDeps(deps rootfsImageInspectDeps) error {
	if deps.lockOSThread == nil || deps.unlockOSThread == nil || deps.unshare == nil || deps.mount == nil ||
		deps.unmount == nil || deps.open == nil || deps.openAt2 == nil || deps.mkdirAt == nil ||
		deps.fstat == nil || deps.fstatfs == nil || deps.statx == nil || deps.read == nil ||
		deps.close == nil || deps.attachLoop == nil || deps.discoverPartitions == nil || deps.probeFilesystem == nil {
		return rootfsAtomicError("image inspection dependencies are incomplete", nil)
	}
	return nil
}

func inspectRootfsBackingSnapshot(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	lease *rootfsLease,
) (rootfsInspectBackingSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return rootfsInspectBackingSnapshot{}, rootfsAtomicError("inspect backing image", err)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.rootfsFD < 0 || lease.access != rootfsReadOnly {
		return rootfsInspectBackingSnapshot{}, rootfsAtomicError(
			"managed image rootfs authority is inactive or writable",
			nil,
		)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, lease.rootfsFD, rootfsStatxFlags, rootfsInspectSnapshotMask, &stat); err != nil {
		return rootfsInspectBackingSnapshot{}, rootfsAtomicError("inspect backing image", err)
	}
	if stat.Mask&rootfsStatxRequiredFile != rootfsStatxRequiredFile || stableRootfsMetadata(stat) != lease.metadata {
		return rootfsInspectBackingSnapshot{}, rootfsAtomicError(
			"backing image identity or admitted metadata changed",
			nil,
		)
	}
	return rootfsInspectBackingSnapshot{
		mask: stat.Mask, attributes: stat.Attributes, attributesMask: stat.Attributes_mask,
		deviceMajor: stat.Dev_major, deviceMinor: stat.Dev_minor, inode: stat.Ino, mode: stat.Mode,
		uid: stat.Uid, gid: stat.Gid, nlink: stat.Nlink, size: stat.Size, blocks: stat.Blocks,
		atime: stat.Atime, mtime: stat.Mtime, ctime: stat.Ctime, mountID: stat.Mnt_id,
	}, nil
}

func prepareRootfsInspectMountpoint(
	ctx context.Context,
	deps rootfsImageInspectDeps,
) (_ *rootfsInspectMountpoint, returnErr error) {
	parentFD, err := deps.open(ctx, rootfsInspectRuntimeDirectory, rootfsInspectOpenFlags, 0)
	if err != nil {
		return nil, rootfsAtomicError("open fixed image inspection runtime directory", err)
	}
	rootFD := -1
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		if rootFD >= 0 {
			returnErr = appendRootfsInspectCleanupError(
				returnErr,
				"close rejected mountpoint",
				deps.close(cleanupCtx, rootFD),
			)
		}
		returnErr = appendRootfsInspectCleanupError(
			returnErr,
			"close runtime directory",
			deps.close(cleanupCtx, parentFD),
		)
	}()
	var parentStat unix.Stat_t
	if err := deps.fstat(ctx, parentFD, &parentStat); err != nil {
		return nil, rootfsAtomicError("inspect fixed image inspection runtime directory", err)
	}
	// CRITICAL: /run/mvmctl is the fixed shared authority layout. Inspection rejects ownership or exact-mode drift and
	// never repairs it, so a privileged effect cannot silently change host policy established during installation.
	if parentStat.Mode&unix.S_IFMT != unix.S_IFDIR || parentStat.Uid != 0 || parentStat.Gid != 0 ||
		parentStat.Mode&07777 != rootfsInspectRuntimeMode {
		return nil, rootfsAtomicError("fixed image inspection runtime directory is not root:root mode 0711", nil)
	}
	if err := deps.mkdirAt(ctx, parentFD, rootfsInspectMountName, rootfsInspectMountMode); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return nil, rootfsAtomicError("create fixed image inspection mountpoint", err)
		}
	}
	rootFD, err = deps.openAt2(ctx, parentFD, rootfsInspectMountName, &unix.OpenHow{
		Flags: rootfsInspectOpenFlags, Resolve: rootfsInspectLeafResolveFlags,
	})
	if err != nil {
		return nil, rootfsAtomicError("open fixed image inspection mountpoint", err)
	}
	identity, err := inspectRootfsMountpointIdentity(ctx, deps, rootFD)
	if err != nil {
		return nil, err
	}
	return &rootfsInspectMountpoint{parentFD: parentFD, rootFD: rootFD, identity: identity}, nil
}

func inspectRootfsMountpointIdentity(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	fd int,
) (rootfsInspectFileIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return rootfsInspectFileIdentity{}, rootfsAtomicError("inspect fixed image inspection mountpoint", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 ||
		stat.Mode&07777 != rootfsInspectMountMode {
		return rootfsInspectFileIdentity{}, rootfsAtomicError(
			"fixed image inspection mountpoint is not root:root mode 0700",
			nil,
		)
	}
	return rootfsInspectFileIdentity{
		device: uint64(stat.Dev), inode: stat.Ino, mode: stat.Mode, uid: stat.Uid, gid: stat.Gid,
		nlink: stat.Nlink, size: stat.Size, mtime: stat.Mtim, ctime: stat.Ctim,
	}, nil
}

func revalidateRootfsInspectMountpoint(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	mountpoint *rootfsInspectMountpoint,
) error {
	if mountpoint == nil || mountpoint.parentFD < 0 || mountpoint.rootFD < 0 {
		return rootfsAtomicError("fixed image inspection mountpoint is inactive", nil)
	}
	reopenedFD, err := deps.openAt2(ctx, mountpoint.parentFD, rootfsInspectMountName, &unix.OpenHow{
		Flags: rootfsInspectOpenFlags, Resolve: rootfsInspectLeafResolveFlags,
	})
	if err != nil {
		return rootfsAtomicError("reopen fixed image inspection mountpoint", err)
	}
	identity, err := inspectRootfsMountpointIdentity(ctx, deps, reopenedFD)
	if err != nil {
		return appendRootfsInspectCleanupError(
			err,
			"close revalidated fixed mountpoint",
			deps.close(context.WithoutCancel(ctx), reopenedFD),
		)
	}
	if identity != mountpoint.identity {
		return appendRootfsInspectCleanupError(
			rootfsAtomicError("fixed image inspection mountpoint identity changed before mount", nil),
			"close revalidated fixed mountpoint",
			deps.close(context.WithoutCancel(ctx), reopenedFD),
		)
	}
	return appendRootfsInspectCleanupError(
		nil,
		"close revalidated fixed mountpoint",
		deps.close(context.WithoutCancel(ctx), reopenedFD),
	)
}

func activeRootfsInspectSource(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	source *rootfsSelectedSource,
	filesystem Filesystem,
) (string, error) {
	if source == nil || source.state == nil {
		return "", rootfsAtomicError("selected rootfs source is inactive", nil)
	}
	state := source.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.parent == nil || state.partitions == nil || state.fd < 0 ||
		state.filesystem != filesystem {
		return "", rootfsAtomicError("selected rootfs source is inactive or mismatched", nil)
	}
	parent := state.parent
	partitions := state.partitions
	parent.mu.Lock()
	defer parent.mu.Unlock()
	partitions.mu.Lock()
	defer partitions.mu.Unlock()
	if err := validateRootfsFilesystemProbeCapabilities(ctx, parent, partitions); err != nil {
		return "", err
	}
	selectedFD, err := selectedRootfsDescriptor(parent, partitions, rootfsMountSource{
		kind: state.kind, identity: state.identity, partition: state.partition,
	})
	if err != nil || selectedFD != state.fd {
		return "", rootfsAtomicError("selected rootfs source no longer matches its retained descriptor", err)
	}
	if err := inspectRootfsProbeIdentity(
		ctx,
		rootfsFilesystemProbeDeps{fstat: deps.fstat},
		state.fd,
		state.identity,
		"before mount",
	); err != nil {
		return "", err
	}
	if state.kind == rootfsMountSourceRaw && state.partition == 0 {
		return rootfsLoopDevicePath(parent.index)
	}
	if state.kind == rootfsMountSourcePartition {
		return rootfsLoopPartitionDevicePath(parent.index, state.partition)
	}
	return "", rootfsAtomicError("selected rootfs source kind is invalid", nil)
}

func pinRootfsInspectMountedRoot(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	mountpoint *rootfsInspectMountpoint,
	source *rootfsSelectedSource,
	filesystem Filesystem,
) (int, error) {
	rootFD, err := deps.openAt2(ctx, mountpoint.parentFD, rootfsInspectMountName, &unix.OpenHow{
		Flags: rootfsInspectOpenFlags, Resolve: rootfsInspectResolveFlags,
	})
	if err != nil {
		return -1, rootfsAtomicError("pin mounted image root", err)
	}
	if _, err := activeRootfsInspectSource(ctx, deps, source, filesystem); err != nil {
		return -1, appendRootfsInspectCleanupError(
			err,
			"close rejected mounted image root",
			deps.close(context.WithoutCancel(ctx), rootFD),
		)
	}
	var statfs unix.Statfs_t
	if err := deps.fstatfs(ctx, rootFD, &statfs); err != nil {
		return -1, appendRootfsInspectCleanupError(
			rootfsAtomicError("inspect mounted image filesystem", err),
			"close rejected mounted image root",
			deps.close(context.WithoutCancel(ctx), rootFD),
		)
	}
	if !rootfsInspectFilesystemMagicMatches(filesystem, statfs.Type) {
		return -1, appendRootfsInspectCleanupError(
			rootfsAtomicError("mounted image filesystem type does not match the request", nil),
			"close rejected mounted image root",
			deps.close(context.WithoutCancel(ctx), rootFD),
		)
	}
	return rootFD, nil
}

func rootfsInspectFilesystemMagicMatches(filesystem Filesystem, magic int64) bool {
	switch filesystem {
	case FilesystemExt2, FilesystemExt3, FilesystemExt4:
		return magic == unix.EXT4_SUPER_MAGIC
	case FilesystemBtrfs:
		return magic == unix.BTRFS_SUPER_MAGIC
	default:
		return false
	}
}

func readRootfsOSRelease(ctx context.Context, deps rootfsImageInspectDeps, rootFD int) (string, error) {
	for _, leaf := range rootfsInspectOSReleaseLeaves {
		content, unavailable, err := readRootfsInspectRegularLeaf(ctx, deps, rootFD, leaf)
		if err != nil {
			return "", err
		}
		if unavailable {
			continue
		}
		return parseRootfsOSRelease(content)
	}
	return "", rootfsAtomicError("image contains no admissible OS release file", nil)
}

func readRootfsInspectRegularLeaf(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	rootFD int,
	leaf string,
) (_ []byte, unavailable bool, returnErr error) {
	fd, err := deps.openAt2(ctx, rootFD, leaf, &unix.OpenHow{
		Flags: rootfsInspectFileOpenFlags, Resolve: rootfsInspectLeafResolveFlags,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) ||
			errors.Is(err, unix.ENOTDIR) {
			return nil, true, nil
		}
		return nil, false, rootfsAtomicError("open fixed OS release leaf", err)
	}
	defer func() {
		returnErr = appendRootfsInspectCleanupError(
			returnErr,
			"close fixed OS release leaf",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	var before unix.Stat_t
	if err := deps.fstat(ctx, fd, &before); err != nil {
		return nil, false, rootfsAtomicError("inspect fixed OS release leaf", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink == 0 || before.Size < 0 ||
		before.Size > maxRootfsOSReleaseBytes {
		return nil, false, rootfsAtomicError("fixed OS release leaf is not a bounded regular file", nil)
	}
	buffer := make([]byte, maxRootfsOSReleaseBytes+1)
	readBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, rootfsAtomicError("read fixed OS release leaf", err)
		}
		count, readErr := deps.read(ctx, fd, buffer[readBytes:])
		if count < 0 || count > len(buffer)-readBytes {
			return nil, false, rootfsAtomicError("read fixed OS release leaf made invalid progress", nil)
		}
		readBytes += count
		if readBytes > maxRootfsOSReleaseBytes {
			return nil, false, rootfsAtomicError("fixed OS release leaf exceeds the supported byte bound", nil)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, false, rootfsAtomicError("read fixed OS release leaf", readErr)
			}
			break
		}
		if count == 0 {
			return nil, false, rootfsAtomicError("read fixed OS release leaf made no progress", nil)
		}
	}
	var after unix.Stat_t
	if err := deps.fstat(ctx, fd, &after); err != nil {
		return nil, false, rootfsAtomicError("reinspect fixed OS release leaf", err)
	}
	if inspectRootfsFileIdentity(before) != inspectRootfsFileIdentity(after) || int64(readBytes) != before.Size {
		return nil, false, rootfsAtomicError("fixed OS release leaf changed while reading", nil)
	}
	return append([]byte(nil), buffer[:readBytes]...), false, nil
}

func inspectRootfsFileIdentity(stat unix.Stat_t) rootfsInspectFileIdentity {
	return rootfsInspectFileIdentity{
		device: uint64(stat.Dev), inode: stat.Ino, mode: stat.Mode, uid: stat.Uid, gid: stat.Gid,
		nlink: stat.Nlink, size: stat.Size, mtime: stat.Mtim, ctime: stat.Ctim,
	}
}

func closeRootfsInspectMountpoint(
	ctx context.Context,
	deps rootfsImageInspectDeps,
	mountpoint *rootfsInspectMountpoint,
) error {
	if mountpoint == nil {
		return nil
	}
	var result error
	if mountpoint.rootFD >= 0 {
		result = appendRootfsInspectCleanupError(result, "close fixed mountpoint", deps.close(ctx, mountpoint.rootFD))
	}
	if mountpoint.parentFD >= 0 {
		result = appendRootfsInspectCleanupError(
			result,
			"close fixed runtime directory",
			deps.close(ctx, mountpoint.parentFD),
		)
	}
	return result
}

func appendRootfsInspectCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	detail := boundedRootfsInspectCleanupDetail(message, cause)
	if primary == nil {
		domainErr := rootfsAtomicError(detail, cause)
		domainErr.Details = map[string]any{
			rootfsInspectCleanupFailedKey: true,
			rootfsInspectCleanupDetailKey: detail,
		}
		return domainErr
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		domainErr = rootfsAtomicError(detail, errors.Join(primary, cause))
		domainErr.Details = map[string]any{
			rootfsInspectCleanupFailedKey: true,
			rootfsInspectCleanupDetailKey: detail,
		}
		return domainErr
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	if _, exists := result.Details[rootfsInspectCleanupFailedKey]; !exists {
		result.Details[rootfsInspectCleanupFailedKey] = true
	}
	if _, exists := result.Details[rootfsInspectCleanupDetailKey]; !exists {
		result.Details[rootfsInspectCleanupDetailKey] = detail
	}
	result.Err = errors.Join(domainErr.Err, cause)
	return &result
}

func boundedRootfsInspectCleanupDetail(message string, cause error) string {
	detail := message
	if cause != nil {
		detail += ": " + cause.Error()
	}
	if len(detail) <= maxRootfsInspectCleanupDetailBytes {
		return detail
	}
	return detail[:maxRootfsInspectCleanupDetailBytes-3] + "..."
}
