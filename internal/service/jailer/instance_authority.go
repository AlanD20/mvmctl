package jailer

import (
	"context"
	"errors"

	"mvmctl/pkg/errs"
)

type instanceAuthority struct {
	deps   instanceAuthorityDeps
	policy instanceAuthorityPolicy
}

type launchLease struct {
	roots       *instanceAuthorityRoots
	directories *instanceUIDDirectories
	releaseLock *authorityLock
	vmLock      *authorityLock
	record      instanceRecord
}

type registeredLease struct {
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
	return newInstanceAuthorityWithPolicy(realInstanceAuthorityDeps(), productionInstanceAuthorityPolicy())
}

func newInstanceAuthorityWithPolicy(
	deps instanceAuthorityDeps,
	policy instanceAuthorityPolicy,
) *instanceAuthority {
	return &instanceAuthority{deps: deps, policy: policy}
}

func (authority *instanceAuthority) lockReleaseSlot(
	ctx context.Context,
	slot releaseSlot,
) (_ *releaseSlotLease, returnErr error) {
	if err := validateReleaseSlotValue(slot); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	roots, err := openInstanceAuthorityRoots(ctx, authority.deps, authority.policy)
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

func (lease *releaseSlotLease) registerLaunch(
	ctx context.Context,
	caller instanceCaller,
	registration launchRegistration,
) (_ *launchLease, returnErr error) {
	if lease == nil || lease.roots == nil || lease.releaseLock == nil || lease.releaseLock.fd < 0 {
		return nil, instanceStateError(registration.vmID, "release slot lease is not active")
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, err
	}
	if err := validateLaunchRegistration(registration); err != nil {
		return nil, err
	}
	if releaseSlotForIdentity(registration.release) != lease.slot {
		return nil, instanceValidationError("launch release identity does not match held release slot")
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

	indexLock, err := lease.roots.acquireIndexLock(ctx)
	if err != nil {
		return nil, err
	}
	location, err := lease.roots.findInstance(ctx, registration.vmID)
	if err != nil {
		return nil, err
	}
	if location != nil && location.uid != caller.uid {
		if closeErr := lease.roots.deps.close(context.WithoutCancel(ctx), location.stateFD); closeErr != nil {
			return nil, appendInstanceOperationError(
				unauthorizedInstanceError(registration.vmID),
				"close foreign instance authority directory",
				closeErr,
			)
		}
		return nil, unauthorizedInstanceError(registration.vmID)
	}
	var existing *instanceRecord
	if location == nil {
		directories, err = lease.roots.openUIDDirectories(ctx, caller.uid)
		if err != nil {
			return nil, err
		}
	} else {
		directories, err = lease.roots.openRuntimeForLocation(ctx, location)
		if err != nil {
			return nil, err
		}
	}
	vmLock, err = directories.acquireVMLock(ctx, registration.vmID)
	if err != nil {
		return nil, err
	}
	if location != nil {
		record, found, readErr := directories.readRecord(ctx, registration.vmID)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			return nil, instanceAtomicError("instance authority record disappeared while locked", nil)
		}
		existing = &record
	}
	record, err := registerInstanceRecord(caller, registration, existing)
	if err != nil {
		return nil, err
	}
	if err := directories.writeRecord(ctx, record); err != nil {
		return nil, err
	}
	if err := indexLock.Release(ctx); err != nil {
		indexLock = nil
		return nil, annotateInstanceRecordReplacement(err, false)
	}
	indexLock = nil
	launch := &launchLease{
		roots:       lease.roots,
		directories: directories,
		releaseLock: lease.releaseLock,
		vmLock:      vmLock,
		record:      record,
	}
	lease.roots = nil
	lease.releaseLock = nil
	directories = nil
	vmLock = nil
	return launch, nil
}

func (lease *releaseSlotLease) requireUnreferenced(
	ctx context.Context,
	release releaseIdentity,
) (returnErr error) {
	if lease == nil || lease.roots == nil || lease.releaseLock == nil || lease.releaseLock.fd < 0 {
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
			"release is referenced by active privileged VM authority",
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

func (authority *instanceAuthority) LockRegistered(
	ctx context.Context,
	caller instanceCaller,
	vmID string,
) (*registeredLease, error) {
	locked, err := authority.lockOwnedInstance(ctx, caller, vmID)
	if err != nil {
		return nil, err
	}
	if locked.record.lifecycle != instanceLifecycleRegistered {
		releaseErr := locked.Release(ctx)
		stateErr := errs.New(
			errs.CodeVMNotRunning,
			"vm has no registered privileged runtime",
			errs.WithEntity(vmID),
		)
		return nil, appendInstanceOperationError(stateErr, "release inactive instance lease", releaseErr)
	}
	return &registeredLease{
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
	if updated != locked.record {
		if err := locked.directories.writeRecord(ctx, updated); err != nil {
			return nil, appendInstanceOperationError(
				err,
				"release failed cleanup registration",
				locked.Release(ctx),
			)
		}
	}
	return &cleanupLease{
		roots:       locked.roots,
		directories: locked.directories,
		vmLock:      locked.vmLock,
		record:      updated,
	}, nil
}

func (lease *cleanupLease) Complete(ctx context.Context) error {
	if lease == nil || lease.vmLock == nil || lease.vmLock.fd < 0 {
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
	roots, err := openInstanceAuthorityRoots(ctx, authority.deps, authority.policy)
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
	directories, err = roots.openRuntimeForLocation(ctx, location)
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
	if lease == nil {
		return nil
	}
	err := releaseInstanceOperationResources(
		ctx,
		nil,
		lease.vmLock,
		nil,
		lease.releaseLock,
		lease.directories,
		lease.roots,
	)
	lease.vmLock = nil
	lease.releaseLock = nil
	lease.directories = nil
	lease.roots = nil
	return err
}

func (lease *registeredLease) Release(ctx context.Context) error {
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
	if primary == nil {
		return cause
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		return domainErr
	}
	return instanceAtomicError(message+": "+cause.Error(), errors.Join(primary, cause))
}
