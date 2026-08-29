package jailer

import (
	"bytes"
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type trustedReleaseReplacementTarget struct {
	directory         *trustedReleaseDirectory
	admission         *trustedReleaseDirectoryAdmission
	directoryIdentity unix.Stat_t
	canonicalManifest []byte
}

// CRITICAL: This is the explicit installed-release replacement transaction. RENAME_EXCHANGE is its only commit point.
// Before it, failures discard only the new candidate. After it, the candidate capability names the new canonical
// release and is close-only; the old release is retired solely through its retained directory descriptor.
func (candidate *trustedReleaseCandidate) replaceInstalled(
	ctx context.Context,
) (changed bool, returnErr error) {
	if candidate == nil || candidate.state != trustedReleaseCandidateReady {
		return false, trustedReleaseStoreError("trusted release candidate is not ready for replacement", nil)
	}
	committed := false
	durabilityUncertain := false
	retiredReleaseRetained := false
	var current *trustedReleaseReplacementTarget
	defer func() {
		if current != nil {
			returnErr = appendTrustedReleaseReplacementError(
				returnErr,
				"release replaced trusted release target",
				current.Release(context.WithoutCancel(ctx)),
			)
		}
		returnErr = appendTrustedReleaseReplacementError(
			returnErr,
			"release trusted release candidate after replacement",
			candidate.Release(context.WithoutCancel(ctx)),
		)
		if committed && returnErr != nil {
			returnErr = annotateTrustedReleaseReplaced(
				returnErr,
				durabilityUncertain,
				retiredReleaseRetained,
			)
		}
	}()

	if err := candidate.requireReadyForPublication(ctx); err != nil {
		return false, err
	}
	var err error
	current, err = candidate.openReplacementTarget(ctx)
	if err != nil {
		return false, err
	}
	if bytes.Equal(current.canonicalManifest, candidate.canonicalManifest) {
		return false, nil
	}
	if err := candidate.architecture.slotLease.requireUnreferenced(ctx, current.admission.identity); err != nil {
		return false, err
	}
	if err := candidate.verifyReservedNameBinding(ctx); err != nil {
		return false, err
	}
	if err := current.verifyCanonicalNameBinding(ctx, candidate); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, trustedReleaseStoreError("replace installed trusted release candidate", err)
	}
	if err := candidate.architecture.deps.exchangeCandidate(
		ctx,
		candidate.architecture.fd,
		candidate.name,
		candidate.architecture.slot.version,
	); err != nil {
		return false, classifyTrustedReleaseExchangeError(err)
	}

	candidate.state = trustedReleaseCandidateReplaced
	committed = true
	changed = true
	cleanupCtx := context.WithoutCancel(ctx)
	if err := candidate.architecture.deps.fsync(cleanupCtx, candidate.architecture.fd); err != nil {
		durabilityUncertain = true
		retiredReleaseRetained = true
		if errs.AsDomainError(err) != nil {
			return true, err
		}
		return true, trustedReleaseStoreError("sync trusted release architecture after replacement", err)
	}

	removed, retirementErr := current.retire(cleanupCtx, candidate)
	retiredReleaseRetained = retirementErr != nil && !removed
	if !removed {
		return true, retirementErr
	}
	if err := candidate.architecture.deps.fsync(cleanupCtx, candidate.architecture.fd); err != nil {
		durabilityUncertain = true
		if retirementErr == nil && errs.AsDomainError(err) != nil {
			return true, err
		}
		return true, appendTrustedReleaseStoreError(
			retirementErr,
			"sync trusted release architecture after old release retirement",
			err,
		)
	}
	return true, retirementErr
}

func (candidate *trustedReleaseCandidate) openReplacementTarget(
	ctx context.Context,
) (_ *trustedReleaseReplacementTarget, returnErr error) {
	fd, err := candidate.architecture.deps.openAt(
		ctx,
		candidate.architecture.fd,
		candidate.architecture.slot.version,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		return nil, classifyTrustedReleaseStoreOpenError("installed release version", err, true)
	}
	directory := &trustedReleaseDirectory{
		deps: candidate.architecture.deps, policy: candidate.architecture.policy,
		slot: candidate.architecture.slot, slotFD: fd, retained: []int{fd},
	}
	target := &trustedReleaseReplacementTarget{directory: directory}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseReplacementError(
			returnErr,
			"close rejected trusted release replacement target",
			target.Release(context.WithoutCancel(ctx)),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		candidate.architecture.deps,
		fd,
		"installed release version",
		candidate.architecture.policy,
		true,
	); err != nil {
		return nil, err
	}
	if err := candidate.architecture.deps.fstat(ctx, fd, &target.directoryIdentity); err != nil {
		return nil, trustedReleaseStoreError("inspect installed trusted release before replacement", err)
	}
	if err := directory.requireExactLeaves(ctx); err != nil {
		return nil, err
	}
	target.admission, err = directory.admit(ctx)
	if err != nil {
		return nil, err
	}
	target.canonicalManifest, err = encodeTrustedReleaseManifest(target.admission.manifest)
	if err != nil {
		return nil, err
	}
	return target, nil
}

func (target *trustedReleaseReplacementTarget) verifyCanonicalNameBinding(
	ctx context.Context,
	candidate *trustedReleaseCandidate,
) (returnErr error) {
	fd, err := candidate.architecture.deps.openAt(
		ctx,
		candidate.architecture.fd,
		candidate.architecture.slot.version,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseStoreUntrusted("installed trusted release changed before replacement", err)
		}
		return trustedReleaseStoreError("open installed trusted release before replacement", err)
	}
	defer func() {
		returnErr = appendTrustedReleaseReplacementError(
			returnErr,
			"close installed trusted release replacement binding check",
			candidate.architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		candidate.architecture.deps,
		fd,
		"installed release before replacement",
		candidate.architecture.policy,
		true,
	); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := candidate.architecture.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect installed trusted release before replacement", err)
	}
	if stat.Dev != target.directoryIdentity.Dev || stat.Ino != target.directoryIdentity.Ino {
		return trustedReleaseStoreUntrusted("installed trusted release identity changed before replacement", nil)
	}
	return nil
}

