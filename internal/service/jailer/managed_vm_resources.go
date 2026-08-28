package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	managedVMRootfsMinBytes    uint64 = 128 << 20
	managedVMRootfsMaxBytes    uint64 = 16 << 40
	managedVMConfigMinBytes    uint64 = 1
	managedVMConfigMaxBytes    uint64 = 1 << 20
	managedVMKernelMinBytes    uint64 = 1
	managedVMKernelMaxBytes    uint64 = 1 << 30
	managedVMCloudInitMinBytes uint64 = 1
	managedVMCloudInitMaxBytes uint64 = 256 << 20

	managedVMResourceDirectoryOpenFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedVMResourceFileOpenFlags      = unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	managedVMResourceResolveFlags       = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV
	managedVMResourceStatxFlags = unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
		unix.AT_STATX_FORCE_SYNC
	managedVMResourceRequiredDirectoryStat = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_INO
	managedVMResourceRequiredFileStat      = managedVMResourceRequiredDirectoryStat | unix.STATX_NLINK |
		unix.STATX_SIZE
	managedVMResourceStatxMask = unix.STATX_BASIC_STATS
)

type vmResourceID string

type kernelResourceID string

type cloudInitPresence uint8

const (
	cloudInitAbsent cloudInitPresence = iota + 1
	cloudInitPresent
)

type vmLaunchResourceSpec struct {
	vmID                vmResourceID
	kernelID            kernelResourceID
	expectedRootfsBytes uint64
	cloudInit           cloudInitPresence
}

type vmLaunchResourceLease struct {
	deps          managedCacheDeps
	cacheIdentity managedCacheIdentity
	ownerUID      uint32
	rootfsFD      int
	configFD      int
	kernelFD      int
	cloudInitFD   int
	retained      []int
}

type managedVMRegularFilePolicy struct {
	name              string
	minimumBytes      uint64
	maximumBytes      uint64
	exactBytes        uint64
	requiredOwnerMode uint16
	allowExecute      bool
}

func newVMLaunchResourceSpec(
	vmID string,
	kernelID string,
	expectedRootfsBytes uint64,
	cloudInit cloudInitPresence,
) (vmLaunchResourceSpec, error) {
	spec := vmLaunchResourceSpec{
		vmID:                vmResourceID(vmID),
		kernelID:            kernelResourceID(kernelID),
		expectedRootfsBytes: expectedRootfsBytes,
		cloudInit:           cloudInit,
	}
	if err := validateVMLaunchResourceSpec(spec); err != nil {
		return vmLaunchResourceSpec{}, err
	}
	return spec, nil
}

func validateVMLaunchResourceSpec(spec vmLaunchResourceSpec) error {
	if !isCanonicalManagedResourceID(string(spec.vmID), 32) {
		return managedVMResourceValidationError(
			"managed VM ID must be 32 lowercase hexadecimal characters",
		)
	}
	if !isCanonicalManagedResourceID(string(spec.kernelID), 64) {
		return managedVMResourceValidationError(
			"managed kernel ID must be 64 lowercase hexadecimal characters",
		)
	}
	if spec.expectedRootfsBytes < managedVMRootfsMinBytes ||
		spec.expectedRootfsBytes > managedVMRootfsMaxBytes {
		return managedVMResourceValidationError("managed VM rootfs size is outside the supported range")
	}
	switch spec.cloudInit {
	case cloudInitAbsent, cloudInitPresent:
		return nil
	default:
		return managedVMResourceValidationError("managed cloud-init presence is invalid")
	}
}

