package loopmount

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	rootfsRootOpenFlags      = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsDirectoryOpenFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsReadOnlyOpenFlags  = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootfsReadWriteOpenFlags = unix.O_RDWR | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW

	rootfsCacheResolveFlags      = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	rootfsDescendantResolveFlags = rootfsCacheResolveFlags | unix.RESOLVE_NO_XDEV
	rootfsStatxFlags             = unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
		unix.AT_STATX_FORCE_SYNC
	rootfsStatxMask              = unix.STATX_BASIC_STATS | unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
	rootfsStatxRequiredDirectory = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID |
		unix.STATX_INO
	rootfsStatxRequiredFile = rootfsStatxRequiredDirectory | unix.STATX_NLINK | unix.STATX_SIZE

	maxRootfsCacheLocatorDepth       = 64
	rootfsZFSSuperMagic        int64 = 0x2fc12fc1
)

// Caller is the authenticated invoking identity for a privileged loopmount request.
// UID alone owns admitted resources. GID is retained identity and never overrides UID ownership checks.
type Caller struct {
	UID uint32
	GID uint32
}

type rootfsCacheLocator struct {
	components []string
}

type rootfsAuthorityDeps struct {
	openRoot func(context.Context, string, int, uint32) (int, error)
	openAt2  func(context.Context, int, string, *unix.OpenHow) (int, error)
	statx    func(context.Context, int, int, int, *unix.Statx_t) error
	statfs   func(context.Context, int, *unix.Statfs_t) error
	close    func(context.Context, int) error
}

type rootfsMountIdentityKind uint8

const (
	rootfsMountIdentityUnavailable rootfsMountIdentityKind = iota
	rootfsMountIdentityLegacy
	rootfsMountIdentityUnique
)

type rootfsMountIdentity struct {
	kind  rootfsMountIdentityKind
	value uint64
}

type rootfsCacheIdentity struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mount       rootfsMountIdentity
}

type rootfsStableMetadata struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mode        uint16
	uid         uint32
	gid         uint32
	nlink       uint32
	size        uint64
}

type rootfsAccess uint8

const (
	rootfsReadOnly rootfsAccess = iota + 1
	rootfsReadWrite
)

type rootfsCacheComponentRole uint8

const (
	rootfsFilesystemRoot rootfsCacheComponentRole = iota
	rootfsCacheAncestor
	rootfsCacheRoot
)

type rootfsSelectionKind uint8

const (
	rootfsVMProvisionSelection rootfsSelectionKind = iota + 1
	rootfsImageInspectSelection
	rootfsImageOptimizeSelection
)

type rootfsSelection struct {
	kind             rootfsSelectionKind
	resourceID       string
	expectedBytes    uint64
	replayBytes      uint64
	replacementBytes uint64
}

type rootfsPolicy struct {
	directories      []string
	expectedBytes    uint64
	replayBytes      uint64
	replacementBytes uint64
	minimumBytes     uint64
	maximumBytes     uint64
	access           rootfsAccess
	retainStagingDir bool
}

func parseRootfsCacheLocator(raw string) (rootfsCacheLocator, error) {
	if strings.ContainsRune(raw, '\x00') {
		return rootfsCacheLocator{}, rootfsValidationError("managed cache locator contains NUL")
	}
	if !filepath.IsAbs(raw) {
		return rootfsCacheLocator{}, rootfsValidationError("managed cache locator must be absolute")
	}
	if len(raw) >= unix.PathMax {
		return rootfsCacheLocator{}, rootfsValidationError("managed cache locator exceeds path length limit")
	}
	if filepath.Clean(raw) != raw {
		return rootfsCacheLocator{}, rootfsValidationError("managed cache locator must be canonical")
	}
	if raw == string(filepath.Separator) {
		return rootfsCacheLocator{}, rootfsValidationError("filesystem root is not a managed cache locator")
	}

	locator := rootfsCacheLocator{
		components: strings.Split(strings.TrimPrefix(raw, string(filepath.Separator)), string(filepath.Separator)),
	}
	if err := validateRootfsCacheLocator(locator); err != nil {
		return rootfsCacheLocator{}, err
	}
	return locator, nil
}

func validateRootfsCacheLocator(locator rootfsCacheLocator) error {
	if len(locator.components) == 0 {
		return rootfsValidationError("managed cache locator has no components")
	}
	if len(locator.components) > maxRootfsCacheLocatorDepth {
		return rootfsValidationError("managed cache locator exceeds component depth limit")
	}
	pathBytes := 0
	for _, component := range locator.components {
		if component == "" || component == "." || component == ".." ||
			strings.ContainsRune(component, filepath.Separator) || strings.ContainsRune(component, '\x00') {
			return rootfsValidationError("managed cache locator has unsafe components")
		}
		if len(component) > unix.NAME_MAX {
			return rootfsValidationError("managed cache locator component exceeds name limit")
		}
		pathBytes += len(component) + 1
	}
	if pathBytes >= unix.PathMax {
		return rootfsValidationError("managed cache locator exceeds path length limit")
	}
	return nil
}

