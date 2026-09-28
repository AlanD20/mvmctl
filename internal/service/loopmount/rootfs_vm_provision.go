package loopmount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

const (
	rootfsProvisionMountFlags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
	rootfsE2fsckPath          = "/usr/sbin/e2fsck"
	rootfsResize2fsPath       = "/usr/sbin/resize2fs"
	rootfsBtrfsPath           = "/usr/bin/btrfs"
	// CRITICAL: filesystem growth is privileged and may retain the VM lock plus mounted capabilities. Keep the
	// absolute subprocess bound compiled into the effect while allowing an earlier caller deadline to win.
	rootfsGrowToolTimeout           = 5 * time.Minute
	maxRootfsGrowToolStdoutBytes    = 4 << 10
	maxRootfsGrowToolStderrBytes    = 4 << 10
	rootfsAgentBinaryPath           = "/usr/bin/mvm-agent"
	rootfsAgentConfigDirectory      = infra.GuestAgentConfigDirectory
	rootfsAgentTokenPath            = infra.GuestAgentTokenPath
	rootfsAgentSystemdPath          = "/etc/systemd/system/mvm-agent.service"
	rootfsAgentSystemdEvidencePath  = "/usr/lib/systemd/systemd"
	rootfsAgentSystemdConfigPath    = "/etc/systemd"
	rootfsAgentSystemdWantsPath     = "/etc/systemd/system/sysinit.target.wants"
	rootfsAgentSystemdWantsLink     = rootfsAgentSystemdWantsPath + "/mvm-agent.service"
	rootfsAgentSystemdWantsTarget   = "../mvm-agent.service"
	rootfsAgentOpenRCPath           = "/etc/init.d/mvm-agent"
	rootfsAgentOpenRCEvidencePath   = "/sbin/openrc"
	rootfsAgentOpenRCInitPath       = "/etc/init.d"
	rootfsAgentOpenRCRunlevelPath   = "/etc/runlevels/default"
	rootfsAgentOpenRCRunlevelLink   = rootfsAgentOpenRCRunlevelPath + "/mvm-agent"
	rootfsAgentOpenRCRunlevelTarget = "/etc/init.d/mvm-agent"
	rootfsSSHConfigPath             = "/etc/ssh/sshd_config.d/mvm.conf"
	rootfsSudoPolicyPath            = "/etc/sudoers.d/mvm"
	rootfsPasswdPath                = "/etc/passwd"
	rootfsHostnamePath              = "/etc/hostname"
	rootfsHostsPath                 = "/etc/hosts"
	rootfsDNSPath                   = "/etc/resolv.conf"
	rootfsFstabPath                 = "/etc/fstab"
	rootfsProvisionReadBufferLimit  = maxRootfsProvisionTextBytes
)

type rootfsVMProvisionAdapter struct {
	deps rootfsVMProvisionDeps
}

type rootfsAgentInitLayout uint8

const (
	rootfsAgentInitUnknown rootfsAgentInitLayout = iota
	rootfsAgentInitSystemd
	rootfsAgentInitOpenRC
)

type rootfsVMProvisionDeps struct {
	lockOSThread       func(context.Context) error
	unlockOSThread     func(context.Context) error
	unshare            func(context.Context, int) error
	mount              func(context.Context, string, string, string, uintptr, string) error
	unmount            func(context.Context, string, int) error
	open               func(context.Context, string, int, uint32) (int, error)
	openAt2            func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt            func(context.Context, int, string, uint32) error
	fstat              func(context.Context, int, *unix.Stat_t) error
	fstatAt            func(context.Context, int, string, *unix.Stat_t, int) error
	fstatfs            func(context.Context, int, *unix.Statfs_t) error
	statx              func(context.Context, int, int, int, *unix.Statx_t) error
	pread              func(context.Context, int, []byte, int64) (int, error)
	pwrite             func(context.Context, int, []byte, int64) (int, error)
	ftruncate          func(context.Context, int, int64) error
	fchown             func(context.Context, int, int, int) error
	fchmod             func(context.Context, int, uint32) error
	fsync              func(context.Context, int) error
	syncfs             func(context.Context, int) error
	renameAt           func(context.Context, int, string, int, string) error
	symlinkAt          func(context.Context, string, int, string) error
	readlinkAt         func(context.Context, int, string, []byte) (int, error)
	unlinkAt           func(context.Context, int, string, int) error
	close              func(context.Context, int) error
	readDirent         func(context.Context, int, []byte) (int, error)
	deviceBytes        func(context.Context, int) (uint64, error)
	tempName           func(context.Context) (string, error)
	run                func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error)
	agentBinary        func(context.Context) ([]byte, error)
	attachLoop         func(context.Context, *rootfsLease) (*rootfsLoopDevice, error)
	discoverPartitions func(context.Context, *rootfsLoopDevice) (*rootfsPartitionSet, error)
	probeFilesystem    func(
		context.Context,
		Filesystem,
		*rootfsLoopDevice,
		*rootfsPartitionSet,
	) (*rootfsSelectedSource, error)
}

type completedVMProvisionCleanup struct{}

