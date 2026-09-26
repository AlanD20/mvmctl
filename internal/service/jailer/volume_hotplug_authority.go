package jailer

import (
	"context"
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

type volumeHotplugAuthority struct {
	locked    *lockedInstance
	processes *reopenedRecordedProcesses
	pidfd     int
	root      int
	namespace int
}

func (authority *instanceAuthority) lockVolumeHotplug(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (_ *volumeHotplugAuthority, returnErr error) {
	locked, err := authority.lockOwnedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
	}
	result := &volumeHotplugAuthority{locked: locked, pidfd: -1, root: -1, namespace: -1}
	defer func() {
		if returnErr != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"release rejected hotplug authority",
				result.release(ctx),
			)
		}
	}()
	record := locked.record
	if record.lifecycle != instanceLifecycleActive || record.launch == nil || record.active == nil {
		return nil, instanceStateError(vmID, "volume hotplug requires an active root-owned instance")
	}
	boot, err := readCurrentProcessBootID(ctx)
	if err != nil || boot != record.launch.hostBootID {
		return nil, processVerificationError(vmID, "volume hotplug host boot does not match active authority", err)
	}
	result.processes, err = reopenRecordedProcesses(ctx, realProcessRecoveryDeps(), record)
	if err != nil {
		return nil, err
	}
	for _, process := range result.processes.state.retained {
		if process.role == "Firecracker" {
			result.pidfd = process.pidfd
		}
	}
	if result.pidfd < 0 {
		return nil, instanceStateError(vmID, "volume hotplug has no retained Firecracker pidfd")
	}
	deps := realProcessIdentityVerifierDeps()
	if _, err := verifyRecoveredActiveIdentity(ctx, deps, record); err != nil {
		return nil, err
	}
	processFD, err := deps.openProcessDirectory(ctx, record.launch.process.pid)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, deps.close(context.WithoutCancel(ctx), processFD)) }()
	result.namespace, err = deps.pinProcessObject(ctx, processFD, processMountNamespaceObject)
	if err != nil {
		return nil, err
	}
	result.root, err = deps.pinProcessObject(ctx, processFD, processRootObject)
	if err != nil {
		return nil, err
	}
	if err := result.verify(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (authority *volumeHotplugAuthority) verify(ctx context.Context) error {
	record := authority.locked.record
	if !authority.locked.vmLock.isActive() {
		return instanceStateError(record.vmID, "volume hotplug VM lock is inactive")
	}
	deps := realProcessIdentityVerifierDeps()
	boot, err := deps.readBootID(ctx)
	if err != nil || boot != record.launch.hostBootID {
		return processVerificationError(record.vmID, "volume hotplug boot identity changed", err)
	}
	if err := authority.processes.verifyLocators(ctx); err != nil {
		return err
	}
	if _, err := verifyRecoveredActiveIdentity(ctx, deps, record); err != nil {
		return err
	}
	namespace, err := inspectProcessObject(ctx, deps, authority.namespace, false)
	if err != nil {
		return err
	}
	root, err := inspectProcessObject(ctx, deps, authority.root, true)
	if err != nil {
		return err
	}
	if namespace != record.active.mountNamespace || root != record.active.jailRoot {
		return processVerificationError(
			record.vmID,
			"volume hotplug namespace or root differs from active authority",
			nil,
		)
	}
	return nil
}

func (authority *volumeHotplugAuthority) release(ctx context.Context) error {
	var result error
	if authority.namespace >= 0 {
		result = unix.Close(authority.namespace)
		authority.namespace = -1
	}
	if authority.root >= 0 {
		result = errors.Join(result, unix.Close(authority.root))
		authority.root = -1
	}
	if authority.processes != nil {
		result = errors.Join(result, authority.processes.Release(context.WithoutCancel(ctx)))
		authority.processes = nil
	}
	if authority.locked != nil {
		result = errors.Join(result, authority.locked.Release(context.WithoutCancel(ctx)))
		authority.locked = nil
	}
	return result
}

// runVolumeHotplugMounts keeps namespace entry on one disposable OS thread. The
// caller waits for restoration even after cancellation; a failed restoration
// destroys that thread instead of returning it to the runtime's worker pool.
func (authority *volumeHotplugAuthority) runVolumeHotplugMounts(
	ctx context.Context,
	transaction *volumeHotplugTransaction,
) error {
	if err := authority.verify(ctx); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		host, err := unix.Open("/proc/thread-self/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			runtime.UnlockOSThread()
			done <- err
			return
		}
		if err := unix.Setns(authority.namespace, unix.CLONE_NEWNS); err != nil {
			closeErr := unix.Close(host)
			runtime.UnlockOSThread()
			done <- errors.Join(err, closeErr)
			return
		}
		effectErr := transaction.applyMount(ctx, authority)
		restoreErr := unix.Setns(host, unix.CLONE_NEWNS)
		closeErr := unix.Close(host)
		if restoreErr == nil {
			runtime.UnlockOSThread()
		}
		done <- errors.Join(effectErr, restoreErr, closeErr)
	}()
	return <-done
}
