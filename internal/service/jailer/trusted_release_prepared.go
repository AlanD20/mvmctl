package jailer

import (
	"context"
	"errors"
	"sync"

	"mvmctl/pkg/errs"
)

type releaseAuthority struct {
	instances         *instanceAuthority
	storeDeps         trustedReleaseStoreDeps
	storePolicy       trustedReleaseStorePolicy
	checksumAuthority *trustedReleaseChecksumAuthority
	archiveFetcher    *trustedReleaseArchiveFetcher
}

type preparedRelease struct {
	state *preparedReleaseState
}

type preparedReleaseState struct {
	mu         sync.Mutex
	status     preparedReleaseStatus
	slotLease  *releaseSlotLease
	store      *trustedReleaseStore
	directory  *trustedReleaseDirectory
	admission  *trustedReleaseDirectoryAdmission
	cleanupErr error
}

type preparedReleaseStatus uint8

const (
	preparedReleaseOpen preparedReleaseStatus = iota + 1
	preparedReleaseTransferred
	preparedReleaseReleased
)

type preparedReleaseSnapshot struct {
	status            preparedReleaseStatus
	slot              releaseSlot
	manifest          trustedReleaseManifest
	identity          releaseIdentity
	hasSlotLease      bool
	releaseLockActive bool
	hasStore          bool
	hasDirectory      bool
	hasAdmission      bool
	hasExecutables    bool
	firecrackerFD     int
	jailerFD          int
}

func newReleaseAuthority() *releaseAuthority {
	return newReleaseAuthorityWithPolicy(
		newInstanceAuthority(),
		realTrustedReleaseStoreDeps(),
		productionTrustedReleaseStorePolicy(),
	)
}

func newReleaseAuthorityWithPolicy(
	instances *instanceAuthority,
	storeDeps trustedReleaseStoreDeps,
	storePolicy trustedReleaseStorePolicy,
) *releaseAuthority {
	return &releaseAuthority{
		instances:         instances,
		storeDeps:         storeDeps,
		storePolicy:       storePolicy,
		checksumAuthority: newTrustedReleaseChecksumAuthority(),
		archiveFetcher:    newTrustedReleaseArchiveFetcher(),
	}
}

// CRITICAL: The release-slot lock precedes every trusted-store read. The returned value exclusively owns each object
// selected while holding that lock, so the verified manifest identity and executable descriptors remain one authority.
func (authority *releaseAuthority) prepareInstalled(
	ctx context.Context,
	slot releaseSlot,
) (_ *preparedRelease, returnErr error) {
	if authority == nil || authority.instances == nil {
		return nil, trustedReleaseStoreError("release authority is not active", nil)
	}
	if err := validateReleaseSlotValue(slot); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("prepare installed trusted release", err)
	}

	var slotLease *releaseSlotLease
	var store *trustedReleaseStore
	var directory *trustedReleaseDirectory
	var admission *trustedReleaseDirectoryAdmission
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releasePreparedReleaseResources(
			ctx,
			returnErr,
			admission,
			directory,
			store,
			slotLease,
			"release rejected prepared trusted release",
		)
	}()

	var err error
	slotLease, err = authority.instances.lockReleaseSlot(ctx, slot)
	if err != nil {
		return nil, err
	}
	store, err = openTrustedReleaseStoreForRead(ctx, authority.storeDeps, authority.storePolicy)
	if err != nil {
		return nil, err
	}
	directory, err = store.openInstalledSlot(ctx, slot)
	if err != nil {
		return nil, err
	}
	admission, err = directory.admit(ctx)
	if err != nil {
		return nil, err
	}

	prepared := &preparedRelease{
		state: &preparedReleaseState{
			status:    preparedReleaseOpen,
			slotLease: slotLease,
			store:     store,
			directory: directory,
			admission: admission,
		},
	}
	slotLease = nil
	store = nil
	directory = nil
	admission = nil
	return prepared, nil
}

func (prepared *preparedRelease) Release(ctx context.Context) error {
	if prepared == nil || prepared.state == nil {
		return nil
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status == preparedReleaseTransferred || state.status == preparedReleaseReleased {
		return state.cleanupErr
	}
	if state.status != preparedReleaseOpen {
		state.cleanupErr = trustedReleaseStoreError("prepared release state is invalid", nil)
		state.status = preparedReleaseReleased
		return state.cleanupErr
	}
	state.cleanupErr = releasePreparedReleaseResources(
		ctx,
		nil,
		state.admission,
		state.directory,
		state.store,
		state.slotLease,
		"release prepared trusted release",
	)
	state.admission = nil
	state.directory = nil
	state.store = nil
	state.slotLease = nil
	state.status = preparedReleaseReleased
	return state.cleanupErr
}

func (prepared *preparedRelease) snapshot() preparedReleaseSnapshot {
	if prepared == nil || prepared.state == nil {
		return preparedReleaseSnapshot{}
	}
	state := prepared.state
	state.mu.Lock()
	defer state.mu.Unlock()
	snapshot := preparedReleaseSnapshot{
		status:         state.status,
		hasSlotLease:   state.slotLease != nil,
		hasStore:       state.store != nil,
		hasDirectory:   state.directory != nil,
		hasAdmission:   state.admission != nil,
		hasExecutables: state.admission != nil && state.admission.executables != nil,
		firecrackerFD:  -1,
		jailerFD:       -1,
	}
	if state.slotLease != nil {
		snapshot.slot = state.slotLease.slot
		snapshot.releaseLockActive = state.slotLease.releaseLock.isActive()
	}
	if state.admission != nil {
		snapshot.manifest = state.admission.manifest
		snapshot.identity = state.admission.identity
		if state.admission.executables != nil {
			snapshot.firecrackerFD = state.admission.executables.firecrackerFD
			snapshot.jailerFD = state.admission.executables.jailerFD
		}
	}
	return snapshot
}

func releasePreparedReleaseResources(
	ctx context.Context,
	primary error,
	admission *trustedReleaseDirectoryAdmission,
	directory *trustedReleaseDirectory,
	store *trustedReleaseStore,
	slotLease *releaseSlotLease,
	description string,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if admission != nil {
		result = appendPreparedReleaseError(
			result,
			description+" directory admission",
			admission.Release(cleanupCtx),
		)
	}
	if directory != nil {
		result = appendPreparedReleaseError(
			result,
			description+" version directory",
			directory.Release(cleanupCtx),
		)
	}
	if store != nil {
		result = appendPreparedReleaseError(
			result,
			description+" store",
			store.Release(cleanupCtx),
		)
	}
	if slotLease != nil {
		result = appendPreparedReleaseError(
			result,
			description+" slot lease",
			slotLease.Release(cleanupCtx),
		)
	}
	return result
}

func appendPreparedReleaseError(primary error, message string, cause error) error {
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
	return trustedReleaseStoreError(message, errors.Join(primary, cause))
}
