package jailer

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

const trustedReleaseManifestStageFlags = unix.O_TMPFILE | unix.O_RDWR | unix.O_CLOEXEC

type trustedReleaseManifestStage struct {
	deps     trustedReleaseStoreDeps
	policy   trustedReleaseStorePolicy
	manifest trustedReleaseManifest
	raw      []byte
	fd       int
	identity unix.Stat_t
}

// CRITICAL: The strict manifest remains an anonymous object until candidate assembly links all three fixed leaves.
// Encoding precedes the anonymous write, and no direct manifest pathname can expose partial authority after a crash.
func (architecture *trustedReleaseArchitectureWriteLease) createManifestStage(
	ctx context.Context,
	manifest trustedReleaseManifest,
) (_ *trustedReleaseManifestStage, returnErr error) {
	if err := architecture.requireActiveSlotLease(); err != nil {
		return nil, err
	}
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return nil, err
	}
	if manifest.slot != architecture.slot {
		return nil, trustedReleaseStoreUntrusted(
			"trusted release manifest stage slot does not match its architecture lease",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("create trusted release manifest stage", err)
	}
	raw, err := encodeTrustedReleaseManifest(manifest)
	if err != nil {
		return nil, err
	}

	var architectureStat unix.Stat_t
	if err := architecture.deps.fstat(ctx, architecture.fd, &architectureStat); err != nil {
		return nil, trustedReleaseStoreError("inspect trusted release manifest staging filesystem", err)
	}
	fd, err := architecture.deps.openAt(
		ctx,
		architecture.fd,
		".",
		trustedReleaseManifestStageFlags,
		trustedReleaseStoreManifestMode,
	)
	if err != nil {
		return nil, trustedReleaseStoreError("create anonymous trusted release manifest stage", err)
	}
	stage := &trustedReleaseManifestStage{
		deps: architecture.deps, policy: architecture.policy, manifest: manifest, raw: raw, fd: fd,
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"release rejected trusted release manifest stage",
			stage.Release(context.WithoutCancel(ctx)),
		)
	}()

	initial, err := stage.inspect(ctx, architectureStat.Dev, 0, false)
	if err != nil {
		return nil, err
	}
	stage.identity = initial
	if err := stage.deps.fchown(
		ctx,
		stage.fd,
		int(stage.policy.expectedUID),
		int(stage.policy.expectedGID),
	); err != nil {
		return nil, trustedReleaseStoreError("set trusted release manifest stage owner", err)
	}
	if err := stage.deps.fchmod(ctx, stage.fd, trustedReleaseStoreManifestMode); err != nil {
		return nil, trustedReleaseStoreError("set trusted release manifest stage mode", err)
	}
	if err := stage.write(ctx); err != nil {
		return nil, err
	}
	if err := stage.deps.fsync(ctx, stage.fd); err != nil {
		return nil, trustedReleaseStoreError("sync trusted release manifest stage", err)
	}
	final, err := stage.inspect(ctx, architectureStat.Dev, uint64(len(stage.raw)), true)
	if err != nil {
		return nil, err
	}
	if final.Dev != initial.Dev || final.Ino != initial.Ino {
		return nil, trustedReleaseStoreError("trusted release manifest stage identity changed", nil)
	}
	stage.identity = final
	if err := stage.requireZeroOffset(ctx); err != nil {
		return nil, err
	}
	return stage, nil
}

func (stage *trustedReleaseManifestStage) write(ctx context.Context) error {
	for written := 0; written < len(stage.raw); {
		if err := ctx.Err(); err != nil {
			return trustedReleaseStoreError("write trusted release manifest stage", err)
		}
		count, writeErr := stage.deps.pwrite(ctx, stage.fd, stage.raw[written:], int64(written))
		if count < 0 || count > len(stage.raw)-written {
			return trustedReleaseStoreError(
				"write trusted release manifest stage",
				fmt.Errorf("invalid positioned write count %d", count),
			)
		}
		written += count
		if writeErr != nil {
			return trustedReleaseStoreError("write trusted release manifest stage", writeErr)
		}
		if count == 0 {
			return trustedReleaseStoreError("write trusted release manifest stage", io.ErrNoProgress)
		}
	}
	return nil
}

func (stage *trustedReleaseManifestStage) inspect(
	ctx context.Context,
	device uint64,
	sizeBytes uint64,
	requireAuthority bool,
) (unix.Stat_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError("inspect trusted release manifest stage", err)
	}
	if stage == nil || stage.fd < 0 {
		return unix.Stat_t{}, trustedReleaseStoreError("trusted release manifest stage is not active", nil)
	}
	var stat unix.Stat_t
	if err := stage.deps.fstat(ctx, stage.fd, &stat); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError("inspect trusted release manifest stage", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 0 || stat.Dev != device || stat.Size < 0 ||
		uint64(stat.Size) != sizeBytes {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"trusted release manifest stage has unsafe filesystem metadata or size",
			nil,
		)
	}
	if requireAuthority && (stat.Uid != stage.policy.expectedUID || stat.Gid != stage.policy.expectedGID ||
		stat.Mode&07777 != trustedReleaseStoreManifestMode) {
		return unix.Stat_t{}, trustedReleaseStoreError(
			"trusted release manifest stage has unexpected owner or mode",
			nil,
		)
	}
	if requireAuthority && (stat.Dev != stage.identity.Dev || stat.Ino != stage.identity.Ino) {
		return unix.Stat_t{}, trustedReleaseStoreError("trusted release manifest stage identity changed", nil)
	}
	return stat, nil
}

func (stage *trustedReleaseManifestStage) requireZeroOffset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("inspect trusted release manifest stage offset", err)
	}
	offset, err := stage.deps.seek(ctx, stage.fd, 0, unix.SEEK_CUR)
	if err != nil {
		return trustedReleaseStoreError("inspect trusted release manifest stage offset", err)
	}
	if offset != 0 {
		return trustedReleaseStoreError("trusted release manifest stage offset is not zero", nil)
	}
	return nil
}

func (stage *trustedReleaseManifestStage) Release(ctx context.Context) error {
	if stage == nil || stage.fd < 0 {
		return nil
	}
	err := stage.deps.close(context.WithoutCancel(ctx), stage.fd)
	stage.fd = -1
	stage.manifest = trustedReleaseManifest{}
	stage.raw = nil
	stage.identity = unix.Stat_t{}
	if err != nil {
		return trustedReleaseStoreError("release trusted release manifest stage", err)
	}
	return nil
}
