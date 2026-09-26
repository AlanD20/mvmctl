package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// awaitCopiedLaunchExecutable observes readiness only; bindAfterHandoff still performs
// the consuming exact-object admission. The source child remains bound by its retained pidfd.
func (lease *launchLease) awaitCopiedLaunchExecutable(ctx context.Context) error {
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.preparedJailDestination == nil || state.processes == nil || state.record.launch == nil {
		return instanceStateError(state.record.vmID, "launch copy readiness authority is unavailable")
	}
	prepared := state.preparedJailDestination.state
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	processes := state.processes.state
	processes.mu.Lock()
	defer processes.mu.Unlock()
	if prepared.status != preparedJailDestinationHandoff || processes.status != blockedLaunchProcessesReleasedToJailer {
		return instanceStateError(state.record.vmID, "launch child has not received its fixed handoff")
	}
	deps := realProcessIdentityVerifierDeps()
	deadline := deps.now().Add(processExecObservationBudget)
	for deps.now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := requireProcessAlive(ctx, deps.pidfdAlive, processes.firecracker.pidfd, state.record.vmID, "Firecracker"); err != nil {
			return err
		}
		if err := requireExactPIDFDLocator(ctx, deps, processes.firecracker.pidfd, state.record.launch.process, state.record.vmID, "Firecracker"); err != nil {
			return err
		}
		ready, err := copiedLaunchExecutableReady(
			ctx,
			prepared,
			state.record.launch.executionIdentity.gid,
			state.record.launch.executionIdentity.uid,
		)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if err := deps.wait(ctx, processExecObservationInterval); err != nil {
			return err
		}
	}
	return processVerificationError(state.record.vmID, "Jailer copied executable readiness deadline exceeded", nil)
}

func copiedLaunchExecutableReady(
	ctx context.Context,
	state *preparedJailDestinationState,
	gid, uid uint32,
) (ready bool, returnErr error) {
	parent := state.parent.fd
	opened := make([]int, 0, 3)
	defer func() {
		for i := len(opened) - 1; i >= 0; i-- {
			returnErr = appendJailDestinationCleanupError(
				returnErr,
				"close copied executable readiness descriptor",
				state.deps.close(context.WithoutCancel(ctx), opened[i]),
				state.vmID,
			)
		}
	}()
	for _, leaf := range []string{state.vmID, "root", "firecracker"} {
		flags := unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if leaf != "firecracker" {
			flags |= unix.O_DIRECTORY
		}
		fd, err := state.deps.openAt(ctx, parent, leaf, flags, 0)
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if err != nil {
			return false, processAuthorityError(state.vmID, "open copied executable readiness", err)
		}
		opened = append(opened, fd)
		parent = fd
	}
	var stat unix.Stat_t
	if err := state.deps.fstat(ctx, parent, &stat); err != nil {
		return false, err
	}
	return stat.Mode == unix.S_IFREG|0700 && stat.Uid == uid && stat.Gid == gid && stat.Nlink == 1 && stat.Size > 0, nil
}
