package jailer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

type jailDestinationLease struct {
	state *jailDestinationLeaseState
}

type jailDestinationLeaseState struct {
	// mu guards bindings and the complete release lifecycle below. vmID is immutable.
	mu         sync.Mutex
	deps       jailDestinationDeps
	vmID       string
	bindings   []jailDestinationBinding
	sealed     bool
	released   bool
	releaseErr error
}

type jailDestinationBinding struct {
	kind     jailDestinationBindingKind
	fd       int
	identity instanceObjectIdentity
	mount    jailDestinationMountIdentity
}

type jailDestinationMountIdentity struct {
	kind managedCacheMountIdentityKind
	id   uint64
}

type jailDestinationBindingKind uint8

const (
	jailDestinationBase jailDestinationBindingKind = iota + 1
	jailDestinationFirecracker
	jailDestinationVM
	jailDestinationRoot
	jailDestinationExecutable
)

type jailDestinationDeps struct {
	open   func(context.Context, string, int, uint32) (int, error)
	openAt func(context.Context, int, string, int, uint32) (int, error)
	fstat  func(context.Context, int, *unix.Stat_t) error
	statx  func(context.Context, int, int, int, *unix.Statx_t) error
	fchmod func(context.Context, int, uint32) error
	fchown func(context.Context, int, int, int) error
	close  func(context.Context, int) error
}

