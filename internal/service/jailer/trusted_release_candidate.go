package jailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sort"

	"golang.org/x/sys/unix"
)

const trustedReleaseCandidateNameAttempts = 8

type trustedReleaseCandidateState uint8

const (
	trustedReleaseCandidateReady trustedReleaseCandidateState = iota
	trustedReleaseCandidateReleased
)

type trustedReleaseCandidate struct {
	architecture      *trustedReleaseArchitectureWriteLease
	name              string
	directory         *trustedReleaseDirectory
	directoryIdentity unix.Stat_t
	admission         *trustedReleaseDirectoryAdmission
	canonicalManifest []byte
	state             trustedReleaseCandidateState
}

type trustedReleaseCandidateLink struct {
	leaf     string
	fd       int
	identity unix.Stat_t
	size     uint64
	mode     uint32
}

// CRITICAL: This is the only transition from anonymous release objects to a named candidate. It consumes the exact
// finalized pair, closes every writable descriptor before shared re-admission, and never creates the canonical version
// leaf. The returned capability owns the pinned architecture and candidate directory until publication or discard.
func (architecture *trustedReleaseArchitectureWriteLease) stageCandidate(
	ctx context.Context,
	stages *trustedReleaseSelectedExecutableStages,
) (_ *trustedReleaseCandidate, returnErr error) {
	canonicalManifest, err := architecture.validateCandidateStages(ctx, stages)
	if err != nil {
		return nil, err
	}
	if err := architecture.recoverCandidates(ctx); err != nil {
		return nil, err
	}
	finalizedManifest := stages.manifest

	candidate := &trustedReleaseCandidate{architecture: architecture, state: trustedReleaseCandidateReady}
	consumeStages := false
	var manifestStage *trustedReleaseManifestStage
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		if manifestStage != nil {
			returnErr = appendTrustedReleaseStoreError(
				returnErr,
				"release rejected trusted release candidate manifest stage",
				manifestStage.Release(cleanupCtx),
			)
		}
		if consumeStages {
			returnErr = appendTrustedReleaseStoreError(
				returnErr,
				"release rejected trusted release candidate executable stages",
				stages.Release(cleanupCtx),
			)
		}
		returnErr = candidate.discard(cleanupCtx, false, returnErr)
	}()

	candidate.name, candidate.directory, candidate.directoryIdentity, err = architecture.createCandidateDirectory(ctx)
	if candidate.name != "" {
		consumeStages = true
		stages.state = trustedReleaseSelectedStagesFailed
	}
	if err != nil {
		return nil, err
	}
	manifestStage, err = architecture.createManifestStage(ctx, finalizedManifest)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonicalManifest, manifestStage.raw) {
		return nil, trustedReleaseStoreError("trusted release candidate manifest encoding changed", nil)
	}
	if err := candidate.requireReadyToLink(ctx, stages, manifestStage); err != nil {
		return nil, err
	}

	links := [...]trustedReleaseCandidateLink{
		{
			leaf: trustedReleaseFirecrackerLeaf, fd: stages.firecracker.fd,
			identity: stages.firecracker.identity, size: stages.firecracker.sizeBytes,
			mode: trustedReleaseStoreExecutableMode,
		},
		{
			leaf: trustedReleaseJailerLeaf, fd: stages.jailer.fd,
			identity: stages.jailer.identity, size: stages.jailer.sizeBytes,
			mode: trustedReleaseStoreExecutableMode,
		},
		{
			leaf: trustedReleaseManifestLeaf, fd: manifestStage.fd,
			identity: manifestStage.identity, size: uint64(len(manifestStage.raw)),
			mode: trustedReleaseStoreManifestMode,
		},
	}
	for _, link := range links {
		if err := architecture.deps.linkAnonymousLeaf(ctx, link.fd, candidate.directory.slotFD, link.leaf); err != nil {
			return nil, trustedReleaseStoreError("link trusted release candidate leaf "+link.leaf, err)
		}
		if err := candidate.verifyLinkedLeaf(ctx, link); err != nil {
			return nil, err
		}
		if err := architecture.deps.fsync(ctx, link.fd); err != nil {
			return nil, trustedReleaseStoreError("sync linked trusted release candidate leaf "+link.leaf, err)
		}
	}

	if err := manifestStage.Release(ctx); err != nil {
		return nil, err
	}
	manifestStage = nil
	if err := stages.Release(ctx); err != nil {
		return nil, err
	}
	consumeStages = false
	if err := architecture.deps.fsync(ctx, candidate.directory.slotFD); err != nil {
		return nil, trustedReleaseStoreError("sync complete trusted release candidate directory", err)
	}
	if err := architecture.deps.fsync(ctx, architecture.fd); err != nil {
		return nil, trustedReleaseStoreError("sync trusted release architecture before candidate admission", err)
	}
	if err := candidate.requireExactLeaves(ctx); err != nil {
		return nil, err
	}
	candidate.admission, err = candidate.directory.admit(ctx)
	if err != nil {
		return nil, err
	}
	admittedRaw, err := encodeTrustedReleaseManifest(candidate.admission.manifest)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonicalManifest, admittedRaw) || candidate.admission.manifest != finalizedManifest {
		return nil, trustedReleaseStoreUntrusted(
			"re-admitted trusted release candidate manifest does not match finalized stages",
			nil,
		)
	}
	candidate.canonicalManifest = bytes.Clone(canonicalManifest)
	candidate.architecture = takeTrustedReleaseArchitectureWriteLease(architecture)
	return candidate, nil
}

