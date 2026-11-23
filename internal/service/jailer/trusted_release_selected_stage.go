package jailer

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

const (
	trustedReleaseSelectedStageFlags     = unix.O_TMPFILE | unix.O_RDWR | unix.O_CLOEXEC
	trustedReleaseSelectedStageWriteMode = uint32(0600)
)

type trustedReleaseSelectedStagesState uint8

const (
	trustedReleaseSelectedStagesCreated trustedReleaseSelectedStagesState = iota
	trustedReleaseSelectedStagesExtracting
	trustedReleaseSelectedStagesExtracted
	trustedReleaseSelectedStagesFinalized
	trustedReleaseSelectedStagesFailed
)

type trustedReleaseSelectedExecutableStages struct {
	deps          trustedReleaseStoreDeps
	policy        trustedReleaseStorePolicy
	archivePolicy trustedReleaseArchivePolicy
	firecracker   *trustedReleaseSelectedExecutableStage
	jailer        *trustedReleaseSelectedExecutableStage
	archiveDigest trustedReleaseArchiveDigest
	manifest      trustedReleaseManifest
	state         trustedReleaseSelectedStagesState
}

type trustedReleaseSelectedExecutableStage struct {
	name      string
	fd        int
	sizeBytes uint64
	identity  unix.Stat_t
}

// CRITICAL: These are the only anonymous objects that may later become the fixed executable leaves. O_TMPFILE keeps
// them unreachable by pathname while extraction and admission run; omitting O_EXCL retains only the later Linkat
// capability required for descriptor-relative publication.
func (lease *trustedReleaseStoreWriteLease) createSelectedExecutableStages(
	ctx context.Context,
	archivePolicy trustedReleaseArchivePolicy,
) (_ *trustedReleaseSelectedExecutableStages, returnErr error) {
	if lease == nil || lease.store == nil || lease.store.binariesFD < 0 || len(lease.store.retained) == 0 {
		return nil, trustedReleaseStoreError("trusted release store write lease is not active", nil)
	}
	if err := validateTrustedReleaseArchivePolicy(archivePolicy); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("create trusted release selected executable stages", err)
	}

	var storeStat unix.Stat_t
	if err := lease.store.deps.fstat(ctx, lease.store.binariesFD, &storeStat); err != nil {
		return nil, trustedReleaseStoreError("inspect trusted release selected staging filesystem", err)
	}

	stages := &trustedReleaseSelectedExecutableStages{
		deps:          lease.store.deps,
		policy:        lease.store.policy,
		archivePolicy: archivePolicy,
		state:         trustedReleaseSelectedStagesCreated,
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = releaseRejectedTrustedReleaseSelectedStages(ctx, returnErr, stages)
	}()

	var err error
	stages.firecracker, err = stages.createStage(ctx, lease.store.binariesFD, storeStat.Dev, "Firecracker")
	if err != nil {
		return nil, err
	}
	stages.jailer, err = stages.createStage(ctx, lease.store.binariesFD, storeStat.Dev, "Jailer")
	if err != nil {
		return nil, err
	}
	return stages, nil
}