func (cache *managedCacheLease) pinVMLaunchResources(
	ctx context.Context,
	spec vmLaunchResourceSpec,
) (_ *vmLaunchResourceLease, returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, err
	}
	if err := validateVMLaunchResourceSpec(spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, managedVMResourceAtomicError("pin managed VM launch resources", err)
	}

	opened := make([]int, 0, 7)
	defer func() {
		if returnErr == nil {
			return
		}
		if closeErr := closeManagedCacheFDs(ctx, cache.deps, opened); closeErr != nil {
			returnErr = appendManagedVMResourceCleanupError(
				returnErr,
				"close rejected managed VM launch resources",
				closeErr,
			)
		}
	}()

	vmsFD, err := openManagedVMResourceDirectory(ctx, cache.deps, cache.cacheFD, "vms", "VM store", cache.ownerUID)
	if err != nil {
		return nil, err
	}
	opened = append(opened, vmsFD)
	vmFD, err := openManagedVMResourceDirectory(
		ctx,
		cache.deps,
		vmsFD,
		string(spec.vmID),
		"VM directory",
		cache.ownerUID,
	)
	if err != nil {
		return nil, err
	}
	opened = append(opened, vmFD)

	rootfsFD, err := openManagedVMResourceFile(
		ctx,
		cache.deps,
		vmFD,
		infra.VMRootfsFilename,
		managedVMRegularFilePolicy{
			name:              "VM rootfs",
			minimumBytes:      managedVMRootfsMinBytes,
			maximumBytes:      managedVMRootfsMaxBytes,
			exactBytes:        spec.expectedRootfsBytes,
			requiredOwnerMode: 0600,
		},
		cache.ownerUID,
	)
	if err != nil {
		return nil, err
	}
	opened = append(opened, rootfsFD)
	configFD, err := openManagedVMResourceFile(
		ctx,
		cache.deps,
		vmFD,
		infra.VMFirecrackerConfigFilename,
		managedVMRegularFilePolicy{
			name:              "Firecracker configuration",
			minimumBytes:      managedVMConfigMinBytes,
			maximumBytes:      managedVMConfigMaxBytes,
			requiredOwnerMode: 0400,
		},
		cache.ownerUID,
	)
	if err != nil {
		return nil, err
	}
	opened = append(opened, configFD)

	cloudInitFD := -1
	if spec.cloudInit == cloudInitPresent {
		cloudInitFD, err = openManagedVMResourceFile(
			ctx,
			cache.deps,
			vmFD,
			infra.VMCloudInitISOFilename,
			managedVMRegularFilePolicy{
				name:              "cloud-init ISO",
				minimumBytes:      managedVMCloudInitMinBytes,
				maximumBytes:      managedVMCloudInitMaxBytes,
				requiredOwnerMode: 0400,
			},
			cache.ownerUID,
		)
		if err != nil {
			return nil, err
		}
		opened = append(opened, cloudInitFD)
	}

	kernelsFD, err := openManagedVMResourceDirectory(
		ctx,
		cache.deps,
		cache.cacheFD,
		"kernels",
		"kernel store",
		cache.ownerUID,
	)
	if err != nil {
		return nil, err
	}
	opened = append(opened, kernelsFD)
	kernelFD, err := openManagedVMResourceFile(
		ctx,
		cache.deps,
		kernelsFD,
		string(spec.kernelID),
		managedVMRegularFilePolicy{
			name:              "kernel",
			minimumBytes:      managedVMKernelMinBytes,
			maximumBytes:      managedVMKernelMaxBytes,
			requiredOwnerMode: 0400,
			allowExecute:      true,
		},
		cache.ownerUID,
	)
	if err != nil {
		return nil, err
	}
	opened = append(opened, kernelFD)

	retained := make([]int, 0, len(cache.retained)+len(opened))
	retained = append(retained, cache.retained...)
	retained = append(retained, opened...)
	lease := &vmLaunchResourceLease{
		deps:          cache.deps,
		cacheIdentity: cache.identity,
		ownerUID:      cache.ownerUID,
		rootfsFD:      rootfsFD,
		configFD:      configFD,
		kernelFD:      kernelFD,
		cloudInitFD:   cloudInitFD,
		retained:      retained,
	}
	cache.retained = nil
	cache.cacheFD = -1
	cache.ownerUID = 0
	cache.identity = managedCacheIdentity{}
	opened = nil
	return lease, nil
}