func (architecture *trustedReleaseArchitectureWriteLease) validateCandidateStages(
	ctx context.Context,
	stages *trustedReleaseSelectedExecutableStages,
) ([]byte, error) {
	if err := architecture.requireActiveSlotLease(); err != nil {
		return nil, err
	}
	if stages == nil || !stages.active() || stages.state != trustedReleaseSelectedStagesFinalized {
		return nil, trustedReleaseStoreError("trusted release executable stages are not finalized for candidate", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("validate trusted release candidate stages", err)
	}
	if stages.policy != architecture.policy || stages.archivePolicy.source.slot != architecture.slot ||
		stages.manifest.slot != architecture.slot || stages.manifest.archiveDigest != stages.archiveDigest ||
		stages.manifest.firecracker.sizeBytes != stages.firecracker.sizeBytes ||
		stages.manifest.jailer.sizeBytes != stages.jailer.sizeBytes {
		return nil, trustedReleaseStoreError(
			"trusted release candidate stages do not match architecture authority",
			nil,
		)
	}
	return encodeTrustedReleaseManifest(stages.manifest)
}

func (architecture *trustedReleaseArchitectureWriteLease) createCandidateDirectory(
	ctx context.Context,
) (string, *trustedReleaseDirectory, unix.Stat_t, error) {
	prefix, err := trustedReleaseCandidatePrefix(architecture.slot)
	if err != nil {
		return "", nil, unix.Stat_t{}, err
	}
	var name string
	for range trustedReleaseCandidateNameAttempts {
		if err := ctx.Err(); err != nil {
			return "", nil, unix.Stat_t{}, trustedReleaseStoreError(
				"create trusted release candidate directory",
				err,
			)
		}
		name, err = architecture.deps.candidateName(ctx, architecture.slot)
		if err != nil {
			return "", nil, unix.Stat_t{}, trustedReleaseStoreError(
				"generate trusted release candidate name",
				err,
			)
		}
		if err := validateTrustedReleaseCandidateName(prefix, name); err != nil {
			return "", nil, unix.Stat_t{}, err
		}
		err = architecture.deps.mkdirAt(ctx, architecture.fd, name, trustedReleaseStoreDirectoryMode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, unix.Stat_t{}, trustedReleaseStoreError(
				"create trusted release candidate directory",
				err,
			)
		}
		break
	}
	if err != nil && errors.Is(err, unix.EEXIST) {
		return "", nil, unix.Stat_t{}, trustedReleaseStoreError(
			"exclusive trusted release candidate names exhausted",
			err,
		)
	}
	if err := ctx.Err(); err != nil {
		return name, nil, unix.Stat_t{}, trustedReleaseStoreError("open trusted release candidate directory", err)
	}
	fd, err := architecture.deps.openAt(ctx, architecture.fd, name, trustedReleaseStoreDirectoryFlags, 0)
	if err != nil {
		return name, nil, unix.Stat_t{}, trustedReleaseStoreError("open trusted release candidate directory", err)
	}
	directory := &trustedReleaseDirectory{
		deps: architecture.deps, policy: architecture.policy, slot: architecture.slot, slotFD: fd, retained: []int{fd},
	}
	if err := architecture.deps.fchown(
		ctx,
		fd,
		int(architecture.policy.expectedUID),
		int(architecture.policy.expectedGID),
	); err != nil {
		return name, directory, unix.Stat_t{}, trustedReleaseStoreError(
			"set trusted release candidate directory owner",
			err,
		)
	}
	if err := architecture.deps.fchmod(ctx, fd, trustedReleaseStoreDirectoryMode); err != nil {
		return name, directory, unix.Stat_t{}, trustedReleaseStoreError(
			"set trusted release candidate directory mode",
			err,
		)
	}
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		architecture.deps,
		fd,
		"candidate",
		architecture.policy,
		true,
	); err != nil {
		return name, directory, unix.Stat_t{}, err
	}
	var architectureStat unix.Stat_t
	if err := architecture.deps.fstat(ctx, architecture.fd, &architectureStat); err != nil {
		return name, directory, unix.Stat_t{}, trustedReleaseStoreError(
			"inspect trusted release architecture for candidate",
			err,
		)
	}
	var candidateStat unix.Stat_t
	if err := architecture.deps.fstat(ctx, fd, &candidateStat); err != nil {
		return name, directory, unix.Stat_t{}, trustedReleaseStoreError(
			"inspect trusted release candidate directory",
			err,
		)
	}
	if candidateStat.Dev != architectureStat.Dev {
		return name, directory, unix.Stat_t{}, trustedReleaseStoreError(
			"trusted release candidate is on a different filesystem",
			nil,
		)
	}
	return name, directory, candidateStat, nil
}

