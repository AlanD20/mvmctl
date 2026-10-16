package jailer

import (
	"context"
	"errors"
	"io"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type trustedReleaseCallerArchive struct {
	reader    io.Reader
	sizeBytes uint64
}

type trustedReleaseInstallIntent uint8

const (
	trustedReleaseInstallAbsentOnly trustedReleaseInstallIntent = iota + 1
	trustedReleaseInstallAllowReplacement
)

type trustedReleaseInstallOutcome uint8

const (
	trustedReleaseInstallUnchanged trustedReleaseInstallOutcome = iota + 1
	trustedReleaseInstallInstalled
	trustedReleaseInstallReplaced
)

type trustedReleaseInstallResult struct {
	outcome  trustedReleaseInstallOutcome
	manifest trustedReleaseManifest
}

// CRITICAL: This method composes the entire private release transaction. It derives every source and namespace name,
// holds the exact slot lock across candidate publication and cleanup, and returns metadata only from full re-admission.
func (authority *releaseAuthority) install(
	ctx context.Context,
	slot releaseSlot,
	intent trustedReleaseInstallIntent,
	callerArchive *trustedReleaseCallerArchive,
) (result trustedReleaseInstallResult, returnErr error) {
	if err := authority.validateInstallInput(slot, intent, callerArchive); err != nil {
		return trustedReleaseInstallResult{}, err
	}
	source, err := newTrustedReleaseSource(slot)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	archivePolicy, err := newTrustedReleaseArchivePolicy(source)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseInstallResult{}, trustedReleaseStoreError("install trusted release", err)
	}

	var writer *trustedReleaseStoreWriteLease
	var archive *trustedReleaseArchiveStage
	var selected *trustedReleaseSelectedExecutableStages
	var slotLease *releaseSlotLease
	var architecture *trustedReleaseArchitectureWriteLease
	var candidate *trustedReleaseCandidate
	completed := false
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if candidate != nil {
			returnErr = appendTrustedReleaseInstallError(
				returnErr,
				"release trusted release install candidate",
				candidate.Release(cleanupCtx),
			)
		}
		if architecture != nil {
			returnErr = appendTrustedReleaseInstallError(
				returnErr,
				"release trusted release install architecture",
				architecture.Release(cleanupCtx),
			)
		}
		if slotLease != nil {
			returnErr = appendTrustedReleaseInstallError(
				returnErr,
				"release trusted release install slot lease",
				slotLease.Release(cleanupCtx),
			)
		}
		if selected != nil {
			returnErr = appendTrustedReleaseInstallError(
				returnErr,
				"release trusted release install executable stages",
				selected.Release(cleanupCtx),
			)
		}
		if archive != nil {
			returnErr = appendTrustedReleaseInstallError(
				returnErr,
				"release trusted release install archive stage",
				archive.Release(cleanupCtx),
			)
		}
		if writer != nil {
			returnErr = appendTrustedReleaseInstallError(
				returnErr,
				"release trusted release install store writer",
				writer.Release(cleanupCtx),
			)
		}
		if !completed {
			result = trustedReleaseInstallResult{}
			return
		}
		if returnErr == nil {
			return
		}
		switch result.outcome {
		case trustedReleaseInstallInstalled:
			returnErr = annotateTrustedReleaseInstalled(returnErr, false)
		case trustedReleaseInstallReplaced:
			returnErr = annotateTrustedReleaseReplaced(returnErr, false, false)
		}
	}()

	digest, err := authority.checksumAuthority.fetch(ctx, source)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	writer, err = openTrustedReleaseStoreForWrite(ctx, authority.storeDeps, authority.storePolicy)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	archive, err = writer.createArchiveStage(ctx)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	if callerArchive == nil {
		err = authority.archiveFetcher.fetch(ctx, source, archive, digest)
	} else {
		err = archive.receive(ctx, callerArchive.reader, callerArchive.sizeBytes, digest)
	}
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	selected, err = writer.createSelectedExecutableStages(ctx, archivePolicy)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	if err := selected.extract(ctx, archive); err != nil {
		return trustedReleaseInstallResult{}, err
	}
	if err := archive.Release(ctx); err != nil {
		archive = nil
		return trustedReleaseInstallResult{}, err
	}
	archive = nil
	if _, err := selected.finalize(ctx); err != nil {
		return trustedReleaseInstallResult{}, err
	}

	slotLease, err = authority.instances.lockReleaseSlot(ctx, slot)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	architecture, err = writer.openArchitectureForWrite(ctx, slotLease)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	if err := writer.Release(ctx); err != nil {
		writer = nil
		return trustedReleaseInstallResult{}, err
	}
	writer = nil
	candidate, err = architecture.stageCandidate(ctx, selected)
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	architecture = nil
	selected = nil

	manifest := candidate.admission.manifest
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return trustedReleaseInstallResult{}, err
	}
	if _, err := manifest.releaseIdentity(); err != nil {
		return trustedReleaseInstallResult{}, err
	}

	var changed bool
	changedOutcome := trustedReleaseInstallInstalled
	switch intent {
	case trustedReleaseInstallAbsentOnly:
		changed, err = candidate.publishAbsent(ctx)
	case trustedReleaseInstallAllowReplacement:
		var exists bool
		exists, err = candidate.installedSlotExists(ctx)
		if err == nil && exists {
			changedOutcome = trustedReleaseInstallReplaced
			changed, err = candidate.replaceInstalled(ctx)
		} else if err == nil {
			changed, err = candidate.publishAbsent(ctx)
		}
	}
	if changed {
		result = trustedReleaseInstallResult{outcome: changedOutcome, manifest: manifest}
		completed = true
		return result, err
	}
	if err != nil {
		return trustedReleaseInstallResult{}, err
	}
	result = trustedReleaseInstallResult{outcome: trustedReleaseInstallUnchanged, manifest: manifest}
	completed = true
	return result, nil
}

