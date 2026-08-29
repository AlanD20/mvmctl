package jailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	trustedReleaseCandidateNamePrefix  = ".mvm-release-"
	trustedReleaseCandidateNameSuffix  = ".tmp"
	trustedReleaseCandidateNonceBytes  = 16
	trustedReleaseRecoveryInspectFlags = unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
)

type trustedReleaseArchitectureWriteLease struct {
	deps      trustedReleaseStoreDeps
	policy    trustedReleaseStorePolicy
	slotLease *releaseSlotLease
	slot      releaseSlot
	fd        int
}

type trustedReleaseRecoveryCandidate struct {
	name              string
	fd                int
	directoryIdentity unix.Stat_t
	leaves            []string
}

func (architecture *trustedReleaseArchitectureWriteLease) requireActiveSlotLease() error {
	if architecture == nil || architecture.fd < 0 {
		return trustedReleaseStoreError("trusted release architecture write lease is not active", nil)
	}
	if architecture.slotLease == nil || architecture.slotLease.roots == nil ||
		architecture.slotLease.releaseLock == nil || architecture.slotLease.releaseLock.fd < 0 ||
		architecture.slotLease.slot != architecture.slot {
		return trustedReleaseStoreError("release slot lease is not active for trusted release architecture", nil)
	}
	return nil
}

// CRITICAL: Architecture creation is the only durable write-side namespace effect allowed before a complete release
// candidate exists. The directory is fixed by the validated slot, pinned once, and safe to retain empty after failure.
func (lease *trustedReleaseStoreWriteLease) openArchitectureForWrite(
	ctx context.Context,
	slotLease *releaseSlotLease,
) (*trustedReleaseArchitectureWriteLease, error) {
	if lease == nil || lease.store == nil || lease.store.binariesFD < 0 || len(lease.store.retained) == 0 {
		return nil, trustedReleaseStoreError("trusted release store write lease is not active", nil)
	}
	if slotLease == nil || slotLease.roots == nil || slotLease.releaseLock == nil || slotLease.releaseLock.fd < 0 {
		return nil, trustedReleaseStoreError("release slot lease is not active for trusted release write", nil)
	}
	slot := slotLease.slot
	if err := validateReleaseSlotValue(slot); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("open trusted release architecture for write", err)
	}

	fd, err := openTrustedReleaseStoreComponent(
		ctx,
		lease.store.deps,
		lease.store.binariesFD,
		slot.architecture,
		lease.store.policy,
		true,
		true,
	)
	if err != nil {
		return nil, err
	}
	return &trustedReleaseArchitectureWriteLease{
		deps: lease.store.deps, policy: lease.store.policy, slotLease: slotLease, slot: slot, fd: fd,
	}, nil
}

func trustedReleaseCandidatePrefix(slot releaseSlot) (string, error) {
	if err := validateReleaseSlotValue(slot); err != nil {
		return "", instanceValidationError(err.Error())
	}
	return trustedReleaseCandidateNamePrefix + releaseSlotDigest(slot) + "-", nil
}

func validateTrustedReleaseCandidateName(prefix, name string) error {
	if !strings.HasPrefix(name, prefix) {
		return trustedReleaseStoreUntrusted("trusted release candidate name is outside its slot namespace", nil)
	}
	nonce := strings.TrimSuffix(strings.TrimPrefix(name, prefix), trustedReleaseCandidateNameSuffix)
	if len(name) != len(prefix)+trustedReleaseCandidateNonceBytes*2+len(trustedReleaseCandidateNameSuffix) ||
		len(nonce) != trustedReleaseCandidateNonceBytes*2 {
		return trustedReleaseStoreUntrusted("trusted release candidate name is malformed", nil)
	}
	for _, value := range nonce {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return trustedReleaseStoreUntrusted("trusted release candidate name is malformed", nil)
		}
	}
	return nil
}

