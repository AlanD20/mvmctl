package jailer

import (
	"context"
	"errors"
	"slices"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const runtimeStatusMaxVMs = 1000

type runtimeStatus string

const (
	runtimeStatusRunning     runtimeStatus = "running"
	runtimeStatusStopped     runtimeStatus = "stopped"
	runtimeStatusCrashed     runtimeStatus = "crashed"
	runtimeStatusUnavailable runtimeStatus = "unavailable"
)

type runtimeStatusProcess struct {
	locator instanceProcessLocator
	pidfd   int
	relay   bool
	status  runtimeStatus
}

func runtimeStatusUnavailableError(vmID, message string, cause error) *errs.DomainError {
	err := processVerificationError(vmID, message, cause)
	err.Details = map[string]any{"runtime_status": string(runtimeStatusUnavailable)}
	return err
}

// observeRuntimeActive compares sealed active metadata without rehashing executables. The pidfds remain open across
// both process observations and the final liveness sweep; a relay observation must not hide a Firecracker exit.
// CALLER MUST HOLD: the root instance index and this record's VM lock.
func observeRuntimeActive(
	ctx context.Context,
	record instanceRecord,
	recovery processRecoveryDeps,
	identity processIdentityVerifierDeps,
) (status runtimeStatus, returnErr error) {
	processes := []runtimeStatusProcess{{locator: record.launch.process, pidfd: -1}}
	if record.launch.relay.kind == instanceRelayPresent {
		processes = append(
			processes,
			runtimeStatusProcess{locator: record.launch.relay.locator, pidfd: -1, relay: true},
		)
	}
	defer func() {
		for index := len(processes) - 1; index >= 0; index-- {
			if processes[index].pidfd >= 0 {
				returnErr = appendProcessVerificationError(returnErr, "close runtime status pidfd",
					recovery.close(context.WithoutCancel(ctx), processes[index].pidfd), record.vmID)
			}
		}
		if returnErr != nil {
			status = runtimeStatusUnavailable
		}
	}()
	status = runtimeStatusRunning
	for index := range processes {
		process := &processes[index]
		fd, err := recovery.pidfdOpen(ctx, process.locator.pid)
		if err != nil {
			if errors.Is(err, unix.ESRCH) {
				process.status = runtimeStatusCrashed
				status = runtimeStatusCrashed
				continue
			}
			return runtimeStatusUnavailable, runtimeStatusUnavailableError(
				record.vmID,
				"open runtime status pidfd",
				err,
			)
		}
		process.pidfd = fd
		flags, err := recovery.descriptorFlags(ctx, fd)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			return runtimeStatusUnavailable, runtimeStatusUnavailableError(
				record.vmID,
				"verify runtime status pidfd flags",
				err,
			)
		}
		process.status, err = checkRuntimeStatusPIDFD(ctx, recovery, *process)
		if err != nil {
			return runtimeStatusUnavailable, runtimeStatusUnavailableError(
				record.vmID,
				"verify runtime status locator",
				err,
			)
		}
		if process.status == runtimeStatusCrashed {
			status = runtimeStatusCrashed
			continue
		}
		var previous observedProcessIdentity
		for pass := range 2 {
			observed, matches, observeErr := observeRuntimeStatusProcess(ctx, identity, record, *process)
			if observeErr != nil {
				// Only a missing proc object followed by a dead retained pidfd proves an exit. Permission, parsing,
				// capability, and close failures remain unavailable even if the process also exits.
				if errors.Is(observeErr, unix.ENOENT) && !runtimeStatusCleanupFailed(observeErr) {
					alive, aliveErr := recovery.pidfdAlive(ctx, fd)
					if aliveErr == nil && !alive {
						process.status = runtimeStatusCrashed
						status = runtimeStatusCrashed
						break
					}
				}
				return runtimeStatusUnavailable, runtimeStatusUnavailableError(
					record.vmID,
					"observe active runtime identity",
					observeErr,
				)
			}
			if !matches || pass > 0 && !observedProcessIdentitiesEqual(previous, observed) {
				status = runtimeStatusCrashed
			}
			previous = observed
		}
	}
	for _, process := range processes {
		if process.pidfd < 0 || process.status == runtimeStatusCrashed {
			continue
		}
		checked, err := checkRuntimeStatusPIDFD(ctx, recovery, process)
		if err != nil {
			return runtimeStatusUnavailable, runtimeStatusUnavailableError(
				record.vmID,
				"recheck runtime status locator",
				err,
			)
		}
		if checked == runtimeStatusCrashed {
			status = runtimeStatusCrashed
		}
	}
	return status, nil
}

