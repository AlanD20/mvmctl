package jailer

import (
	"bytes"
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// CRITICAL: This is the absent-slot publication transaction. The no-replace rename is its only commit point. Before
// that syscall, every failure discards the reserved candidate. After it succeeds, cleanup is close-only and no path in
// this method may unlink the installed leaves.
func (candidate *trustedReleaseCandidate) publishAbsent(
	ctx context.Context,
) (changed bool, returnErr error) {
	if candidate == nil || candidate.state != trustedReleaseCandidateReady {
		return false, trustedReleaseStoreError("trusted release candidate is not ready for publication", nil)
	}
	committed := false
	durabilityUncertain := false
	defer func() {
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"release trusted release candidate after absent publication",
			candidate.Release(context.WithoutCancel(ctx)),
		)
		if committed && returnErr != nil {
			returnErr = annotateTrustedReleaseInstalled(returnErr, durabilityUncertain)
		}
	}()

	if err := candidate.requireReadyForPublication(ctx); err != nil {
		return false, err
	}
	existingManifest, exists, err := candidate.readInstalledManifestForPublication(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		if bytes.Equal(existingManifest, candidate.canonicalManifest) {
			return false, nil
		}
		return false, errs.AlreadyExists(
			errs.CodeBinaryAlreadyExists,
			"trusted release slot already contains a different complete release",
		)
	}
	if err := ctx.Err(); err != nil {
		return false, trustedReleaseStoreError("publish absent trusted release candidate", err)
	}
	if err := candidate.architecture.deps.renameCandidateNoReplace(
		ctx,
		candidate.architecture.fd,
		candidate.name,
		candidate.architecture.slot.version,
	); err != nil {
		return false, classifyTrustedReleaseNoReplaceError(err)
	}

	candidate.state = trustedReleaseCandidateInstalled
	committed = true
	changed = true
	cleanupCtx := context.WithoutCancel(ctx)
	if err := candidate.architecture.deps.fsync(cleanupCtx, candidate.architecture.fd); err != nil {
		durabilityUncertain = true
		if errs.AsDomainError(err) != nil {
			return true, err
		}
		return true, trustedReleaseStoreError("sync trusted release architecture after absent publication", err)
	}
	return true, nil
}

func (candidate *trustedReleaseCandidate) requireReadyForPublication(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("validate trusted release candidate publication", err)
	}
	if candidate.architecture == nil {
		return trustedReleaseStoreError("trusted release candidate architecture is not active for publication", nil)
	}
	if err := candidate.architecture.requireActiveSlotLease(); err != nil {
		return err
	}
	if candidate.directory == nil || candidate.directory.slotFD < 0 || len(candidate.directory.retained) == 0 ||
		candidate.directory.slot != candidate.architecture.slot ||
		candidate.directory.policy != candidate.architecture.policy {
		return trustedReleaseStoreError("trusted release candidate directory is not active for publication", nil)
	}
	if candidate.admission == nil || candidate.admission.executables == nil ||
		len(candidate.admission.executables.retained) != 2 || candidate.admission.executables.firecrackerFD < 0 ||
		candidate.admission.executables.jailerFD < 0 {
		return trustedReleaseStoreError("trusted release candidate admission is not active for publication", nil)
	}
	prefix, err := trustedReleaseCandidatePrefix(candidate.architecture.slot)
	if err != nil {
		return err
	}
	if err := validateTrustedReleaseCandidateName(prefix, candidate.name); err != nil {
		return err
	}
	canonical, err := encodeTrustedReleaseManifest(candidate.admission.manifest)
	if err != nil {
		return err
	}
	identity, err := candidate.admission.manifest.releaseIdentity()
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, candidate.canonicalManifest) || identity != candidate.admission.identity ||
		candidate.admission.manifest.slot != candidate.architecture.slot {
		return trustedReleaseStoreUntrusted("trusted release candidate admission changed before publication", nil)
	}
	if err := candidate.directory.requireExactLeaves(ctx); err != nil {
		return err
	}
	return candidate.verifyReservedNameBinding(ctx)
}

func (candidate *trustedReleaseCandidate) verifyReservedNameBinding(ctx context.Context) (returnErr error) {
	fd, err := candidate.architecture.deps.openAt(
		ctx,
		candidate.architecture.fd,
		candidate.name,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseStoreUntrusted("reserved trusted release candidate is missing or unsafe", err)
		}
		return trustedReleaseStoreError("open reserved trusted release candidate before publication", err)
	}
	defer func() {
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close reserved trusted release candidate publication check",
			candidate.architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		candidate.architecture.deps,
		fd,
		"candidate before publication",
		candidate.architecture.policy,
		true,
	); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := candidate.architecture.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect reserved trusted release candidate before publication", err)
	}
	if stat.Dev != candidate.directoryIdentity.Dev || stat.Ino != candidate.directoryIdentity.Ino {
		return trustedReleaseStoreUntrusted(
			"reserved trusted release candidate identity changed before publication",
			nil,
		)
	}
	return nil
}

func (candidate *trustedReleaseCandidate) readInstalledManifestForPublication(
	ctx context.Context,
) (canonical []byte, exists bool, returnErr error) {
	fd, err := candidate.architecture.deps.openAt(
		ctx,
		candidate.architecture.fd,
		candidate.architecture.slot.version,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, classifyTrustedReleaseStoreOpenError("installed release version", err, false)
	}
	directory := &trustedReleaseDirectory{
		deps: candidate.architecture.deps, policy: candidate.architecture.policy,
		slot: candidate.architecture.slot, slotFD: fd, retained: []int{fd},
	}
	var admission *trustedReleaseDirectoryAdmission
	defer func() {
		if admission != nil {
			returnErr = appendTrustedReleaseStoreError(
				returnErr,
				"close existing trusted release admission after publication check",
				admission.Release(context.WithoutCancel(ctx)),
			)
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close existing trusted release directory after publication check",
			directory.Release(context.WithoutCancel(ctx)),
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
		return nil, false, err
	}
	if err := directory.requireExactLeaves(ctx); err != nil {
		return nil, false, err
	}
	admission, err = directory.admit(ctx)
	if err != nil {
		return nil, false, err
	}
	canonical, err = encodeTrustedReleaseManifest(admission.manifest)
	if err != nil {
		return nil, false, err
	}
	return canonical, true, nil
}

func classifyTrustedReleaseNoReplaceError(cause error) error {
	if errors.Is(cause, unix.ENOSYS) || errors.Is(cause, unix.EOPNOTSUPP) || errors.Is(cause, unix.EINVAL) {
		return trustedReleaseStoreError("host does not support atomic no-replace trusted release publication", cause)
	}
	if errors.Is(cause, unix.EXDEV) {
		return trustedReleaseStoreError("trusted release candidate publication crossed filesystems", cause)
	}
	return trustedReleaseStoreError("atomically publish absent trusted release candidate", cause)
}

func annotateTrustedReleaseInstalled(cause error, durabilityUncertain bool) *errs.DomainError {
	domainErr := errs.AsDomainError(cause)
	if domainErr == nil {
		domainErr = trustedReleaseStoreError("trusted release installed with a later failure", cause)
	}
	if domainErr.Details == nil {
		domainErr.Details = make(map[string]any)
	}
	domainErr.Details["release_installed"] = true
	if durabilityUncertain {
		domainErr.Details["durability_uncertain"] = true
	}
	return domainErr
}

func renameTrustedReleaseCandidateNoReplace(
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
