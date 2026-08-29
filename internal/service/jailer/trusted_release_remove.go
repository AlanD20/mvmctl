package jailer

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const trustedReleaseRemovalRenameAttempts = 8

type trustedReleaseRemovalTargetState uint8

const (
	trustedReleaseRemovalTargetAdmitted trustedReleaseRemovalTargetState = iota + 1
	trustedReleaseRemovalTargetCommitted
	trustedReleaseRemovalTargetReleased
)

type trustedReleaseRemovalTarget struct {
	directory         *trustedReleaseDirectory
	admission         *trustedReleaseDirectoryAdmission
	directoryIdentity unix.Stat_t
	reservedName      string
	state             trustedReleaseRemovalTargetState
}

// CRITICAL: Removal first renames one fully admitted canonical release to the reserved namespace. That atomic rename
// is the only commit point. After it succeeds, the retained target is close-only and canonical absence is authoritative.
func (authority *releaseAuthority) removeInstalled(
	ctx context.Context,
	slot releaseSlot,
) (removed bool, returnErr error) {
	if authority == nil || authority.instances == nil {
		return false, trustedReleaseRemovalError("release authority is not active for removal", nil)
	}
	if err := validateReleaseSlotValue(slot); err != nil {
		return false, instanceValidationError(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return false, trustedReleaseRemovalError("remove installed trusted release", err)
	}

	committed := false
	durabilityUncertain := false
	retiredReleaseRetained := false
	var slotLease *releaseSlotLease
	var store *trustedReleaseStore
	var architecture *trustedReleaseArchitectureWriteLease
	var target *trustedReleaseRemovalTarget
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if target != nil {
			returnErr = appendTrustedReleaseRemovalError(
				returnErr,
				"release trusted release removal target",
				target.Release(cleanupCtx),
			)
		}
		if architecture != nil {
			returnErr = appendTrustedReleaseRemovalError(
				returnErr,
				"release trusted release architecture after removal",
				architecture.Release(cleanupCtx),
			)
		}
		if store != nil {
			returnErr = appendTrustedReleaseRemovalError(
				returnErr,
				"release trusted release store after removal",
				store.Release(cleanupCtx),
			)
		}
		if slotLease != nil {
			returnErr = appendTrustedReleaseRemovalError(
				returnErr,
				"release trusted release slot lease after removal",
				slotLease.Release(cleanupCtx),
			)
		}
		if committed && returnErr != nil {
			returnErr = annotateTrustedReleaseRemoved(
				returnErr,
				durabilityUncertain,
				retiredReleaseRetained,
			)
		}
	}()

	var err error
	slotLease, err = authority.instances.lockReleaseSlot(ctx, slot)
	if err != nil {
		return false, err
	}
	store, err = openTrustedReleaseStoreForRead(ctx, authority.storeDeps, authority.storePolicy)
	if trustedReleaseIsSafelyAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	architecture, err = store.openExistingArchitecture(ctx, slotLease)
	if trustedReleaseIsSafelyAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	target, err = architecture.openRemovalTarget(ctx)
	if err != nil {
		return false, err
	}
	if target == nil {
		if err := architecture.recoverCandidates(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := architecture.recoverCandidates(ctx); err != nil {
		return false, err
	}
	if err := slotLease.requireUnreferenced(ctx, target.admission.identity); err != nil {
		return false, err
	}

	prefix, err := trustedReleaseCandidatePrefix(slot)
	if err != nil {
		return false, err
	}
	for attempt := 0; attempt < trustedReleaseRemovalRenameAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, trustedReleaseRemovalError("reserve installed trusted release for removal", err)
		}
		reservedName, err := architecture.deps.candidateName(ctx, slot)
		if err != nil {
			return false, trustedReleaseRemovalError("generate trusted release removal name", err)
		}
		if err := validateTrustedReleaseCandidateName(prefix, reservedName); err != nil {
			return false, err
		}
		if err := target.verifyNameBinding(ctx, architecture, slot.version, "before removal"); err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, trustedReleaseRemovalError("commit installed trusted release removal", err)
		}
		err = architecture.deps.renameInstalledNoReplace(
			ctx,
			architecture.fd,
			slot.version,
			reservedName,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return false, classifyTrustedReleaseRemovalRenameError(err)
		}
		target.reservedName = reservedName
		target.state = trustedReleaseRemovalTargetCommitted
		committed = true
		removed = true
		retiredReleaseRetained = true
		break
	}
	if !committed {
		return false, trustedReleaseRemovalError(
			fmt.Sprintf("reserve installed trusted release after %d attempts", trustedReleaseRemovalRenameAttempts),
			unix.EEXIST,
		)
	}

	cleanupCtx := context.WithoutCancel(ctx)
	if err := architecture.deps.fsync(cleanupCtx, architecture.fd); err != nil {
		durabilityUncertain = true
		return true, trustedReleaseRemovalError("sync trusted release architecture after removal commit", err)
	}
	retiredReleaseRemoved, retirementErr := target.retire(cleanupCtx, architecture)
	if !retiredReleaseRemoved {
		return true, retirementErr
	}
	retiredReleaseRetained = false
	if err := architecture.deps.fsync(cleanupCtx, architecture.fd); err != nil {
		durabilityUncertain = true
		return true, appendTrustedReleaseRemovalError(
			retirementErr,
			"sync trusted release architecture after removal cleanup",
			err,
		)
	}
	return true, retirementErr
}

