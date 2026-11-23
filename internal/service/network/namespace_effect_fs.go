package network

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	namespaceFilesystemRoot    = "/"
	namespaceThreadNetworkPath = "/proc/thread-self/ns/net"
	namespaceProcFDPathPrefix  = "/proc/self/fd/"
	namespacePlaceholderMode   = uint32(0o600)

	namespaceDirectoryOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	namespaceLeafInspectFlags   = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	namespaceCurrentOpenFlags   = unix.O_RDONLY | unix.O_CLOEXEC
	namespaceTargetStatxFlags   = unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
		unix.AT_STATX_SYNC_AS_STAT
	namespaceTargetStatxMask = unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE

	namespaceResolveBeneath = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	namespaceResolveManaged = namespaceResolveBeneath | unix.RESOLVE_NO_XDEV
)

type namespaceEffectPolicy struct {
	rootPath    string
	expectedUID uint32
	expectedGID uint32
}

func productionNamespaceEffectPolicy() namespaceEffectPolicy {
	return namespaceEffectPolicy{rootPath: namespaceFilesystemRoot, expectedUID: 0, expectedGID: 0}
}

func openExactNamespaceTarget(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
	key namespaceKey,
	target namespaceTargetIdentity,
) (_ int, _ int, returnErr error) {
	parentFD := -1
	placeholderFD := -1
	rootFD, err := deps.open(ctx, policy.rootPath, namespaceDirectoryOpenFlags, 0)
	if err != nil {
		return parentFD, placeholderFD, fmt.Errorf("open namespace filesystem root: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeConsumedNamespaceFD(context.WithoutCancel(ctx), deps, &rootFD))
	}()
	if err := verifyNamespaceDirectory(ctx, deps, rootFD, policy, false, namespaceTargetIdentity{}); err != nil {
		return parentFD, placeholderFD, fmt.Errorf("verify namespace filesystem root: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return parentFD, placeholderFD, err
	}
	parentName := "run/mvmctl/authority/" + strconv.FormatUint(uint64(key.ownerUID), 10) + "/netns"
	parentFD, err = deps.openAt2(ctx, rootFD, parentName, &unix.OpenHow{
		Flags: namespaceDirectoryOpenFlags, Resolve: namespaceResolveBeneath,
	})
	if err != nil {
		return parentFD, placeholderFD, fmt.Errorf("open exact namespace authority directory: %w", err)
	}
	if err := verifyNamespaceDirectory(ctx, deps, parentFD, policy, true, target); err != nil {
		return parentFD, placeholderFD, fmt.Errorf("verify exact namespace authority directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return parentFD, placeholderFD, err
	}
	placeholderFD, err = openExactNamespacePlaceholder(ctx, deps, policy, parentFD, key.vmID, target)
	if err != nil {
		return parentFD, placeholderFD, err
	}
	return parentFD, placeholderFD, nil
}

func openExactNamespacePlaceholder(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
	parentFD int,
	vmID VMID,
	target namespaceTargetIdentity,
) (int, error) {
	fd, err := deps.openAt2(ctx, parentFD, string(vmID), &unix.OpenHow{
		Flags: namespaceLeafInspectFlags, Resolve: namespaceResolveManaged,
	})
	if err != nil {
		return -1, fmt.Errorf("open exact namespace placeholder: %w", err)
	}
	if err := verifyNamespacePlaceholder(ctx, deps, fd, policy, target); err != nil {
		return fd, err
	}
	return fd, nil
}

func verifyNamespaceDirectory(
	ctx context.Context,
	deps namespaceEffectDeps,
	fd int,
	policy namespaceEffectPolicy,
	exact bool,
	target namespaceTargetIdentity,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Dev == 0 || stat.Ino == 0 || (!exact && stat.Mode&0o022 != 0) {
		return errors.New("namespace directory has unexpected type, owner, mode, or identity")
	}
	if exact && (stat.Mode&0o7777 != 0o700 || stat.Dev != target.directoryDevice ||
		stat.Ino != target.directoryInode) {
		return errors.New("namespace authority directory identity changed")
	}
	if exact {
		mountIdentity, err := inspectNamespaceTargetMountIdentity(ctx, deps, fd)
		if err != nil {
			return err
		}
		if mountIdentity != target.directoryMount {
			return errors.New("namespace authority directory mount identity changed")
		}
	}
	return nil
}

func verifyNamespacePlaceholder(
	ctx context.Context,
	deps namespaceEffectDeps,
	fd int,
	policy namespaceEffectPolicy,
	target namespaceTargetIdentity,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return fmt.Errorf("inspect namespace placeholder: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&0o7777 != namespacePlaceholderMode || stat.Nlink != 1 || stat.Size != 0 || stat.Dev == 0 ||
		stat.Ino == 0 {
		return errors.New(
			"namespace placeholder is not an empty owned one-link regular file with exact mode",
		)
	}
	if stat.Dev != target.placeholderDevice || stat.Ino != target.placeholderInode {
		return errors.New("namespace placeholder identity changed")
	}
	mountIdentity, err := inspectNamespaceTargetMountIdentity(ctx, deps, fd)
	if err != nil {
		return err
	}
	if mountIdentity != target.placeholderMount {
		return errors.New("namespace placeholder mount identity changed")
	}
	return nil
}

func inspectNamespaceTargetMountIdentity(
	ctx context.Context,
	deps namespaceEffectDeps,
	fd int,
) (namespaceTargetMountIdentity, error) {
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, &stat); err != nil {
		return namespaceTargetMountIdentity{}, fmt.Errorf("inspect namespace target mount identity: %w", err)
	}
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
		if stat.Mnt_id == 0 {
			return namespaceTargetMountIdentity{}, errors.New("namespace target unique mount identity is zero")
		}
		return namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: stat.Mnt_id}, nil
	case stat.Mask&unix.STATX_MNT_ID != 0:
		if stat.Mnt_id == 0 {
			return namespaceTargetMountIdentity{}, errors.New("namespace target mount identity is zero")
		}
		return namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: stat.Mnt_id}, nil
	default:
		return namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnavailable}, nil
	}
}

