package jailer

import (
	"context"
	"errors"
	"sort"
	"sync"

	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

type instanceAuthority struct {
	deps          instanceAuthorityDeps
	policy        instanceAuthorityPolicy
	authorityTree instanceAuthorityTree
	runtimeDeps   managedVMRuntimeDeps
	cgroupDeps    managedVMCgroupDeps
}

const maxInstanceAuthorityCleanupDetailBytes = 512

type launchLease struct {
	state *launchLeaseState
}

type launchLeaseState struct {
	mu                      sync.Mutex
	directories             *instanceUIDDirectories
	vmLock                  *vmAuthorityLease
	prepared                *preparedRelease
	processes               *blockedLaunchProcesses
	preparedJailDestination *preparedJailDestination
	jailDestination         *jailDestinationLease
	networkNamespace        *topologySelectedNetworkNamespace
	topology                *network.VMTopologyLease
	runtimeDirectory        *managedVMRuntimeLease
	privateRuntimeDirectory *managedVMRuntimeLease
	persistentOutputs       *managedVMPersistentOutputLease
	// A post-replacement error still returns abort authority but must not authorize new output effects.
	registrationComplete    bool
	cgroupDeps              managedVMCgroupDeps
	preparedManagedVMCgroup *preparedManagedVMCgroup
	managedVMCgroup         *managedVMCgroupLease
	record                  instanceRecord
	released                bool
	cleanupErr              error
}

type launchLeaseSnapshot struct {
	record                     instanceRecord
	hasVMLock                  bool
	hasDirectories             bool
	hasProcesses               bool
	hasPreparedJailDestination bool
	hasJailDestination         bool
	hasRuntimeDirectory        bool
	hasPreparedManagedVMCgroup bool
	hasManagedVMCgroup         bool
	prepared                   preparedReleaseSnapshot
	released                   bool
}

type activeLease struct {
	state *activeLeaseState
}

type activeLeaseState struct {
	mu                      sync.Mutex
	roots                   *instanceAuthorityRoots
	directories             *instanceUIDDirectories
	vmLock                  *vmAuthorityLease
	prepared                *preparedRelease
	processes               *blockedLaunchProcesses
	jailDestination         *jailDestinationLease
	networkNamespace        *topologySelectedNetworkNamespace
	topology                *network.VMTopologyLease
	runtimeDirectory        *managedVMRuntimeLease
	privateRuntimeDirectory *managedVMRuntimeLease
	persistentOutputs       *managedVMPersistentOutputLease
	managedVMCgroup         *managedVMCgroupLease
	activationComplete      bool
	detachStarted           bool
	record                  instanceRecord
	released                bool
	releaseErr              error
}

type cleanupLease struct {
	state *cleanupLeaseState
}

type cleanupLeaseState struct {
	mu                      sync.Mutex
	roots                   *instanceAuthorityRoots
	directories             *instanceUIDDirectories
	vmLock                  *vmAuthorityLease
	prepared                *preparedRelease
	processes               *blockedLaunchProcesses
	recoveredProcesses      *reopenedRecordedProcesses
	preparedJailDestination *preparedJailDestination
	jailDestination         *jailDestinationLease
	networkNamespace        *topologySelectedNetworkNamespace
	topology                *network.VMTopologyLease
	runtimeDirectory        *managedVMRuntimeLease
	privateRuntimeDirectory *managedVMRuntimeLease
	persistentOutputs       *managedVMPersistentOutputLease
	runtimeRecovery         *managedVMRuntimeRecovery
	priorBootAbsence        *system.PriorBootVMAbsence
	freshRecoveryAdmission  *freshRecoveryAdmission
	freshRecoveryAdmitting  bool
	preparedManagedVMCgroup *preparedManagedVMCgroup
	managedVMCgroup         *managedVMCgroupLease
	record                  instanceRecord
	released                bool
	releaseErr              error
}

type releaseSlotLease struct {
	roots       *instanceAuthorityRoots
	releaseLock *authorityLock
	slot        releaseSlot
	cgroupDeps  managedVMCgroupDeps
}

type lockedInstance struct {
	roots       *instanceAuthorityRoots
	directories *instanceUIDDirectories
	vmLock      *vmAuthorityLease
	record      instanceRecord
}

type lockedActivePair struct {
	roots       *instanceAuthorityRoots
	source      *activeLease
	destination *activeLease
}

type instanceActivePairCandidate struct {
	vmID        string
	location    *instanceLocation
	directories *instanceUIDDirectories
	vmLock      *vmAuthorityLease
	record      instanceRecord
}

func newInstanceAuthority() *instanceAuthority {
	return newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		productionInstanceAuthorityPolicy(),
		systemInstanceAuthorityTree{},
	)
}

func newInstanceAuthorityWithPolicy(
	deps instanceAuthorityDeps,
	policy instanceAuthorityPolicy,
	authorityTree instanceAuthorityTree,
) *instanceAuthority {
	return &instanceAuthority{
		deps:          deps,
		policy:        policy,
		authorityTree: authorityTree,
		runtimeDeps:   realManagedVMRuntimeDeps(),
		cgroupDeps:    realManagedVMCgroupDeps(),
	}
}