func validateRootfsCaller(caller Caller) error {
	if caller.UID == 0 {
		return rootfsValidationError("loopmount caller UID must be nonzero")
	}
	if caller.GID == 0 {
		return rootfsValidationError("loopmount caller GID must be nonzero")
	}
	return nil
}

func admitVMProvisionRootfs(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	caller Caller,
	request VMProvisionRequest,
) (*rootfsLease, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return admitRootfs(ctx, deps, caller, request.CacheLocator, rootfsSelection{
		kind:          rootfsVMProvisionSelection,
		resourceID:    request.VMID,
		expectedBytes: request.ExpectedRootfsBytes,
		replayBytes:   request.Plan.ResizeBytes,
	})
}

func admitImageInspectRootfs(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	caller Caller,
	request ImageInspectRequest,
) (*rootfsLease, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return admitRootfs(ctx, deps, caller, request.CacheLocator, rootfsSelection{
		kind:          rootfsImageInspectSelection,
		resourceID:    request.ImageID,
		expectedBytes: request.ExpectedImageBytes,
	})
}

func admitImageOptimizeRootfs(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	caller Caller,
	request ImageOptimizeRequest,
) (*rootfsLease, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return admitRootfs(ctx, deps, caller, request.CacheLocator, rootfsSelection{
		kind:          rootfsImageOptimizeSelection,
		resourceID:    request.ImageID,
		expectedBytes: request.ExpectedImageBytes,
	})
}

// CRITICAL: The locator is only a namespace hint. Every effect-visible object is opened once beneath a retained
// descriptor chain, and no later pathname reopen can replace the admitted rootfs authority.
func admitRootfs(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	caller Caller,
	rawLocator string,
	selection rootfsSelection,
) (_ *rootfsLease, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("pin managed rootfs", err)
	}
	if err := validateRootfsAuthorityDeps(deps); err != nil {
		return nil, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return nil, err
	}
	locator, err := parseRootfsCacheLocator(rawLocator)
	if err != nil {
		return nil, err
	}
	policy, err := rootfsPolicyForSelection(selection)
	if err != nil {
		return nil, err
	}

	retained := make([]int, 0, len(locator.components)+len(policy.directories)+2)
	defer func() {
		if returnErr == nil {
			return
		}
		if closeErr := closeRootfsDescriptors(context.WithoutCancel(ctx), deps, retained); closeErr != nil {
			returnErr = appendRootfsCleanupError(returnErr, "close rejected rootfs descriptors", closeErr)
		}
	}()

	rootFD, err := deps.openRoot(ctx, "/", rootfsRootOpenFlags, 0)
	if err != nil {
		return nil, rootfsAtomicError("open filesystem root for managed cache", err)
	}
	retained = append(retained, rootFD)
	if _, err := inspectRootfsCacheDirectory(
		ctx,
		deps,
		rootFD,
		"filesystem root",
		rootfsFilesystemRoot,
		caller,
	); err != nil {
		return nil, err
	}

	parentFD := rootFD
	cacheIdentity := rootfsCacheIdentity{}
	for index, component := range locator.components {
		// CRITICAL: A managed cache may intentionally live on a dedicated local mount. Mount crossing is permitted only
		// while pinning this locator; fixed resource descendants add RESOLVE_NO_XDEV below the admitted cache root.
		fd, openErr := openRootfsComponent(
			ctx,
			deps,
			parentFD,
			component,
			rootfsDirectoryOpenFlags,
			rootfsCacheResolveFlags,
			"managed cache component "+component,
		)
		if openErr != nil {
			return nil, openErr
		}
		retained = append(retained, fd)
		role := rootfsCacheAncestor
		if index == len(locator.components)-1 {
			role = rootfsCacheRoot
		}
		cacheIdentity, err = inspectRootfsCacheDirectory(ctx, deps, fd, component, role, caller)
		if err != nil {
			return nil, err
		}
		parentFD = fd
	}

	for _, directory := range policy.directories {
		fd, openErr := openRootfsComponent(
			ctx,
			deps,
			parentFD,
			directory,
			rootfsDirectoryOpenFlags,
			rootfsDescendantResolveFlags,
			"managed rootfs directory "+directory,
		)
		if openErr != nil {
			return nil, openErr
		}
		retained = append(retained, fd)
		if err := inspectRootfsDescendantDirectory(ctx, deps, fd, directory, caller.UID); err != nil {
			return nil, err
		}
		parentFD = fd
	}

	leafFlags := rootfsReadOnlyOpenFlags
	if policy.access == rootfsReadWrite {
		leafFlags = rootfsReadWriteOpenFlags
	}
	rootfsFD, err := openRootfsComponent(
		ctx,
		deps,
		parentFD,
		"rootfs.img",
		leafFlags,
		rootfsDescendantResolveFlags,
		"managed rootfs image",
	)
	if err != nil {
		return nil, err
	}
	retained = append(retained, rootfsFD)
	metadata, err := inspectRootfsFile(ctx, deps, rootfsFD, caller.UID, policy)
	if err != nil {
		return nil, err
	}

	stagingFD := -1
	if policy.retainStagingDir {
		stagingFD = parentFD
	}
	lease := &rootfsLease{
		deps:          deps,
		caller:        caller,
		cacheIdentity: cacheIdentity,
		metadata:      metadata,
		admittedBytes: metadata.size,
		access:        policy.access,
		parentFD:      parentFD,
		stagingFD:     stagingFD,
		rootfsFD:      rootfsFD,
		retained:      retained,
	}
	retained = nil
	return lease, nil
}