func (authority *releaseAuthority) validateInstallInput(
	slot releaseSlot,
	intent trustedReleaseInstallIntent,
	callerArchive *trustedReleaseCallerArchive,
) error {
	if authority == nil || authority.instances == nil || authority.checksumAuthority == nil ||
		authority.checksumAuthority.client == nil || authority.archiveFetcher == nil ||
		authority.archiveFetcher.client == nil {
		return trustedReleaseStoreError("release authority is not active for installation", nil)
	}
	if err := validateReleaseSlotValue(slot); err != nil {
		return instanceValidationError(err.Error())
	}
	if intent != trustedReleaseInstallAbsentOnly && intent != trustedReleaseInstallAllowReplacement {
		return trustedReleaseStoreError("trusted release install intent is invalid", nil)
	}
	if callerArchive != nil && (callerArchive.reader == nil || callerArchive.sizeBytes == 0 ||
		callerArchive.sizeBytes > trustedReleaseArchiveMaxBytes) {
		return trustedReleaseStoreError("trusted release caller archive is invalid", nil)
	}
	return nil
}

func appendTrustedReleaseInstallError(primary error, message string, cause error) error {
	if primary == nil && cause != nil && errs.AsDomainError(cause) != nil {
		return cause
	}
	return appendTrustedReleaseStoreError(primary, message, cause)
}

func (candidate *trustedReleaseCandidate) installedSlotExists(
	ctx context.Context,
) (_ bool, returnErr error) {
	if candidate == nil || candidate.state != trustedReleaseCandidateReady || candidate.architecture == nil {
		return false, trustedReleaseStoreError("trusted release candidate is not active for slot selection", nil)
	}
	if err := candidate.architecture.requireActiveSlotLease(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, trustedReleaseStoreError("inspect installed trusted release slot", err)
	}
	fd, err := candidate.architecture.deps.openAt(
		ctx,
		candidate.architecture.fd,
		candidate.architecture.slot.version,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, classifyTrustedReleaseStoreOpenError("installed release version", err, true)
	}
	defer func() {
		returnErr = appendTrustedReleaseInstallError(
			returnErr,
			"close installed trusted release slot selection",
			candidate.architecture.deps.close(context.WithoutCancel(ctx), fd),
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
		return false, err
	}
	var architectureStat unix.Stat_t
	if err := candidate.architecture.deps.fstat(ctx, candidate.architecture.fd, &architectureStat); err != nil {
		return false, trustedReleaseStoreError("inspect trusted release architecture during slot selection", err)
	}
	var slotStat unix.Stat_t
	if err := candidate.architecture.deps.fstat(ctx, fd, &slotStat); err != nil {
		return false, trustedReleaseStoreError("inspect installed trusted release during slot selection", err)
	}
	if slotStat.Dev != architectureStat.Dev {
		return false, trustedReleaseStoreUntrusted("installed trusted release is on a different filesystem", nil)
	}
	return true, nil
}