func (authority *instanceAuthority) lockReleaseSlot(
	ctx context.Context,
	slot releaseSlot,
) (_ *releaseSlotLease, returnErr error) {
	if err := validateReleaseSlotValue(slot); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	roots, err := openInstanceAuthorityRoots(ctx, authority.deps, authority.policy, authority.authorityTree)
	if err != nil {
		return nil, err
	}
	var releaseLock *authorityLock
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releaseInstanceOperationResources(
			ctx,
			returnErr,
			nil,
			nil,
			releaseLock,
			nil,
			roots,
		)
	}()
	releaseLock, err = roots.acquireReleaseSlotLock(ctx, slot)
	if err != nil {
		return nil, err
	}
	lease := &releaseSlotLease{
		roots: roots, releaseLock: releaseLock, slot: slot, cgroupDeps: authority.cgroupDeps,
	}
	roots = nil
	releaseLock = nil
	return lease, nil
}

// registerLaunching consumes the prepared release only after the launching record has replaced its predecessor. That
// keeps the exact manifest identity and pinned executable descriptors under the release-slot lock, and leaves no
// caller-shaped seam that could substitute release hashes.
func (prepared *preparedRelease) registerLaunching(
	ctx context.Context,
	caller instanceCaller,
	intent launchIntent,
	processes *blockedLaunchProcesses,
) (_ *launchLease, returnErr error) {
	if prepared == nil || prepared.state == nil {
		return nil, instanceStateError(intent.vmID, "prepared release is not active")
	}
	preparedState := prepared.state
	preparedState.mu.Lock()
	defer preparedState.mu.Unlock()
	if preparedState.status != preparedReleaseOpen || preparedState.slotLease == nil ||
		preparedState.slotLease.roots == nil || !preparedState.slotLease.releaseLock.isActive() ||
		preparedState.store == nil || preparedState.directory == nil || preparedState.admission == nil ||
		preparedState.admission.executables == nil {
		return nil, instanceStateError(intent.vmID, "prepared release is not active")
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if err := validateLaunchIntent(intent); err != nil {
		return nil, err
	}
	processState, locators, err := processes.lockForRegistration(ctx, intent.vmID)
	if err != nil {
		return nil, err
	}
	defer processState.mu.Unlock()
	release := preparedState.admission.identity
	if err := validateReleaseIdentityValue(release); err != nil {
		return nil, instanceAtomicError("prepared release identity is invalid", err)
	}
	if releaseSlotForIdentity(release) != preparedState.slotLease.slot {
		return nil, instanceAtomicError("prepared release identity does not match held release slot", nil)
	}
	var indexLock *authorityLock
	var vmLock *vmAuthorityLease
	var directories *instanceUIDDirectories
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releaseInstanceOperationResources(
			ctx,
			returnErr,
			vmLock,
			indexLock,
			nil,
			directories,
			nil,
		)
	}()

	indexLock, err = preparedState.slotLease.roots.acquireIndexLock(ctx)
	if err != nil {
		return nil, err
	}
	location, err := preparedState.slotLease.roots.findInstance(ctx, intent.vmID)
	if err != nil {
		return nil, err
	}
	if location != nil && location.uid != caller.uid {
		if closeErr := preparedState.slotLease.roots.deps.close(
			context.WithoutCancel(ctx),
			location.stateFD,
		); closeErr != nil {
			return nil, appendInstanceOperationError(
				unauthorizedInstanceError(intent.vmID),
				"close foreign instance authority directory",
				closeErr,
			)
		}
		return nil, unauthorizedInstanceError(intent.vmID)
	}
	var existing *instanceRecord
	if location == nil {
		directories, err = preparedState.slotLease.roots.openUIDDirectories(ctx, caller.uid)
		if err != nil {
			return nil, err
		}
	} else {
		directories, err = preparedState.slotLease.roots.openAuthorityForLocation(ctx, location)
		if err != nil {
			return nil, err
		}
	}
	vmLock, err = directories.acquireVMLock(ctx, intent.vmID)
	if err != nil {
		return nil, err
	}
	if location != nil {
		record, found, readErr := directories.readRecord(ctx, intent.vmID)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			return nil, instanceAtomicError("instance authority record disappeared while locked", nil)
		}
		existing = &record
	}
	if existing != nil {
		if err := authorizeInstanceRecord(caller, *existing); err != nil {
			return nil, err
		}
		if existing.lifecycle != instanceLifecycleCleaned {
			return nil, instanceStateError(intent.vmID, "execution identity allocation requires a cleaned instance")
		}
	}
	execution, err := vmLock.prepareExecutionIdentity(ctx, caller, intent.vmID)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release execution identity allocation", execution.Release(context.WithoutCancel(ctx)))
	}()
	intent.executionIdentity, err = execution.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if err := prepareProtectedVMGrantInventory(ctx, caller.uid); err != nil {
		return nil, err
	}
	record, err := registerLaunchingInstanceRecord(caller, intent, locators, release, existing)
	if err != nil {
		return nil, err
	}
	writeErr := directories.writeRecord(ctx, record)
	if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
		return nil, writeErr
	}
	transferredProcesses := transferBlockedLaunchProcessesLocked(processState)
	launch := &launchLease{
		state: &launchLeaseState{
			directories: directories,
			vmLock:      vmLock,
			cgroupDeps:  preparedState.slotLease.cgroupDeps,
			prepared: &preparedRelease{state: &preparedReleaseState{
				status:    preparedReleaseOpen,
				slotLease: preparedState.slotLease,
				store:     preparedState.store,
				directory: preparedState.directory,
				admission: preparedState.admission,
			}},
			processes: transferredProcesses,
			record:    record,
		},
	}
	preparedState.slotLease = nil
	preparedState.store = nil
	preparedState.directory = nil
	preparedState.admission = nil
	preparedState.status = preparedReleaseTransferred
	directories = nil
	vmLock = nil
	indexReleaseErr := indexLock.Release(context.WithoutCancel(ctx))
	indexLock = nil
	if writeErr != nil {
		return launch, appendInstanceOperationError(
			writeErr,
			"release global index lock after launching record replacement",
			indexReleaseErr,
		)
	}
	if indexReleaseErr != nil {
		return launch, annotateInstanceRecordReplacement(indexReleaseErr, false)
	}
	launch.state.registrationComplete = true
	return launch, nil
}

