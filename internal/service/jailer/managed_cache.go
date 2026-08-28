package jailer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	managedCacheRootOpenFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedCacheOpenFlags     = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedCacheResolveFlags  = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	managedCacheRequiredStat  = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_INO
	managedCacheStatxFlags    = unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
		unix.AT_STATX_FORCE_SYNC
	managedCacheStatxMask = unix.STATX_BASIC_STATS | unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
	// Linux does not expose ZFS_SUPER_MAGIC through x/sys/unix.
	managedCacheZFSSuperMagic int64 = 0x2fc12fc1

	maxManagedCacheLocatorDepth = 64
)

type managedCacheLocator struct {
	components []string
}

type managedCacheIdentity struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mountID     uint64
	mountIDKind managedCacheMountIdentityKind
}

type managedCacheMountIdentityKind uint8

const (
	managedCacheMountIdentityUnavailable managedCacheMountIdentityKind = iota
	managedCacheMountIdentityLegacy
	managedCacheMountIdentityUnique
)

type managedCachePolicy struct {
	rootPath   string
	trustedUID uint32
}

type managedCacheDeps struct {
	open    func(context.Context, string, int, uint32) (int, error)
	openAt2 func(context.Context, int, string, *unix.OpenHow) (int, error)
	statx   func(context.Context, int, int, int, *unix.Statx_t) error
	statfs  func(context.Context, int, *unix.Statfs_t) error
	close   func(context.Context, int) error
}

type managedCacheLease struct {
	deps     managedCacheDeps
	cacheFD  int
	ownerUID uint32
	identity managedCacheIdentity
	retained []int
}

type managedCacheComponentRole uint8

const (
	managedCacheFilesystemRoot managedCacheComponentRole = iota
	managedCacheAncestor
	managedCacheRoot
)

func productionManagedCachePolicy() managedCachePolicy {
	return managedCachePolicy{rootPath: "/", trustedUID: 0}
}

func parseManagedCacheLocator(raw string) (managedCacheLocator, error) {
	if strings.ContainsRune(raw, '\x00') {
		return managedCacheLocator{}, managedCacheValidationError("managed cache locator contains NUL")
	}
	if !filepath.IsAbs(raw) {
		return managedCacheLocator{}, managedCacheValidationError("managed cache locator must be absolute")
	}
	if len(raw) >= unix.PathMax {
		return managedCacheLocator{}, managedCacheValidationError("managed cache locator exceeds path length limit")
	}
	if filepath.Clean(raw) != raw {
		return managedCacheLocator{}, managedCacheValidationError("managed cache locator must be canonical")
	}
	if raw == string(filepath.Separator) {
		return managedCacheLocator{}, managedCacheValidationError("filesystem root is not a managed cache locator")
	}
	components := strings.Split(strings.TrimPrefix(raw, string(filepath.Separator)), string(filepath.Separator))
	locator := managedCacheLocator{components: components}
	if err := validateManagedCacheLocator(locator); err != nil {
		return managedCacheLocator{}, err
	}
	return locator, nil
}

