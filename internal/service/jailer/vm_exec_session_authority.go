package jailer

import (
	"context"
	"errors"
	"sort"
	"sync"

	"mvmctl/pkg/errs"
)

type vmExecSessionAuthority struct {
	instances    *instanceAuthority
	capacity     *vmExecCapacityAuthority
	recoveryDeps processRecoveryDeps
	identityDeps processIdentityVerifierDeps
}

type vmExecSessionVMAdmission struct {
	vmID             string
	launchGeneration uint64
}

type vmExecSessionAdmission struct {
	reservationID       vmExecReservationID
	ownerUID            uint32
	source              vmExecSessionVMAdmission
	destination         vmExecSessionVMAdmission
	hostBootID          string
	relayStopBoottimeNS uint64
}

type vmExecSessionLease struct {
	state *vmExecSessionLeaseState
}

type vmExecSessionLeaseState struct {
	// mu guards the retained active-runtime and capacity capabilities and every lifecycle field below.
	mu                   sync.Mutex
	roots                *instanceAuthorityRoots
	source               *activeLease
	destination          *activeLease
	sourceProcesses      *reopenedRecordedProcesses
	destinationProcesses *reopenedRecordedProcesses
	capacity             *vmExecCapacityLease
	admission            vmExecSessionAdmission
	admissionValid       bool
	released             bool
	releaseErr           error
}

func newVMExecSessionAuthorityWithDeps(
	instances *instanceAuthority,
	capacity *vmExecCapacityAuthority,
	recoveryDeps processRecoveryDeps,
	identityDeps processIdentityVerifierDeps,
) (*vmExecSessionAuthority, error) {
	if instances == nil || capacity == nil {
		return nil, vmExecSessionError("vm-exec session authority dependencies are incomplete", nil)
	}
	if err := validateProcessRecoveryDeps(recoveryDeps); err != nil {
		return nil, vmExecSessionError("vm-exec session process recovery dependencies are incomplete", err)
	}
	if err := validateProcessIdentityVerifierDeps(identityDeps); err != nil {
		return nil, vmExecSessionError("vm-exec session process identity dependencies are incomplete", err)
	}
	return &vmExecSessionAuthority{
		instances: instances, capacity: capacity, recoveryDeps: recoveryDeps, identityDeps: identityDeps,
	}, nil
}

// reserve admits two exact active runtimes and only then reserves the root-global execution capacity. The global lock
// order is release -> index -> VM locks in lexical VM-ID order -> exec capacity -> network -> firewall. This inert
// authority acquires only the index, VM, and capacity suffix and retains every acquired capability for reverse release.
func (authority *vmExecSessionAuthority) reserve(
	ctx context.Context,
	caller instanceCaller,
	sourceVMID string,
	destinationVMID string,
) (_ *vmExecSessionLease, returnErr error) {
	if authority == nil || authority.instances == nil || authority.capacity == nil {
		return nil, vmExecSessionError("vm-exec session authority is unavailable", nil)
	}
	if ctx == nil {
		return nil, vmExecSessionError("vm-exec session context is required", nil)
	}
	pair, err := authority.instances.LockActivePair(ctx, caller, sourceVMID, destinationVMID)
	if err != nil {
		return nil, err
	}
	roots := pair.roots
	source := pair.source
	destination := pair.destination
	transferred := false
	var sourceProcesses *reopenedRecordedProcesses
	var destinationProcesses *reopenedRecordedProcesses
	defer func() {
		if transferred {
			return
		}
		returnErr = releaseVMExecSessionResources(
			ctx, returnErr, nil, destinationProcesses, sourceProcesses, destination, source, roots,
		)
	}()

	hostBootID, sourceProcesses, destinationProcesses, err := authority.readmitActivePair(ctx, source, destination)
	if err != nil {
		return nil, err
	}
	sourceRecord := source.snapshot()
	destinationRecord := destination.snapshot()
	intent := vmExecCapacityIntent{
		caller:                      vmExecCapacityCaller{ownerUID: caller.uid},
		sourceVMID:                  sourceVMID,
		sourceLaunchGeneration:      sourceRecord.launchGeneration,
		destinationVMID:             destinationVMID,
		destinationLaunchGeneration: destinationRecord.launchGeneration,
	}
	capacity, capacityErr := authority.capacity.reserve(ctx, intent)
	if capacity == nil {
		if capacityErr == nil {
			capacityErr = vmExecSessionError("vm-exec capacity returned no lease", nil)
		}
		return nil, capacityErr
	}
	lease := &vmExecSessionLease{state: &vmExecSessionLeaseState{
		roots: roots, source: source, destination: destination,
		sourceProcesses: sourceProcesses, destinationProcesses: destinationProcesses,
		capacity: capacity,
	}}
	transferred = true
	if capacity.state == nil {
		return lease, joinVMExecSessionErrors(
			capacityErr,
			vmExecSessionError("vm-exec capacity returned an invalid lease", nil),
		)
	}
	capacityRecord := capacity.state.record
	projectionErr := validateVMExecSessionCapacityProjection(capacityRecord, intent, hostBootID)
	relayStopBoottimeNS, stopErr := capacity.relayStopBoottimeNS()
	postProofErr := authority.recheckActivePairAfterCapacity(
		ctx,
		hostBootID,
		sourceProcesses,
		destinationProcesses,
	)
	resultErr := joinVMExecSessionErrors(capacityErr, projectionErr, stopErr, postProofErr)
	if resultErr != nil {
		return lease, resultErr
	}
	lease.state.admission = vmExecSessionAdmission{
		reservationID: capacityRecord.reservationID,
		ownerUID:      caller.uid,
		source: vmExecSessionVMAdmission{
			vmID: sourceVMID, launchGeneration: sourceRecord.launchGeneration,
		},
		destination: vmExecSessionVMAdmission{
			vmID: destinationVMID, launchGeneration: destinationRecord.launchGeneration,
		},
		hostBootID: capacityRecord.hostBootID, relayStopBoottimeNS: relayStopBoottimeNS,
	}
	lease.state.admissionValid = true
	return lease, nil
}