func checkRuntimeStatusPIDFD(
	ctx context.Context,
	deps processRecoveryDeps,
	process runtimeStatusProcess,
) (runtimeStatus, error) {
	alive, err := deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return runtimeStatusUnavailable, err
	}
	if !alive {
		return runtimeStatusCrashed, nil
	}
	locator, err := deps.deriveLocator(ctx, process.pidfd)
	if err != nil {
		return runtimeStatusUnavailable, err
	}
	if locator != process.locator {
		return runtimeStatusCrashed, nil
	}
	alive, err = deps.pidfdAlive(ctx, process.pidfd)
	if err != nil {
		return runtimeStatusUnavailable, err
	}
	if !alive {
		return runtimeStatusCrashed, nil
	}
	return runtimeStatusRunning, nil
}

func runtimeStatusCleanupFailed(err error) bool {
	domainErr := errs.AsDomainError(err)
	return domainErr != nil && domainErr.Details["process_identity_cleanup_failed"] == true
}

func observeRuntimeStatusProcess(
	ctx context.Context,
	deps processIdentityVerifierDeps,
	record instanceRecord,
	process runtimeStatusProcess,
) (observed observedProcessIdentity, matches bool, returnErr error) {
	descriptors := processVerificationDescriptors{deps: deps, vmID: record.vmID}
	defer func() {
		returnErr = descriptors.closeAll(ctx, returnErr)
		if returnErr != nil {
			observed = observedProcessIdentity{}
			matches = false
		}
	}()
	directoryFD, err := deps.openProcessDirectory(ctx, process.locator.pid)
	if err != nil {
		return observed, false, err
	}
	descriptors.add(directoryFD)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, directoryFD, "runtime process directory"); err != nil {
		return observed, false, err
	}
	observed.process, err = observeProcessMetadata(ctx, deps, directoryFD, process.locator.pid)
	if err != nil {
		return observed, false, err
	}
	observed.mountNamespace, err = pinObservedObject(
		ctx, deps, &descriptors, directoryFD, processMountNamespaceObject, "runtime mount namespace", false,
	)
	if err != nil {
		return observed, false, err
	}
	observed.networkNamespace, err = pinObservedObject(
		ctx, deps, &descriptors, directoryFD, processNetworkNamespaceObject, "runtime network namespace", false,
	)
	if err != nil {
		return observed, false, err
	}
	rootFD, err := deps.pinProcessObject(ctx, directoryFD, processRootObject)
	if err != nil {
		return observed, false, err
	}
	descriptors.add(rootFD)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, rootFD, "runtime process root"); err != nil {
		return observed, false, err
	}
	var rootStat unix.Stat_t
	if err := deps.fstat(ctx, rootFD, &rootStat); err != nil {
		return observed, false, err
	}
	observed.root = instanceObjectIdentity{device: uint64(rootStat.Dev), inode: rootStat.Ino}
	executableFD, err := deps.pinProcessObject(ctx, directoryFD, processExecutableObject)
	if err != nil {
		return observed, false, err
	}
	descriptors.add(executableFD)
	if err := requireProcessDescriptorCLOEXEC(ctx, deps, executableFD, "runtime executable"); err != nil {
		return observed, false, err
	}
	var executableStat unix.Stat_t
	if err := deps.fstat(ctx, executableFD, &executableStat); err != nil {
		return observed, false, err
	}
	observed.executable = processExecutableDescriptorIdentity{
		device: uint64(executableStat.Dev), inode: executableStat.Ino, sizeBytes: uint64(executableStat.Size),
		mode: executableStat.Mode, uid: executableStat.Uid, gid: executableStat.Gid, links: executableStat.Nlink,
		modified: executableStat.Mtim, changed: executableStat.Ctim,
	}
	if observed.process.startTimeTicks != process.locator.startTimeTicks || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return observed, false, nil
	}
	if process.relay {
		relay := record.active.relay
		return observed, observed.process.userIDs == relay.userIDs && observed.process.groupIDs == relay.groupIDs &&
			slices.Equal(observed.process.supplementaryGroups, relay.supplementaryGroups) &&
			observed.mountNamespace == relay.mountNamespace && observed.root == relay.root &&
			processExecutableMatchesRecordedRelay(observed.executable, relay.executable), nil
	}
	active := record.active
	return observed, observed.process.userIDs == active.userIDs && observed.process.groupIDs == active.groupIDs &&
		slices.Equal(observed.process.supplementaryGroups, active.supplementaryGroups) &&
		observed.process.cgroupPath == infra.CgroupV2Root+"/"+infra.JailerCgroupParent+"/"+record.vmID &&
		observed.mountNamespace == active.mountNamespace && observed.root == active.jailRoot &&
		rootStat.Mode == unix.S_IFDIR|0755 && rootStat.Uid == 0 && rootStat.Gid == 0 &&
		processExecutableMatchesRecordedFirecracker(observed.executable, active.executable), nil
}