func (store *trustedReleaseStore) openExistingArchitecture(
	ctx context.Context,
	slotLease *releaseSlotLease,
) (*trustedReleaseArchitectureWriteLease, error) {
	if store == nil || store.binariesFD < 0 || len(store.retained) == 0 {
		return nil, trustedReleaseRemovalError("trusted release store is not active for removal", nil)
	}
	if slotLease == nil || slotLease.roots == nil || slotLease.releaseLock == nil || slotLease.releaseLock.fd < 0 {
		return nil, trustedReleaseRemovalError("release slot lease is not active for removal", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseRemovalError("open trusted release architecture for removal", err)
	}
	fd, err := store.deps.openAt(
		ctx,
		store.binariesFD,
		slotLease.slot.architecture,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		return nil, classifyTrustedReleaseStoreOpenError("release architecture", err, true)
	}
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		store.deps,
		fd,
		"release architecture",
		store.policy,
		true,
	); err != nil {
		return nil, appendTrustedReleaseRemovalError(
			err,
			"close rejected trusted release architecture for removal",
			store.deps.close(context.WithoutCancel(ctx), fd),
		)
	}
	return &trustedReleaseArchitectureWriteLease{
		deps: store.deps, policy: store.policy, slotLease: slotLease, slot: slotLease.slot, fd: fd,
	}, nil
}

