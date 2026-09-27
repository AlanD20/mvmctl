package jailer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type jailCleanupDirectory uint8

const (
	jailCleanupVM jailCleanupDirectory = iota + 1
	jailCleanupRoot
	jailCleanupRun
	jailCleanupMVM
	jailCleanupRuntime
	jailCleanupDevices
	jailCleanupDeviceNet
	jailCleanupVolumes
	jailCleanupSnapshot
)

// removeStoppedJailTree runs only after the retained process authorities have proved exit.
// It opens only receiver-derived names and never traverses an unexpected directory or mount.
func removeStoppedJailTree(ctx context.Context, record instanceRecord) (returnErr error) {
	if record.lifecycle != instanceLifecycleCleaning || record.launch == nil || !vmIDPattern.MatchString(record.vmID) {
		return instanceStateError(record.vmID, "cleaning authority is required to remove the stopped jail tree")
	}
	deps := realJailDestinationDeps()
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if err := inspectTrustedJailAncestor(ctx, deps, fd, "/"); err != nil {
		return err
	}
	for _, name := range []string{"var", "lib", "mvmctl", "jailer", "firecracker"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := unix.Openat2(fd, name, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return err
		}
		var inspectErr error
		if name == "var" || name == "lib" {
			inspectErr = inspectTrustedJailAncestor(ctx, deps, next, name)
		} else {
			inspectErr = inspectExactJailAncestor(ctx, deps, next, name)
		}
		if inspectErr != nil {
			return errors.Join(inspectErr, unix.Close(next))
		}
		closeErr := unix.Close(fd)
		fd = next
		if closeErr != nil {
			return closeErr
		}
	}
	return removeJailDirectory(ctx, fd, record.vmID, jailCleanupVM, record)
}

func removeJailDirectory(
	ctx context.Context,
	parent int,
	name string,
	kind jailCleanupDirectory,
	record instanceRecord,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return processAuthorityError(record.vmID, "open stopped jail directory "+name, err)
	}
	file := os.NewFile(uintptr(fd), "stopped-jail-directory")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var original unix.Stat_t
	if err := unix.Fstat(fd, &original); err != nil {
		return err
	}
	if !stoppedJailDirectoryAdmitted(kind, original, record) {
		return processAuthorityError(record.vmID, "stopped jail directory metadata is not admitted", nil)
	}
	if kind == jailCleanupRoot && record.active != nil &&
		(uint64(original.Dev) != record.active.jailRoot.device || original.Ino != record.active.jailRoot.inode) {
		return processAuthorityError(record.vmID, "stopped jail root differs from committed identity", nil)
	}
	if kind == jailCleanupRoot {
		if err := removeStoppedSnapshotProducerDirectories(ctx, fd, record); err != nil {
			return err
		}
		if err := archiveStoppedVMLaunchDevices(ctx, record, fd); err != nil {
			return err
		}
		if err := removeStoppedVolumeHotplugJournal(ctx, fd, record); err != nil {
			return err
		}
	}
	entryLimit := 64
	if kind == jailCleanupVolumes {
		entryLimit = managedMMIOMaxInterruptSlots + 1
	}
	names, err := file.Readdirnames(entryLimit)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) >= entryLimit {
		return processAuthorityError(record.vmID, "stopped jail directory has unexpected entries", nil)
	}
	for _, child := range names {
		childKind, leaf, err := allowedJailCleanupEntry(kind, child)
		if err != nil {
			return processAuthorityError(record.vmID, "stopped jail tree contains an unexpected entry", err)
		}
		if leaf {
			if err := removeJailFixedLeaf(ctx, fd, child, kind, record); err != nil {
				return err
			}
		} else if err := removeJailDirectory(ctx, fd, child, childKind, record); err != nil {
			return err
		}
	}
	var current unix.Stat_t
	if err := unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT); err != nil {
		return err
	}
	if current.Dev != original.Dev || current.Ino != original.Ino || current.Mode != original.Mode ||
		current.Uid != original.Uid || current.Gid != original.Gid {
		return processAuthorityError(record.vmID, "stopped jail directory changed before removal", nil)
	}
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(parent)
}

func stoppedJailDirectoryAdmitted(kind jailCleanupDirectory, stat unix.Stat_t, record instanceRecord) bool {
	rootOwned := stat.Uid == 0 && stat.Gid == 0
	isolatedOwned := record.launch != nil && stat.Uid == record.launch.executionIdentity.uid &&
		stat.Gid == record.launch.executionIdentity.gid
	mode := uint32(0755)
	if kind == jailCleanupVM || kind == jailCleanupRuntime {
		mode = 0700
	}
	if stat.Mode == unix.S_IFDIR|mode && rootOwned {
		return true
	}
	// Jailer 1.16.1 owns these private directories at 0700 before the
	// controller seals the jail root. The VM root itself may retain that
	// shape only when no active identity was committed.
	if stat.Mode != unix.S_IFDIR|0700 || !isolatedOwned {
		return false
	}
	switch kind {
	case jailCleanupRoot:
		return record.active == nil
	case jailCleanupRun, jailCleanupDevices, jailCleanupDeviceNet:
		return true
	default:
		return false
	}
}