func (target *trustedReleaseReplacementTarget) verifyRetiredNameBinding(
	ctx context.Context,
	candidate *trustedReleaseCandidate,
) (returnErr error) {
	fd, err := candidate.architecture.deps.openAt(
		ctx,
		candidate.architecture.fd,
		candidate.name,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseStoreUntrusted("retired trusted release name is missing or unsafe", err)
		}
		return trustedReleaseStoreError("open retired trusted release name", err)
	}
	defer func() {
		returnErr = appendTrustedReleaseReplacementError(
			returnErr,
			"close retired trusted release binding check",
			candidate.architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		candidate.architecture.deps,
		fd,
		"retired release",
		candidate.architecture.policy,
		true,
	); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := candidate.architecture.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect retired trusted release name", err)
	}
	if stat.Dev != target.directoryIdentity.Dev || stat.Ino != target.directoryIdentity.Ino {
		return trustedReleaseStoreUntrusted("retired trusted release name identity changed", nil)
	}
	return nil
}

func (target *trustedReleaseReplacementTarget) retire(
	ctx context.Context,
	candidate *trustedReleaseCandidate,
) (removed bool, returnErr error) {
	if target.admission != nil {
		returnErr = appendTrustedReleaseReplacementError(
			returnErr,
			"close retired trusted release admission",
			target.admission.Release(context.WithoutCancel(ctx)),
		)
		target.admission = nil
	}
	if err := target.verifyRetiredNameBinding(ctx, candidate); err != nil {
		if returnErr == nil {
			return false, err
		}
		return false, appendTrustedReleaseStoreError(returnErr, "verify retired trusted release name", err)
	}

	canRemoveDirectory := true
	for _, leaf := range []string{
		trustedReleaseManifestLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseFirecrackerLeaf,
	} {
		if err := candidate.architecture.deps.unlinkAt(ctx, target.directory.slotFD, leaf, 0); err != nil {
			returnErr = appendTrustedReleaseStoreError(returnErr, "remove retired trusted release leaf "+leaf, err)
			canRemoveDirectory = false
		}
	}
	if err := candidate.architecture.deps.fsync(ctx, target.directory.slotFD); err != nil {
		returnErr = appendTrustedReleaseStoreError(returnErr, "sync retired trusted release directory", err)
		canRemoveDirectory = false
	}
	if !canRemoveDirectory {
		return false, returnErr
	}
	if err := target.verifyRetiredNameBinding(ctx, candidate); err != nil {
		if returnErr == nil {
			return false, err
		}
		return false, appendTrustedReleaseStoreError(returnErr, "reverify retired trusted release name", err)
	}
	if err := candidate.architecture.deps.unlinkAt(
		ctx,
		candidate.architecture.fd,
		candidate.name,
		unix.AT_REMOVEDIR,
	); err != nil {
		return false, appendTrustedReleaseStoreError(returnErr, "remove retired trusted release directory", err)
	}
	return true, returnErr
}

func (target *trustedReleaseReplacementTarget) Release(ctx context.Context) error {
	if target == nil {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	if target.admission != nil {
		result = appendTrustedReleaseReplacementError(
			result,
			"release trusted release replacement target admission",
			target.admission.Release(cleanupCtx),
		)
		target.admission = nil
	}
	if target.directory != nil {
		result = appendTrustedReleaseReplacementError(
			result,
			"release trusted release replacement target directory",
			target.directory.Release(cleanupCtx),
		)
		target.directory = nil
	}
	target.directoryIdentity = unix.Stat_t{}
	target.canonicalManifest = nil
	return result
}

func classifyTrustedReleaseExchangeError(cause error) error {
	if errors.Is(cause, unix.ENOSYS) || errors.Is(cause, unix.EOPNOTSUPP) || errors.Is(cause, unix.EINVAL) {
		return trustedReleaseStoreError("host does not support atomic trusted release exchange", cause)
	}
	if errors.Is(cause, unix.EXDEV) {
		return trustedReleaseStoreError("trusted release replacement crossed filesystems", cause)
	}
	return trustedReleaseStoreError("atomically replace installed trusted release", cause)
}

func appendTrustedReleaseReplacementError(primary error, message string, cause error) error {
	if primary == nil && errs.AsDomainError(cause) != nil {
		return cause
	}
	return appendTrustedReleaseStoreError(primary, message, cause)
}

func annotateTrustedReleaseReplaced(
	cause error,
	durabilityUncertain bool,
	retiredReleaseRetained bool,
) *errs.DomainError {
	domainErr := errs.AsDomainError(cause)
	if domainErr == nil {
		domainErr = trustedReleaseStoreError("trusted release replaced with a later failure", cause)
	}
	if domainErr.Details == nil {
		domainErr.Details = make(map[string]any)
	}
	domainErr.Details["release_replaced"] = true
	if durabilityUncertain {
		domainErr.Details["durability_uncertain"] = true
	}
	if retiredReleaseRetained {
		domainErr.Details["retired_release_retained"] = true
	}
	return domainErr
}

func exchangeTrustedReleaseCandidate(
	ctx context.Context,
	parentFD int,
	source string,
	target string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Renameat2(parentFD, source, parentFD, target, unix.RENAME_EXCHANGE)
}
