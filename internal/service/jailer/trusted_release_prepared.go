package jailer

import (
	"context"
	"errors"

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
	slotLease *releaseSlotLease
	store     *trustedReleaseStore
	directory *trustedReleaseDirectory
	admission *trustedReleaseDirectoryAdmission
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
		slotLease: slotLease,
		store:     store,
		directory: directory,
		admission: admission,
	}
	slotLease = nil
	store = nil
	directory = nil
	admission = nil
	return prepared, nil
}

func (prepared *preparedRelease) Release(ctx context.Context) error {
	if prepared == nil {
		return nil
	}
	err := releasePreparedReleaseResources(
		ctx,
		nil,
		prepared.admission,
		prepared.directory,
		prepared.store,
		prepared.slotLease,
		"release prepared trusted release",
	)
	prepared.admission = nil
	prepared.directory = nil
	prepared.store = nil
	prepared.slotLease = nil
	return err
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