func (candidate *trustedReleaseCandidate) requireReadyToLink(
	ctx context.Context,
	stages *trustedReleaseSelectedExecutableStages,
	manifest *trustedReleaseManifestStage,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("begin trusted release candidate linking", err)
	}
	if candidate == nil || candidate.directory == nil || candidate.directory.slotFD < 0 ||
		candidate.architecture == nil {
		return trustedReleaseStoreError("trusted release candidate is not active for linking", nil)
	}
	if err := candidate.architecture.requireActiveSlotLease(); err != nil {
		return err
	}
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		candidate.directory.deps,
		candidate.directory.slotFD,
		"candidate before linking",
		candidate.directory.policy,
		true,
	); err != nil {
		return err
	}
	var directoryStat unix.Stat_t
	if err := candidate.directory.deps.fstat(ctx, candidate.directory.slotFD, &directoryStat); err != nil {
		return trustedReleaseStoreError("inspect trusted release candidate before linking", err)
	}
	if directoryStat.Dev != candidate.directoryIdentity.Dev || directoryStat.Ino != candidate.directoryIdentity.Ino {
		return trustedReleaseStoreError("trusted release candidate directory identity changed before linking", nil)
	}
	for _, stage := range []*trustedReleaseSelectedExecutableStage{stages.firecracker, stages.jailer} {
		if _, err := inspectTrustedReleaseSelectedStage(
			ctx,
			stages.deps,
			stages.policy,
			stage,
			directoryStat.Dev,
			stage.sizeBytes,
			trustedReleaseStoreExecutableMode,
			true,
		); err != nil {
			return err
		}
		if err := requireTrustedReleaseSelectedStageZeroOffset(ctx, stages.deps, stage); err != nil {
			return err
		}
	}
	if _, err := manifest.inspect(ctx, directoryStat.Dev, uint64(len(manifest.raw)), true); err != nil {
		return err
	}
	return manifest.requireZeroOffset(ctx)
}