func pinManagedCache(
	ctx context.Context,
	deps managedCacheDeps,
	policy managedCachePolicy,
	caller instanceCaller,
	locator managedCacheLocator,
) (_ *managedCacheLease, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, managedCacheAtomicError("pin managed cache", err)
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if err := validateManagedCachePolicy(policy); err != nil {
		return nil, err
	}
	if err := validateManagedCacheLocator(locator); err != nil {
		return nil, err
	}

	retained := make([]int, 0, len(locator.components)+1)
	defer func() {
		if returnErr == nil {
			return
		}
		if closeErr := closeManagedCacheFDs(ctx, deps, retained); closeErr != nil {
			returnErr = appendManagedCacheCleanupError(
				returnErr,
				"close rejected managed cache descriptors",
				closeErr,
			)
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, managedCacheRootOpenFlags, 0)
	if err != nil {
		return nil, managedCacheAtomicError("open filesystem root for managed cache", err)
	}
	retained = append(retained, rootFD)
	if _, err := inspectManagedCacheDirectory(
		ctx,
		deps,
		rootFD,
		"filesystem root",
		managedCacheFilesystemRoot,
		policy,
		caller,
	); err != nil {
		return nil, err
	}

	parentFD := rootFD
	identity := managedCacheIdentity{}
	for index, component := range locator.components {
		if err := ctx.Err(); err != nil {
			return nil, managedCacheAtomicError("open managed cache component", err)
		}
		// CRITICAL: The locator may cross onto a dedicated large-filesystem mount. RESOLVE_NO_XDEV is
		// intentionally reserved for descendant resource opens beneath the already-pinned cache root.
		how := unix.OpenHow{
			Flags:   uint64(managedCacheOpenFlags),
			Resolve: uint64(managedCacheResolveFlags),
		}
		fd, openErr := deps.openAt2(ctx, parentFD, component, &how)
		if openErr != nil {
			return nil, classifyManagedCacheOpenError(component, openErr)
		}
		retained = append(retained, fd)
		role := managedCacheAncestor
		if index == len(locator.components)-1 {
			role = managedCacheRoot
		}
		identity, err = inspectManagedCacheDirectory(ctx, deps, fd, component, role, policy, caller)
		if err != nil {
			return nil, err
		}
		parentFD = fd
	}

	lease := &managedCacheLease{
		deps:     deps,
		cacheFD:  parentFD,
		ownerUID: caller.uid,
		identity: identity,
		retained: retained,
	}
	retained = nil
	return lease, nil
}

func repinManagedCache(
	ctx context.Context,
	deps managedCacheDeps,
	policy managedCachePolicy,
	caller instanceCaller,
	locator managedCacheLocator,
	expected managedCacheIdentity,
) (_ *managedCacheLease, returnErr error) {
	lease, err := pinManagedCache(ctx, deps, policy, caller, locator)
	if err != nil {
		return nil, err
	}
	if lease.identity != expected {
		identityErr := managedCacheAtomicError("managed cache identity does not match registered authority", nil)
		return nil, appendManagedCacheCleanupError(
			identityErr,
			"release mismatched managed cache lease",
			lease.Release(context.WithoutCancel(ctx)),
		)
	}
	return lease, nil
}

func validateManagedCachePolicy(policy managedCachePolicy) error {
	if !filepath.IsAbs(policy.rootPath) || filepath.Clean(policy.rootPath) != policy.rootPath ||
		strings.ContainsRune(policy.rootPath, '\x00') {
		return managedCacheAtomicError("managed cache filesystem root policy is invalid", nil)
	}
	return nil
}

func validateManagedCacheLocator(locator managedCacheLocator) error {
	if len(locator.components) == 0 {
		return managedCacheValidationError("managed cache locator has no components")
	}
	if len(locator.components) > maxManagedCacheLocatorDepth {
		return managedCacheValidationError("managed cache locator exceeds component depth limit")
	}
	pathBytes := 0
	for _, component := range locator.components {
		if component == "" || component == "." || component == ".." ||
			strings.ContainsRune(component, filepath.Separator) || strings.ContainsRune(component, '\x00') {
			return managedCacheValidationError("managed cache locator has unsafe components")
		}
		if len(component) > unix.NAME_MAX {
			return managedCacheValidationError("managed cache locator component exceeds name limit")
		}
		pathBytes += len(component) + 1
	}
	if pathBytes >= unix.PathMax {
		return managedCacheValidationError("managed cache locator exceeds path length limit")
	}
	return nil
}

func inspectManagedCacheDirectory(
	ctx context.Context,
	deps managedCacheDeps,
	fd int,
	name string,
	role managedCacheComponentRole,
	policy managedCachePolicy,
	caller instanceCaller,
) (managedCacheIdentity, error) {
	if err := ctx.Err(); err != nil {
		return managedCacheIdentity{}, managedCacheAtomicError("inspect managed cache directory "+name, err)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, managedCacheStatxFlags, managedCacheStatxMask, &stat); err != nil {
		return managedCacheIdentity{}, managedCacheAtomicError("inspect managed cache directory "+name, err)
	}
	if stat.Mask&managedCacheRequiredStat != managedCacheRequiredStat {
		return managedCacheIdentity{}, managedCacheValidationError(
			"managed cache directory " + name + " has incomplete stable identity",
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return managedCacheIdentity{}, managedCacheValidationError(
			"managed cache component " + name + " is not a directory",
		)
	}
	if stat.Attributes_mask&unix.STATX_ATTR_AUTOMOUNT != 0 &&
		stat.Attributes&unix.STATX_ATTR_AUTOMOUNT != 0 {
		return managedCacheIdentity{}, managedCacheValidationError(
			"managed cache component " + name + " uses unsupported automount topology",
		)
	}
	if role == managedCacheRoot {
		var filesystem unix.Statfs_t
		if err := deps.statfs(ctx, fd, &filesystem); err != nil {
			return managedCacheIdentity{}, managedCacheAtomicError(
				"inspect managed cache filesystem topology "+name,
				err,
			)
		}
		if !isSupportedManagedCacheFilesystem(filesystem.Type) {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache root uses unsupported filesystem topology",
			)
		}
	}
	if stat.Mode&0022 != 0 {
		return managedCacheIdentity{}, managedCacheValidationError(
			"managed cache component " + name + " is writable by another user",
		)
	}
	switch role {
	case managedCacheFilesystemRoot:
		if stat.Uid != policy.trustedUID {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache filesystem root has unexpected ownership",
			)
		}
	case managedCacheAncestor:
		if stat.Uid != policy.trustedUID && stat.Uid != caller.uid {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache ancestor " + name + " has foreign ownership",
			)
		}
	case managedCacheRoot:
		if stat.Uid != caller.uid {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache root is not owned by the invoking user",
			)
		}
		if stat.Mode&0700 != 0700 {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache root must grant its owner read, write, and traversal access",
			)
		}
	default:
		return managedCacheIdentity{}, managedCacheAtomicError("managed cache component role is invalid", nil)
	}
	if stat.Ino == 0 || (stat.Dev_major == 0 && stat.Dev_minor == 0) {
		return managedCacheIdentity{}, managedCacheValidationError(
			"managed cache directory " + name + " has incomplete stable identity",
		)
	}
	identity := managedCacheIdentity{
		deviceMajor: stat.Dev_major,
		deviceMinor: stat.Dev_minor,
		inode:       stat.Ino,
	}
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
		if stat.Mnt_id == 0 {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache directory " + name + " has incomplete stable mount identity",
			)
		}
		identity.mountIDKind = managedCacheMountIdentityUnique
		identity.mountID = stat.Mnt_id
	case stat.Mask&unix.STATX_MNT_ID != 0:
		if stat.Mnt_id == 0 {
			return managedCacheIdentity{}, managedCacheValidationError(
				"managed cache directory " + name + " has incomplete stable mount identity",
			)
		}
		identity.mountIDKind = managedCacheMountIdentityLegacy
		identity.mountID = stat.Mnt_id
	}
	return identity, nil
}