// CRITICAL: Recovery admits every matching candidate before the first unlink. Once removal starts it uses an
// uncancelled context, unlinks only fixed leaves through pinned descriptors, and never recursively removes content.
func (architecture *trustedReleaseArchitectureWriteLease) recoverCandidates(
	ctx context.Context,
) (returnErr error) {
	if err := architecture.requireActiveSlotLease(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("recover trusted release candidates", err)
	}
	prefix, err := trustedReleaseCandidatePrefix(architecture.slot)
	if err != nil {
		return err
	}
	names, err := architecture.deps.readDirNames(ctx, architecture.fd)
	if err != nil {
		return trustedReleaseStoreError("enumerate trusted release architecture", err)
	}
	sort.Strings(names)

	candidates := make([]trustedReleaseRecoveryCandidate, 0)
	defer func() {
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close trusted release recovery candidates",
			closeTrustedReleaseRecoveryCandidates(ctx, architecture.deps, candidates),
		)
	}()
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		candidate, err := architecture.openRecoveryCandidate(ctx, prefix, name)
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("begin trusted release candidate recovery cleanup", err)
	}

	cleanupCtx := context.WithoutCancel(ctx)
	for _, candidate := range candidates {
		if err := architecture.verifyRecoveryCandidateBinding(
			cleanupCtx,
			candidate,
			"before cleanup",
		); err != nil {
			return err
		}
		for _, leaf := range candidate.leaves {
			if err := architecture.deps.unlinkAt(cleanupCtx, candidate.fd, leaf, 0); err != nil {
				return trustedReleaseStoreError("remove trusted release recovery leaf "+leaf, err)
			}
		}
		if err := architecture.deps.fsync(cleanupCtx, candidate.fd); err != nil {
			return trustedReleaseStoreError("sync cleaned trusted release recovery candidate", err)
		}
		if err := architecture.verifyRecoveryCandidateBinding(
			cleanupCtx,
			candidate,
			"before directory removal",
		); err != nil {
			return err
		}
		if err := architecture.deps.unlinkAt(
			cleanupCtx,
			architecture.fd,
			candidate.name,
			unix.AT_REMOVEDIR,
		); err != nil {
			return trustedReleaseStoreError("remove trusted release recovery candidate", err)
		}
		if err := architecture.deps.fsync(cleanupCtx, architecture.fd); err != nil {
			return trustedReleaseStoreError("sync trusted release architecture after recovery", err)
		}
	}
	if err := architecture.deps.fsync(cleanupCtx, architecture.fd); err != nil {
		return trustedReleaseStoreError("confirm trusted release architecture recovery durability", err)
	}
	return nil
}

func (architecture *trustedReleaseArchitectureWriteLease) openRecoveryCandidate(
	ctx context.Context,
	prefix string,
	name string,
) (_ trustedReleaseRecoveryCandidate, returnErr error) {
	if err := validateTrustedReleaseCandidateName(prefix, name); err != nil {
		return trustedReleaseRecoveryCandidate{}, err
	}
	fd, err := architecture.deps.openAt(ctx, architecture.fd, name, trustedReleaseStoreDirectoryFlags, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseRecoveryCandidate{}, trustedReleaseStoreUntrusted(
				"trusted release recovery candidate is unsafe",
				err,
			)
		}
		return trustedReleaseRecoveryCandidate{}, trustedReleaseStoreError(
			"open trusted release recovery candidate",
			err,
		)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release recovery candidate",
			architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		architecture.deps,
		fd,
		"recovery candidate",
		architecture.policy,
		true,
	); err != nil {
		return trustedReleaseRecoveryCandidate{}, err
	}
	var directoryIdentity unix.Stat_t
	if err := architecture.deps.fstat(ctx, fd, &directoryIdentity); err != nil {
		return trustedReleaseRecoveryCandidate{}, trustedReleaseStoreError(
			"inspect trusted release recovery candidate identity",
			err,
		)
	}

	names, err := architecture.deps.readDirNames(ctx, fd)
	if err != nil {
		return trustedReleaseRecoveryCandidate{}, trustedReleaseStoreError(
			"enumerate trusted release recovery candidate",
			err,
		)
	}
	seen := make(map[string]struct{}, len(names))
	for _, leaf := range names {
		if _, exists := seen[leaf]; exists {
			return trustedReleaseRecoveryCandidate{}, trustedReleaseStoreUntrusted(
				"trusted release recovery candidate contains a duplicate leaf",
				nil,
			)
		}
		seen[leaf] = struct{}{}
		if err := architecture.admitRecoveryLeaf(ctx, fd, leaf); err != nil {
			return trustedReleaseRecoveryCandidate{}, err
		}
	}
	ordered := make([]string, 0, len(seen))
	for _, leaf := range []string{
		trustedReleaseManifestLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseFirecrackerLeaf,
	} {
		if _, exists := seen[leaf]; exists {
			ordered = append(ordered, leaf)
		}
	}
	if len(ordered) != len(seen) {
		return trustedReleaseRecoveryCandidate{}, trustedReleaseStoreUntrusted(
			"trusted release recovery candidate contains an unexpected entry",
			nil,
		)
	}
	return trustedReleaseRecoveryCandidate{
		name: name, fd: fd, directoryIdentity: directoryIdentity, leaves: ordered,
	}, nil
}