func (candidate *trustedReleaseCandidate) verifyLinkedLeaf(
	ctx context.Context,
	link trustedReleaseCandidateLink,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("verify linked trusted release candidate leaf "+link.leaf, err)
	}
	var stat unix.Stat_t
	if err := candidate.directory.deps.fstat(ctx, link.fd, &stat); err != nil {
		return trustedReleaseStoreError("verify linked trusted release candidate leaf "+link.leaf, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Dev != candidate.directoryIdentity.Dev ||
		stat.Dev != link.identity.Dev || stat.Ino != link.identity.Ino || stat.Nlink != 1 ||
		stat.Uid != candidate.directory.policy.expectedUID || stat.Gid != candidate.directory.policy.expectedGID ||
		stat.Mode&07777 != link.mode || stat.Size < 0 || uint64(stat.Size) != link.size ||
		stat.Mtim != link.identity.Mtim {
		return trustedReleaseStoreError(
			"linked trusted release candidate leaf "+link.leaf+" has unsafe metadata or identity",
			nil,
		)
	}
	return nil
}

func (candidate *trustedReleaseCandidate) requireExactLeaves(ctx context.Context) error {
	names, err := candidate.directory.deps.readDirNames(ctx, candidate.directory.slotFD)
	if err != nil {
		return trustedReleaseStoreError("enumerate complete trusted release candidate", err)
	}
	sort.Strings(names)
	want := []string{trustedReleaseFirecrackerLeaf, trustedReleaseJailerLeaf, trustedReleaseManifestLeaf}
	sort.Strings(want)
	if !slices.Equal(names, want) {
		return trustedReleaseStoreUntrusted("trusted release candidate does not contain exactly the fixed leaves", nil)
	}
	return nil
}

func takeTrustedReleaseArchitectureWriteLease(
	architecture *trustedReleaseArchitectureWriteLease,
) *trustedReleaseArchitectureWriteLease {
	owned := *architecture
	architecture.fd = -1
	architecture.slotLease = nil
	return &owned
}

func (candidate *trustedReleaseCandidate) Release(ctx context.Context) error {
	if candidate == nil || candidate.state == trustedReleaseCandidateReleased {
		return nil
	}
	return candidate.discard(context.WithoutCancel(ctx), true, nil)
}

func (candidate *trustedReleaseCandidate) discard(
	ctx context.Context,
	releaseArchitecture bool,
	primary error,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if candidate.admission != nil {
		result = appendTrustedReleaseStoreError(
			result,
			"release trusted release candidate admission",
			candidate.admission.Release(cleanupCtx),
		)
		candidate.admission = nil
	}
	removeDirectory := true
	if candidate.directory != nil && candidate.directory.slotFD >= 0 {
		for _, leaf := range []string{
			trustedReleaseManifestLeaf,
			trustedReleaseJailerLeaf,
			trustedReleaseFirecrackerLeaf,
		} {
			err := candidate.directory.deps.unlinkAt(cleanupCtx, candidate.directory.slotFD, leaf, 0)
			if err != nil && !errors.Is(err, unix.ENOENT) {
				result = appendTrustedReleaseStoreError(result, "remove trusted release candidate leaf "+leaf, err)
				removeDirectory = false
			}
		}
		if err := candidate.directory.deps.fsync(cleanupCtx, candidate.directory.slotFD); err != nil {
			result = appendTrustedReleaseStoreError(result, "sync discarded trusted release candidate directory", err)
			removeDirectory = false
		}
	}
	if removeDirectory && candidate.name != "" && candidate.architecture != nil && candidate.architecture.fd >= 0 {
		err := candidate.architecture.deps.unlinkAt(
			cleanupCtx,
			candidate.architecture.fd,
			candidate.name,
			unix.AT_REMOVEDIR,
		)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			result = appendTrustedReleaseStoreError(result, "remove discarded trusted release candidate", err)
			removeDirectory = false
		}
		if removeDirectory {
			result = appendTrustedReleaseStoreError(
				result,
				"sync trusted release architecture after candidate discard",
				candidate.architecture.deps.fsync(cleanupCtx, candidate.architecture.fd),
			)
		}
	}
	if candidate.directory != nil {
		result = appendTrustedReleaseStoreError(
			result,
			"close trusted release candidate directory",
			candidate.directory.Release(cleanupCtx),
		)
		candidate.directory = nil
	}
	if releaseArchitecture && candidate.architecture != nil {
		result = appendTrustedReleaseStoreError(
			result,
			"close trusted release candidate architecture",
			candidate.architecture.Release(cleanupCtx),
		)
		candidate.architecture = nil
	}
	candidate.name = ""
	candidate.directoryIdentity = unix.Stat_t{}
	candidate.canonicalManifest = nil
	candidate.state = trustedReleaseCandidateReleased
	return result
}

func randomTrustedReleaseCandidateName(ctx context.Context, slot releaseSlot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prefix, err := trustedReleaseCandidatePrefix(slot)
	if err != nil {
		return "", err
	}
	var nonce [trustedReleaseCandidateNonceBytes]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(nonce[:]) + trustedReleaseCandidateNameSuffix, nil
}

func linkAnonymousTrustedReleaseLeaf(
	ctx context.Context,
	sourceFD int,
	targetDirectoryFD int,
	targetLeaf string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch targetLeaf {
	case trustedReleaseFirecrackerLeaf, trustedReleaseJailerLeaf, trustedReleaseManifestLeaf:
		return unix.Linkat(sourceFD, "", targetDirectoryFD, targetLeaf, unix.AT_EMPTY_PATH)
	default:
		return unix.EINVAL
	}
}
