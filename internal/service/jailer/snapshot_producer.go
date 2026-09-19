package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// The root-only ancestor of the execution runtime excludes operator writers. The temporary
// directory is private to the already-admitted producer, never used as immutable restore backing.
func prepareSnapshotProducerDirectory(
	ctx context.Context,
	session *snapshotAPISession,
	snapshotID string,
) (_ int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	name := "capture-" + snapshotID
	if err := unix.Mkdirat(session.directoryFD, name, 0700); err != nil {
		return -1, err
	}
	fd, err := unix.Openat2(session.directoryFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return -1, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	if err := unix.Fchown(
		fd,
		int(session.record.active.userIDs.effective),
		int(session.record.active.groupIDs.effective),
	); err != nil {
		return -1, err
	}
	if err := unix.Fchmod(fd, 0700); err != nil {
		return -1, err
	}
	if err := unix.Fsync(session.directoryFD); err != nil {
		return -1, err
	}
	return fd, nil
}

func copySnapshotProducerArtifact(
	ctx context.Context,
	entry *snapshotStoreEntry,
	producerFD int,
	leaf string,
	record instanceRecord,
	memoryBytes uint64,
) (_ snapshotArtifact, returnErr error) {
	if leaf != "memory" && leaf != "vmstate" {
		return snapshotArtifact{}, instanceValidationError("snapshot producer leaf is unsupported")
	}
	fd, err := unix.Openat2(producerFD, leaf, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return snapshotArtifact{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	before, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil {
		return snapshotArtifact{}, err
	}
	if before.uid != record.active.userIDs.effective || before.gid != record.active.groupIDs.effective ||
		before.mode != unix.S_IFREG|0600 || before.nlink != 1 {
		return snapshotArtifact{}, instanceValidationError("snapshot output was not written by the admitted producer")
	}
	if leaf == "memory" && before.size != memoryBytes {
		return snapshotArtifact{}, instanceValidationError("snapshot memory differs from trusted captured RAM extent")
	}
	if leaf == "vmstate" && (before.size == 0 || before.size > managedSnapshotVMStateMaxBytes) {
		return snapshotArtifact{}, instanceValidationError(
			"complete snapshot vmstate exceeds the approved loader ceiling",
		)
	}
	artifact, err := entry.copyArtifact(ctx, leaf, fd, before.size)
	if err != nil {
		return snapshotArtifact{}, err
	}
	after, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil {
		return snapshotArtifact{}, err
	}
	if before != after {
		return snapshotArtifact{}, instanceValidationError("snapshot producer output changed during protected copy")
	}
	return artifact, nil
}

func removeSnapshotProducerDirectory(
	ctx context.Context,
	session *snapshotAPISession,
	producerFD int,
	snapshotID string,
) error {
	for _, leaf := range []string{"memory", "vmstate"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(producerFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode != unix.S_IFREG|0600 || stat.Uid != session.record.active.userIDs.effective ||
			stat.Gid != session.record.active.groupIDs.effective || stat.Nlink != 1 {
			return instanceValidationError("snapshot temporary producer artifact identity changed")
		}
		if err := unix.Unlinkat(producerFD, leaf, 0); err != nil {
			return err
		}
	}
	var pinned, current unix.Stat_t
	if err := unix.Fstat(producerFD, &pinned); err != nil {
		return err
	}
	name := "capture-" + snapshotID
	if err := unix.Fstatat(session.directoryFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if pinned.Dev != current.Dev || pinned.Ino != current.Ino {
		return instanceValidationError("snapshot temporary directory identity changed")
	}
	if err := unix.Unlinkat(session.directoryFD, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(session.directoryFD)
}