func instanceRecordWasReplaced(err error) bool {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		return false
	}
	replaced, ok := domainErr.Details["record_replaced"].(bool)
	return ok && replaced
}

func (lease *releaseSlotLease) requireUnreferenced(
	ctx context.Context,
	release releaseIdentity,
) (returnErr error) {
	if lease == nil || lease.roots == nil || !lease.releaseLock.isActive() {
		return instanceStateError("", "release slot lease is not active")
	}
	if err := validateReleaseIdentityValue(release); err != nil {
		return instanceValidationError(err.Error())
	}
	if releaseSlotForIdentity(release) != lease.slot {
		return instanceValidationError("release identity does not match held release slot")
	}
	var indexLock *authorityLock
	defer func() {
		if indexLock != nil {
			returnErr = appendInstanceOperationError(
				returnErr,
				"release global index lock",
				indexLock.Release(context.WithoutCancel(ctx)),
			)
		}
	}()
	indexLock, err := lease.roots.acquireIndexLock(ctx)
	if err != nil {
		return err
	}
	referenced, err := lease.roots.releaseIsReferenced(ctx, release)
	if err != nil {
		return err
	}
	if referenced {
		return errs.New(
			errs.CodeVMStateInvalid,
			"release is referenced by live privileged VM authority",
			errs.WithClass(errs.ClassConflict),
		)
	}
	if err := indexLock.Release(ctx); err != nil {
		indexLock = nil
		return err
	}
	indexLock = nil
	return nil
}

func (authority *instanceAuthority) LockActive(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (*activeLease, error) {
	locked, err := authority.lockOwnedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
	}
	if locked.record.lifecycle != instanceLifecycleActive {
		releaseErr := locked.Release(ctx)
		stateErr := errs.New(
			errs.CodeVMNotRunning,
			"vm has no active privileged runtime",
			errs.WithEntity(vmID),
		)
		return nil, appendInstanceOperationError(stateErr, "release inactive instance lease", releaseErr)
	}
	return &activeLease{
		state: &activeLeaseState{
			roots:       locked.roots,
			directories: locked.directories,
			vmLock:      locked.vmLock,
			record:      locked.record,
		},
	}, nil
}

func (authority *instanceAuthority) LockActivePair(
	ctx context.Context,
	caller instanceCaller,
	sourceVMID string,
	destinationVMID string,
) (_ *lockedActivePair, returnErr error) {
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(sourceVMID) || !vmIDPattern.MatchString(destinationVMID) {
		return nil, instanceValidationError("active pair VM identity is invalid")
	}
	if sourceVMID == destinationVMID {
		return nil, instanceValidationError("active pair requires distinct VMs")
	}
	roots, err := openInstanceAuthorityRoots(ctx, authority.deps, authority.policy, authority.authorityTree)
	if err != nil {
		return nil, err
	}
	var indexLock *authorityLock
	candidates := []*instanceActivePairCandidate{{vmID: sourceVMID}, {vmID: destinationVMID}}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releaseInstanceActivePairCandidates(ctx, returnErr, candidates, indexLock, roots)
	}()
	indexLock, err = roots.acquireIndexLock(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		candidate.location, err = roots.findInstance(ctx, candidate.vmID)
		if err != nil {
			return nil, err
		}
		if candidate.location == nil {
			return nil, errs.NotFound(
				errs.CodeVMNotFound,
				"vm has no privileged authority",
				errs.WithEntity(candidate.vmID),
			)
		}
		if candidate.location.uid != caller.uid {
			return nil, unauthorizedInstanceError(candidate.vmID)
		}
	}
	for _, candidate := range candidates {
		candidate.directories, err = roots.openAuthorityForLocation(ctx, candidate.location)
		candidate.location = nil
		if err != nil {
			return nil, err
		}
	}
	lexical := append([]*instanceActivePairCandidate{}, candidates...)
	sort.Slice(lexical, func(left, right int) bool { return lexical[left].vmID < lexical[right].vmID })
	for _, candidate := range lexical {
		candidate.vmLock, err = candidate.directories.acquireVMLock(ctx, candidate.vmID)
		if err != nil {
			return nil, err
		}
	}
	for _, candidate := range candidates {
		var found bool
		candidate.record, found, err = candidate.directories.readRecord(ctx, candidate.vmID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, instanceAtomicError("instance authority record disappeared while locked", nil)
		}
		if err := authorizeInstanceRecord(caller, candidate.record); err != nil {
			return nil, err
		}
		if candidate.record.lifecycle != instanceLifecycleActive {
			return nil, errs.New(
				errs.CodeVMNotRunning,
				"vm has no active privileged runtime",
				errs.WithEntity(candidate.vmID),
			)
		}
	}
	if err := indexLock.Release(ctx); err != nil {
		indexLock = nil
		return nil, err
	}
	indexLock = nil
	pair := &lockedActivePair{
		roots: roots,
		source: &activeLease{state: &activeLeaseState{
			directories: candidates[0].directories,
			vmLock:      candidates[0].vmLock,
			record:      candidates[0].record,
		}},
		destination: &activeLease{state: &activeLeaseState{
			directories: candidates[1].directories,
			vmLock:      candidates[1].vmLock,
			record:      candidates[1].record,
		}},
	}
	roots = nil
	for _, candidate := range candidates {
		candidate.directories = nil
		candidate.vmLock = nil
	}
	return pair, nil
}