func validateRootfsAuthorityDeps(deps rootfsAuthorityDeps) error {
	if deps.openRoot == nil || deps.openAt2 == nil || deps.statx == nil || deps.statfs == nil || deps.close == nil {
		return rootfsAtomicError("managed rootfs authority dependencies are incomplete", nil)
	}
	return nil
}

func rootfsPolicyForSelection(selection rootfsSelection) (rootfsPolicy, error) {
	switch selection.kind {
	case rootfsVMProvisionSelection:
		if !validLowerHexID(selection.resourceID, 32) || selection.expectedBytes < minimumRootfsBytes ||
			selection.expectedBytes > maximumRootfsBytes {
			return rootfsPolicy{}, rootfsValidationError("managed VM rootfs selection is invalid")
		}
		if selection.replayBytes != 0 && (selection.replayBytes < selection.expectedBytes ||
			selection.replayBytes > maximumRootfsBytes) {
			return rootfsPolicy{}, rootfsValidationError("managed VM rootfs replay size is invalid")
		}
		return rootfsPolicy{
			directories:   []string{"vms", selection.resourceID},
			expectedBytes: selection.expectedBytes,
			replayBytes:   selection.replayBytes,
			minimumBytes:  minimumRootfsBytes,
			maximumBytes:  maximumRootfsBytes,
			access:        rootfsReadWrite,
		}, nil
	case rootfsImageInspectSelection, rootfsImageOptimizeSelection:
		if !validLowerHexID(selection.resourceID, 64) || selection.expectedBytes < minimumImageBytes ||
			selection.expectedBytes > maximumImageBytes {
			return rootfsPolicy{}, rootfsValidationError("managed image rootfs selection is invalid")
		}
		access := rootfsReadOnly
		if selection.replayBytes != 0 && (selection.kind != rootfsImageOptimizeSelection ||
			selection.replayBytes < minimumImageBytes || selection.replayBytes > selection.expectedBytes) {
			return rootfsPolicy{}, rootfsValidationError("managed image shrink replay size is invalid")
		}
		if selection.kind == rootfsImageOptimizeSelection {
			access = rootfsReadWrite
		}
		if selection.replacementBytes != 0 && (selection.kind != rootfsImageOptimizeSelection ||
			selection.replacementBytes < minimumImageBytes || selection.replacementBytes > maximumImageBytes) {
			return rootfsPolicy{}, rootfsValidationError("managed image replacement replay size is invalid")
		}
		return rootfsPolicy{
			directories:      []string{"images", "staging", selection.resourceID},
			expectedBytes:    selection.expectedBytes,
			replayBytes:      selection.replayBytes,
			replacementBytes: selection.replacementBytes,
			minimumBytes:     minimumImageBytes,
			maximumBytes:     maximumImageBytes,
			access:           access,
			retainStagingDir: true,
		}, nil
	default:
		return rootfsPolicy{}, rootfsAtomicError("managed rootfs selection is invalid", nil)
	}
}

func openRootfsComponent(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	parentFD int,
	component string,
	flags int,
	resolve int,
	name string,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, rootfsAtomicError("open "+name, err)
	}
	how := unix.OpenHow{Flags: uint64(flags), Resolve: uint64(resolve)}
	fd, err := deps.openAt2(ctx, parentFD, component, &how)
	if err != nil {
		return -1, classifyRootfsOpenError(name, err)
	}
	return fd, nil
}