func inspectNetworkNamespaceFD(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
	fd int,
) (namespaceIdentity, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return namespaceIdentity{}, fmt.Errorf("inspect namespace descriptor: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != namespaceHandleMode ||
		stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID || stat.Nlink != 1 || stat.Size != 0 ||
		stat.Dev == 0 || stat.Ino == 0 {
		return namespaceIdentity{}, errors.New("namespace descriptor has unexpected type, owner, mode, or identity")
	}
	var filesystem unix.Statfs_t
	if err := deps.fstatfs(ctx, fd, &filesystem); err != nil {
		return namespaceIdentity{}, fmt.Errorf("inspect namespace filesystem: %w", err)
	}
	if filesystem.Type != unix.NSFS_MAGIC {
		return namespaceIdentity{}, errors.New("descriptor is not an nsfs namespace mount")
	}
	namespaceType, err := deps.ioctlNamespaceType(ctx, fd)
	if err != nil {
		return namespaceIdentity{}, fmt.Errorf("inspect namespace type: %w", err)
	}
	if namespaceType != unix.CLONE_NEWNET {
		return namespaceIdentity{}, errors.New("descriptor is not a network namespace")
	}
	return namespaceIdentity{device: stat.Dev, inode: stat.Ino}, nil
}

func restoreHostNetworkNamespace(
	ctx context.Context,
	deps namespaceEffectDeps,
	policy namespaceEffectPolicy,
	hostFD int,
	want namespaceIdentity,
) (safe bool, returnErr error) {
	if hostFD < 0 || !validNamespaceIdentity(want) {
		return false, errors.New("original host network namespace pin is incomplete")
	}
	if err := deps.setns(ctx, hostFD, unix.CLONE_NEWNET); err != nil {
		return false, fmt.Errorf("restore original host network namespace: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	currentFD, err := deps.open(ctx, namespaceThreadNetworkPath, namespaceCurrentOpenFlags, 0)
	if err != nil {
		return false, fmt.Errorf("verify restored host network namespace: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeConsumedNamespaceFD(ctx, deps, &currentFD))
	}()
	got, err := inspectNetworkNamespaceFD(ctx, deps, policy, currentFD)
	if err != nil {
		return false, fmt.Errorf("verify restored host network namespace: %w", err)
	}
	if got != want {
		return false, errors.New("restored network namespace does not match pinned host namespace")
	}
	return true, nil
}

func closeConsumedNamespaceFD(ctx context.Context, deps namespaceEffectDeps, descriptor *int) error {
	if descriptor == nil || *descriptor < 0 {
		return nil
	}
	fd := *descriptor
	*descriptor = -1
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return fmt.Errorf("close network namespace descriptor: %w", err)
	}
	return nil
}
