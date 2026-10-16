package loopmount

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

const (
	rootfsMkfsExt4Path        = "/usr/sbin/mkfs.ext4"
	imageConversionHeadroom   = 150 << 20
	maxImageConversionEntries = 1 << 20
	maxImageConversionDepth   = 64
)

func convertRootfsImage(ctx context.Context, deps rootfsVMProvisionDeps, lease *rootfsLease) (
	outcome imageOptimizeEffectOutcome, returnErr error,
) {
	outcome = imageOptimizeEffectOutcome{state: imageOptimizeEffectStateUnchanged,
		filesystem: FilesystemExt4, lease: completedImageShrinkCleanup{}}
	transaction := lease.imageConversion
	if transaction == nil || transaction.record == nil {
		return outcome, rootfsAtomicError("image conversion durable authority is missing", nil)
	}
	record := transaction.record
	if record.Phase == imageConversionReady || record.Phase == imageConversionComplete {
		return finishImageConversion(ctx, lease, transaction)
	}
	original, err := inspectImageConversionFile(
		ctx,
		lease.rootfsFD,
		transaction.caller.UID,
		transaction.caller.GID,
		maximumImageBytes,
	)
	if err != nil || original != record.Original {
		return outcome, rootfsAtomicError("image conversion original changed before snapshot", err)
	}
	if err := transaction.openWorkingFiles(ctx); err != nil {
		return outcome, err
	}
	if record.Phase == imageConversionPrepared {
		if err := copyImageConversionSnapshot(ctx, lease.rootfsFD, transaction.snapshotFD, record.Original.Size); err != nil {
			return outcome, err
		}
		after, err := inspectImageConversionFile(
			ctx,
			lease.rootfsFD,
			transaction.caller.UID,
			transaction.caller.GID,
			maximumImageBytes,
		)
		if err != nil || after != record.Original {
			return outcome, rootfsAtomicError("image conversion source changed while snapshotting", err)
		}
		record.Snapshot, err = inspectImageConversionFile(ctx, transaction.snapshotFD, 0, 0, maximumImageBytes)
		if err != nil {
			return outcome, err
		}
		record.SourceDigest, err = imageConversionDigest(ctx, transaction.snapshotFD, record.Snapshot.Size)
		if err != nil {
			return outcome, err
		}
		record.Phase = imageConversionSnapshot
		if err := transaction.persist(ctx); err != nil {
			return outcome, err
		}
	} else {
		digest, err := imageConversionDigest(ctx, transaction.snapshotFD, record.Snapshot.Size)
		if err != nil || digest != record.SourceDigest {
			return outcome, rootfsAtomicError("retained conversion snapshot digest changed", err)
		}
	}
	if err := constructImageConversionCandidate(ctx, deps, transaction); err != nil {
		return outcome, err
	}
	record.Candidate, err = inspectImageConversionFile(ctx, transaction.candidateFD, 0, 0, maximumImageBytes)
	if err != nil {
		return outcome, err
	}
	record.ResultDigest, err = imageConversionDigest(ctx, transaction.candidateFD, record.Candidate.Size)
	if err != nil {
		return outcome, err
	}
	record.Phase = imageConversionReady
	if err := transaction.persist(ctx); err != nil {
		return outcome, err
	}
	return finishImageConversion(ctx, lease, transaction)
}

func (transaction *imageConversionTransaction) openWorkingFiles(ctx context.Context) error {
	for _, item := range []struct {
		name     string
		fd       *int
		identity imageConversionIdentity
	}{{"source.img", &transaction.snapshotFD, transaction.record.Snapshot},
		{"candidate.img", &transaction.candidateFD, transaction.record.Candidate}} {
		if *item.fd < 0 {
			fd, err := unix.Openat2(transaction.workspaceFD, item.name, &unix.OpenHow{
				Flags: rootfsReadWriteOpenFlags, Resolve: rootfsDescendantResolveFlags,
			})
			if err != nil {
				return rootfsAtomicError("open retained conversion backing file", err)
			}
			*item.fd = fd
		}
		identity, err := inspectImageConversionFile(ctx, *item.fd, 0, 0, maximumImageBytes)
		if err != nil || !sameImageConversionInode(identity, item.identity) {
			return rootfsAtomicError("conversion backing inode no longer matches its receipt", err)
		}
		if item.name == "source.img" && transaction.record.Phase == imageConversionSnapshot &&
			identity != item.identity {
			return rootfsAtomicError("immutable conversion snapshot metadata changed", nil)
		}
	}
	return nil
}