func validateActiveManagedCacheLease(cache *managedCacheLease) error {
	if cache == nil || cache.cacheFD < 0 || cache.ownerUID == 0 || len(cache.retained) == 0 ||
		cache.deps.openAt2 == nil || cache.deps.statx == nil || cache.deps.close == nil {
		return managedVMResourceAtomicError("managed cache lease is not active", nil)
	}
	return nil
}

func openManagedVMResourceDirectory(
	ctx context.Context,
	deps managedCacheDeps,
	parentFD int,
	component string,
	name string,
	ownerUID uint32,
) (int, error) {
	fd, err := openManagedVMResourceComponent(ctx, deps, parentFD, component, managedVMResourceDirectoryOpenFlags, name)
	if err != nil {
		return -1, err
	}
	if err := inspectManagedVMResourceDirectory(ctx, deps, fd, name, ownerUID); err != nil {
		if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
			err = appendManagedVMResourceCleanupError(err, "close rejected managed VM directory", closeErr)
		}
		return -1, err
	}
	return fd, nil
}

func openManagedVMResourceFile(
	ctx context.Context,
	deps managedCacheDeps,
	parentFD int,
	component string,
	policy managedVMRegularFilePolicy,
	ownerUID uint32,
) (int, error) {
	fd, err := openManagedVMResourceComponent(
		ctx,
		deps,
		parentFD,
		component,
		managedVMResourceFileOpenFlags,
		policy.name,
	)
	if err != nil {
		return -1, err
	}
	if err := inspectManagedVMResourceFile(ctx, deps, fd, policy, ownerUID); err != nil {
		if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
			err = appendManagedVMResourceCleanupError(err, "close rejected managed VM file", closeErr)
		}
		return -1, err
	}
	return fd, nil
}

func openManagedVMResourceComponent(
	ctx context.Context,
	deps managedCacheDeps,
	parentFD int,
	component string,
	flags int,
	name string,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, managedVMResourceAtomicError("open managed "+name, err)
	}
	how := unix.OpenHow{Flags: uint64(flags), Resolve: uint64(managedVMResourceResolveFlags)}
	fd, err := deps.openAt2(ctx, parentFD, component, &how)
	if err != nil {
		return -1, classifyManagedVMResourceOpenError(name, err)
	}
	return fd, nil
}

func inspectManagedVMResourceDirectory(
	ctx context.Context,
	deps managedCacheDeps,
	fd int,
	name string,
	ownerUID uint32,
) error {
	stat, err := statManagedVMResource(ctx, deps, fd, name, managedVMResourceRequiredDirectoryStat)
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return managedVMResourceValidationError("managed " + name + " is not a directory")
	}
	if stat.Uid != ownerUID {
		return managedVMResourceValidationError("managed " + name + " is not owned by the invoking user")
	}
	if stat.Mode&0700 != 0700 {
		return managedVMResourceValidationError("managed " + name + " must grant owner read, write, and traversal")
	}
	if stat.Mode&0022 != 0 {
		return managedVMResourceValidationError("managed " + name + " is writable by another user")
	}
	if stat.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return managedVMResourceValidationError("managed " + name + " has unsupported special mode bits")
	}
	return nil
}

