package loopmount

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

func finishImageConversion(ctx context.Context, lease *rootfsLease, transaction *imageConversionTransaction) (
	outcome imageOptimizeEffectOutcome, returnErr error,
) {
	outcome = imageOptimizeEffectOutcome{state: imageOptimizeEffectStateUnchanged,
		filesystem: FilesystemExt4, lease: completedImageShrinkCleanup{}}
	record := transaction.record
	current, err := inspectImageConversionFile(ctx, lease.rootfsFD, transaction.caller.UID, transaction.caller.GID, maximumImageBytes)
	if err != nil {
		return outcome, err
	}
	if sameImageConversionInode(current, record.Original) {
		if record.Phase != imageConversionReady || current != record.Original || transaction.workspaceFD < 0 {
			return outcome, rootfsAtomicError("conversion publication original no longer matches prepared authority", nil)
		}
		if transaction.candidateFD < 0 {
			transaction.candidateFD, err = unix.Openat2(transaction.workspaceFD, "candidate.img", &unix.OpenHow{
				Flags: rootfsReadWriteOpenFlags, Resolve: rootfsDescendantResolveFlags,
			})
			if err != nil {
				return outcome, rootfsAtomicError("open prepared conversion candidate", err)
			}
		}
		var candidateStat unix.Stat_t
		if err := unix.Fstat(transaction.candidateFD, &candidateStat); err != nil {
			return outcome, rootfsAtomicError("inspect prepared conversion candidate", err)
		}
		if candidateStat.Uid != 0 && candidateStat.Uid != transaction.caller.UID ||
			candidateStat.Uid == 0 && candidateStat.Gid != 0 ||
			candidateStat.Uid == transaction.caller.UID && candidateStat.Gid != transaction.caller.GID {
			return outcome, rootfsAtomicError("prepared conversion candidate ownership changed", nil)
		}
		candidate, err := inspectImageConversionFile(ctx, transaction.candidateFD, candidateStat.Uid, candidateStat.Gid, maximumImageBytes)
		if err != nil || !sameImageConversionInode(candidate, record.Candidate) || candidate.Size != record.Candidate.Size {
			return outcome, rootfsAtomicError("prepared conversion candidate identity changed", err)
		}
		digest, err := imageConversionDigest(ctx, transaction.candidateFD, candidate.Size)
		if err != nil || digest != record.ResultDigest {
			return outcome, rootfsAtomicError("prepared conversion candidate bytes changed", err)
		}
		if err := unix.Fchown(transaction.candidateFD, int(transaction.caller.UID), int(transaction.caller.GID)); err != nil {
			return outcome, rootfsAtomicError("set conversion result caller ownership", err)
		}
		if err := unix.Fsync(transaction.candidateFD); err != nil {
			return outcome, rootfsCleanupError("sync conversion result ownership", err)
		}
		if err := revalidateImageConversionLeaf(ctx, lease.stagingFD, "rootfs.img", record.Original, true); err != nil {
			return outcome, err
		}
		if err := revalidateImageConversionLeaf(ctx, transaction.workspaceFD, "candidate.img", record.Candidate, false); err != nil {
			return outcome, err
		}
		// CRITICAL: Exchange never unlinks the old inode. Any uncertain outcome retains both
		// the immutable snapshot and the displaced leaf for exact-inode replay; no path fallback exists.
		outcome.state = imageOptimizeEffectStateOutcomeUnknown
		if err := unix.Renameat2(transaction.workspaceFD, "candidate.img", lease.stagingFD, "rootfs.img", unix.RENAME_EXCHANGE); err != nil {
			return outcome, rootfsAtomicError("exchange exact conversion candidate and original", err)
		}
		outcome.state = imageOptimizeEffectStateImageReplacedDurabilityUncertain
		if err := revalidateImageConversionLeaf(ctx, transaction.workspaceFD, "candidate.img", record.Original, false); err != nil {
			outcome.state = imageOptimizeEffectStateOutcomeUnknown
			return outcome, err
		}
	} else if sameImageConversionInode(current, record.Candidate) && current.Size == record.Candidate.Size {
		outcome.state = imageOptimizeEffectStateImageReplacedDurabilityUncertain
	} else {
		return outcome, rootfsAtomicError("conversion replay rootfs is neither the admitted original nor its candidate", nil)
	}
	resultFD, err := unix.Openat2(lease.stagingFD, "rootfs.img", &unix.OpenHow{
		Flags: rootfsReadWriteOpenFlags, Resolve: rootfsDescendantResolveFlags,
	})
	if err != nil {
		return outcome, rootfsAtomicError("pin published conversion result", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close published conversion result", unix.Close(resultFD))
	}()
	result, err := inspectImageConversionFile(ctx, resultFD, transaction.caller.UID, transaction.caller.GID, maximumImageBytes)
	if err != nil || !sameImageConversionInode(result, record.Candidate) || result.Size != record.Candidate.Size {
		return outcome, rootfsAtomicError("published conversion result identity changed", err)
	}
	digest, err := imageConversionDigest(ctx, resultFD, result.Size)
	if err != nil || digest != record.ResultDigest {
		return outcome, rootfsAtomicError("published conversion result digest changed", err)
	}
	after, err := inspectImageConversionFile(ctx, resultFD, transaction.caller.UID, transaction.caller.GID, maximumImageBytes)
	if err != nil || after != result {
		return outcome, rootfsAtomicError("published conversion result changed during admission", err)
	}
	if err := unix.Fsync(resultFD); err != nil {
		return outcome, rootfsCleanupError("sync published conversion result", err)
	}
	if transaction.workspaceFD >= 0 {
		if err := unix.Fsync(transaction.workspaceFD); err != nil {
			return outcome, rootfsCleanupError("sync displaced conversion original", err)
		}
	}
	if err := syncImageConversionDirectory(ctx, lease.stagingFD); err != nil {
		return outcome, err
	}
	record.Phase = imageConversionComplete
	if err := transaction.persist(ctx); err != nil {
		return outcome, err
	}
	if err := transaction.cleanupWorkspace(ctx, lease); err != nil {
		return outcome, err
	}
	outcome.state = imageOptimizeEffectStateImageReplaced
	outcome.imageBytes = uint64(record.Candidate.Size)
	return outcome, nil
}

func revalidateImageConversionLeaf(ctx context.Context, parentFD int, name string, expected imageConversionIdentity, timestamps bool) (
	returnErr error,
) {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("conversion publication context ended", err)
	}
	fd, err := unix.Openat2(parentFD, name, &unix.OpenHow{
		Flags: rootfsReadOnlyOpenFlags, Resolve: rootfsDescendantResolveFlags,
	})
	if err != nil {
		return rootfsAtomicError("reopen exact conversion publication leaf", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close conversion publication leaf", unix.Close(fd))
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return rootfsAtomicError("inspect conversion publication leaf", err)
	}
	identity := imageConversionStatIdentity(stat)
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || !sameImageConversionInode(identity, expected) ||
		identity.Size != expected.Size || identity.Mtime != expected.Mtime || timestamps && identity.Ctime != expected.Ctime {
		return rootfsAtomicError("conversion publication leaf changed", nil)
	}
	return nil
}

func (transaction *imageConversionTransaction) cleanupWorkspace(ctx context.Context, lease *rootfsLease) error {
	if transaction.workspaceFD < 0 {
		return nil
	}
	for _, item := range []struct {
		name     string
		identity imageConversionIdentity
	}{{"source.img", transaction.record.Snapshot}, {"candidate.img", transaction.record.Original}} {
		err := revalidateImageConversionLeaf(ctx, transaction.workspaceFD, item.name, item.identity, false)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		if err := unix.Unlinkat(transaction.workspaceFD, item.name, 0); err != nil {
			return rootfsCleanupError("remove exact completed conversion workspace leaf", err)
		}
	}
	if err := unix.Fsync(transaction.workspaceFD); err != nil {
		return rootfsCleanupError("sync completed conversion workspace cleanup", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(lease.stagingFD, imageConversionWorkspace, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return rootfsAtomicError("inspect completed conversion workspace name", err)
	}
	if stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 ||
		!sameImageConversionInode(imageConversionStatIdentity(stat), transaction.record.Workspace) {
		return rootfsAtomicError("completed conversion workspace name changed", nil)
	}
	if err := unix.Unlinkat(lease.stagingFD, imageConversionWorkspace, unix.AT_REMOVEDIR); err != nil {
		return rootfsCleanupError("remove exact completed conversion workspace directory", err)
	}
	return syncImageConversionDirectory(ctx, lease.stagingFD)
}

func syncImageConversionDirectory(ctx context.Context, parentFD int) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("conversion directory sync context ended", err)
	}
	fd, err := unix.Openat2(parentFD, ".", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rootfsDescendantResolveFlags,
	})
	if err != nil {
		return rootfsAtomicError("pin conversion directory for sync", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close conversion sync directory", unix.Close(fd))
	}()
	return rootfsCleanupErrorIfAny("sync conversion directory", unix.Fsync(fd))
}
