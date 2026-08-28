package jailer

import (
	"context"

	"golang.org/x/sys/unix"
)

const (
	trustedReleaseArchiveStageFlags = unix.O_TMPFILE | unix.O_EXCL | unix.O_RDWR | unix.O_CLOEXEC
	trustedReleaseArchiveStageMode  = uint32(0600)
)

type trustedReleaseArchiveStage struct {
	deps trustedReleaseStoreDeps
	fd   int
}

// CRITICAL: The stage is anonymous, unlinkable, and created on the pinned trusted-store filesystem. No path is
// generated or accepted, and no fallback can move the bytes outside this descriptor authority.
func (lease *trustedReleaseStoreWriteLease) createArchiveStage(
	ctx context.Context,
) (_ *trustedReleaseArchiveStage, returnErr error) {
	if lease == nil || lease.store == nil || lease.store.binariesFD < 0 || len(lease.store.retained) == 0 {
		return nil, trustedReleaseStoreError("trusted release store write lease is not active", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("create trusted release archive stage", err)
	}

	fd, err := lease.store.deps.openAt(
		ctx,
		lease.store.binariesFD,
		".",
		trustedReleaseArchiveStageFlags,
		trustedReleaseArchiveStageMode,
	)
	if err != nil {
		return nil, trustedReleaseStoreError("create anonymous trusted release archive stage", err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release archive stage",
			lease.store.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()

	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("inspect trusted release archive stage", err)
	}
	if err := verifyTrustedReleaseArchiveStage(ctx, lease.store, fd, false); err != nil {
		return nil, err
	}
	if err := lease.store.deps.fchown(
		ctx,
		fd,
		int(lease.store.policy.expectedUID),
		int(lease.store.policy.expectedGID),
	); err != nil {
		return nil, trustedReleaseStoreError("set trusted release archive stage owner", err)
	}
	if err := lease.store.deps.fchmod(ctx, fd, trustedReleaseArchiveStageMode); err != nil {
		return nil, trustedReleaseStoreError("set trusted release archive stage mode", err)
	}
	if err := verifyTrustedReleaseArchiveStage(ctx, lease.store, fd, true); err != nil {
		return nil, err
	}

	return &trustedReleaseArchiveStage{deps: lease.store.deps, fd: fd}, nil
}

func verifyTrustedReleaseArchiveStage(
	ctx context.Context,
	store *trustedReleaseStore,
	fd int,
	requireIdentity bool,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("inspect trusted release archive stage", err)
	}
	var stat unix.Stat_t
	if err := store.deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect trusted release archive stage", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 0 || stat.Size != 0 {
		return trustedReleaseStoreError("trusted release archive stage has unsafe filesystem metadata", nil)
	}
	if requireIdentity && (stat.Uid != store.policy.expectedUID || stat.Gid != store.policy.expectedGID ||
		stat.Mode&07777 != trustedReleaseArchiveStageMode) {
		return trustedReleaseStoreError("trusted release archive stage has unexpected owner or mode", nil)
	}
	return nil
}

func (stage *trustedReleaseArchiveStage) Release(ctx context.Context) error {
	if stage == nil || stage.fd < 0 {
		return nil
	}
	err := stage.deps.close(context.WithoutCancel(ctx), stage.fd)
	stage.fd = -1
	if err != nil {
		return trustedReleaseStoreError("release trusted release archive stage", err)
	}
	return nil
}