func retainJailDestination(
	ctx context.Context,
	deps jailDestinationDeps,
	vmID string,
) (_ *jailDestinationLease, returnErr error) {
	if err := validateJailDestinationDeps(deps); err != nil {
		return nil, processAuthorityError(vmID, "jail destination dependencies are incomplete", err)
	}
	if ctx == nil {
		return nil, processAuthorityError(vmID, "jail destination context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, processAuthorityError(vmID, "retain jail destination", err)
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("vm ID is invalid")
	}
	if infra.JailerChrootBase != "/var/lib/mvmctl/jailer" {
		return nil, processAuthorityError(vmID, "Jailer chroot base constant is inconsistent", nil)
	}
	lease := &jailDestinationLease{state: &jailDestinationLeaseState{deps: deps, vmID: vmID, sealed: true}}
	committed := false
	defer func() {
		if committed {
			return
		}
		returnErr = appendJailDestinationCleanupError(
			returnErr,
			"release rejected jail destination authority",
			lease.Release(context.WithoutCancel(ctx)),
			vmID,
		)
	}()

	directoryFlags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	currentFD, err := deps.open(ctx, "/", directoryFlags, 0)
	if err != nil {
		return nil, processAuthorityError(vmID, "open jail destination root", err)
	}
	if err := inspectTrustedJailAncestor(ctx, deps, currentFD, "/"); err != nil {
		primary := processAuthorityError(vmID, "inspect jail destination root", err)
		return nil, appendJailDestinationCleanupError(
			primary,
			"close rejected jail destination root",
			deps.close(context.WithoutCancel(ctx), currentFD),
			vmID,
		)
	}
	for _, leaf := range []string{"var", "lib"} {
		nextFD, openErr := deps.openAt(ctx, currentFD, leaf, directoryFlags, 0)
		if openErr != nil {
			primary := processAuthorityError(vmID, "open jail destination ancestor "+leaf, openErr)
			return nil, appendJailDestinationCleanupError(
				primary,
				"close rejected jail destination ancestor",
				deps.close(context.WithoutCancel(ctx), currentFD),
				vmID,
			)
		}
		inspectErr := inspectTrustedJailAncestor(ctx, deps, nextFD, leaf)
		closeErr := deps.close(context.WithoutCancel(ctx), currentFD)
		if inspectErr != nil || closeErr != nil {
			cleanupErr := deps.close(context.WithoutCancel(ctx), nextFD)
			return nil, processAuthorityError(
				vmID,
				"inspect jail destination ancestor "+leaf,
				errors.Join(inspectErr, closeErr, cleanupErr),
			)
		}
		currentFD = nextFD
	}
	mvmctlFD, err := deps.openAt(ctx, currentFD, "mvmctl", directoryFlags, 0)
	closeLibErr := deps.close(context.WithoutCancel(ctx), currentFD)
	if err != nil || closeLibErr != nil {
		if mvmctlFD >= 0 {
			closeLibErr = errors.Join(closeLibErr, deps.close(context.WithoutCancel(ctx), mvmctlFD))
		}
		return nil, processAuthorityError(vmID, "open shared mvmctl jail ancestor", errors.Join(err, closeLibErr))
	}
	if err := inspectSharedJailStateBase(ctx, deps, mvmctlFD); err != nil {
		return nil, appendJailDestinationCleanupError(
			processAuthorityError(vmID, "inspect shared mvmctl jail ancestor", err),
			"close rejected mvmctl jail ancestor",
			deps.close(context.WithoutCancel(ctx), mvmctlFD),
			vmID,
		)
	}
	managedMount, err := inspectJailDestinationMount(ctx, deps, mvmctlFD)
	if err != nil {
		return nil, appendJailDestinationCleanupError(
			processAuthorityError(vmID, "inspect mvmctl jail ancestor mount identity", err),
			"close rejected mvmctl jail ancestor",
			deps.close(context.WithoutCancel(ctx), mvmctlFD),
			vmID,
		)
	}
	currentFD = mvmctlFD

	baseFD, err := deps.openAt(ctx, currentFD, "jailer", directoryFlags, 0)
	closeAncestorErr := deps.close(context.WithoutCancel(ctx), currentFD)
	if err != nil || closeAncestorErr != nil {
		if baseFD >= 0 {
			closeAncestorErr = errors.Join(closeAncestorErr, deps.close(context.WithoutCancel(ctx), baseFD))
		}
		return nil, processAuthorityError(vmID, "open Jailer base authority", errors.Join(err, closeAncestorErr))
	}
	if err := lease.addBinding(ctx, jailDestinationBase, baseFD, unix.S_IFDIR|0700, 0, 0, true); err != nil {
		return nil, err
	}
	if lease.state.bindings[0].mount != managedMount {
		return nil, processAuthorityError(vmID, "Jailer base is a substituted mount", nil)
	}
	if err := lease.openDirectoryBinding(
		ctx,
		jailDestinationFirecracker,
		"firecracker",
		unix.S_IFDIR|0700,
	); err != nil {
		return nil, err
	}
	if err := lease.openDirectoryBinding(ctx, jailDestinationVM, vmID, unix.S_IFDIR|0700); err != nil {
		return nil, err
	}
	if err := lease.openDirectoryBinding(ctx, jailDestinationRoot, "root", unix.S_IFDIR|0755); err != nil {
		return nil, err
	}
	parentFD := lease.state.bindings[len(lease.state.bindings)-1].fd
	executableFD, err := deps.openAt(
		ctx,
		parentFD,
		"firecracker",
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, processAuthorityError(vmID, "open copied Firecracker authority", err)
	}
	if err := lease.addBinding(ctx, jailDestinationExecutable, executableFD, unix.S_IFREG|0755, 0, 0, false); err != nil {
		return nil, err
	}
	committed = true
	return lease, nil
}

func validateJailDestinationDeps(deps jailDestinationDeps) error {
	if deps.open == nil || deps.openAt == nil || deps.fstat == nil || deps.statx == nil || deps.close == nil {
		return fmt.Errorf("one or more jail destination effects are unavailable")
	}
	return nil
}

func (lease *jailDestinationLease) openDirectoryBinding(
	ctx context.Context,
	kind jailDestinationBindingKind,
	leaf string,
	expectedMode uint32,
) error {
	return lease.openDirectoryBindingWithFlags(
		ctx,
		kind,
		leaf,
		expectedMode,
		0,
		0,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
	)
}

func (lease *jailDestinationLease) openDirectoryBindingWithFlags(
	ctx context.Context,
	kind jailDestinationBindingKind,
	leaf string,
	expectedMode uint32,
	expectedUID, expectedGID uint32,
	flags int,
) error {
	parentFD := lease.state.bindings[len(lease.state.bindings)-1].fd
	fd, err := lease.state.deps.openAt(ctx, parentFD, leaf, flags, 0)
	if err != nil {
		return processAuthorityError(lease.state.vmID, "open jail destination binding "+leaf, err)
	}
	return lease.addBinding(ctx, kind, fd, expectedMode, expectedUID, expectedGID, true)
}

func (lease *jailDestinationLease) addBinding(
	ctx context.Context,
	kind jailDestinationBindingKind,
	fd int,
	expectedMode uint32,
	expectedUID, expectedGID uint32,
	requireDirectory bool,
) error {
	lease.state.bindings = append(lease.state.bindings, jailDestinationBinding{kind: kind, fd: fd})
	var stat unix.Stat_t
	if err := lease.state.deps.fstat(ctx, fd, &stat); err != nil {
		return processAuthorityError(lease.state.vmID, "inspect jail destination binding", err)
	}
	if stat.Dev == 0 || stat.Ino == 0 || stat.Mode != expectedMode ||
		stat.Uid != expectedUID || stat.Gid != expectedGID {
		return processAuthorityError(lease.state.vmID, "jail destination binding metadata is invalid", nil)
	}
	if requireDirectory && stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return processAuthorityError(lease.state.vmID, "jail destination binding is not a directory", nil)
	}
	if !requireDirectory && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size <= 0 || stat.Nlink != 1) {
		return processAuthorityError(lease.state.vmID, "copied Firecracker metadata is invalid", nil)
	}
	mount, err := inspectJailDestinationMount(ctx, lease.state.deps, fd)
	if err != nil {
		return processAuthorityError(lease.state.vmID, "inspect jail destination mount identity", err)
	}
	if len(lease.state.bindings) > 1 && mount != lease.state.bindings[len(lease.state.bindings)-2].mount {
		return processAuthorityError(lease.state.vmID, "jail destination contains a substituted mount", nil)
	}
	lease.state.bindings[len(lease.state.bindings)-1].identity = instanceObjectIdentity{
		device: uint64(stat.Dev), inode: stat.Ino,
	}
	lease.state.bindings[len(lease.state.bindings)-1].mount = mount
	return nil
}