func inspectManagedVMResourceFile(
	ctx context.Context,
	deps managedCacheDeps,
	fd int,
	policy managedVMRegularFilePolicy,
	ownerUID uint32,
) error {
	stat, err := statManagedVMResource(ctx, deps, fd, policy.name, managedVMResourceRequiredFileStat)
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return managedVMResourceValidationError("managed " + policy.name + " is not a regular file")
	}
	if stat.Uid != ownerUID {
		return managedVMResourceValidationError("managed " + policy.name + " is not owned by the invoking user")
	}
	if stat.Nlink != 1 {
		return managedVMResourceValidationError("managed " + policy.name + " must have exactly one link")
	}
	if stat.Mode&policy.requiredOwnerMode != policy.requiredOwnerMode {
		return managedVMResourceValidationError("managed " + policy.name + " does not grant required owner access")
	}
	if stat.Mode&0022 != 0 {
		return managedVMResourceValidationError("managed " + policy.name + " is writable by another user")
	}
	if stat.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return managedVMResourceValidationError("managed " + policy.name + " has unsupported special mode bits")
	}
	if !policy.allowExecute && stat.Mode&0111 != 0 {
		return managedVMResourceValidationError("managed " + policy.name + " has unsupported execute access")
	}
	if stat.Size < policy.minimumBytes || stat.Size > policy.maximumBytes {
		return managedVMResourceValidationError("managed " + policy.name + " size is outside the supported range")
	}
	if policy.exactBytes != 0 && stat.Size != policy.exactBytes {
		return managedVMResourceValidationError("managed " + policy.name + " size does not match the expected size")
	}
	return nil
}

func statManagedVMResource(
	ctx context.Context,
	deps managedCacheDeps,
	fd int,
	name string,
	requiredMask uint32,
) (unix.Statx_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Statx_t{}, managedVMResourceAtomicError("inspect managed "+name, err)
	}
	var stat unix.Statx_t
	if err := deps.statx(ctx, fd, managedVMResourceStatxFlags, managedVMResourceStatxMask, &stat); err != nil {
		return unix.Statx_t{}, managedVMResourceAtomicError("inspect managed "+name, err)
	}
	if stat.Mask&requiredMask != requiredMask || stat.Ino == 0 || (stat.Dev_major == 0 && stat.Dev_minor == 0) {
		return unix.Statx_t{}, managedVMResourceValidationError("managed " + name + " has incomplete stable metadata")
	}
	if stat.Attributes_mask&unix.STATX_ATTR_AUTOMOUNT != 0 && stat.Attributes&unix.STATX_ATTR_AUTOMOUNT != 0 {
		return unix.Statx_t{}, managedVMResourceValidationError(
			"managed " + name + " uses unsupported automount topology",
		)
	}
	return stat, nil
}

func classifyManagedVMResourceOpenError(name string, cause error) error {
	message := "open managed " + name
	if errors.Is(cause, unix.ELOOP) || errors.Is(cause, unix.ENOTDIR) || errors.Is(cause, unix.EXDEV) ||
		errors.Is(cause, unix.ENOENT) || errors.Is(cause, unix.EACCES) {
		return errs.WrapMsg(errs.CodeValidationFailed, message, cause)
	}
	return managedVMResourceAtomicError(message, cause)
}

func (lease *vmLaunchResourceLease) Release(ctx context.Context) error {
	if lease == nil || len(lease.retained) == 0 {
		return nil
	}
	err := closeManagedCacheFDs(ctx, lease.deps, lease.retained)
	lease.retained = nil
	lease.cacheIdentity = managedCacheIdentity{}
	lease.ownerUID = 0
	lease.rootfsFD = -1
	lease.configFD = -1
	lease.kernelFD = -1
	lease.cloudInitFD = -1
	if err != nil {
		return managedVMResourceCleanupError("release managed VM launch resource descriptors", err)
	}
	return nil
}

func appendManagedVMResourceCleanupError(primary error, message string, cause error) error {
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
		domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"] = true
		return domainErr
	}
	return managedVMResourceCleanupError(message+": "+cause.Error(), errors.Join(primary, cause))
}

func managedVMResourceAtomicError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(errs.ClassInternal))
}

func managedVMResourceCleanupError(message string, cause error) *errs.DomainError {
	domainErr := managedVMResourceAtomicError(message, cause)
	domainErr.Details = map[string]any{"managed_vm_resource_descriptor_cleanup_failed": true}
	return domainErr
}

func isCanonicalManagedResourceID(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := 0; index < len(value); index++ {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

func managedVMResourceValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}