func inspectRootfsCacheDirectory(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	fd int,
	name string,
	role rootfsCacheComponentRole,
	caller Caller,
) (rootfsCacheIdentity, error) {
	stat, err := statRootfsObject(ctx, deps, fd, "managed cache directory "+name, rootfsStatxRequiredDirectory)
	if err != nil {
		return rootfsCacheIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return rootfsCacheIdentity{}, rootfsValidationError("managed cache component " + name + " is not a directory")
	}
	if stat.Mode&0022 != 0 {
		return rootfsCacheIdentity{}, rootfsValidationError(
			"managed cache component " + name + " is writable by another user",
		)
	}
	switch role {
	case rootfsFilesystemRoot:
		if stat.Uid != 0 {
			return rootfsCacheIdentity{}, rootfsValidationError("managed cache filesystem root is not root-owned")
		}
	case rootfsCacheAncestor:
		if stat.Uid != 0 && stat.Uid != caller.UID {
			return rootfsCacheIdentity{}, rootfsValidationError(
				"managed cache ancestor " + name + " has foreign ownership",
			)
		}
	case rootfsCacheRoot:
		if stat.Uid != caller.UID {
			return rootfsCacheIdentity{}, rootfsValidationError(
				"managed cache root is not owned by the invoking user",
			)
		}
		if stat.Mode&0700 != 0700 {
			return rootfsCacheIdentity{}, rootfsValidationError(
				"managed cache root must grant its owner read, write, and traversal access",
			)
		}
		var filesystem unix.Statfs_t
		if err := ctx.Err(); err != nil {
			return rootfsCacheIdentity{}, rootfsAtomicError("inspect managed cache filesystem topology", err)
		}
		if err := deps.statfs(ctx, fd, &filesystem); err != nil {
			return rootfsCacheIdentity{}, rootfsAtomicError("inspect managed cache filesystem topology", err)
		}
		if !isSupportedRootfsCacheFilesystem(filesystem.Type) {
			return rootfsCacheIdentity{}, rootfsValidationError(
				"managed cache root uses unsupported filesystem topology",
			)
		}
	default:
		return rootfsCacheIdentity{}, rootfsAtomicError("managed cache component role is invalid", nil)
	}

	mount, err := rootfsMountIdentityFromStat(stat, name)
	if err != nil {
		return rootfsCacheIdentity{}, err
	}
	return rootfsCacheIdentity{
		deviceMajor: stat.Dev_major,
		deviceMinor: stat.Dev_minor,
		inode:       stat.Ino,
		mount:       mount,
	}, nil
}

func inspectRootfsDescendantDirectory(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	fd int,
	name string,
	ownerUID uint32,
) error {
	stat, err := statRootfsObject(ctx, deps, fd, "managed rootfs directory "+name, rootfsStatxRequiredDirectory)
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return rootfsValidationError("managed rootfs directory " + name + " is not a directory")
	}
	if stat.Uid != ownerUID {
		return rootfsValidationError("managed rootfs directory " + name + " is not owned by the invoking user")
	}
	if stat.Mode&0700 != 0700 {
		return rootfsValidationError("managed rootfs directory " + name + " does not grant required owner access")
	}
	if stat.Mode&0022 != 0 {
		return rootfsValidationError("managed rootfs directory " + name + " is writable by another user")
	}
	if stat.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return rootfsValidationError("managed rootfs directory " + name + " has unsupported special mode bits")
	}
	return nil
}

func inspectRootfsFile(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	fd int,
	ownerUID uint32,
	policy rootfsPolicy,
) (rootfsStableMetadata, error) {
	first, err := statRootfsObject(ctx, deps, fd, "managed rootfs image", rootfsStatxRequiredFile)
	if err != nil {
		return rootfsStableMetadata{}, err
	}
	second, err := statRootfsObject(ctx, deps, fd, "managed rootfs image", rootfsStatxRequiredFile)
	if err != nil {
		return rootfsStableMetadata{}, err
	}
	firstMetadata := stableRootfsMetadata(first)
	if firstMetadata != stableRootfsMetadata(second) {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image metadata changed during admission")
	}
	if first.Mode&unix.S_IFMT != unix.S_IFREG {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image is not a regular file")
	}
	if first.Uid != ownerUID {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image is not owned by the invoking user")
	}
	if first.Nlink != 1 {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image must have exactly one link")
	}
	if first.Mode&0600 != 0600 {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image does not grant owner read and write")
	}
	if first.Mode&0022 != 0 {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image is writable by another user")
	}
	if first.Mode&0111 != 0 {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image has unsupported execute access")
	}
	if first.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image has unsupported special mode bits")
	}
	if first.Size < policy.minimumBytes || first.Size > policy.maximumBytes {
		return rootfsStableMetadata{}, rootfsValidationError("managed rootfs image size is outside the supported range")
	}
	if first.Size != policy.expectedBytes && (policy.replayBytes == 0 || first.Size != policy.replayBytes) &&
		(policy.replacementBytes == 0 || first.Size != policy.replacementBytes) {
		return rootfsStableMetadata{}, rootfsValidationError(
			"managed rootfs image size does not match an admitted size",
		)
	}
	return firstMetadata, nil
}