func copyImageConversionSnapshot(ctx context.Context, sourceFD, targetFD int, size int64) error {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("conversion snapshot context ended", err)
	}
	if err := unix.Ftruncate(targetFD, 0); err != nil {
		return rootfsAtomicError("reset exact unpublished conversion snapshot", err)
	}
	if err := unix.IoctlFileClone(targetFD, sourceFD); err == nil {
		return rootfsCleanupErrorIfAny("sync conversion reflink snapshot", unix.Fsync(targetFD))
	} else if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EXDEV) &&
		!errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINVAL) {
		return rootfsAtomicError("clone conversion snapshot", err)
	}
	buffer := make([]byte, 1<<20)
	zeroes := make([]byte, len(buffer))
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return rootfsAtomicError("conversion snapshot context ended", err)
		}
		chunk := buffer[:min(int64(len(buffer)), size-offset)]
		if n, err := unix.Pread(sourceFD, chunk, offset); err != nil || n != len(chunk) {
			return rootfsAtomicError("read conversion snapshot chunk", errors.Join(err, io.ErrUnexpectedEOF))
		}
		if !bytes.Equal(chunk, zeroes[:len(chunk)]) {
			if n, err := unix.Pwrite(targetFD, chunk, offset); err != nil || n != len(chunk) {
				return rootfsAtomicError("write conversion snapshot chunk", errors.Join(err, io.ErrShortWrite))
			}
		}
		offset += int64(len(chunk))
	}
	if err := unix.Ftruncate(targetFD, size); err != nil {
		return rootfsAtomicError("set bounded conversion snapshot size", err)
	}
	return rootfsCleanupErrorIfAny("sync conversion snapshot", unix.Fsync(targetFD))
}