func (authority *instanceAuthority) BeginCleanup(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (*cleanupLease, error) {
	return beginFreshRecoveryCleanupWithDeps(
		ctx,
		authority,
		caller,
		vmID,
		realProcessRecoveryDeps(),
		realProcessIdentityVerifierDeps(),
	)
}

func beginFreshRecoveryCleanupWithDeps(
	ctx context.Context,
	authority *instanceAuthority,
	caller instanceCaller,
	vmID string,
	recoveryDeps processRecoveryDeps,
	identityDeps processIdentityVerifierDeps,
) (_ *cleanupLease, returnErr error) {
	if err := validateProcessRecoveryDeps(recoveryDeps); err != nil {
		return nil, processVerificationError(vmID, "fresh-recovery process dependencies are incomplete", err)
	}
	if err := validateProcessIdentityVerifierDeps(identityDeps); err != nil {
		return nil, processVerificationError(vmID, "fresh-recovery identity dependencies are incomplete", err)
	}
	locked, err := authority.lockOwnedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
	}
	if locked.record.lifecycle == instanceLifecycleCleaned {
		_, cleanupErr := beginInstanceCleanup(caller, locked.record)
		return nil, appendInstanceOperationError(
			cleanupErr,
			"release rejected cleanup lease",
			locked.Release(ctx),
		)
	}
	var recovered *reopenedRecordedProcesses
	var observation processObservation
	defer func() {
		if returnErr == nil || recovered == nil {
			return
		}
		returnErr = appendInstanceOperationError(
			returnErr,
			"release rejected fresh-recovery process authority",
			recovered.Release(context.WithoutCancel(ctx)),
		)
	}()
	if locked.record.launch == nil {
		return nil, appendInstanceOperationError(
			instanceStateError(vmID, "fresh recovery has no recorded launch identity"),
			"release rejected cleanup lease",
			locked.Release(ctx),
		)
	}
	bootID, err := identityDeps.readBootID(ctx)
	if err != nil {
		return nil, appendInstanceOperationError(
			processVerificationError(vmID, "read current host boot ID for fresh recovery", err),
			"release rejected cleanup lease",
			locked.Release(ctx),
		)
	}
	if !hostBootIDPattern.MatchString(bootID) {
		return nil, appendInstanceOperationError(
			processVerificationError(vmID, "current host boot ID for fresh recovery is invalid", nil),
			"release rejected cleanup lease",
			locked.Release(ctx),
		)
	}
	if bootID == locked.record.launch.hostBootID {
		recovered, err = reopenRecordedProcesses(ctx, recoveryDeps, locked.record)
		if err != nil {
			return nil, appendInstanceOperationError(err, "release rejected cleanup lease", locked.Release(ctx))
		}
		if locked.record.active != nil {
			observation, err = verifyRecoveredActiveIdentity(ctx, identityDeps, locked.record)
			if err != nil {
				return nil, appendInstanceOperationError(err, "release rejected cleanup lease", locked.Release(ctx))
			}
		} else {
			observation, err = recovered.observeLaunchingFirecracker(ctx, identityDeps)
			if err != nil {
				return nil, appendInstanceOperationError(err, "release rejected cleanup lease", locked.Release(ctx))
			}
		}
		if err := recovered.verifyLocators(ctx); err != nil {
			return nil, appendInstanceOperationError(err, "release rejected cleanup lease", locked.Release(ctx))
		}
	} else if err := locked.requireFreshRecoveryManagedVMCgroupAbsent(
		ctx,
		managedVMCgroupReadDepsFrom(authority.cgroupDeps),
	); err != nil {
		return nil, appendInstanceOperationError(err, "release rejected cleanup lease", locked.Release(ctx))
	}
	updated, err := beginInstanceCleanup(caller, locked.record)
	if err != nil {
		return nil, appendInstanceOperationError(
			err,
			"release rejected cleanup lease",
			locked.Release(ctx),
		)
	}
	var writeErr error
	if updated != locked.record {
		writeErr = locked.directories.writeRecord(ctx, updated)
		if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
			return nil, appendInstanceOperationError(
				writeErr,
				"release failed cleanup registration",
				locked.Release(ctx),
			)
		}
	}
	cleanupState := &cleanupLeaseState{
		roots:              locked.roots,
		directories:        locked.directories,
		vmLock:             locked.vmLock,
		recoveredProcesses: recovered,
		runtimeRecovery: &managedVMRuntimeRecovery{
			deps:   authority.runtimeDeps,
			policy: managedVMRuntimePolicyFromInstanceAuthority(authority.policy),
		},
		record: updated,
	}
	if recovered != nil {
		cleanupState.freshRecoveryAdmission = &freshRecoveryAdmission{
			sourceLifecycle: locked.record.lifecycle,
			hostBootID:      bootID,
			observation:     observation,
			locator:         locked.record.launch.process,
			cgroupDeps:      authority.cgroupDeps,
			identityDeps:    identityDeps,
			processVerifier: retainedRecoveredCgroupProcessVerifier{
				processes: recovered,
				locator:   locked.record.launch.process,
				vmID:      locked.record.vmID,
			},
		}
	}
	cleanup := &cleanupLease{state: cleanupState}
	locked.roots = nil
	locked.directories = nil
	locked.vmLock = nil
	recovered = nil
	if writeErr != nil {
		return cleanup, writeErr
	}
	if err := cleanup.admitFreshRecovery(ctx); err != nil {
		return cleanup, err
	}
	return cleanup, nil
}