func validateVMExecSessionCapacityProjection(
	record vmExecCapacityRecord,
	intent vmExecCapacityIntent,
	hostBootID string,
) error {
	if err := validateVMExecCapacityRecord(record); err != nil {
		return err
	}
	if record.ownerUID != intent.caller.ownerUID || record.sourceVMID != intent.sourceVMID ||
		record.sourceLaunchGeneration != intent.sourceLaunchGeneration ||
		record.destinationVMID != intent.destinationVMID ||
		record.destinationLaunchGeneration != intent.destinationLaunchGeneration || record.hostBootID != hostBootID {
		return vmExecSessionError("vm-exec capacity does not match the proven active-runtime pair", nil)
	}
	return nil
}

func (authority *vmExecSessionAuthority) recheckActivePairAfterCapacity(
	ctx context.Context,
	hostBootID string,
	source *reopenedRecordedProcesses,
	destination *reopenedRecordedProcesses,
) error {
	if source == nil || destination == nil {
		return vmExecSessionError("vm-exec active-runtime process authority is incomplete", nil)
	}
	if err := source.verifyLocators(ctx); err != nil {
		return err
	}
	if err := destination.verifyLocators(ctx); err != nil {
		return err
	}
	finalBootID, err := authority.identityDeps.readBootID(ctx)
	if err != nil {
		return processVerificationError("", "recheck host boot ID after VM-exec capacity reservation", err)
	}
	if finalBootID != hostBootID {
		return processVerificationError("", "host boot ID changed across VM-exec capacity reservation", nil)
	}
	return nil
}

func (authority *vmExecSessionAuthority) readmitActivePair(
	ctx context.Context,
	source *activeLease,
	destination *activeLease,
) (_ string, _ *reopenedRecordedProcesses, _ *reopenedRecordedProcesses, returnErr error) {
	bootID, err := authority.identityDeps.readBootID(ctx)
	if err != nil {
		return "", nil, nil, processVerificationError("", "read host boot ID for VM-exec session", err)
	}
	if !hostBootIDPattern.MatchString(bootID) {
		return "", nil, nil, processVerificationError("", "current host boot ID for VM-exec session is invalid", nil)
	}
	leases := []*activeLease{source, destination}
	processes := make([]*reopenedRecordedProcesses, 2)
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releaseVMExecSessionProcesses(ctx, returnErr, processes[1], processes[0])
	}()
	for index, lease := range leases {
		record := lease.snapshot()
		if record.launch == nil || record.active == nil || record.launch.hostBootID != bootID {
			return "", nil, nil, processVerificationError(
				record.vmID, "active VM-exec runtime is not on the current host boot", nil,
			)
		}
		recovered, err := reopenRecordedProcesses(ctx, authority.recoveryDeps, record)
		if err != nil {
			return "", nil, nil, err
		}
		processes[index] = recovered
		if err := verifyRecoveredActiveIdentity(ctx, authority.identityDeps, record); err != nil {
			return "", nil, nil, err
		}
		if err := recovered.verifyLocators(ctx); err != nil {
			return "", nil, nil, err
		}
	}
	// CRITICAL: Repeat both pidfd-bound locator/liveness proofs after all expensive identity and digest reads. A stable
	// boot recheck then brackets the whole pair observation before the later capacity lock can be acquired.
	for _, recovered := range processes {
		if err := recovered.verifyLocators(ctx); err != nil {
			return "", nil, nil, err
		}
	}
	finalBootID, err := authority.identityDeps.readBootID(ctx)
	if err != nil {
		return "", nil, nil, processVerificationError("", "recheck host boot ID for VM-exec session", err)
	}
	if finalBootID != bootID {
		return "", nil, nil, processVerificationError(
			"", "host boot ID changed during VM-exec session admission", nil,
		)
	}
	return bootID, processes[0], processes[1], nil
}

