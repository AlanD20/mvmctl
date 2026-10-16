package jailer

import (
	"context"
	"errors"
	"runtime"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
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
	return admitVolumeHotplugAuthority(ctx, locked)
}

func (authority *instanceAuthority) lockVolumeObservation(
	ctx context.Context, caller instanceCaller, vmID string,
) (*volumeHotplugAuthority, error) {
	locked, err := authority.lockObservedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
	}
	return admitVolumeHotplugAuthority(ctx, locked)
}

func admitVolumeHotplugAuthority(
	ctx context.Context,
	locked *lockedInstance,
) (_ *volumeHotplugAuthority, returnErr error) {
	vmID := locked.record.vmID
	var err error
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
// caller waits for restoration even after cancellation. Once CLONE_FS is
// unshared, the locked goroutine exits without unlocking so the runtime retires
// its thread rather than reusing changed filesystem attributes.
func (authority *volumeHotplugAuthority) runVolumeHotplugMounts(
	ctx context.Context,
	transaction *volumeHotplugTransaction,
) error {
	if err := authority.verify(ctx); err != nil {
		return err
	}
	if err := runVolumeHotplugMountWorker(ctx, authority.namespace, transaction); err != nil {
		return err
	}
	return authority.verify(ctx)
}

// runVolumeHotplugMountWorker clones host source mounts before namespace entry,
// then publishes only the detached mount through retained target descriptors.
func runVolumeHotplugMountWorker(
	ctx context.Context,
	namespace int,
	transaction *volumeHotplugTransaction,
) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		host, err := unix.Open("/proc/thread-self/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			runtime.UnlockOSThread()
			done <- instanceAtomicError("open host mount namespace for volume transition", err)
			return
		}
		// Go's worker threads share CLONE_FS. A mount-namespace setns must first
		// detach this thread's filesystem attributes; after that, retire the
		// thread instead of returning it to the runtime's worker pool.
		if err := unix.Unshare(unix.CLONE_FS); err != nil {
			closeErr := unix.Close(host)
			runtime.UnlockOSThread()
			done <- errors.Join(instanceAtomicError("isolate volume worker filesystem attributes", err), closeErr)
			return
		}
		mount, err := transaction.prepareMount(ctx)
		if err != nil {
			done <- errors.Join(err, unix.Close(host))
			return
		}
		closeMount := func() error {
			if mount < 0 {
				return nil
			}
			if err := unix.Close(mount); err != nil {
				return instanceAtomicError("close prepared volume source mount", err)
			}
			return nil
		}
		if err := unix.Setns(namespace, unix.CLONE_NEWNS); err != nil {
			closeErr := unix.Close(host)
			done <- errors.Join(instanceAtomicError("enter retained volume mount namespace", err), closeErr, closeMount())
			return
		}
		effectErr := transaction.applyMount(ctx, mount)
		mountCloseErr := closeMount()
		restoreErr := unix.Setns(host, unix.CLONE_NEWNS)
		closeErr := unix.Close(host)
		if effectErr != nil && errs.AsDomainError(effectErr) == nil {
			effectErr = instanceAtomicError("apply volume mount transition", effectErr)
		}
		if restoreErr != nil {
			restoreErr = instanceAtomicError("restore host mount namespace after volume transition", restoreErr)
		}
		if closeErr != nil {
			closeErr = instanceAtomicError("close host mount namespace after volume transition", closeErr)
		}
		done <- errors.Join(effectErr, mountCloseErr, restoreErr, closeErr)
	}()
	return <-done
}
