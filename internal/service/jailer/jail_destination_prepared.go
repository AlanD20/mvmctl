package jailer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type preparedJailDestination struct {
	state *preparedJailDestinationState
}

type preparedJailDestinationState struct {
	// mu guards the retained parent binding and the complete absence/handoff lifecycle below.
	mu         sync.Mutex
	deps       jailDestinationDeps
	vmID       string
	parent     jailDestinationBinding
	status     preparedJailDestinationStatus
	releaseErr error
}

type preparedJailDestinationStatus uint8

const (
	preparedJailDestinationAbsent preparedJailDestinationStatus = iota + 1
	preparedJailDestinationHandoff
	preparedJailDestinationTransferred
	preparedJailDestinationReleased
)

func prepareJailDestination(
	ctx context.Context,
	deps jailDestinationDeps,
	vmID string,
) (_ *preparedJailDestination, returnErr error) {
	if err := validatePreparedJailDestinationDeps(deps); err != nil {
		return nil, processAuthorityError(vmID, "prepared jail destination dependencies are incomplete", err)
	}
	if ctx == nil {
		return nil, processAuthorityError(vmID, "prepared jail destination context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, processAuthorityError(vmID, "prepare jail destination", err)
	}
	if !vmIDPattern.MatchString(vmID) || infra.JailerChrootBase != "/var/lib/mvmctl/jailer" {
		return nil, instanceValidationError("jail destination identity is invalid")
	}
	directoryFlags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	currentFD, err := deps.open(ctx, "/", directoryFlags, 0)
	if err != nil {
		return nil, processAuthorityError(vmID, "open jail destination root", err)
	}
	closeCurrent := func(primary error) error {
		return appendJailDestinationCleanupError(
			primary,
			"close prepared jail destination traversal",
			deps.close(context.WithoutCancel(ctx), currentFD),
			vmID,
		)
	}
	if err := inspectTrustedJailAncestor(ctx, deps, currentFD, "/"); err != nil {
		return nil, closeCurrent(processAuthorityError(vmID, "inspect jail destination root", err))
	}
	for _, leaf := range []string{"var", "lib"} {
		nextFD, openErr := deps.openAt(ctx, currentFD, leaf, directoryFlags, 0)
		if openErr != nil {
			return nil, closeCurrent(processAuthorityError(vmID, "open jail destination ancestor "+leaf, openErr))
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
	for _, leaf := range []string{"mvmctl", "jailer", "firecracker"} {
		nextFD, openErr := deps.openAt(ctx, currentFD, leaf, directoryFlags, 0)
		if openErr != nil {
			return nil, closeCurrent(processAuthorityError(vmID, "open exact jail destination ancestor "+leaf, openErr))
		}
		inspectErr := inspectExactJailAncestor(ctx, deps, nextFD, leaf)
		closeErr := deps.close(context.WithoutCancel(ctx), currentFD)
		if inspectErr != nil || closeErr != nil {
			cleanupErr := deps.close(context.WithoutCancel(ctx), nextFD)
			return nil, processAuthorityError(
				vmID,
				"inspect exact jail destination ancestor "+leaf,
				errors.Join(inspectErr, closeErr, cleanupErr),
			)
		}
		currentFD = nextFD
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, currentFD, &stat); err != nil {
		return nil, closeCurrent(processAuthorityError(vmID, "inspect retained jail destination parent", err))
	}
	prepared := &preparedJailDestination{state: &preparedJailDestinationState{
		deps: deps,
		vmID: vmID,
		parent: jailDestinationBinding{
			kind: jailDestinationFirecracker,
			fd:   currentFD,
			identity: instanceObjectIdentity{
				device: uint64(stat.Dev), inode: stat.Ino,
			},
		},
		status: preparedJailDestinationAbsent,
	}}
	if err := prepared.recheckAbsent(ctx); err != nil {
		return nil, appendJailDestinationCleanupError(
			err,
			"release rejected prepared jail destination",
			prepared.Release(context.WithoutCancel(ctx)),
			vmID,
		)
	}
	return prepared, nil
}

func validatePreparedJailDestinationDeps(deps jailDestinationDeps) error {
	if err := validateJailDestinationDeps(deps); err != nil {
		return err
	}
	if deps.fchmod == nil || deps.fchown == nil {
		return fmt.Errorf("jail destination sealing effects are unavailable")
	}
	return nil
}

func (prepared *preparedJailDestination) recheckAbsent(ctx context.Context) error {
	if prepared == nil || prepared.state == nil {
		return processAuthorityError("", "prepared jail destination is not active", nil)
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return recheckPreparedJailDestinationAbsentLocked(ctx, state)
}

func recheckPreparedJailDestinationAbsentLocked(ctx context.Context, state *preparedJailDestinationState) error {
	if state.status != preparedJailDestinationAbsent || state.parent.fd < 0 {
		return processAuthorityError(state.vmID, "prepared jail destination is not awaiting handoff", nil)
	}
	var stat unix.Stat_t
	if err := state.deps.fstat(ctx, state.parent.fd, &stat); err != nil {
		return processAuthorityError(state.vmID, "reinspect jail destination parent", err)
	}
	if uint64(stat.Dev) != state.parent.identity.device || stat.Ino != state.parent.identity.inode ||
		stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 {
		return processAuthorityError(state.vmID, "jail destination parent changed", nil)
	}
	fd, err := state.deps.openAt(
		ctx,
		state.parent.fd,
		state.vmID,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err == nil {
		closeErr := state.deps.close(context.WithoutCancel(ctx), fd)
		return appendJailDestinationCleanupError(
			processAuthorityError(state.vmID, "jail destination is not absent", nil),
			"close unexpected jail destination",
			closeErr,
			state.vmID,
		)
	}
	if !errors.Is(err, unix.ENOENT) {
		return processAuthorityError(state.vmID, "prove jail destination absence", err)
	}
	return nil
}

func (prepared *preparedJailDestination) lockForHandoff(
	ctx context.Context,
) (*preparedJailDestinationState, error) {
	if prepared == nil || prepared.state == nil {
		return nil, processAuthorityError("", "prepared jail destination is not active", nil)
	}
	state := prepared.state
	state.mu.Lock()
	if err := recheckPreparedJailDestinationAbsentLocked(ctx, state); err != nil {
		state.mu.Unlock()
		return nil, err
	}
	return state, nil
}

func (prepared *preparedJailDestination) bindAfterHandoff(
	ctx context.Context,
	ownerUID uint32,
	ownerGID uint32,
) (_ *jailDestinationLease, returnErr error) {
	if prepared == nil || prepared.state == nil {
		return nil, processAuthorityError("", "prepared jail destination is not active", nil)
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status != preparedJailDestinationHandoff || state.parent.fd < 0 {
		return nil, processAuthorityError(state.vmID, "jail destination handoff is not complete", nil)
	}
	lease := &jailDestinationLease{state: &jailDestinationLeaseState{
		deps: state.deps,
		vmID: state.vmID,
		bindings: []jailDestinationBinding{{
			kind: state.parent.kind, fd: state.parent.fd, identity: state.parent.identity,
		}},
	}}
	committed := false
	defer func() {
		if committed {
			return
		}
		returnErr = appendJailDestinationCleanupError(
			returnErr,
			"release rejected bound jail destination",
			lease.Release(context.WithoutCancel(ctx)),
			state.vmID,
		)
	}()
	if err := lease.openDirectoryBinding(ctx, jailDestinationVM, state.vmID, unix.S_IFDIR|0700); err != nil {
		return nil, err
	}
	if err := lease.openDirectoryBinding(ctx, jailDestinationRoot, "root", unix.S_IFDIR|0755); err != nil {
		return nil, err
	}
	rootFD := lease.state.bindings[len(lease.state.bindings)-1].fd
	executableFD, err := state.deps.openAt(
		ctx,
		rootFD,
		"firecracker",
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, processAuthorityError(state.vmID, "open copied Firecracker authority", err)
	}
	lease.state.bindings = append(
		lease.state.bindings,
		jailDestinationBinding{kind: jailDestinationExecutable, fd: executableFD},
	)
	var executableStat unix.Stat_t
	if err := state.deps.fstat(ctx, executableFD, &executableStat); err != nil {
		return nil, processAuthorityError(state.vmID, "inspect copied Firecracker before sealing", err)
	}
	if executableStat.Dev == 0 || executableStat.Ino == 0 || executableStat.Mode != unix.S_IFREG|0755 ||
		executableStat.Uid != ownerUID || executableStat.Gid != ownerGID || executableStat.Nlink != 1 ||
		executableStat.Size <= 0 || uint64(executableStat.Size) > maxProcessSystemExecutableBytes {
		return nil, processAuthorityError(state.vmID, "copied Firecracker pre-seal metadata is invalid", nil)
	}
	if err := state.deps.fchown(ctx, rootFD, 0, 0); err != nil {
		return nil, processAuthorityError(state.vmID, "seal jail root owner", err)
	}
	if err := state.deps.fchmod(ctx, rootFD, 0755); err != nil {
		return nil, processAuthorityError(state.vmID, "seal jail root mode", err)
	}
	if err := state.deps.fchown(ctx, executableFD, 0, 0); err != nil {
		return nil, processAuthorityError(state.vmID, "seal copied Firecracker owner", err)
	}
	if err := state.deps.fchmod(ctx, executableFD, 0755); err != nil {
		return nil, processAuthorityError(state.vmID, "seal copied Firecracker mode", err)
	}
	if err := lease.reinspectBindings(ctx); err != nil {
		return nil, err
	}
	state.parent = jailDestinationBinding{fd: -1}
	state.status = preparedJailDestinationTransferred
	committed = true
	return lease, nil
}

func (lease *jailDestinationLease) reinspectBindings(ctx context.Context) error {
	for index := range lease.state.bindings {
		binding := &lease.state.bindings[index]
		var stat unix.Stat_t
		if err := lease.state.deps.fstat(ctx, binding.fd, &stat); err != nil {
			return processAuthorityError(lease.state.vmID, "reinspect sealed jail destination binding", err)
		}
		switch binding.kind {
		case jailDestinationFirecracker, jailDestinationVM:
			if stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 {
				return processAuthorityError(lease.state.vmID, "sealed Jailer ancestor metadata is invalid", nil)
			}
		case jailDestinationRoot:
			if stat.Mode != unix.S_IFDIR|0755 || stat.Uid != 0 || stat.Gid != 0 {
				return processAuthorityError(lease.state.vmID, "sealed jail root metadata is invalid", nil)
			}
		case jailDestinationExecutable:
			if stat.Mode != unix.S_IFREG|0755 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size <= 0 {
				return processAuthorityError(lease.state.vmID, "sealed copied Firecracker metadata is invalid", nil)
			}
		default:
			return processAuthorityError(lease.state.vmID, "sealed jail destination binding kind is invalid", nil)
		}
		binding.identity = instanceObjectIdentity{device: uint64(stat.Dev), inode: stat.Ino}
	}
	return nil
}

func (prepared *preparedJailDestination) Release(ctx context.Context) error {
	if prepared == nil || prepared.state == nil {
		return nil
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status == preparedJailDestinationReleased || state.status == preparedJailDestinationTransferred {
		return state.releaseErr
	}
	state.releaseErr = appendJailDestinationCleanupError(
		nil,
		"close prepared jail destination parent",
		state.deps.close(processCleanupContext(ctx), state.parent.fd),
		state.vmID,
	)
	state.parent = jailDestinationBinding{fd: -1}
	state.status = preparedJailDestinationReleased
	return state.releaseErr
}