func (lease *cleanupLease) Complete(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "cleanup lease is not active")
	}
	if ctx == nil {
		state := lease.state
		state.mu.Lock()
		defer state.mu.Unlock()
		return instanceStateError(state.record.vmID, "cleanup completion context is required")
	}
	if err := lease.admitFreshRecovery(ctx); err != nil {
		return err
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if ctx == nil {
		return instanceStateError(state.record.vmID, "cleanup completion context is required")
	}
	if state.released || state.vmLock == nil || !state.vmLock.isActive() || state.directories == nil {
		return instanceStateError(state.record.vmID, "cleanup lease is not active")
	}
	if state.freshRecoveryAdmission != nil || state.freshRecoveryAdmitting {
		return instanceStateError(state.record.vmID, "fresh-recovery admission is incomplete")
	}
	if err := resolveCleanupManagedVMRuntimeLocked(ctx, state); err != nil {
		return err
	}
	if err := releaseCleanupRuntimeResourcesLocked(ctx, state); err != nil {
		return err
	}
	updated, err := completeInstanceCleanup(state.record)
	if err != nil {
		return err
	}
	if err := state.directories.writeRecord(ctx, updated); err != nil {
		if domainErr := errs.AsDomainError(err); domainErr != nil {
			if replaced, _ := domainErr.Details["record_replaced"].(bool); replaced {
				state.record = updated
			}
		}
		return err
	}
	state.record = updated
	return nil
}

func releaseCleanupRuntimeResourcesLocked(ctx context.Context, state *cleanupLeaseState) error {
	if state.topology != nil {
		return instanceStateError(state.record.vmID, "retained network topology must complete lifecycle cleanup")
	}
	if state.processes != nil {
		if err := state.processes.terminateAndReap(ctx); err != nil {
			return err
		}
	}
	if state.recoveredProcesses != nil {
		if err := state.recoveredProcesses.terminateAndReap(ctx); err != nil {
			return err
		}
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if err := releaseCleanupManagedVMCgroupLocked(cleanupCtx, state); err != nil {
		return err
	}
	if err := revokeProtectedVMGrants(cleanupCtx, state.record.ownerUID, state.record.vmID, state.record.launch.executionIdentity.uid); err != nil {
		return err
	}
	if err := revokeIsolatedPersistentOutputs(cleanupCtx, state.record); err != nil {
		return err
	}
	if state.jailDestination != nil {
		if err := state.jailDestination.Release(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "release cleanup jail destination authority", err)
		}
		state.jailDestination = nil
	}
	if state.preparedJailDestination != nil {
		if err := state.preparedJailDestination.Release(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "release cleanup prepared jail destination authority", err)
		}
		state.preparedJailDestination = nil
	}
	if state.processes != nil {
		if err := state.processes.Release(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "release cleanup process authority", err)
		}
		state.processes = nil
	}
	if state.recoveredProcesses != nil {
		if err := state.recoveredProcesses.Release(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "release recovered cleanup process authority", err)
		}
		state.recoveredProcesses = nil
	}
	if state.runtimeDirectory != nil {
		if err := state.runtimeDirectory.RemoveFixedLeaves(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "remove cleanup managed VM runtime leaves", err)
		}
		if err := state.runtimeDirectory.Remove(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "remove cleanup managed VM runtime directory", err)
		}
		if err := state.runtimeDirectory.Release(cleanupCtx); err != nil {
			return appendInstanceOperationError(nil, "release cleanup managed VM runtime directory", err)
		}
		state.runtimeDirectory = nil
	}
	if state.privateRuntimeDirectory != nil {
		if err := state.privateRuntimeDirectory.RemoveFixedLeaves(cleanupCtx); err != nil {
			return err
		}
		if err := state.privateRuntimeDirectory.Remove(cleanupCtx); err != nil {
			return err
		}
		if err := state.privateRuntimeDirectory.Release(cleanupCtx); err != nil {
			return err
		}
		state.privateRuntimeDirectory = nil
	}
	return releaseCleanupPersistentOutputsLocked(cleanupCtx, state)
}

func (authority *instanceAuthority) lockOwnedInstance(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (_ *lockedInstance, returnErr error) {
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("vm ID is invalid")
	}
	roots, err := openInstanceAuthorityRoots(ctx, authority.deps, authority.policy, authority.authorityTree)
	if err != nil {
		return nil, err
	}
	var indexLock *authorityLock
	var vmLock *vmAuthorityLease
	var directories *instanceUIDDirectories
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releaseInstanceOperationResources(
			ctx,
			returnErr,
			vmLock,
			indexLock,
			nil,
			directories,
			roots,
		)
	}()
	indexLock, err = roots.acquireIndexLock(ctx)
	if err != nil {
		return nil, err
	}
	location, err := roots.findInstance(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if location == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, "vm has no privileged authority", errs.WithEntity(vmID))
	}
	if location.uid != caller.uid {
		if closeErr := authority.deps.close(context.WithoutCancel(ctx), location.stateFD); closeErr != nil {
			return nil, appendInstanceOperationError(
				unauthorizedInstanceError(vmID),
				"close foreign instance authority directory",
				closeErr,
			)
		}
		return nil, unauthorizedInstanceError(vmID)
	}
	directories, err = roots.openAuthorityForLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	vmLock, err = directories.acquireVMLock(ctx, vmID)
	if err != nil {
		return nil, err
	}
	record, found, err := directories.readRecord(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, instanceAtomicError("instance authority record disappeared while locked", nil)
	}
	if err := authorizeInstanceRecord(caller, record); err != nil {
		return nil, err
	}
	if err := indexLock.Release(ctx); err != nil {
		indexLock = nil
		return nil, err
	}
	indexLock = nil
	locked := &lockedInstance{roots: roots, directories: directories, vmLock: vmLock, record: record}
	roots = nil
	directories = nil
	vmLock = nil
	return locked, nil
}

