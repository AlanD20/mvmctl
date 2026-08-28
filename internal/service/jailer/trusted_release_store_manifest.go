package jailer

import (
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

const (
	trustedReleaseStoreReadFlags    = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	trustedReleaseStoreManifestMode = uint32(0600)
)

func (directory *trustedReleaseDirectory) readManifest(
	ctx context.Context,
) (manifest trustedReleaseManifest, returnErr error) {
	if directory == nil || directory.slotFD < 0 || len(directory.retained) == 0 {
		return trustedReleaseManifest{}, trustedReleaseStoreError(
			"installed trusted release directory is not active",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseManifest{}, trustedReleaseStoreError("read trusted release manifest", err)
	}

	manifestFD, err := directory.deps.openAt(
		ctx,
		directory.slotFD,
		trustedReleaseManifestLeaf,
		trustedReleaseStoreReadFlags,
		0,
	)
	if err != nil {
		return trustedReleaseManifest{}, classifyTrustedReleaseManifestOpenError(err)
	}
	defer func() {
		if err := directory.deps.close(context.WithoutCancel(ctx), manifestFD); err != nil {
			returnErr = appendTrustedReleaseStoreError(returnErr, "close trusted release manifest", err)
		}
	}()

	before, err := inspectTrustedReleaseManifestDescriptor(ctx, directory, manifestFD)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	raw, err := readTrustedReleaseManifestBytes(ctx, directory.deps, manifestFD)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	after, err := inspectTrustedReleaseManifestDescriptor(ctx, directory, manifestFD)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	if !sameTrustedReleaseManifestStat(before, after) || int64(len(raw)) != after.Size {
		return trustedReleaseManifest{}, trustedReleaseStoreUntrusted(
			"trusted release manifest changed while being read",
			nil,
		)
	}

	manifest, err = decodeTrustedReleaseManifest(raw)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	if manifest.slot != directory.slot {
		return trustedReleaseManifest{}, trustedReleaseStoreUntrusted(
			"trusted release manifest slot does not match its directory",
			nil,
		)
	}
	return manifest, nil
}

func inspectTrustedReleaseManifestDescriptor(
	ctx context.Context,
	directory *trustedReleaseDirectory,
	fd int,
) (unix.Stat_t, error) {
	if err := ctx.Err(); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError("inspect trusted release manifest", err)
	}
	var stat unix.Stat_t
	if err := directory.deps.fstat(ctx, fd, &stat); err != nil {
		return unix.Stat_t{}, trustedReleaseStoreError("inspect trusted release manifest", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release manifest is not a regular file",
			nil,
		)
	}
	if stat.Uid != directory.policy.expectedUID || stat.Gid != directory.policy.expectedGID ||
		stat.Mode&07777 != trustedReleaseStoreManifestMode {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release manifest has unexpected owner or mode",
			nil,
		)
	}
	if stat.Nlink != 1 {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release manifest must have exactly one link",
			nil,
		)
	}
	if stat.Size <= 0 || stat.Size > maxTrustedReleaseManifestBytes {
		return unix.Stat_t{}, trustedReleaseStoreUntrusted(
			"trusted release manifest size is outside the admitted range",
			nil,
		)
	}
	return stat, nil
}

func readTrustedReleaseManifestBytes(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	fd int,
) ([]byte, error) {
	buffer := make([]byte, maxTrustedReleaseManifestBytes+1)
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, trustedReleaseStoreError("read trusted release manifest", err)
		}
		count, readErr := deps.read(ctx, fd, buffer[total:])
		if count < 0 || count > len(buffer)-total {
			return nil, trustedReleaseStoreError(
				"read trusted release manifest",
				fmt.Errorf("invalid read count %d", count),
			)
		}
		total += count
		if total > maxTrustedReleaseManifestBytes {
			return nil, trustedReleaseStoreUntrusted("trusted release manifest exceeds size limit", nil)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return buffer[:total], nil
			}
			return nil, trustedReleaseStoreError("read trusted release manifest", readErr)
		}
		if count == 0 {
			return buffer[:total], nil
		}
	}
}

func sameTrustedReleaseManifestStat(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev &&
		first.Ino == second.Ino &&
		first.Mode == second.Mode &&
		first.Nlink == second.Nlink &&
		first.Uid == second.Uid &&
		first.Gid == second.Gid &&
		first.Size == second.Size &&
		first.Mtim == second.Mtim &&
		first.Ctim == second.Ctim
}

func classifyTrustedReleaseManifestOpenError(cause error) error {
	if errors.Is(cause, unix.ENOENT) || errors.Is(cause, unix.ELOOP) || errors.Is(cause, unix.ENOTDIR) {
		return trustedReleaseStoreUntrusted("trusted release manifest is missing or unsafe", cause)
	}
	return trustedReleaseStoreError("open trusted release manifest", cause)
}