func statRootfsObject(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	fd int,
	name string,
	requiredMask uint32,
) (unix.Statx_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Statx_t{}, rootfsAtomicError("inspect "+name, err)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, rootfsStatxFlags, rootfsStatxMask, &stat); err != nil {
		return unix.Statx_t{}, rootfsAtomicError("inspect "+name, err)
	}
	if stat.Mask&requiredMask != requiredMask || stat.Ino == 0 ||
		(stat.Dev_major == 0 && stat.Dev_minor == 0) {
		return unix.Statx_t{}, rootfsValidationError(name + " has incomplete stable metadata")
	}
	if stat.Attributes_mask&unix.STATX_ATTR_AUTOMOUNT != 0 && stat.Attributes&unix.STATX_ATTR_AUTOMOUNT != 0 {
		return unix.Statx_t{}, rootfsValidationError(name + " uses unsupported automount topology")
	}
	return stat, nil
}

func stableRootfsMetadata(stat unix.Statx_t) rootfsStableMetadata {
	return rootfsStableMetadata{
		deviceMajor: stat.Dev_major,
		deviceMinor: stat.Dev_minor,
		inode:       stat.Ino,
		mode:        stat.Mode,
		uid:         stat.Uid,
		gid:         stat.Gid,
		nlink:       stat.Nlink,
		size:        stat.Size,
	}
}

func rootfsMountIdentityFromStat(stat unix.Statx_t, name string) (rootfsMountIdentity, error) {
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
		if stat.Mnt_id == 0 {
			return rootfsMountIdentity{}, rootfsValidationError(
				"managed cache directory " + name + " has incomplete stable mount identity",
			)
		}
		return rootfsMountIdentity{kind: rootfsMountIdentityUnique, value: stat.Mnt_id}, nil
	case stat.Mask&unix.STATX_MNT_ID != 0:
		if stat.Mnt_id == 0 {
			return rootfsMountIdentity{}, rootfsValidationError(
				"managed cache directory " + name + " has incomplete stable mount identity",
			)
		}
		return rootfsMountIdentity{kind: rootfsMountIdentityLegacy, value: stat.Mnt_id}, nil
	default:
		return rootfsMountIdentity{kind: rootfsMountIdentityUnavailable}, nil
	}
}

func isSupportedRootfsCacheFilesystem(filesystemType int64) bool {
	switch filesystemType {
	case unix.EXT4_SUPER_MAGIC,
		unix.XFS_SUPER_MAGIC,
		unix.BTRFS_SUPER_MAGIC,
		unix.F2FS_SUPER_MAGIC,
		unix.BCACHEFS_SUPER_MAGIC,
		unix.TMPFS_MAGIC,
		rootfsZFSSuperMagic:
		return true
	default:
		return false
	}
}

func classifyRootfsOpenError(name string, cause error) error {
	message := "open " + name
	if errors.Is(cause, unix.ELOOP) || errors.Is(cause, unix.ENOTDIR) || errors.Is(cause, unix.EXDEV) ||
		errors.Is(cause, unix.ENOENT) || errors.Is(cause, unix.EACCES) || errors.Is(cause, unix.EPERM) ||
		errors.Is(cause, unix.ENXIO) || errors.Is(cause, unix.EISDIR) || errors.Is(cause, unix.ENODEV) {
		return errs.WrapMsg(errs.CodeValidationFailed, message, cause)
	}
	return rootfsAtomicError(message, cause)
}

func rootfsValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}

func rootfsAtomicError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeLoopMountError, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeLoopMountError, message, cause, errs.WithClass(errs.ClassInternal))
}

func systemRootfsAuthorityDeps() rootfsAuthorityDeps {
	return rootfsAuthorityDeps{
		openRoot: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		openAt2: func(_ context.Context, parentFD int, component string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, component, how)
		},
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		statfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			return unix.Fstatfs(fd, stat)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}