func (stages *trustedReleaseSelectedExecutableStages) createStage(
	ctx context.Context,
	storeFD int,
	storeDevice uint64,
	name string,
) (_ *trustedReleaseSelectedExecutableStage, returnErr error) {
	fd, err := stages.deps.openAt(
		ctx,
		storeFD,
		".",
		trustedReleaseSelectedStageFlags,
		trustedReleaseSelectedStageWriteMode,
	)
	if err != nil {
		return nil, trustedReleaseStoreError("create anonymous trusted release "+name+" stage", err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release "+name+" stage",
			stages.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()

	stage := &trustedReleaseSelectedExecutableStage{name: name, fd: fd}
	initial, err := inspectTrustedReleaseSelectedStage(
		ctx,
		stages.deps,
		stages.policy,
		stage,
		storeDevice,
		0,
		trustedReleaseSelectedStageWriteMode,
		false,
	)
	if err != nil {
		return nil, err
	}
	if err := requireTrustedReleaseSelectedStageZeroOffset(ctx, stages.deps, stage); err != nil {
		return nil, err
	}
	stage.identity = initial
	if err := stages.deps.fchown(
		ctx,
		fd,
		int(stages.policy.expectedUID),
		int(stages.policy.expectedGID),
	); err != nil {
		return nil, trustedReleaseStoreError("set trusted release "+name+" stage owner", err)
	}
	if err := stages.deps.fchmod(ctx, fd, trustedReleaseSelectedStageWriteMode); err != nil {
		return nil, trustedReleaseStoreError("set trusted release "+name+" stage mode", err)
	}
	final, err := inspectTrustedReleaseSelectedStage(
		ctx,
		stages.deps,
		stages.policy,
		stage,
		storeDevice,
		0,
		trustedReleaseSelectedStageWriteMode,
		true,
	)
	if err != nil {
		return nil, err
	}
	if initial.Dev != final.Dev || initial.Ino != final.Ino {
		return nil, trustedReleaseStoreError("trusted release "+name+" stage identity changed while creating", nil)
	}
	stage.identity = final
	return stage, nil
}

// CRITICAL: This wrapper is the only state transition that authorizes parser callbacks. A parser failure permanently
// poisons both selected objects, so incomplete bytes can never be finalized by a later call.
func (stages *trustedReleaseSelectedExecutableStages) extract(
	ctx context.Context,
	archive *trustedReleaseArchiveStage,
) error {
	if !stages.active() {
		return trustedReleaseStoreError("trusted release selected executable stages are not active", nil)
	}
	if stages.state != trustedReleaseSelectedStagesCreated {
		return trustedReleaseStoreError("trusted release selected executable stages are not ready for extraction", nil)
	}
	if archive == nil || archive.fd < 0 {
		return trustedReleaseStoreError("trusted release archive stage is not active", nil)
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("extract trusted release selected executables", err)
	}

	stages.state = trustedReleaseSelectedStagesExtracting
	if err := archive.extract(ctx, stages.archivePolicy, stages); err != nil {
		stages.state = trustedReleaseSelectedStagesFailed
		return err
	}
	stages.archiveDigest = archive.archiveDigest
	stages.state = trustedReleaseSelectedStagesExtracted
	return nil
}

func (stages *trustedReleaseSelectedExecutableStages) writeFirecracker(
	ctx context.Context,
	value []byte,
	offset uint64,
) error {
	if stages == nil {
		return trustedReleaseStoreError("trusted release selected executable stages are not active", nil)
	}
	return stages.write(ctx, stages.firecracker, value, offset)
}

func (stages *trustedReleaseSelectedExecutableStages) writeJailer(
	ctx context.Context,
	value []byte,
	offset uint64,
) error {
	if stages == nil {
		return trustedReleaseStoreError("trusted release selected executable stages are not active", nil)
	}
	return stages.write(ctx, stages.jailer, value, offset)
}

func (stages *trustedReleaseSelectedExecutableStages) write(
	ctx context.Context,
	stage *trustedReleaseSelectedExecutableStage,
	value []byte,
	offset uint64,
) error {
	if !stages.active() || stages.state != trustedReleaseSelectedStagesExtracting || stage == nil || stage.fd < 0 {
		return trustedReleaseStoreError("trusted release selected executable stage is not writable", nil)
	}
	if len(value) == 0 || offset != stage.sizeBytes {
		stages.state = trustedReleaseSelectedStagesFailed
		return trustedReleaseStoreError("trusted release "+stage.name+" stage write is not sequential", nil)
	}
	if offset > trustedReleaseArchiveMaxMemberBytes ||
		uint64(len(value)) > trustedReleaseArchiveMaxMemberBytes-offset {
		stages.state = trustedReleaseSelectedStagesFailed
		return trustedReleaseStoreUntrusted("trusted release "+stage.name+" stage exceeds archive member policy", nil)
	}

	for written := 0; written < len(value); {
		if err := ctx.Err(); err != nil {
			stages.state = trustedReleaseSelectedStagesFailed
			return trustedReleaseStoreError("write trusted release "+stage.name+" stage", err)
		}
		count, writeErr := stages.deps.pwrite(
			ctx,
			stage.fd,
			value[written:],
			int64(offset)+int64(written),
		)
		if count < 0 || count > len(value)-written {
			stages.state = trustedReleaseSelectedStagesFailed
			return trustedReleaseStoreError(
				"write trusted release "+stage.name+" stage",
				fmt.Errorf("invalid positioned write count %d", count),
			)
		}
		written += count
		stage.sizeBytes = offset + uint64(written)
		if writeErr != nil {
			stages.state = trustedReleaseSelectedStagesFailed
			return trustedReleaseStoreError("write trusted release "+stage.name+" stage", writeErr)
		}
		if count == 0 {
			stages.state = trustedReleaseSelectedStagesFailed
			return trustedReleaseStoreError("write trusted release "+stage.name+" stage", io.ErrNoProgress)
		}
	}
	return nil
}

// CRITICAL: Both complete objects pass stable full-byte hashing and ELF admission before either is made executable.
// The resulting descriptors remain anonymous and unpublished; the later atomic publication slice consumes them.
func (stages *trustedReleaseSelectedExecutableStages) finalize(
	ctx context.Context,
) (trustedReleaseManifest, error) {
	if !stages.active() {
		return trustedReleaseManifest{}, trustedReleaseStoreError(
			"trusted release selected executable stages are not active",
			nil,
		)
	}
	if stages.state != trustedReleaseSelectedStagesExtracted {
		return trustedReleaseManifest{}, trustedReleaseStoreError(
			"trusted release selected executable stages are not ready for finalization",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseManifest{}, trustedReleaseStoreError("finalize trusted release selected executables", err)
	}

	stages.state = trustedReleaseSelectedStagesFailed
	firecracker, err := stages.admit(ctx, stages.firecracker)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	jailer, err := stages.admit(ctx, stages.jailer)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	if err := stages.makeExecutable(ctx, stages.firecracker); err != nil {
		return trustedReleaseManifest{}, err
	}
	if err := stages.makeExecutable(ctx, stages.jailer); err != nil {
		return trustedReleaseManifest{}, err
	}

	manifest := trustedReleaseManifest{
		schemaVersion: trustedReleaseManifestSchemaVersion,
		slot:          stages.archivePolicy.source.slot,
		archiveDigest: stages.archiveDigest,
		firecracker:   firecracker,
		jailer:        jailer,
	}
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return trustedReleaseManifest{}, err
	}
	stages.manifest = manifest
	stages.state = trustedReleaseSelectedStagesFinalized
	return manifest, nil
}

func (stages *trustedReleaseSelectedExecutableStages) admit(
	ctx context.Context,
	stage *trustedReleaseSelectedExecutableStage,
) (trustedReleaseExecutable, error) {
	before, err := inspectTrustedReleaseSelectedStage(
		ctx,
		stages.deps,
		stages.policy,
		stage,
		stage.identity.Dev,
		stage.sizeBytes,
		trustedReleaseSelectedStageWriteMode,
		true,
	)
	if err != nil {
		return trustedReleaseExecutable{}, err
	}
	if err := requireTrustedReleaseSelectedStageZeroOffset(ctx, stages.deps, stage); err != nil {
		return trustedReleaseExecutable{}, err
	}

	executable := trustedReleaseExecutable{sizeBytes: stage.sizeBytes}
	readPolicy := trustedReleaseExecutablePolicy{name: stage.name, expected: executable}
	header, digest, err := hashTrustedReleaseExecutable(ctx, stages.deps, stage.fd, readPolicy)
	if err != nil {
		return trustedReleaseExecutable{}, err
	}
	after, err := inspectTrustedReleaseSelectedStage(
		ctx,
		stages.deps,
		stages.policy,
		stage,
		stage.identity.Dev,
		stage.sizeBytes,
		trustedReleaseSelectedStageWriteMode,
		true,
	)
	if err != nil {
		return trustedReleaseExecutable{}, err
	}
	if !sameTrustedReleaseExecutableStat(before, after) {
		return trustedReleaseExecutable{}, trustedReleaseStoreError(
			"trusted release "+stage.name+" stage changed while being read",
			nil,
		)
	}
	if err := requireTrustedReleaseSelectedStageZeroOffset(ctx, stages.deps, stage); err != nil {
		return trustedReleaseExecutable{}, err
	}
	if err := validateTrustedReleaseELFHeader(
		header[:],
		stage.sizeBytes,
		stages.archivePolicy.source,
	); err != nil {
		return trustedReleaseExecutable{}, err
	}
	executable.digest = digest
	return executable, nil
}

func (stages *trustedReleaseSelectedExecutableStages) makeExecutable(
	ctx context.Context,
	stage *trustedReleaseSelectedExecutableStage,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("finalize trusted release "+stage.name+" stage", err)
	}
	if err := stages.deps.fchmod(ctx, stage.fd, trustedReleaseStoreExecutableMode); err != nil {
		return trustedReleaseStoreError("set trusted release "+stage.name+" stage executable mode", err)
	}
	if err := stages.deps.fsync(ctx, stage.fd); err != nil {
		return trustedReleaseStoreError("sync trusted release "+stage.name+" stage", err)
	}
	final, err := inspectTrustedReleaseSelectedStage(
		ctx,
		stages.deps,
		stages.policy,
		stage,
		stage.identity.Dev,
		stage.sizeBytes,
		trustedReleaseStoreExecutableMode,
		true,
	)
	if err != nil {
		return err
	}
	if final.Dev != stage.identity.Dev || final.Ino != stage.identity.Ino {
		return trustedReleaseStoreError("trusted release "+stage.name+" stage identity changed while finalizing", nil)
	}
	stage.identity = final
	return requireTrustedReleaseSelectedStageZeroOffset(ctx, stages.deps, stage)
}

func inspectTrustedReleaseSelectedStage(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	policy trustedReleaseStorePolicy,
	stage *trustedReleaseSelectedExecutableStage,
	storeDevice uint64,
	expectedSize uint64,
	expectedMode uint32,
	requireIdentity bool,
) (unix.Stat_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError("inspect trusted release selected executable stage", err)
	}
	if stage == nil || stage.fd < 0 {
		return unix.Stat_t{}, trustedReleaseStoreError("trusted release selected executable stage is not active", nil)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, stage.fd, &stat); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError("inspect trusted release "+stage.name+" stage", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 0 || stat.Dev != storeDevice ||
		stat.Size < 0 || uint64(stat.Size) != expectedSize {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"trusted release "+stage.name+" stage has unsafe filesystem metadata or size",
			nil,
		)
	}
	if requireIdentity && (stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&07777 != expectedMode) {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"trusted release "+stage.name+" stage has unexpected owner or mode",
			nil,
		)
	}
	if requireIdentity && (stat.Dev != stage.identity.Dev || stat.Ino != stage.identity.Ino) {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"trusted release "+stage.name+" stage identity changed",
			nil,
		)
	}
	return stat, nil
}