func constructImageConversionCandidate(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	transaction *imageConversionTransaction,
) (
	returnErr error,
) {
	if err := deps.lockOSThread(ctx); err != nil {
		return rootfsAtomicError("lock image conversion OS thread", err)
	}
	if err := ctx.Err(); err != nil {
		return appendRootfsCleanupError(
			rootfsAtomicError("conversion context ended", err),
			"unlock image conversion thread",
			deps.unlockOSThread(context.WithoutCancel(ctx)),
		)
	}
	if err := deps.unshare(ctx, unix.CLONE_NEWNS); err != nil {
		return appendRootfsCleanupError(
			rootfsAtomicError("create conversion mount namespace", err),
			"unlock image conversion thread",
			deps.unlockOSThread(context.WithoutCancel(ctx)),
		)
	}
	// CRITICAL: The namespace is permanently confined to this receiver's locked thread.
	// Source bytes are an immutable root-owned snapshot; no caller-writable image is mounted.
	cleanupCtx := context.WithoutCancel(ctx)
	var sourceLoop, targetLoop *rootfsLoopDevice
	var partitions *rootfsPartitionSet
	var source *rootfsSelectedSource
	var mountpoint *rootfsInspectMountpoint
	mounted := false
	rootFD := -1
	defer func() {
		if rootFD >= 0 {
			returnErr = appendRootfsCleanupError(
				returnErr,
				"close conversion mounted root",
				deps.close(cleanupCtx, rootFD),
			)
		}
		if mounted {
			returnErr = appendRootfsCleanupError(
				returnErr,
				"unmount conversion snapshot",
				deps.unmount(cleanupCtx, rootfsInspectMountPath, 0),
			)
		}
		returnErr = appendRootfsCleanupError(
			returnErr,
			"release conversion block devices",
			errors.Join(
				source.Release(cleanupCtx),
				partitions.Release(cleanupCtx),
				targetLoop.Release(cleanupCtx),
				sourceLoop.Release(cleanupCtx),
			),
		)
		if mountpoint != nil {
			returnErr = appendRootfsCleanupError(returnErr, "close conversion mountpoint",
				errors.Join(deps.close(cleanupCtx, mountpoint.rootFD), deps.close(cleanupCtx, mountpoint.parentFD)))
		}
	}()
	if err := deps.mount(ctx, "", "/", "", rootfsInspectPrivateFlags, ""); err != nil {
		return rootfsAtomicError("make conversion mounts private", err)
	}
	var err error
	mountpoint, err = prepareRootfsInspectMountpoint(ctx, deps.inspectDeps())
	if err != nil {
		return err
	}
	sourceLoop, err = deps.attachLoop(ctx, &rootfsLease{rootfsFD: transaction.snapshotFD, access: rootfsReadOnly})
	if err != nil {
		return err
	}
	partitions, err = deps.discoverPartitions(ctx, sourceLoop)
	if err != nil {
		return err
	}
	source, err = deps.probeFilesystem(ctx, FilesystemBtrfs, sourceLoop, partitions)
	if err != nil {
		return err
	}
	sourcePath, err := activeRootfsInspectSource(ctx, deps.inspectDeps(), source, FilesystemBtrfs)
	if err != nil {
		return err
	}
	if err := revalidateRootfsInspectMountpoint(ctx, deps.inspectDeps(), mountpoint); err != nil {
		return err
	}
	if err := deps.mount(ctx, sourcePath, rootfsInspectMountPath, string(FilesystemBtrfs), rootfsInspectMountFlags, "nologreplay"); err != nil {
		return rootfsAtomicError("mount immutable conversion snapshot", err)
	}
	mounted = true
	rootFD, err = pinRootfsInspectMountedRoot(ctx, deps.inspectDeps(), mountpoint, source, FilesystemBtrfs)
	if err != nil {
		return err
	}
	capacity, entries, err := imageConversionCapacity(ctx, rootFD)
	if err != nil {
		return err
	}
	// A retry reconstructs only this unpublished root-owned candidate inode from the same snapshot.
	if err := unix.Ftruncate(transaction.candidateFD, 0); err != nil {
		return rootfsAtomicError("reset unpublished conversion candidate", err)
	}
	if err := unix.Ftruncate(transaction.candidateFD, int64(capacity)); err != nil {
		return rootfsAtomicError("allocate bounded conversion candidate", err)
	}
	targetLoop, err = deps.attachLoop(ctx, &rootfsLease{rootfsFD: transaction.candidateFD, access: rootfsReadWrite})
	if err != nil {
		return err
	}
	targetLoop.state.mu.Lock()
	targetPath, pathErr := rootfsLoopDevicePath(targetLoop.state.index)
	var targetStat unix.Stat_t
	statErr := deps.fstat(ctx, targetLoop.state.fd, &targetStat)
	_, identityErr := validateRootfsLoopDevice(targetLoop.state.index, targetStat)
	targetLoop.state.mu.Unlock()
	if err := errors.Join(pathErr, statErr, identityErr); err != nil {
		return rootfsAtomicError("conversion target loop authority changed", err)
	}
	// Fixed host tools read the private mounted guest tree as data. No guest binary is executed.
	args := []string{rootfsMkfsExt4Path, "-F", "-L", "rootfs", "-N", strconv.FormatUint(entries+1024, 10),
		"-d", rootfsInspectMountPath, targetPath}
	if err := runImageConversionTool(ctx, deps, args, false); err != nil {
		return err
	}
	if err := runImageConversionTool(ctx, deps, []string{rootfsE2fsckPath, "-f", "-p", targetPath}, true); err != nil {
		return err
	}
	if err := targetLoop.Release(cleanupCtx); err != nil {
		return err
	}
	return rootfsCleanupErrorIfAny("sync completed conversion candidate", unix.Fsync(transaction.candidateFD))
}