func (architecture *trustedReleaseArchitectureWriteLease) verifyRecoveryCandidateBinding(
	ctx context.Context,
	candidate trustedReleaseRecoveryCandidate,
	description string,
) (returnErr error) {
	fd, err := architecture.deps.openAt(
		ctx,
		architecture.fd,
		candidate.name,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseStoreUntrusted(
				"trusted release recovery candidate changed "+description,
				err,
			)
		}
		return trustedReleaseStoreError(
			"open trusted release recovery candidate "+description,
			err,
		)
	}
	defer func() {
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close trusted release recovery candidate binding check "+description,
			architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		architecture.deps,
		fd,
		"recovery candidate "+description,
		architecture.policy,
		true,
	); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := architecture.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError(
			"inspect trusted release recovery candidate binding "+description,
			err,
		)
	}
	if stat.Dev != candidate.directoryIdentity.Dev || stat.Ino != candidate.directoryIdentity.Ino {
		return trustedReleaseStoreUntrusted(
			"trusted release recovery candidate identity changed "+description,
			nil,
		)
	}
	return nil
}

func (architecture *trustedReleaseArchitectureWriteLease) admitRecoveryLeaf(
	ctx context.Context,
	candidateFD int,
	leaf string,
) (returnErr error) {
	wantMode := trustedReleaseStoreExecutableMode
	minBytes := trustedReleaseExecutableMinBytes
	maxBytes := trustedReleaseExecutableMaxBytes
	if leaf == trustedReleaseManifestLeaf {
		wantMode = trustedReleaseStoreManifestMode
		minBytes = 1
		maxBytes = maxTrustedReleaseManifestBytes
	} else if leaf != trustedReleaseFirecrackerLeaf && leaf != trustedReleaseJailerLeaf {
		return trustedReleaseStoreUntrusted(
			"trusted release recovery candidate contains an unexpected entry",
			nil,
		)
	}

	fd, err := architecture.deps.openAt(ctx, candidateFD, leaf, trustedReleaseRecoveryInspectFlags, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return trustedReleaseStoreUntrusted("trusted release recovery leaf is unsafe", err)
		}
		return trustedReleaseStoreError("open trusted release recovery leaf", err)
	}
	defer func() {
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close trusted release recovery leaf",
			architecture.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()

	var stat unix.Stat_t
	if err := architecture.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect trusted release recovery leaf", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != architecture.policy.expectedUID ||
		stat.Gid != architecture.policy.expectedGID || stat.Mode&07777 != wantMode || stat.Nlink != 1 ||
		stat.Size < int64(minBytes) || stat.Size > int64(maxBytes) {
		return trustedReleaseStoreUntrusted("trusted release recovery leaf has unsafe metadata", nil)
	}
	return nil
}

func closeTrustedReleaseRecoveryCandidates(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	candidates []trustedReleaseRecoveryCandidate,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	for index := len(candidates) - 1; index >= 0; index-- {
		if err := deps.close(cleanupCtx, candidates[index].fd); err != nil {
			result = errors.Join(result, fmt.Errorf("candidate %s: %w", candidates[index].name, err))
		}
	}
	return result
}

func readTrustedReleaseDirectoryNames(ctx context.Context, fd int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	duplicateFD, err := unix.Openat(fd, ".", trustedReleaseStoreDirectoryFlags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicateFD), "trusted-release-directory")
	if file == nil {
		closeErr := unix.Close(duplicateFD)
		return nil, errors.Join(fmt.Errorf("create trusted release directory reader"), closeErr)
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (architecture *trustedReleaseArchitectureWriteLease) Release(ctx context.Context) error {
	if architecture == nil || architecture.fd < 0 {
		return nil
	}
	err := architecture.deps.close(context.WithoutCancel(ctx), architecture.fd)
	architecture.fd = -1
	architecture.slotLease = nil
	if err != nil {
		return trustedReleaseStoreError("release trusted release architecture write lease", err)
	}
	return nil
}