func requireTrustedReleaseSelectedStageZeroOffset(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	stage *trustedReleaseSelectedExecutableStage,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("inspect trusted release selected executable stage offset", err)
	}
	offset, err := deps.seek(ctx, stage.fd, 0, unix.SEEK_CUR)
	if err != nil {
		return trustedReleaseStoreError("inspect trusted release "+stage.name+" stage offset", err)
	}
	if offset != 0 {
		return trustedReleaseStoreError("trusted release "+stage.name+" stage offset is not zero", nil)
	}
	return nil
}

func (stages *trustedReleaseSelectedExecutableStages) active() bool {
	return stages != nil && stages.firecracker != nil && stages.firecracker.fd >= 0 &&
		stages.jailer != nil && stages.jailer.fd >= 0
}

func (stages *trustedReleaseSelectedExecutableStages) Release(ctx context.Context) error {
	if stages == nil {
		return nil
	}
	fds := make([]int, 0, 2)
	if stages.firecracker != nil && stages.firecracker.fd >= 0 {
		fds = append(fds, stages.firecracker.fd)
	}
	if stages.jailer != nil && stages.jailer.fd >= 0 {
		fds = append(fds, stages.jailer.fd)
	}
	err := closeTrustedReleaseStoreFDs(ctx, stages.deps, fds)
	if stages.firecracker != nil {
		stages.firecracker.fd = -1
		stages.firecracker.sizeBytes = 0
		stages.firecracker.identity = unix.Stat_t{}
	}
	if stages.jailer != nil {
		stages.jailer.fd = -1
		stages.jailer.sizeBytes = 0
		stages.jailer.identity = unix.Stat_t{}
	}
	stages.archiveDigest = trustedReleaseArchiveDigest{}
	stages.manifest = trustedReleaseManifest{}
	stages.state = trustedReleaseSelectedStagesFailed
	if err != nil {
		return trustedReleaseStoreError("release trusted release selected executable stages", err)
	}
	return nil
}

func releaseRejectedTrustedReleaseSelectedStages(
	ctx context.Context,
	primary error,
	stages *trustedReleaseSelectedExecutableStages,
) error {
	if stages == nil {
		return primary
	}
	return appendTrustedReleaseStoreError(
		primary,
		"release rejected trusted release selected executable stages",
		stages.Release(context.WithoutCancel(ctx)),
	)
}

var _ trustedReleaseArchiveSelectedWriter = (*trustedReleaseSelectedExecutableStages)(nil)
