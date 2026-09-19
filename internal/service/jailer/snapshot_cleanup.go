package jailer

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// CALLER MUST HOLD: exact cleanup authority after verified Firecracker process absence.
// Interrupted output stays private until this point; no uncertain capture is resumed or adopted.
func removeStoppedSnapshotProducerDirectories(
	ctx context.Context,
	runtimeFD int,
	record instanceRecord,
) (returnErr error) {
	fd, err := unix.Openat2(runtimeFD, ".", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "stopped-snapshot-producers")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, readErr := file.Readdirnames(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, name := range names {
			if !strings.HasPrefix(name, "capture-") {
				continue
			}
			if !isCanonicalManagedResourceID(strings.TrimPrefix(name, "capture-"), 64) {
				return instanceValidationError("stopped capture directory name is not canonical")
			}
			if err := removeStoppedSnapshotProducer(ctx, runtimeFD, name, record); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return unix.Fsync(runtimeFD)
		}
	}
}

func removeStoppedSnapshotProducer(
	ctx context.Context,
	runtimeFD int,
	name string,
	record instanceRecord,
) (returnErr error) {
	if record.lifecycle != instanceLifecycleCleaning || record.active == nil {
		return instanceStateError(record.vmID, "snapshot producer removal requires stopped active identity")
	}
	fd, err := unix.Openat2(runtimeFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "stopped-capture")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var original unix.Stat_t
	if err := unix.Fstat(fd, &original); err != nil {
		return err
	}
	rootStaging := original.Uid == 0 && original.Gid == 0
	if original.Mode != unix.S_IFDIR|0700 ||
		(!rootStaging && (original.Uid != record.active.userIDs.effective || original.Gid != record.active.groupIDs.effective)) {
		return instanceValidationError("stopped capture directory ownership differs from producer")
	}
	names, err := file.Readdirnames(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 2 {
		return instanceValidationError("stopped capture contains unexpected outputs")
	}
	if rootStaging && len(names) != 0 {
		return instanceValidationError("ungranted snapshot producer staging directory is not empty")
	}
	for _, leaf := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if leaf != "memory" && leaf != "vmstate" {
			return instanceValidationError("stopped capture leaf is outside fixed output policy")
		}
		artifact, err := unix.Openat2(fd, leaf, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: managedVMResourceResolveFlags,
		})
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		statErr := unix.Fstat(artifact, &stat)
		closeErr := unix.Close(artifact)
		if err := errors.Join(statErr, closeErr); err != nil {
			return err
		}
		if stat.Mode != unix.S_IFREG|0600 || stat.Uid != original.Uid || stat.Gid != original.Gid || stat.Nlink != 1 {
			return instanceValidationError("stopped capture artifact metadata differs from producer")
		}
		if err := unix.Unlinkat(fd, leaf, 0); err != nil {
			return err
		}
	}
	var current unix.Stat_t
	if err := unix.Fstatat(runtimeFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if original.Dev != current.Dev || original.Ino != current.Ino {
		return instanceValidationError("stopped capture directory identity changed")
	}
	return unix.Unlinkat(runtimeFD, name, unix.AT_REMOVEDIR)
}