func unauthorizedInstanceError(vmID string) *errs.DomainError {
	return errs.New(
		errs.CodeUnauthorized,
		"vm privileged authority belongs to another user",
		errs.WithEntity(vmID),
	)
}

func (lease *launchLease) prepareJailDestination(
	ctx context.Context,
	deps jailDestinationDeps,
) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || state.directories == nil || state.processes == nil ||
		state.preparedJailDestination != nil || state.jailDestination != nil {
		return instanceStateError(state.record.vmID, "launch lease cannot prepare a jail destination")
	}
	destination, err := prepareJailDestination(ctx, deps, state.record.vmID)
	if err != nil {
		return err
	}
	state.preparedJailDestination = destination
	return nil
}

func (lease *launchLease) releaseToJailer(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.processes == nil || state.processes.state == nil ||
		state.preparedJailDestination == nil || state.networkNamespace == nil ||
		state.preparedManagedVMCgroup == nil || state.managedVMCgroup != nil {
		return instanceStateError(state.record.vmID, "launch lease is not ready for Jailer handoff")
	}
	if err := state.persistentOutputs.requireReady(); err != nil {
		return err
	}
	destinationState, err := state.preparedJailDestination.lockForHandoff(ctx)
	if err != nil {
		return err
	}
	defer destinationState.mu.Unlock()
	processState := state.processes.state
	processState.mu.Lock()
	defer processState.mu.Unlock()
	err = releaseRetainedLaunchProcessesToJailerLocked(
		ctx,
		processState,
		func(recheckCtx context.Context) error {
			if err := recheckPreparedJailDestinationAbsentLocked(recheckCtx, destinationState); err != nil {
				return err
			}
			return state.preparedManagedVMCgroup.RecheckAbsent(recheckCtx)
		},
	)
	if err != nil {
		return err
	}
	if err := state.preparedManagedVMCgroup.MarkJailerHandoff(); err != nil {
		return err
	}
	destinationState.status = preparedJailDestinationHandoff
	return nil
}

func (lease *launchLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.cleanupErr
	}
	return instanceStateError(state.record.vmID, "live launch authority must be consumed by Abort or activation")
}

// Abort consumes a live launch lease into cleanup only after the cleaning record replaces launching authority.
func (lease *launchLease) Abort(ctx context.Context) (_ *cleanupLease, returnErr error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || state.directories == nil || state.prepared == nil ||
		state.processes == nil {
		return nil, instanceStateError(state.record.vmID, "launch lease is not active")
	}
	if ctx == nil {
		return nil, instanceStateError(state.record.vmID, "launch abort context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("abort launch authority", err)
	}
	if state.record.launch == nil {
		return nil, instanceStateError(state.record.vmID, "launch authority identity is incomplete")
	}
	caller := instanceCaller{uid: state.record.ownerUID, gid: state.record.launch.ownerGID}
	record, err := beginInstanceCleanup(caller, cloneInstanceRecord(state.record))
	if err != nil {
		return nil, err
	}
	writeErr := state.directories.writeRecord(ctx, record)
	if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
		return nil, writeErr
	}
	target := &cleanupLease{state: &cleanupLeaseState{
		directories:             state.directories,
		vmLock:                  state.vmLock,
		prepared:                state.prepared,
		processes:               state.processes,
		preparedJailDestination: state.preparedJailDestination,
		jailDestination:         state.jailDestination,
		networkNamespace:        state.networkNamespace,
		topology:                state.topology,
		runtimeDirectory:        state.runtimeDirectory,
		privateRuntimeDirectory: state.privateRuntimeDirectory,
		persistentOutputs:       state.persistentOutputs,
		preparedManagedVMCgroup: state.preparedManagedVMCgroup,
		managedVMCgroup:         state.managedVMCgroup,
		record:                  record,
	}}
	state.directories = nil
	state.vmLock = nil
	state.prepared = nil
	state.processes = nil
	state.preparedJailDestination = nil
	state.jailDestination = nil
	state.networkNamespace = nil
	state.topology = nil
	state.runtimeDirectory = nil
	state.privateRuntimeDirectory = nil
	state.persistentOutputs = nil
	state.preparedManagedVMCgroup = nil
	state.managedVMCgroup = nil
	state.record = record
	state.released = true
	if writeErr != nil {
		return target, writeErr
	}
	return target, nil
}

func (lease *launchLease) retainTopologyAuthority(
	ctx context.Context,
) (*topologyAuthorityLease, error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "launch lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil {
		return nil, instanceStateError("", "launch lease is not active")
	}
	return state.vmLock.retainTopologyAuthority(ctx)
}

type topologySelectedNetworkNamespace struct {
	identity   instanceObjectIdentity
	production *network.JailerNamespaceLease
}

func (lease *launchLease) retainTopologySelectedNetworkNamespace(
	ctx context.Context,
	topology *topologyAuthorityLease,
	namespace topologySelectedNetworkNamespace,
) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "launch lease is not active")
	}
	if namespace.identity.device == 0 || namespace.identity.inode == 0 {
		return instanceValidationError("topology-selected network namespace identity is invalid")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.vmLock == nil || state.networkNamespace != nil {
		return instanceStateError(state.record.vmID, "launch lease cannot retain a network namespace identity")
	}
	ownerUID, key, err := topology.binding()
	if err != nil {
		return err
	}
	if ownerUID != state.record.ownerUID || key != state.vmLock.state.key {
		return instanceStateError(state.record.vmID, "network namespace topology authority binding is invalid")
	}
	retained := namespace
	state.networkNamespace = &retained
	return nil
}