func isSupportedManagedCacheFilesystem(filesystemType int64) bool {
	switch filesystemType {
	case unix.EXT4_SUPER_MAGIC,
		unix.XFS_SUPER_MAGIC,
		unix.BTRFS_SUPER_MAGIC,
		unix.F2FS_SUPER_MAGIC,
		unix.BCACHEFS_SUPER_MAGIC,
		unix.TMPFS_MAGIC,
		managedCacheZFSSuperMagic:
		return true
	default:
		return false
	}
}

func classifyManagedCacheOpenError(component string, cause error) error {
	message := "open managed cache component " + component
	if errors.Is(cause, unix.ELOOP) || errors.Is(cause, unix.ENOTDIR) || errors.Is(cause, unix.EXDEV) ||
		errors.Is(cause, unix.ENOENT) {
		return errs.WrapMsg(errs.CodeValidationFailed, message, cause)
	}
	return managedCacheAtomicError(message, cause)
}

func (lease *managedCacheLease) Release(ctx context.Context) error {
	if lease == nil || len(lease.retained) == 0 {
		return nil
	}
	err := closeManagedCacheFDs(ctx, lease.deps, lease.retained)
	lease.retained = nil
	lease.cacheFD = -1
	lease.ownerUID = 0
	lease.identity = managedCacheIdentity{}
	if err != nil {
		return managedCacheCleanupError("release managed cache descriptors", err)
	}
	return nil
}

func closeManagedCacheFDs(ctx context.Context, deps managedCacheDeps, fds []int) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(cleanupCtx, fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func appendManagedCacheCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		return cause
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		if domainErr.Details == nil {
			domainErr.Details = make(map[string]any)
		}
		domainErr.Details["managed_cache_descriptor_cleanup_failed"] = true
		return domainErr
	}
	return managedCacheCleanupError(message+": "+cause.Error(), errors.Join(primary, cause))
}

func managedCacheValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}

func managedCacheAtomicError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(errs.ClassInternal))
}

func managedCacheCleanupError(message string, cause error) *errs.DomainError {
	domainErr := managedCacheAtomicError(message, cause)
	domainErr.Details = map[string]any{"managed_cache_descriptor_cleanup_failed": true}
	return domainErr
}

func realManagedCacheDeps() managedCacheDeps {
	return managedCacheDeps{
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		statfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			return unix.Fstatfs(fd, stat)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
	}
}