func (completedVMProvisionCleanup) CleanupProvisionVM(context.Context) error { return nil }

// newSystemRootfsVMProvisionAdapter binds the composition root's exact embedded-agent accessor. The privileged
// receiver must exit after this adapter unshares its permanently locked OS thread.
func newSystemRootfsVMProvisionAdapter(
	agentBinary func(context.Context) ([]byte, error),
) *rootfsVMProvisionAdapter {
	loopDeps := systemRootfsLoopDeps()
	partitionDeps := systemRootfsPartitionDiscoveryDeps()
	probeDeps := systemRootfsFilesystemProbeDeps()
	return &rootfsVMProvisionAdapter{deps: rootfsVMProvisionDeps{
		lockOSThread:   func(context.Context) error { runtime.LockOSThread(); return nil },
		unlockOSThread: func(context.Context) error { runtime.UnlockOSThread(); return nil },
		unshare:        func(_ context.Context, flags int) error { return unix.Unshare(flags) },
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
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		fstatAt: func(_ context.Context, fd int, path string, stat *unix.Stat_t, flags int) error {
			return unix.Fstatat(fd, path, stat, flags)
		},
		fstatfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error { return unix.Fstatfs(fd, stat) },
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		pread: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			return unix.Pread(fd, data, offset)
		},
		pwrite: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			return unix.Pwrite(fd, data, offset)
		},
		ftruncate: func(_ context.Context, fd int, size int64) error { return unix.Ftruncate(fd, size) },
		fchown:    func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod:    func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		fsync:     func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		syncfs:    func(_ context.Context, fd int) error { return unix.Syncfs(fd) },
		renameAt: func(_ context.Context, oldFD int, oldName string, newFD int, newName string) error {
			return unix.Renameat(oldFD, oldName, newFD, newName)
		},
		symlinkAt: func(_ context.Context, target string, fd int, name string) error {
			return unix.Symlinkat(target, fd, name)
		},
		readlinkAt: func(_ context.Context, fd int, path string, buffer []byte) (int, error) {
			return unix.Readlinkat(fd, path, buffer)
		},
		unlinkAt: func(_ context.Context, fd int, name string, flags int) error {
			return unix.Unlinkat(fd, name, flags)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		readDirent: func(_ context.Context, fd int, buffer []byte) (int, error) {
			return unix.ReadDirent(fd, buffer)
		},
		deviceBytes: func(_ context.Context, fd int) (uint64, error) {
			size, err := unix.IoctlGetInt(fd, unix.BLKGETSIZE64)
			if err != nil || size < 0 {
				return 0, err
			}
			return uint64(size), nil
		},
		tempName: func(context.Context) (string, error) {
			var value [16]byte
			if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
				return "", err
			}
			return ".mvmctl-provision-" + hex.EncodeToString(value[:]), nil
		},
		run:         system.DefaultRunner.Run,
		agentBinary: agentBinary,
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

func (adapter *rootfsVMProvisionAdapter) ProvisionVM(
	ctx context.Context,
	lease *rootfsLease,
	filesystem Filesystem,
	plan VMProvisionPlan,
) (outcome vmProvisionEffectOutcome, returnErr error) {
	outcome = vmProvisionEffectOutcome{
		state: vmProvisionEffectStateUnchanged, filesystem: filesystem,
		lease: completedVMProvisionCleanup{},
	}
	if ctx == nil {
		return outcome, rootfsAtomicError("vm provisioning context is required", nil)
	}
	if adapter == nil || validateRootfsVMProvisionDeps(adapter.deps) != nil {
		return outcome, rootfsAtomicError("vm provisioning dependencies are incomplete", nil)
	}
	if err := filesystem.Validate(); err != nil {
		return outcome, rootfsAtomicError("vm provisioning filesystem is invalid", err)
	}
	if filesystem == FilesystemXFS && plan.ResizeBytes != 0 {
		return outcome, rootfsValidationError(
			"managed XFS resize is unsupported; no backing or filesystem mutation was attempted",
		)
	}
	admittedBytes, err := inspectRootfsProvisionLease(ctx, adapter.deps, lease)
	if err != nil {
		return outcome, err
	}
	if err := plan.Validate(admittedBytes); err != nil {
		return outcome, rootfsAtomicError("vm provisioning plan is invalid", err)
	}
	if plan.Agent != nil && adapter.deps.agentBinary == nil {
		return outcome, rootfsAtomicError("VM embedded-agent source is unavailable", nil)
	}
	var cloudInitSeed *rootfsCloudInitSeed
	if plan.InjectCloudInit {
		cloudInitSeed, err = readRootfsCloudInitSeed(ctx, adapter.deps, lease)
		if err != nil {
			return outcome, err
		}
	}
	if err := adapter.deps.lockOSThread(ctx); err != nil {
		return outcome, rootfsAtomicError("lock VM provisioning OS thread", err)
	}
	// CRITICAL: Cancellation must be observed after LockOSThread and before unshare. Only a thread that still shares
	// the process mount namespace may be unlocked and returned to Go's thread pool.
	if err := ctx.Err(); err != nil {
		return outcome, appendRootfsProvisionCleanupError(
			rootfsAtomicError("vm provisioning context ended before mount namespace creation", err),
			"unlock VM provisioning OS thread",
			adapter.deps.unlockOSThread(context.WithoutCancel(ctx)),
		)
	}
	if err := adapter.deps.unshare(ctx, unix.CLONE_NEWNS); err != nil {
		return outcome, appendRootfsProvisionCleanupError(
			rootfsAtomicError("create VM provisioning mount namespace", err),
			"unlock VM provisioning OS thread",
			adapter.deps.unlockOSThread(context.WithoutCancel(ctx)),
		)
	}

	// CRITICAL: A thread with a private mount namespace must terminate with the receiver process.
	tracker := &rootfsProvisionTracker{}
	cleanupCtx := context.WithoutCancel(ctx)
	var mountpoint *rootfsInspectMountpoint
	var loop *rootfsLoopDevice
	var partitions *rootfsPartitionSet
	var source *rootfsSelectedSource
	mounted := false
	mountedRootFD := -1
	filesystemSynced := false
	targetBytes := admittedBytes
	if plan.ResizeBytes != 0 {
		targetBytes = plan.ResizeBytes
	}
	defer func() {
		cleanupErr := error(nil)
		if mountedRootFD >= 0 {
			cleanupErr = appendRootfsProvisionCleanupError(
				cleanupErr,
				"close pinned provisioned root",
				adapter.deps.close(cleanupCtx, mountedRootFD),
			)
		}
		if mounted {
			cleanupErr = appendRootfsProvisionCleanupError(
				cleanupErr,
				"unmount provisioned rootfs",
				adapter.deps.unmount(cleanupCtx, rootfsInspectMountPath, 0),
			)
		}
		cleanupErr = appendRootfsProvisionCleanupError(
			cleanupErr,
			"release selected provisioned rootfs source",
			source.Release(cleanupCtx),
		)
		cleanupErr = appendRootfsProvisionCleanupError(
			cleanupErr,
			"release provisioned rootfs partitions",
			partitions.Release(cleanupCtx),
		)
		cleanupErr = appendRootfsProvisionCleanupError(
			cleanupErr,
			"release provisioned rootfs loop device",
			loop.Release(cleanupCtx),
		)
		cleanupErr = appendRootfsProvisionCleanupError(
			cleanupErr,
			"close VM provisioning mountpoint",
			closeRootfsInspectMountpoint(cleanupCtx, adapter.deps.inspectDeps(), mountpoint),
		)
		cleanupErr = appendRootfsProvisionCleanupError(
			cleanupErr,
			"sync and revalidate provisioned backing rootfs",
			finalizeRootfsProvisionBacking(cleanupCtx, adapter.deps, lease, targetBytes),
		)
		if tracker.modified && filesystemSynced && cleanupErr == nil && returnErr == nil {
			tracker.durable = true
		}
		returnErr = appendRootfsProvisionCleanupError(returnErr, "clean up VM provisioning effects", cleanupErr)
		outcome.state = tracker.state()
		outcome.rootfsBytes = targetBytes
		if returnErr != nil && outcome.state == vmProvisionEffectStateModified {
			outcome.state = vmProvisionEffectStateModifiedDurabilityUncertain
		}
	}()

	if err := adapter.deps.mount(ctx, "", "/", "", rootfsInspectPrivateFlags, ""); err != nil {
		return outcome, rootfsAtomicError("make VM provisioning mounts recursively private", err)
	}
	if admittedBytes < targetBytes {
		if err := growRootfsProvisionBacking(ctx, adapter.deps, lease, targetBytes, tracker); err != nil {
			return outcome, err
		}
	}
	mountpoint, err = prepareRootfsInspectMountpoint(ctx, adapter.deps.inspectDeps())
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
	sourcePath, sourceBytes, err := activeRootfsProvisionSource(ctx, adapter.deps, source, filesystem)
	if err != nil {
		return outcome, err
	}
	if plan.ResizeBytes != 0 && sourceBytes < targetBytes {
		return outcome, rootfsAtomicError("selected rootfs device cannot reach the requested grow target", nil)
	}
	if err := revalidateRootfsInspectMountpoint(ctx, adapter.deps.inspectDeps(), mountpoint); err != nil {
		return outcome, err
	}
	tracker.outcomeUnknown = true
	if err := adapter.deps.mount(
		ctx,
		sourcePath,
		rootfsInspectMountPath,
		string(filesystem),
		rootfsProvisionMountFlags,
		rootfsReadWriteMountOptions(filesystem),
	); err != nil {
		return outcome, rootfsAtomicError("mount selected rootfs read-write", err)
	}
	mounted = true
	tracker.completeMutation()
	mountedRootFD, err = pinRootfsProvisionMountedRoot(ctx, adapter.deps, mountpoint, source, filesystem)
	if err != nil {
		return outcome, err
	}
	if plan.ResizeBytes != 0 {
		mountedRootFD, mounted, err = convergeRootfsProvisionGrow(
			ctx,
			adapter.deps,
			mountpoint,
			source,
			filesystem,
			sourcePath,
			targetBytes,
			mountedRootFD,
			mounted,
			tracker,
			&outcome.commandsRun,
		)
		if err != nil {
			return outcome, err
		}
	}
	tree := rootfsGuestTree{
		deps: adapter.deps.guestTreeDeps(), rootFD: mountedRootFD, tracker: tracker, cloudInit: cloudInitSeed,
	}
	filesWritten, err := applyRootfsProvisionPlan(ctx, tree, adapter.deps, plan)
	outcome.filesWritten = filesWritten
	if err != nil {
		return outcome, err
	}
	if err := adapter.deps.syncfs(ctx, mountedRootFD); err != nil {
		return outcome, rootfsCleanupError("sync provisioned guest filesystem", err)
	}
	filesystemSynced = true
	return outcome, nil
}

func validateRootfsVMProvisionDeps(deps rootfsVMProvisionDeps) error {
	if deps.lockOSThread == nil || deps.unlockOSThread == nil || deps.unshare == nil || deps.mount == nil ||
		deps.unmount == nil || deps.open == nil || deps.fstatfs == nil || deps.statx == nil || deps.ftruncate == nil ||
		deps.syncfs == nil || deps.deviceBytes == nil || deps.run == nil ||
		deps.attachLoop == nil || deps.discoverPartitions == nil || deps.probeFilesystem == nil {
		return rootfsAtomicError("vm provisioning dependencies are incomplete", nil)
	}
	if err := validateRootfsGuestTreeDeps(deps.guestTreeDeps()); err != nil {
		return err
	}
	return validateRootfsImageInspectDeps(deps.inspectDeps())
}

func (deps rootfsVMProvisionDeps) inspectDeps() rootfsImageInspectDeps {
	return rootfsImageInspectDeps{
		lockOSThread: deps.lockOSThread, unlockOSThread: deps.unlockOSThread, unshare: deps.unshare,
		mount: deps.mount, unmount: deps.unmount, open: deps.open, openAt2: deps.openAt2, mkdirAt: deps.mkdirAt,
		fstat: deps.fstat, fstatfs: deps.fstatfs, statx: deps.statx,
		read: func(ctx context.Context, fd int, data []byte) (int, error) {
			count, err := deps.pread(ctx, fd, data, 0)
			if count == 0 && err == nil {
				return 0, io.EOF
			}
			return count, err
		},
		close: deps.close, attachLoop: deps.attachLoop, discoverPartitions: deps.discoverPartitions,
		probeFilesystem: deps.probeFilesystem,
	}
}

func (deps rootfsVMProvisionDeps) guestTreeDeps() rootfsGuestTreeDeps {
	return rootfsGuestTreeDeps{
		openAt2: deps.openAt2, mkdirAt: deps.mkdirAt, fstat: deps.fstat, fstatAt: deps.fstatAt, fstatfs: deps.fstatfs,
		pread: deps.pread, pwrite: deps.pwrite, fchown: deps.fchown, fchmod: deps.fchmod, fsync: deps.fsync,
		renameAt: deps.renameAt, symlinkAt: deps.symlinkAt, readlinkAt: deps.readlinkAt,
		unlinkAt: deps.unlinkAt, close: deps.close, tempName: deps.tempName, readDirent: deps.readDirent,
	}
}

func inspectRootfsProvisionLease(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	lease *rootfsLease,
) (uint64, error) {
	if lease == nil {
		return 0, rootfsAtomicError("managed VM rootfs authority is missing", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.rootfsFD < 0 || lease.access != rootfsReadWrite || lease.admittedBytes == 0 {
		return 0, rootfsAtomicError("managed VM rootfs authority is inactive or read-only", nil)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, lease.rootfsFD, rootfsStatxFlags, rootfsStatxMask, &stat); err != nil {
		return 0, rootfsAtomicError("inspect managed VM backing rootfs", err)
	}
	if stat.Mask&rootfsStatxRequiredFile != rootfsStatxRequiredFile || stableRootfsMetadata(stat) != lease.metadata ||
		stat.Size != lease.admittedBytes {
		return 0, rootfsAtomicError("managed VM backing rootfs identity or admitted size changed", nil)
	}
	return lease.admittedBytes, nil
}

func growRootfsProvisionBacking(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	lease *rootfsLease,
	target uint64,
	tracker *rootfsProvisionTracker,
) error {
	if target > math.MaxInt64 {
		return rootfsAtomicError("managed VM grow target exceeds host offset range", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.rootfsFD < 0 || lease.access != rootfsReadWrite || lease.admittedBytes >= target {
		return rootfsAtomicError("managed VM backing rootfs is not eligible for grow", nil)
	}
	if lease.protectedVM != nil {
		tracker.outcomeUnknown = true
		if err := lease.protectedVM.GrowRootfs(ctx, target); err != nil {
			return err
		}
		tracker.markModified()
		tracker.completeMutation()
		return nil
	}
	if err := deps.ftruncate(ctx, lease.rootfsFD, int64(target)); err != nil {
		return rootfsAtomicError("grow managed VM backing rootfs", err)
	}
	tracker.markModified()
	if err := deps.fsync(ctx, lease.rootfsFD); err != nil {
		return rootfsCleanupError("sync grown managed VM backing rootfs", err)
	}
	return nil
}

func activeRootfsProvisionSource(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	source *rootfsSelectedSource,
	filesystem Filesystem,
) (string, uint64, error) {
	path, err := activeRootfsInspectSource(ctx, deps.inspectDeps(), source, filesystem)
	if err != nil {
		return "", 0, err
	}
	state := source.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.fd < 0 {
		return "", 0, rootfsAtomicError("selected rootfs source is inactive", nil)
	}
	bytes, err := deps.deviceBytes(ctx, state.fd)
	if err != nil || bytes == 0 {
		return "", 0, rootfsAtomicError("inspect selected rootfs device capacity", err)
	}
	return path, bytes, nil
}

func pinRootfsProvisionMountedRoot(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	mountpoint *rootfsInspectMountpoint,
	source *rootfsSelectedSource,
	filesystem Filesystem,
) (int, error) {
	rootFD, err := deps.openAt2(ctx, mountpoint.parentFD, rootfsInspectMountName, &unix.OpenHow{
		Flags: rootfsGuestDirectoryFlags, Resolve: rootfsInspectResolveFlags,
	})
	if err != nil {
		return -1, rootfsAtomicError("pin mounted VM rootfs", err)
	}
	if _, _, err := activeRootfsProvisionSource(ctx, deps, source, filesystem); err != nil {
		return -1, appendRootfsProvisionCleanupError(
			err,
			"close rejected mounted VM rootfs",
			deps.close(context.WithoutCancel(ctx), rootFD),
		)
	}
	var statfs unix.Statfs_t
	if err := deps.fstatfs(ctx, rootFD, &statfs); err != nil {
		return -1, appendRootfsProvisionCleanupError(
			rootfsAtomicError("inspect mounted VM rootfs", err),
			"close rejected mounted VM rootfs",
			deps.close(context.WithoutCancel(ctx), rootFD),
		)
	}
	if !rootfsInspectFilesystemMagicMatches(filesystem, statfs.Type) {
		return -1, appendRootfsProvisionCleanupError(
			rootfsAtomicError("mounted VM rootfs filesystem does not match the request", nil),
			"close rejected mounted VM rootfs",
			deps.close(context.WithoutCancel(ctx), rootFD),
		)
	}
	return rootFD, nil
}

func rootfsReadWriteMountOptions(filesystem Filesystem) string {
	if filesystem == FilesystemXFS {
		return "nouuid"
	}
	return ""
}

func convergeRootfsProvisionGrow(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	mountpoint *rootfsInspectMountpoint,
	source *rootfsSelectedSource,
	filesystem Filesystem,
	sourcePath string,
	target uint64,
	mountedRootFD int,
	mounted bool,
	tracker *rootfsProvisionTracker,
	commandsRun *uint32,
) (int, bool, error) {
	converged, err := probeRootfsProvisionCapacity(ctx, deps, source, filesystem, mountedRootFD, target)
	if err != nil {
		return mountedRootFD, mounted, err
	}
	if converged {
		return mountedRootFD, mounted, nil
	}
	if filesystem == FilesystemBtrfs {
		if err := runRootfsGrowTool(
			ctx,
			deps,
			[]string{rootfsBtrfsPath, "filesystem", "resize", "max", rootfsInspectMountPath},
			[]int{0},
			tracker,
		); err != nil {
			return mountedRootFD, mounted, err
		}
		*commandsRun = *commandsRun + 1
	} else {
		if err := deps.close(context.WithoutCancel(ctx), mountedRootFD); err != nil {
			return -1, mounted, rootfsCleanupError("close VM root before offline ext grow", err)
		}
		mountedRootFD = -1
		if err := deps.unmount(context.WithoutCancel(ctx), rootfsInspectMountPath, 0); err != nil {
			return mountedRootFD, mounted, rootfsCleanupError("unmount VM rootfs for offline ext grow", err)
		}
		mounted = false
		if err := runRootfsGrowTool(
			ctx,
			deps,
			[]string{rootfsE2fsckPath, "-f", "-p", sourcePath},
			[]int{0, 1, 2},
			tracker,
		); err != nil {
			return mountedRootFD, mounted, err
		}
		*commandsRun = *commandsRun + 1
		if err := runRootfsGrowTool(
			ctx,
			deps,
			[]string{rootfsResize2fsPath, sourcePath},
			[]int{0},
			tracker,
		); err != nil {
			return mountedRootFD, mounted, err
		}
		*commandsRun = *commandsRun + 1
		if _, _, err := activeRootfsProvisionSource(ctx, deps, source, filesystem); err != nil {
			return mountedRootFD, mounted, err
		}
		tracker.outcomeUnknown = true
		extBytes, blockBytes, err := readExtFilesystemSize(ctx, deps, source, target, true)
		if err != nil {
			return mountedRootFD, mounted, err
		}
		if !rootfsCapacityConverged(extBytes, target, blockBytes) {
			return mountedRootFD, mounted, rootfsAtomicError(
				"grown ext filesystem did not reach the requested capacity", nil,
			)
		}
		if err := deps.mount(
			ctx,
			sourcePath,
			rootfsInspectMountPath,
			string(filesystem),
			rootfsProvisionMountFlags,
			"",
		); err != nil {
			return mountedRootFD, mounted, rootfsAtomicError("remount grown VM rootfs read-write", err)
		}
		mounted = true
		tracker.completeMutation()
		mountedRootFD, err = pinRootfsProvisionMountedRoot(ctx, deps, mountpoint, source, filesystem)
		if err != nil {
			return mountedRootFD, mounted, err
		}
	}
	converged, err = probeRootfsProvisionCapacity(ctx, deps, source, filesystem, mountedRootFD, target)
	if err != nil {
		return mountedRootFD, mounted, err
	}
	if !converged {
		return mountedRootFD, mounted, rootfsAtomicError(
			"grown VM filesystem did not reach the requested capacity",
			nil,
		)
	}
	return mountedRootFD, mounted, nil
}

func probeRootfsProvisionCapacity(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	source *rootfsSelectedSource,
	filesystem Filesystem,
	rootFD int,
	target uint64,
) (bool, error) {
	if filesystem != FilesystemBtrfs {
		capacity, blockSize, err := readExtFilesystemSize(ctx, deps, source, target, false)
		if err != nil {
			return false, err
		}
		return rootfsCapacityConverged(capacity, target, blockSize), nil
	}
	var stat unix.Statfs_t
	if err := deps.fstatfs(ctx, rootFD, &stat); err != nil {
		return false, rootfsAtomicError("probe managed VM filesystem capacity", err)
	}
	if stat.Bsize <= 0 || stat.Blocks == 0 || uint64(stat.Blocks) > math.MaxUint64/uint64(stat.Bsize) {
		return false, rootfsAtomicError("managed VM filesystem capacity is invalid", nil)
	}
	capacity := uint64(stat.Blocks) * uint64(stat.Bsize)
	return rootfsCapacityConverged(capacity, target, uint64(stat.Bsize)), nil
}

func runRootfsGrowTool(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	args []string,
	acceptedExitCodes []int,
	tracker *rootfsProvisionTracker,
) error {
	tracker.outcomeUnknown = true
	result, runErr := deps.run(ctx, args, system.RunCmdOpts{
		Check: false, Capture: true, Cwd: "/", Timeout: rootfsGrowToolTimeout,
		Env: map[string]string{"LANG": "C", "LC_ALL": "C"}, ReplaceEnv: true,
		MaxStdoutBytes: maxRootfsGrowToolStdoutBytes, MaxStderrBytes: maxRootfsGrowToolStderrBytes,
	})
	if runErr != nil || result == nil {
		return rootfsAtomicError("execute fixed rootfs grow tool", runErr)
	}
	accepted := false
	for _, code := range acceptedExitCodes {
		if result.ExitCode == code {
			accepted = true
			break
		}
	}
	if !accepted {
		return rootfsAtomicError(
			fmt.Sprintf("fixed rootfs grow tool failed with exit code %d", result.ExitCode),
			nil,
		)
	}
	tracker.completeMutation()
	return nil
}

func finalizeRootfsProvisionBacking(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	lease *rootfsLease,
	expectedBytes uint64,
) error {
	if lease == nil {
		return rootfsAtomicError("managed VM backing rootfs authority is missing", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.rootfsFD < 0 || lease.access != rootfsReadWrite {
		return rootfsAtomicError("managed VM backing rootfs authority is inactive", nil)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, lease.rootfsFD, rootfsStatxFlags, rootfsStatxMask, &stat); err != nil {
		return rootfsAtomicError("reinspect provisioned backing rootfs", err)
	}
	expected := lease.metadata
	expected.size = expectedBytes
	if stat.Mask&rootfsStatxRequiredFile != rootfsStatxRequiredFile || stableRootfsMetadata(stat) != expected {
		return rootfsAtomicError("provisioned backing rootfs identity or final size changed", nil)
	}
	if err := deps.fsync(ctx, lease.rootfsFD); err != nil {
		return rootfsCleanupError("sync provisioned backing rootfs", err)
	}
	return nil
}

func applyRootfsProvisionPlan(
	ctx context.Context,
	tree rootfsGuestTree,
	deps rootfsVMProvisionDeps,
	plan VMProvisionPlan,
) (uint32, error) {
	var filesWritten uint32
	write := func(file rootfsProvisionFile) error {
		changed, err := tree.replaceFile(ctx, file)
		if changed {
			filesWritten++
		}
		return err
	}
	if plan.DeblobOS != nil {
		written, err := applyRootfsDeblob(ctx, tree, *plan.DeblobOS)
		filesWritten += written
		if err != nil {
			return filesWritten, err
		}
	}
	if plan.Hostname != nil {
		current, unavailable, err := tree.readOptional(ctx, rootfsHostsPath, rootfsProvisionReadBufferLimit)
		if err != nil {
			return filesWritten, err
		}
		if unavailable {
			current = nil
		}
		hosts, err := rewriteRootfsHosts(current, *plan.Hostname)
		if err != nil {
			return filesWritten, err
		}
		if err := write(rootfsProvisionFile{
			path: rootfsHostnamePath, content: []byte(*plan.Hostname + "\n"), mode: 0644,
		}); err != nil {
			return filesWritten, err
		}
		if err := write(rootfsProvisionFile{path: rootfsHostsPath, content: hosts, mode: 0644}); err != nil {
			return filesWritten, err
		}
	}
	if plan.DNS != nil {
		if err := write(rootfsProvisionFile{
			path: rootfsDNSPath, content: []byte("nameserver " + *plan.DNS + "\n"), mode: 0644,
		}); err != nil {
			return filesWritten, err
		}
	}
	accounts := make(map[string]rootfsGuestAccount, 2)
	account := func(username string) (rootfsGuestAccount, error) {
		if existing, ok := accounts[username]; ok {
			return existing, nil
		}
		parsed, written, err := ensureRootfsGuestAccount(ctx, tree, username)
		filesWritten += written
		if err != nil {
			return rootfsGuestAccount{}, err
		}
		accounts[username] = parsed
		return parsed, nil
	}
	if plan.SSH != nil {
		sshAccount, err := account(plan.SSH.User)
		if err != nil {
			return filesWritten, err
		}
		if len(plan.SSH.PublicKeys) > 0 {
			if plan.SSH.User != "root" {
				rootAccount, err := account("root")
				if err != nil {
					return filesWritten, err
				}
				if _, err := tree.ensureDirectory(
					ctx,
					"/root/.ssh",
					0700,
					rootAccount.uid,
					rootAccount.gid,
				); err != nil {
					return filesWritten, err
				}
				if err := write(rootfsProvisionFile{
					path:    "/root/.ssh/authorized_keys",
					content: []byte(strings.Join(plan.SSH.PublicKeys, "\n") + "\n"),
					mode:    0600,
				}); err != nil {
					return filesWritten, err
				}
			}
			if _, err := tree.ensureDirectory(
				ctx,
				"/"+sshAccount.home+"/.ssh",
				0700,
				sshAccount.uid,
				sshAccount.gid,
			); err != nil {
				return filesWritten, err
			}
			if err := write(rootfsProvisionFile{
				path:    "/" + sshAccount.home + "/.ssh/authorized_keys",
				content: []byte(strings.Join(plan.SSH.PublicKeys, "\n") + "\n"), mode: 0600,
				uid: sshAccount.uid, gid: sshAccount.gid,
			}); err != nil {
				return filesWritten, err
			}
		}
		if _, err := tree.ensureDirectory(ctx, "/etc/ssh/sshd_config.d", 0755, 0, 0); err != nil {
			return filesWritten, err
		}
		if err := write(rootfsProvisionFile{
			path: rootfsSSHConfigPath, content: rootfsSSHDConfig(plan.SSH.User), mode: 0644,
		}); err != nil {
			return filesWritten, err
		}
		written, err := configureRootfsSSHBoot(ctx, tree)
		filesWritten += written
		if err != nil {
			return filesWritten, err
		}
	}
	if plan.SudoUser != nil {
		if _, err := account(*plan.SudoUser); err != nil {
			return filesWritten, err
		}
		if _, err := tree.ensureDirectory(ctx, "/etc/sudoers.d", 0750, 0, 0); err != nil {
			return filesWritten, err
		}
		if err := write(rootfsProvisionFile{
			path:    rootfsSudoPolicyPath,
			content: []byte(*plan.SudoUser + " ALL=(ALL) NOPASSWD: ALL\n"), mode: 0440,
		}); err != nil {
			return filesWritten, err
		}
		if err := repairRootfsSudoOwnership(ctx, tree); err != nil {
			return filesWritten, err
		}
	}
	if plan.Agent != nil {
		layout, err := detectRootfsAgentInitLayout(ctx, tree)
		if err != nil {
			return filesWritten, err
		}
		binary, err := deps.agentBinary(ctx)
		if err != nil || len(binary) == 0 || len(binary) > maxRootfsAgentBytes {
			return filesWritten, rootfsAtomicError("embedded managed VM agent binary is unavailable or unbounded", err)
		}
		if _, err := tree.ensureDirectory(ctx, "/usr/bin", 0755, 0, 0); err != nil {
			return filesWritten, err
		}
		if _, err := tree.ensureDirectory(ctx, rootfsAgentConfigDirectory, 0700, 0, 0); err != nil {
			return filesWritten, err
		}
		files := []rootfsProvisionFile{
			{path: rootfsAgentBinaryPath, content: binary, mode: 0755},
			{path: rootfsAgentTokenPath, content: []byte(plan.Agent.Token + "\n"), mode: 0600},
		}
		switch layout {
		case rootfsAgentInitSystemd:
			for _, directory := range []string{"/etc/systemd/system", rootfsAgentSystemdWantsPath} {
				if _, err := tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
					return filesWritten, err
				}
			}
			files = append(files, rootfsProvisionFile{
				path: rootfsAgentSystemdPath, content: rootfsAgentSystemdService(plan.Agent.Port), mode: 0644,
			})
		case rootfsAgentInitOpenRC:
			for _, directory := range []string{rootfsAgentOpenRCInitPath, rootfsAgentOpenRCRunlevelPath} {
				if _, err := tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
					return filesWritten, err
				}
			}
			files = append(files, rootfsProvisionFile{
				path: rootfsAgentOpenRCPath, content: rootfsAgentOpenRCService(plan.Agent.Port), mode: 0755,
			})
		default:
			return filesWritten, rootfsAtomicError("managed guest agent init layout is unsupported", nil)
		}
		for _, file := range files {
			if err := write(file); err != nil {
				return filesWritten, err
			}
		}
		changed, err := tree.replaceAgentEnablementLink(ctx, layout)
		if changed {
			filesWritten++
		}
		if err != nil {
			return filesWritten, err
		}
	}
	if plan.FixFstab {
		current, unavailable, err := tree.readOptional(ctx, rootfsFstabPath, rootfsProvisionReadBufferLimit)
		if err != nil {
			return filesWritten, err
		}
		if !unavailable {
			content, err := rewriteRootfsFstab(current)
			if err != nil {
				return filesWritten, err
			}
			if err := write(rootfsProvisionFile{path: rootfsFstabPath, content: content, mode: 0644}); err != nil {
				return filesWritten, err
			}
		}
	}
	if plan.InjectCloudInit {
		written, err := applyRootfsCloudInitSeed(ctx, tree)
		filesWritten += written
		if err != nil {
			return filesWritten, err
		}
	}
	return filesWritten, nil
}

func detectRootfsAgentInitLayout(
	ctx context.Context,
	tree rootfsGuestTree,
) (rootfsAgentInitLayout, error) {
	systemdBinary, err := tree.fixedPathHasType(ctx, rootfsAgentSystemdEvidencePath, unix.S_IFREG)
	if err != nil {
		return rootfsAgentInitUnknown, err
	}
	systemdConfig, err := tree.fixedPathHasType(ctx, rootfsAgentSystemdConfigPath, unix.S_IFDIR)
	if err != nil {
		return rootfsAgentInitUnknown, err
	}
	openRCInit, err := tree.fixedPathHasType(ctx, rootfsAgentOpenRCInitPath, unix.S_IFDIR)
	if err != nil {
		return rootfsAgentInitUnknown, err
	}
	openRCRunlevel, err := tree.fixedPathHasType(ctx, rootfsAgentOpenRCRunlevelPath, unix.S_IFDIR)
	if err != nil {
		return rootfsAgentInitUnknown, err
	}
	// Missing required directories rule out OpenRC without probing a usrmerged
	// /sbin alias. Complete candidates still require the confined binary probe.
	openRCBinary := false
	if openRCInit && openRCRunlevel {
		openRCBinary, err = tree.fixedPathHasType(ctx, rootfsAgentOpenRCEvidencePath, unix.S_IFREG)
		if err != nil {
			return rootfsAgentInitUnknown, err
		}
	}
	systemd := systemdBinary && systemdConfig
	openRC := openRCBinary && openRCInit && openRCRunlevel
	if systemd == openRC {
		return rootfsAgentInitUnknown, rootfsAtomicError(
			"managed guest agent init layout is missing or ambiguous",
			nil,
		)
	}
	if systemd {
		return rootfsAgentInitSystemd, nil
	}
	return rootfsAgentInitOpenRC, nil
}

func validRootfsAgentEnablementLink(path string, target string) bool {
	return path == rootfsAgentSystemdWantsLink && target == rootfsAgentSystemdWantsTarget ||
		path == rootfsAgentOpenRCRunlevelLink && target == rootfsAgentOpenRCRunlevelTarget
}

func rootfsSSHDConfig(user string) []byte {
	content := "PubkeyAuthentication yes\n" +
		"AuthorizedKeysFile .ssh/authorized_keys\n" +
		"PasswordAuthentication no\n" +
		"PermitEmptyPasswords no\n" +
		"UsePAM yes\n" +
		"UseDNS no\n" +
		"GSSAPIAuthentication no\n"
	if user == "root" {
		return []byte(content + "PermitRootLogin prohibit-password\n")
	}
	return []byte(content + "AllowUsers " + user + "\n")
}

func rootfsAgentSystemdService(port int) []byte {
	return fmt.Appendf(nil, `[Unit]
Description=MVM Agent
DefaultDependencies=no

[Service]
Type=simple
ExecStart=/usr/bin/mvm-agent -port %d -token-file %s
Restart=always
RestartSec=2

[Install]
WantedBy=sysinit.target
`, port, rootfsAgentTokenPath)
}

func rootfsAgentOpenRCService(port int) []byte {
	return fmt.Appendf(nil, `#!/sbin/openrc-run

description="MVM Agent"
command=/usr/bin/mvm-agent
command_args="-port %d -token-file %s"
pidfile=/run/mvm-agent.pid
command_background=true

depend() {
    need localmount
}
`, port, rootfsAgentTokenPath)
}