func releaseVMExecSessionProcesses(
	ctx context.Context,
	primary error,
	destination *reopenedRecordedProcesses,
	source *reopenedRecordedProcesses,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if destination != nil {
		result = appendVMExecSessionError(
			result,
			"release destination VM-exec process authority",
			destination.Release(cleanupCtx),
		)
	}
	if source != nil {
		result = appendVMExecSessionError(
			result,
			"release source VM-exec process authority",
			source.Release(cleanupCtx),
		)
	}
	return result
}

func (lease *vmExecSessionLease) admission() (vmExecSessionAdmission, error) {
	if lease == nil || lease.state == nil {
		return vmExecSessionAdmission{}, vmExecSessionError("vm-exec session lease is unavailable", nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return vmExecSessionAdmission{}, vmExecSessionError("vm-exec session lease is released", nil)
	}
	if !state.admissionValid {
		return vmExecSessionAdmission{}, vmExecSessionError("vm-exec session admission is incomplete", nil)
	}
	return state.admission, nil
}

// Release drops capacity, destination/source process authorities, active leases in descending lexical VM-ID order,
// and pair roots last. It never reacquires a lock and runs once across copied leases with cancellation detached.
func (lease *vmExecSessionLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	} else {
		state.releaseErr = vmExecSessionError("vm-exec session release context is required", nil)
	}
	state.releaseErr = releaseVMExecSessionResources(
		cleanupCtx,
		state.releaseErr,
		state.capacity,
		state.destinationProcesses,
		state.sourceProcesses,
		state.destination,
		state.source,
		state.roots,
	)
	state.capacity = nil
	state.destinationProcesses = nil
	state.destination = nil
	state.sourceProcesses = nil
	state.source = nil
	state.roots = nil
	state.released = true
	return state.releaseErr
}

func releaseVMExecSessionResources(
	ctx context.Context,
	primary error,
	capacity *vmExecCapacityLease,
	destinationProcesses *reopenedRecordedProcesses,
	sourceProcesses *reopenedRecordedProcesses,
	destination *activeLease,
	source *activeLease,
	roots *instanceAuthorityRoots,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if capacity != nil {
		result = appendVMExecSessionError(result, "release VM-exec capacity", capacity.Release(cleanupCtx))
	}
	if destinationProcesses != nil {
		result = appendVMExecSessionError(
			result,
			"release destination VM-exec process authority",
			destinationProcesses.Release(cleanupCtx),
		)
	}
	if sourceProcesses != nil {
		result = appendVMExecSessionError(
			result,
			"release source VM-exec process authority",
			sourceProcesses.Release(cleanupCtx),
		)
	}
	active := []*activeLease{source, destination}
	sort.Slice(active, func(left, right int) bool {
		return activeLeaseVMID(active[left]) > activeLeaseVMID(active[right])
	})
	for _, candidate := range active {
		if candidate != nil {
			result = appendVMExecSessionError(result, "release active VM-exec runtime", candidate.Release(cleanupCtx))
		}
	}
	if roots != nil {
		result = appendVMExecSessionError(result, "release VM-exec pair authority roots", roots.Release(cleanupCtx))
	}
	return result
}

func activeLeaseVMID(lease *activeLease) string {
	if lease == nil {
		return ""
	}
	return lease.snapshot().vmID
}

func appendVMExecSessionError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	joined := cause
	if primary != nil {
		joined = errors.Join(primary, cause)
	}
	source := errs.AsDomainError(primary)
	if source == nil {
		source = errs.AsDomainError(cause)
	}
	if source == nil {
		return vmExecSessionError("vm-exec session cleanup failed: "+message, joined)
	}
	result := *source
	result.Details = cloneInstanceErrorDetails(source.Details)
	if result.Details == nil {
		result.Details = make(map[string]any, 2)
	}
	result.Details["vm_exec_session_cleanup_failed"] = true
	if _, exists := result.Details["vm_exec_session_cleanup_detail"]; !exists {
		result.Details["vm_exec_session_cleanup_detail"] = boundedInstanceCleanupDetail(message, cause)
	}
	result.Err = joined
	return &result
}

func joinVMExecSessionErrors(primary error, additional ...error) error {
	causes := make([]error, 0, len(additional)+1)
	if primary != nil {
		causes = append(causes, primary)
	}
	causes = append(causes, additional...)
	joined := errors.Join(causes...)
	if joined == nil {
		return nil
	}
	source := errs.AsDomainError(primary)
	if source == nil {
		for _, cause := range additional {
			source = errs.AsDomainError(cause)
			if source != nil {
				break
			}
		}
	}
	if source == nil {
		return vmExecSessionError("vm-exec session admission failed", joined)
	}
	result := *source
	result.Details = cloneInstanceErrorDetails(source.Details)
	result.Err = joined
	return &result
}

func vmExecSessionError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(errs.ClassInternal))
}