func (lease *launchLease) snapshot() launchLeaseSnapshot {
	if lease == nil || lease.state == nil {
		return launchLeaseSnapshot{}
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	snapshot := launchLeaseSnapshot{
		record:                     cloneInstanceRecord(state.record),
		hasVMLock:                  state.vmLock != nil,
		hasDirectories:             state.directories != nil,
		hasProcesses:               state.processes != nil,
		hasPreparedJailDestination: state.preparedJailDestination != nil,
		hasJailDestination:         state.jailDestination != nil,
		hasRuntimeDirectory:        state.runtimeDirectory != nil,
		hasPreparedManagedVMCgroup: state.preparedManagedVMCgroup != nil,
		hasManagedVMCgroup:         state.managedVMCgroup != nil,
		released:                   state.released,
	}
	if state.prepared != nil {
		snapshot.prepared = state.prepared.snapshot()
	}
	return snapshot
}

func cloneInstanceRecord(record instanceRecord) instanceRecord {
	if record.launch != nil {
		launch := *record.launch
		launch.cacheLocator.components = append([]string(nil), launch.cacheLocator.components...)
		if launch.outputIdentity != nil {
			outputs := *launch.outputIdentity
			if outputs.metrics != nil {
				metrics := *outputs.metrics
				outputs.metrics = &metrics
			}
			launch.outputIdentity = &outputs
		}
		record.launch = &launch
	}
	if record.active != nil {
		active := *record.active
		if active.supplementaryGroups != nil {
			active.supplementaryGroups = append([]uint32{}, active.supplementaryGroups...)
		}
		if active.relay.supplementaryGroups != nil {
			active.relay.supplementaryGroups = append([]uint32{}, active.relay.supplementaryGroups...)
		}
		record.active = &active
	}
	return record
}

func (lease *activeLease) BeginCleanup(ctx context.Context) (_ *cleanupLease, returnErr error) {
	if lease == nil || lease.state == nil {
		return nil, instanceStateError("", "active lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.detachStarted || state.vmLock == nil || state.directories == nil {
		return nil, instanceStateError(state.record.vmID, "active lease is not active")
	}
	if ctx == nil {
		return nil, instanceStateError(state.record.vmID, "active cleanup context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("begin cleanup of active authority", err)
	}
	if state.record.active == nil {
		return nil, instanceStateError(state.record.vmID, "active authority identity is incomplete")
	}
	caller := instanceCaller{uid: state.record.ownerUID, gid: state.record.launch.ownerGID}
	record, err := beginInstanceCleanup(caller, cloneInstanceRecord(state.record))
	if err != nil {
		return nil, err
	}
	writeErr := state.directories.writeRecord(ctx, record)
	if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
		return nil, writeErr
	}
	target := &cleanupLease{state: &cleanupLeaseState{
		roots:                   state.roots,
		directories:             state.directories,
		vmLock:                  state.vmLock,
		prepared:                state.prepared,
		processes:               state.processes,
		jailDestination:         state.jailDestination,
		networkNamespace:        state.networkNamespace,
		topology:                state.topology,
		runtimeDirectory:        state.runtimeDirectory,
		privateRuntimeDirectory: state.privateRuntimeDirectory,
		persistentOutputs:       state.persistentOutputs,
		managedVMCgroup:         state.managedVMCgroup,
		record:                  record,
	}}
	state.roots = nil
	state.directories = nil
	state.vmLock = nil
	state.prepared = nil
	state.processes = nil
	state.jailDestination = nil
	state.networkNamespace = nil
	state.topology = nil
	state.runtimeDirectory = nil
	state.privateRuntimeDirectory = nil
	state.persistentOutputs = nil
	state.managedVMCgroup = nil
	state.record = record
	state.released = true
	if writeErr != nil {
		return target, writeErr
	}
	return target, nil
}

func (lease *activeLease) snapshot() instanceRecord {
	if lease == nil || lease.state == nil {
		return instanceRecord{}
	}
	lease.state.mu.Lock()
	defer lease.state.mu.Unlock()
	return cloneInstanceRecord(lease.state.record)
}

func (lease *cleanupLease) snapshot() instanceRecord {
	if lease == nil || lease.state == nil {
		return instanceRecord{}
	}
	lease.state.mu.Lock()
	defer lease.state.mu.Unlock()
	return cloneInstanceRecord(lease.state.record)
}

func (lease *activeLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.topology != nil || state.prepared != nil || state.processes != nil || state.jailDestination != nil ||
		state.runtimeDirectory != nil || state.privateRuntimeDirectory != nil || state.managedVMCgroup != nil || state.persistentOutputs != nil {
		return instanceStateError(state.record.vmID, "live active authority must be consumed by BeginCleanup")
	}
	state.releaseErr = releaseInstanceOperationResources(
		ctx,
		nil,
		state.vmLock,
		nil,
		nil,
		state.directories,
		state.roots,
	)
	state.vmLock = nil
	state.directories = nil
	state.roots = nil
	state.released = true
	return state.releaseErr
}

func (lease *cleanupLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if state.freshRecoveryAdmitting {
		return instanceStateError(state.record.vmID, "fresh-recovery admission is in progress")
	}
	if state.record.lifecycle != instanceLifecycleCleaned || state.processes != nil ||
		state.recoveredProcesses != nil ||
		state.preparedJailDestination != nil ||
		state.jailDestination != nil ||
		state.runtimeDirectory != nil ||
		state.privateRuntimeDirectory != nil ||
		state.persistentOutputs != nil ||
		state.runtimeRecovery != nil ||
		state.freshRecoveryAdmission != nil ||
		state.preparedManagedVMCgroup != nil ||
		state.managedVMCgroup != nil {
		return instanceStateError(state.record.vmID, "cleanup must complete before releasing retained authority")
	}
	result := error(nil)
	result = releaseInstanceOperationResources(
		ctx,
		result,
		state.vmLock,
		nil,
		nil,
		state.directories,
		state.roots,
	)
	if state.prepared != nil {
		result = appendInstanceOperationError(
			result,
			"release cleanup prepared release authority",
			state.prepared.Release(context.WithoutCancel(ctx)),
		)
	}
	state.vmLock = nil
	state.directories = nil
	state.roots = nil
	state.prepared = nil
	state.processes = nil
	state.preparedJailDestination = nil
	state.jailDestination = nil
	state.freshRecoveryAdmission = nil
	state.preparedManagedVMCgroup = nil
	state.managedVMCgroup = nil
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}

func (lease *releaseSlotLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	err := releaseInstanceOperationResources(ctx, nil, nil, nil, lease.releaseLock, nil, lease.roots)
	lease.releaseLock = nil
	lease.roots = nil
	return err
}

func (locked *lockedInstance) Release(ctx context.Context) error {
	if locked == nil {
		return nil
	}
	err := releaseInstanceOperationResources(ctx, nil, locked.vmLock, nil, nil, locked.directories, locked.roots)
	locked.vmLock = nil
	locked.directories = nil
	locked.roots = nil
	return err
}

func releaseInstanceActivePairCandidates(
	ctx context.Context,
	primary error,
	candidates []*instanceActivePairCandidate,
	indexLock *authorityLock,
	roots *instanceAuthorityRoots,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	lexical := append([]*instanceActivePairCandidate{}, candidates...)
	sort.Slice(lexical, func(left, right int) bool { return lexical[left].vmID > lexical[right].vmID })
	for _, candidate := range lexical {
		if candidate.vmLock != nil {
			result = appendInstanceOperationError(
				result,
				"release active-pair VM lock",
				candidate.vmLock.Release(cleanupCtx),
			)
			candidate.vmLock = nil
		}
	}
	if indexLock != nil {
		result = appendInstanceOperationError(result, "release active-pair index lock", indexLock.Release(cleanupCtx))
	}
	for index := len(candidates) - 1; index >= 0; index-- {
		candidate := candidates[index]
		if candidate.location != nil {
			result = appendInstanceOperationError(
				result,
				"close located active-pair instance directory",
				roots.deps.close(cleanupCtx, candidate.location.stateFD),
			)
			candidate.location = nil
		}
		if candidate.directories != nil {
			result = appendInstanceOperationError(
				result,
				"release active-pair instance directories",
				candidate.directories.Release(cleanupCtx),
			)
			candidate.directories = nil
		}
	}
	if roots != nil {
		result = appendInstanceOperationError(result, "release active-pair authority roots", roots.Release(cleanupCtx))
	}
	return result
}

func releaseInstanceOperationResources(
	ctx context.Context,
	primary error,
	vmLock *vmAuthorityLease,
	indexLock *authorityLock,
	releaseLock *authorityLock,
	directories *instanceUIDDirectories,
	roots *instanceAuthorityRoots,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if vmLock != nil {
		result = appendInstanceOperationError(result, "release VM lock", vmLock.Release(cleanupCtx))
	}
	if indexLock != nil {
		result = appendInstanceOperationError(result, "release global index lock", indexLock.Release(cleanupCtx))
	}
	if releaseLock != nil {
		result = appendInstanceOperationError(result, "release release lock", releaseLock.Release(cleanupCtx))
	}
	if directories != nil {
		result = appendInstanceOperationError(
			result,
			"release instance UID directories",
			directories.Release(cleanupCtx),
		)
	}
	if roots != nil {
		result = appendInstanceOperationError(
			result,
			"release instance authority roots",
			roots.Release(cleanupCtx),
		)
	}
	return result
}

func appendInstanceOperationError(primary error, message string, cause error) error {
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
	var result *errs.DomainError
	if source == nil {
		result = instanceAtomicError("instance authority cleanup failed", joined)
	} else {
		cloned := *source
		cloned.Details = cloneInstanceErrorDetails(source.Details)
		cloned.Err = joined
		result = &cloned
	}
	if result.Details == nil {
		result.Details = make(map[string]any, 2)
	}
	result.Details["instance_authority_cleanup_failed"] = true
	if _, exists := result.Details["instance_authority_cleanup_detail"]; !exists {
		result.Details["instance_authority_cleanup_detail"] = boundedInstanceCleanupDetail(message, cause)
	}
	return result
}

func cloneInstanceErrorDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details)+2)
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}

func boundedInstanceCleanupDetail(message string, cause error) string {
	detail := message + ": " + cause.Error()
	if len(detail) <= maxInstanceAuthorityCleanupDetailBytes {
		return detail
	}
	const suffix = "..."
	limit := maxInstanceAuthorityCleanupDetailBytes - len(suffix)
	for limit > 0 && detail[limit]&0xc0 == 0x80 {
		limit--
	}
	return detail[:limit] + suffix
}
