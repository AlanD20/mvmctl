package jailer

import (
	"context"
	"errors"
	"sync"

	"mvmctl/pkg/errs"
)

type instanceAuthority struct {
	deps          instanceAuthorityDeps
	policy        instanceAuthorityPolicy
	authorityTree instanceAuthorityTree
}

const maxInstanceAuthorityCleanupDetailBytes = 512

type launchLease struct {
	state *launchLeaseState
}

type launchLeaseState struct {
	mu          sync.Mutex
	directories *instanceUIDDirectories
	vmLock      *authorityLock
	prepared    *preparedRelease
	record      instanceRecord
	released    bool
	cleanupErr  error
}

type launchLeaseSnapshot struct {
	record         instanceRecord
	hasVMLock      bool
	hasDirectories bool
	prepared       preparedReleaseSnapshot
	released       bool
}

type activeLease struct {
	roots       *instanceAuthorityRoots
	directories *instanceUIDDirectories
	vmLock      *authorityLock
	record      instanceRecord
}

type cleanupLease struct {
	roots       *instanceAuthorityRoots
	directories *instanceUIDDirectories
	vmLock      *authorityLock
	record      instanceRecord
}

type releaseSlotLease struct {
	roots       *instanceAuthorityRoots
	releaseLock *authorityLock
	slot        releaseSlot
}

type lockedInstance struct {
	roots       *instanceAuthorityRoots
	directories *instanceUIDDirectories
	vmLock      *authorityLock
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
	return &instanceAuthority{deps: deps, policy: policy, authorityTree: authorityTree}
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
	lease := &releaseSlotLease{roots: roots, releaseLock: releaseLock, slot: slot}
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
	release := preparedState.admission.identity
	if err := validateReleaseIdentityValue(release); err != nil {
		return nil, instanceAtomicError("prepared release identity is invalid", err)
	}
	if releaseSlotForIdentity(release) != preparedState.slotLease.slot {
		return nil, instanceAtomicError("prepared release identity does not match held release slot", nil)
	}
	var indexLock, vmLock *authorityLock
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

	indexLock, err := preparedState.slotLease.roots.acquireIndexLock(ctx)
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
	record, err := registerLaunchingInstanceRecord(caller, intent, release, existing)
	if err != nil {
		return nil, err
	}
	writeErr := directories.writeRecord(ctx, record)
	if writeErr != nil && !instanceRecordWasReplaced(writeErr) {
		return nil, writeErr
	}
	launch := &launchLease{
		state: &launchLeaseState{
			directories: directories,
			vmLock:      vmLock,
			prepared: &preparedRelease{state: &preparedReleaseState{
				status:    preparedReleaseOpen,
				slotLease: preparedState.slotLease,
				store:     preparedState.store,
				directory: preparedState.directory,
				admission: preparedState.admission,
			}},
			record: record,
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
		roots:       locked.roots,
		directories: locked.directories,
		vmLock:      locked.vmLock,
		record:      locked.record,
	}, nil
}

func (authority *instanceAuthority) BeginCleanup(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (*cleanupLease, error) {
	locked, err := authority.lockOwnedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
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
	cleanup := &cleanupLease{
		roots:       locked.roots,
		directories: locked.directories,
		vmLock:      locked.vmLock,
		record:      updated,
	}
	if writeErr != nil {
		return cleanup, writeErr
	}
	return cleanup, nil
}

func (lease *cleanupLease) Complete(ctx context.Context) error {
	if lease == nil || !lease.vmLock.isActive() {
		return instanceStateError("", "cleanup lease is not active")
	}
	updated, err := completeInstanceCleanup(lease.record)
	if err != nil {
		return err
	}
	if err := lease.directories.writeRecord(ctx, updated); err != nil {
		if domainErr := errs.AsDomainError(err); domainErr != nil {
			if replaced, _ := domainErr.Details["record_replaced"].(bool); replaced {
				lease.record = updated
			}
		}
		return err
	}
	lease.record = updated
	return nil
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
	var indexLock, vmLock *authorityLock
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
	state.cleanupErr = releaseInstanceOperationResources(
		ctx,
		nil,
		state.vmLock,
		nil,
		nil,
		state.directories,
		nil,
	)
	if state.prepared != nil {
		state.cleanupErr = appendInstanceOperationError(
			state.cleanupErr,
			"release launching prepared release authority",
			state.prepared.Release(context.WithoutCancel(ctx)),
		)
	}
	state.vmLock = nil
	state.directories = nil
	state.prepared = nil
	state.released = true
	return state.cleanupErr
}

func (lease *launchLease) snapshot() launchLeaseSnapshot {
	if lease == nil || lease.state == nil {
		return launchLeaseSnapshot{}
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	snapshot := launchLeaseSnapshot{
		record:         cloneInstanceRecord(state.record),
		hasVMLock:      state.vmLock != nil,
		hasDirectories: state.directories != nil,
		released:       state.released,
	}
	if state.prepared != nil {
		snapshot.prepared = state.prepared.snapshot()
	}
	return snapshot
}

func cloneInstanceRecord(record instanceRecord) instanceRecord {
	if record.launch != nil {
		launch := *record.launch
		record.launch = &launch
	}
	if record.active != nil {
		active := *record.active
		active.supplementaryGroups = append([]uint32{}, active.supplementaryGroups...)
		active.relay.supplementaryGroups = append([]uint32{}, active.relay.supplementaryGroups...)
		record.active = &active
	}
	return record
}

func (lease *activeLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	err := releaseInstanceOperationResources(ctx, nil, lease.vmLock, nil, nil, lease.directories, lease.roots)
	lease.vmLock = nil
	lease.directories = nil
	lease.roots = nil
	return err
}

func (lease *cleanupLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	err := releaseInstanceOperationResources(ctx, nil, lease.vmLock, nil, nil, lease.directories, lease.roots)
	lease.vmLock = nil
	lease.directories = nil
	lease.roots = nil
	return err
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

func releaseInstanceOperationResources(
	ctx context.Context,
	primary error,
	vmLock *authorityLock,
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