func (architecture *trustedReleaseArchitectureWriteLease) openRemovalTarget(
	ctx context.Context,
) (_ *trustedReleaseRemovalTarget, returnErr error) {
	if err := architecture.requireActiveSlotLease(); err != nil {
		return nil, err
	}
	fd, err := architecture.deps.openAt(
		ctx,
		architecture.fd,
		architecture.slot.version,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, classifyTrustedReleaseStoreOpenError("installed release version", err, false)
	}
	directory := &trustedReleaseDirectory{
		deps: architecture.deps, policy: architecture.policy,
		slot: architecture.slot, slotFD: fd, retained: []int{fd},
	}
	target := &trustedReleaseRemovalTarget{
		directory: directory,
		state:     trustedReleaseRemovalTargetAdmitted,
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseRemovalError(
			returnErr,
			"release rejected trusted release removal target",
			target.Release(context.WithoutCancel(ctx)),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		architecture.deps,
		fd,
		"installed release version",
		architecture.policy,
		true,
	); err != nil {
		return nil, err
	}
	if err := architecture.deps.fstat(ctx, fd, &target.directoryIdentity); err != nil {
		return nil, trustedReleaseStoreError("inspect installed trusted release before removal", err)
	}
	var architectureIdentity unix.Stat_t
	if err := architecture.deps.fstat(ctx, architecture.fd, &architectureIdentity); err != nil {
		return nil, trustedReleaseStoreError("inspect trusted release architecture before removal", err)
	}
	if architectureIdentity.Dev != target.directoryIdentity.Dev {
		return nil, trustedReleaseStoreUntrusted(
			"installed trusted release is on a different filesystem from its architecture",
			nil,
		)
	}
	if err := directory.requireExactLeaves(ctx); err != nil {
		return nil, err
	}
	target.admission, err = directory.admit(ctx)
	if err != nil {
		return nil, err
	}
	return target, nil
}

func (target *trustedReleaseRemovalTarget) verifyNameBinding(
	ctx context.Context,
	architecture *trustedReleaseArchitectureWriteLease,
	name string,
	description string,
) (returnErr error) {
	fd, err := architecture.deps.openAt(ctx, architecture.fd, name, trustedReleaseStoreDirectoryFlags, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseStoreUntrusted("trusted release removal target changed "+description, err)
		}
		return trustedReleaseStoreError("open trusted release removal target "+description, err)
	}
	defer func() {
		returnErr = appendTrustedReleaseRemovalError(
			returnErr,
			"close trusted release removal binding check "+description,
			architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		architecture.deps,
		fd,
		"removal target "+description,
		architecture.policy,
		true,
	); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := architecture.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect trusted release removal target "+description, err)
	}
	if stat.Dev != target.directoryIdentity.Dev || stat.Ino != target.directoryIdentity.Ino {
		return trustedReleaseStoreUntrusted("trusted release removal target identity changed "+description, nil)
	}
	return nil
}

func (target *trustedReleaseRemovalTarget) retire(
	ctx context.Context,
	architecture *trustedReleaseArchitectureWriteLease,
) (removed bool, returnErr error) {
	if err := target.requireCommittedForRetirement(architecture); err != nil {
		return false, err
	}
	if target.admission != nil {
		returnErr = appendTrustedReleaseRemovalError(
			returnErr,
			"close removed trusted release admission",
			target.admission.Release(context.WithoutCancel(ctx)),
		)
		target.admission = nil
	}
	if err := target.verifyNameBinding(ctx, architecture, target.reservedName, "before cleanup"); err != nil {
		return false, appendTrustedReleaseRemovalError(returnErr, "verify removed trusted release binding", err)
	}

	canRemoveDirectory := true
	for _, leaf := range []string{
		trustedReleaseManifestLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseFirecrackerLeaf,
	} {
		if err := architecture.deps.unlinkAt(ctx, target.directory.slotFD, leaf, 0); err != nil {
			returnErr = appendTrustedReleaseRemovalError(
				returnErr,
				"remove retired trusted release leaf "+leaf,
				trustedReleaseRemovalError("unlink retired trusted release leaf "+leaf, err),
			)
			canRemoveDirectory = false
		}
	}
	if err := architecture.deps.fsync(ctx, target.directory.slotFD); err != nil {
		returnErr = appendTrustedReleaseRemovalError(
			returnErr,
			"sync retired trusted release directory",
			trustedReleaseRemovalError("fsync retired trusted release directory", err),
		)
		canRemoveDirectory = false
	}
	if !canRemoveDirectory {
		return false, returnErr
	}
	if err := target.verifyNameBinding(ctx, architecture, target.reservedName, "before directory removal"); err != nil {
		return false, appendTrustedReleaseRemovalError(returnErr, "reverify removed trusted release binding", err)
	}
	if err := architecture.deps.unlinkAt(
		ctx,
		architecture.fd,
		target.reservedName,
		unix.AT_REMOVEDIR,
	); err != nil {
		return false, appendTrustedReleaseRemovalError(
			returnErr,
			"remove retired trusted release directory",
			trustedReleaseRemovalError("rmdir retired trusted release directory", err),
		)
	}
	return true, returnErr
}