func inspectJailDestinationMount(
	ctx context.Context,
	deps jailDestinationDeps,
	fd int,
) (jailDestinationMountIdentity, error) {
	var stat unix.Statx_t
	mask := unix.STATX_BASIC_STATS | unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
	flags := unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW | unix.AT_STATX_FORCE_SYNC
	if err := deps.statx(ctx, fd, flags, mask, &stat); err != nil {
		return jailDestinationMountIdentity{}, err
	}
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
		if stat.Mnt_id == 0 {
			return jailDestinationMountIdentity{}, fmt.Errorf("unique mount identity is invalid")
		}
		return jailDestinationMountIdentity{kind: managedCacheMountIdentityUnique, id: stat.Mnt_id}, nil
	case stat.Mask&unix.STATX_MNT_ID != 0:
		if stat.Mnt_id == 0 {
			return jailDestinationMountIdentity{}, fmt.Errorf("legacy mount identity is invalid")
		}
		return jailDestinationMountIdentity{kind: managedCacheMountIdentityLegacy, id: stat.Mnt_id}, nil
	default:
		return jailDestinationMountIdentity{}, fmt.Errorf("mount identity is unavailable")
	}
}

func inspectTrustedJailAncestor(ctx context.Context, deps jailDestinationDeps, fd int, name string) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0022 != 0 {
		return fmt.Errorf("trusted jail ancestor %s has invalid metadata", name)
	}
	return nil
}

func inspectExactJailAncestor(ctx context.Context, deps jailDestinationDeps, fd int, name string) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return err
	}
	if stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 {
		return fmt.Errorf("trusted jail ancestor %s is not exact root:root mode 0700", name)
	}
	return nil
}

func inspectSharedJailStateBase(ctx context.Context, deps jailDestinationDeps, fd int) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return err
	}
	// CRITICAL: ADR-0016 admits existing shared-base metadata unchanged; private jail ancestors
	// retain their exact root:root mode 0700 policy.
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Mode&0022 != 0 {
		return fmt.Errorf("shared jail state base mvmctl has invalid metadata")
	}
	return nil
}

func (lease *jailDestinationLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := processCleanupContext(ctx)
	result := error(nil)
	for index := len(state.bindings) - 1; index >= 0; index-- {
		result = appendJailDestinationCleanupError(
			result,
			"close jail destination descriptor",
			state.deps.close(cleanupCtx, state.bindings[index].fd),
			state.vmID,
		)
	}
	state.bindings = nil
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}

func appendJailDestinationCleanupError(primary error, message string, cause error, vmID string) error {
	if cause == nil {
		return primary
	}
	combined := appendBlockedLaunchProcessCleanupError(primary, message, cause, vmID)
	domain := errs.AsDomainError(combined)
	if domain == nil {
		return combined
	}
	cloned := *domain
	cloned.Details = cloneInstanceErrorDetails(domain.Details)
	delete(cloned.Details, "blocked_launch_process_cleanup_failed")
	delete(cloned.Details, "blocked_launch_process_cleanup_detail")
	cloned.Details["jail_destination_cleanup_failed"] = true
	cloned.Details["jail_destination_cleanup_detail"] = boundedInstanceCleanupDetail(message, cause)
	return &cloned
}