func allowedJailCleanupEntry(kind jailCleanupDirectory, name string) (jailCleanupDirectory, bool, error) {
	switch kind {
	case jailCleanupVM:
		if name == "root" {
			return jailCleanupRoot, false, nil
		}
	case jailCleanupRoot:
		switch name {
		case "snapshot":
			return jailCleanupSnapshot, false, nil
		case "vmm-runtime":
			return jailCleanupRuntime, false, nil
		case "run":
			return jailCleanupRun, false, nil
		case "dev":
			return jailCleanupDevices, false, nil
		case "volumes":
			return jailCleanupVolumes, false, nil
		case "firecracker", "rootfs", "kernel", infra.VMFirecrackerPIDFilename,
			infra.VMCloudInitISOFilename, launchDeviceAuthorityLeaf:
			return 0, true, nil
		}
	case jailCleanupRun:
		if name == "mvm" {
			return jailCleanupMVM, false, nil
		}
	case jailCleanupMVM:
		switch name {
		case "runtime":
			return jailCleanupRuntime, false, nil
		case infra.VMFirecrackerConfigFilename, infra.VMFirecrackerLogFilename, infra.VMFirecrackerMetricsFilename:
			return 0, true, nil
		}
	case jailCleanupDevices:
		switch name {
		case "net":
			return jailCleanupDeviceNet, false, nil
		case "kvm", "urandom", "userfaultfd":
			return 0, true, nil
		}
	case jailCleanupDeviceNet:
		if name == "tun" {
			return 0, true, nil
		}
	case jailCleanupVolumes:
		if strings.HasSuffix(name, ".raw") && isCanonicalManagedResourceID(strings.TrimSuffix(name, ".raw"), 64) {
			return 0, true, nil
		}
	case jailCleanupSnapshot:
		if name == "vmstate" || name == "memory" {
			return 0, true, nil
		}
	}
	return 0, false, fmt.Errorf("unexpected fixed jail entry %q", name)
}

func removeJailFixedLeaf(
	ctx context.Context,
	parent int,
	name string,
	kind jailCleanupDirectory,
	record instanceRecord,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var original unix.Stat_t
	if err := unix.Fstat(fd, &original); err != nil {
		return err
	}
	ownerAllowed := original.Uid == 0 && original.Gid == 0 ||
		original.Uid == record.launch.executionIdentity.uid && original.Gid == record.launch.executionIdentity.gid
	if !ownerAllowed || original.Nlink != 1 || original.Mode&0022 != 0 {
		return processAuthorityError(record.vmID, "stopped jail leaf metadata is not admitted", nil)
	}
	// A failed publication may leave the exclusive root-owned staging mode. Only this
	// fixed bounded metadata leaf, never an arbitrary caller file, is removed after exit.
	if kind == jailCleanupRoot && name == launchDeviceAuthorityLeaf &&
		(original.Uid != 0 || original.Gid != 0 || original.Size < 0 ||
			original.Size > int64(launchDeviceAuthorityMaxBytes) ||
			(original.Mode != unix.S_IFREG|0400 && original.Mode != unix.S_IFREG|0600)) {
		return processAuthorityError(record.vmID, "stopped launch device authority metadata is not admitted", nil)
	}
	// Jailer's fixed PID leaf is display-only. Never read it as process authority.
	if kind == jailCleanupRoot && name == infra.VMFirecrackerPIDFilename &&
		(original.Mode != unix.S_IFREG|0600 || original.Uid != 0 || original.Gid != 0 ||
			original.Size < 0 || original.Size > 20) {
		return processAuthorityError(record.vmID, "stopped Jailer PID leaf metadata is not admitted", nil)
	}
	// Volume sources lived only in the departed private mount namespace. Cleanup may remove
	// only the empty root-owned attachment placeholders, never caller data or a surviving mount.
	if (kind == jailCleanupVolumes || kind == jailCleanupSnapshot) && (original.Uid != 0 || original.Gid != 0 ||
		original.Mode != unix.S_IFREG|0600 || original.Size != 0) {
		return processAuthorityError(record.vmID, "stopped jail volume is not an empty mount placeholder", nil)
	}
	if kind == jailCleanupDevices || kind == jailCleanupDeviceNet {
		major, minor := uint32(10), uint32(232)
		if name == "tun" {
			minor = 200
		}
		if name == "userfaultfd" {
			minor = 257
		}
		if name == "urandom" {
			major, minor = 1, 9
		}
		if original.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(uint64(original.Rdev)) != major ||
			unix.Minor(uint64(original.Rdev)) != minor {
			return processAuthorityError(record.vmID, "stopped jail device is not admitted", nil)
		}
	} else if original.Mode&unix.S_IFMT != unix.S_IFREG {
		return processAuthorityError(record.vmID, "stopped jail leaf is not a regular file", nil)
	}
	if kind == jailCleanupRoot && name == "firecracker" && record.active != nil &&
		(uint64(original.Dev) != record.active.executable.device || original.Ino != record.active.executable.inode) {
		return processAuthorityError(record.vmID, "stopped jail executable differs from committed identity", nil)
	}
	var current unix.Stat_t
	if err := unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT); err != nil {
		return err
	}
	if current.Dev != original.Dev || current.Ino != original.Ino || current.Mode != original.Mode ||
		current.Uid != original.Uid || current.Gid != original.Gid || current.Nlink != original.Nlink {
		return processAuthorityError(record.vmID, "stopped jail leaf changed before removal", nil)
	}
	return unix.Unlinkat(parent, name, 0)
}