func (target *trustedReleaseRemovalTarget) requireCommittedForRetirement(
	architecture *trustedReleaseArchitectureWriteLease,
) error {
	if target == nil || target.state != trustedReleaseRemovalTargetCommitted {
		return trustedReleaseRemovalError("trusted release removal target is not committed for retirement", nil)
	}
	if err := architecture.requireActiveSlotLease(); err != nil {
		return err
	}
	if target.directory == nil || target.directory.slotFD < 0 || len(target.directory.retained) == 0 ||
		target.directory.slot != architecture.slot || target.directory.policy != architecture.policy ||
		target.directoryIdentity.Ino == 0 {
		return trustedReleaseRemovalError("trusted release removal target is not active for retirement", nil)
	}
	prefix, err := trustedReleaseCandidatePrefix(architecture.slot)
	if err != nil {
		return err
	}
	if err := validateTrustedReleaseCandidateName(prefix, target.reservedName); err != nil {
		return err
	}
	return nil
}

func (target *trustedReleaseRemovalTarget) Release(ctx context.Context) error {
	if target == nil || target.state == trustedReleaseRemovalTargetReleased {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	if target.admission != nil {
		result = appendTrustedReleaseRemovalError(
			result,
			"release trusted release removal target admission",
			target.admission.Release(cleanupCtx),
		)
		target.admission = nil
	}
	if target.directory != nil {
		result = appendTrustedReleaseRemovalError(
			result,
			"release trusted release removal target directory",
			target.directory.Release(cleanupCtx),
		)
		target.directory = nil
	}
	target.directoryIdentity = unix.Stat_t{}
	target.reservedName = ""
	target.state = trustedReleaseRemovalTargetReleased
	return result
}

func trustedReleaseIsSafelyAbsent(err error) bool {
	domainErr := errs.AsDomainError(err)
	return domainErr != nil && domainErr.Code == errs.CodeBinaryNotFound && domainErr.Err == nil
}

func trustedReleaseRemovalError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeBinaryRemoveFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(
		errs.CodeBinaryRemoveFailed,
		message,
		cause,
		errs.WithClass(errs.ClassInternal),
	)
}

func appendTrustedReleaseRemovalError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		if errs.AsDomainError(cause) != nil {
			return cause
		}
		return trustedReleaseRemovalError(message, cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		return domainErr
	}
	return trustedReleaseRemovalError(message, errors.Join(primary, cause))
}

func annotateTrustedReleaseRemoved(
	cause error,
	durabilityUncertain bool,
	retiredReleaseRetained bool,
) *errs.DomainError {
	domainErr := errs.AsDomainError(cause)
	if domainErr == nil {
		domainErr = trustedReleaseRemovalError("trusted release removed with a later failure", cause)
	}
	if domainErr.Details == nil {
		domainErr.Details = make(map[string]any)
	}
	domainErr.Details["release_removed"] = true
	if durabilityUncertain {
		domainErr.Details["durability_uncertain"] = true
	}
	if retiredReleaseRetained {
		domainErr.Details["retired_release_retained"] = true
	}
	return domainErr
}

func classifyTrustedReleaseRemovalRenameError(cause error) error {
	if errors.Is(cause, unix.ENOSYS) || errors.Is(cause, unix.EOPNOTSUPP) || errors.Is(cause, unix.EINVAL) {
		return trustedReleaseRemovalError("host does not support atomic trusted release removal", cause)
	}
	if errors.Is(cause, unix.EXDEV) {
		return trustedReleaseRemovalError("trusted release removal crossed filesystems", cause)
	}
	return trustedReleaseRemovalError("atomically reserve installed trusted release for removal", cause)
}

func renameTrustedReleaseInstalledNoReplace(
	ctx context.Context,
	parentFD int,
	source string,
	target string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Renameat2(parentFD, source, parentFD, target, unix.RENAME_NOREPLACE)
}