func runImageConversionTool(ctx context.Context, deps rootfsVMProvisionDeps, args []string, fsck bool) error {
	result, err := deps.run(ctx, args, system.RunCmdOpts{
		Capture: true, Check: false, Cwd: "/", Timeout: rootfsGrowToolTimeout,
		Env: map[string]string{"LANG": "C", "LC_ALL": "C"}, ReplaceEnv: true,
		MaxStdoutBytes: maxRootfsGrowToolStdoutBytes, MaxStderrBytes: maxRootfsGrowToolStderrBytes,
	})
	if err != nil || result == nil ||
		result.ExitCode != 0 && !(fsck && (result.ExitCode == 1 || result.ExitCode == 2)) {
		return rootfsAtomicError("fixed image conversion tool did not complete successfully", err)
	}
	return nil
}

func imageConversionCapacity(ctx context.Context, rootFD int) (uint64, uint64, error) {
	var bytes, entries uint64
	seen := make(map[uint64]struct{})
	var walk func(int, int) error
	walk = func(parentFD, depth int) (returnErr error) {
		if depth > maxImageConversionDepth {
			return rootfsAtomicError("conversion tree exceeds directory depth bound", nil)
		}
		fd, err := unix.Openat2(parentFD, ".", &unix.OpenHow{
			Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rootfsDescendantResolveFlags,
		})
		if err != nil {
			return rootfsAtomicError("open conversion tree directory", err)
		}
		defer func() {
			returnErr = appendRootfsCleanupError(returnErr, "close conversion tree directory", unix.Close(fd))
		}()
		buffer := make([]byte, 32<<10)
		for {
			if err := ctx.Err(); err != nil {
				return rootfsAtomicError("conversion tree context ended", err)
			}
			n, err := unix.ReadDirent(fd, buffer)
			if err != nil {
				return rootfsAtomicError("read bounded conversion tree directory", err)
			}
			if n == 0 {
				return nil
			}
			_, _, names := unix.ParseDirent(buffer[:n], -1, nil)
			for _, name := range names {
				entries++
				if entries > maxImageConversionEntries {
					return rootfsAtomicError("conversion tree exceeds entry bound", nil)
				}
				child, err := unix.Openat2(fd, name, &unix.OpenHow{
					Flags: unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rootfsDescendantResolveFlags,
				})
				if err != nil {
					return rootfsAtomicError("pin conversion tree entry", err)
				}
				var stat unix.Stat_t
				statErr := unix.Fstat(child, &stat)
				if statErr == nil && stat.Mode&unix.S_IFMT == unix.S_IFDIR {
					statErr = walk(child, depth+1)
				}
				if err := errors.Join(statErr, unix.Close(child)); err != nil {
					return rootfsAtomicError("inspect conversion tree entry", err)
				}
				if _, exists := seen[stat.Ino]; exists {
					continue
				}
				seen[stat.Ino] = struct{}{}
				if stat.Size < 0 || uint64(stat.Size) > maximumImageBytes-bytes {
					return rootfsAtomicError("conversion tree exceeds admitted image byte bound", nil)
				}
				bytes += uint64(stat.Size)
			}
		}
	}
	if err := walk(rootFD, 0); err != nil {
		return 0, 0, err
	}
	// Preserve the existing converter's 150 MiB headroom, with inode-table space derived
	// from the admitted tree. This is output construction, not a new storage quota.
	metadataBytes := (entries + 1024) * 512
	if bytes > maximumImageBytes-imageConversionHeadroom-metadataBytes-(1<<20) {
		return 0, 0, rootfsAtomicError("conversion candidate exceeds admitted image byte bound", nil)
	}
	capacity := ((bytes + imageConversionHeadroom + metadataBytes + (1 << 20) - 1) >> 20) << 20
	return capacity, entries, nil
}

func rootfsCleanupErrorIfAny(message string, err error) error {
	if err == nil {
		return nil
	}
	return rootfsCleanupError(message, err)
}
