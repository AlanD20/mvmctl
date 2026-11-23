package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

const (
	assetMirrorEnvironment        = "MVM_ASSET_MIRROR"
	trustedReleaseArchiveMaxBytes = 128 << 20
)

// trustedReleaseClient is the private API-side effect boundary for the root-owned release store.
type trustedReleaseClient interface {
	Install(
		context.Context,
		jailer.TrustedReleaseInstallRequest,
		io.ReadCloser,
		uint64,
	) (jailer.TrustedReleaseInstallResult, error)
	Remove(
		context.Context,
		jailer.TrustedReleaseRemoveRequest,
	) (jailer.TrustedReleaseRemoveResult, error)
}

// openTrustedReleaseMirrorArchive opens only the exact release archive. Absence selects the root-owned origin fetch.
func openTrustedReleaseMirrorArchive(version, architecture string) (io.ReadCloser, uint64, error) {
	mirror, configured := os.LookupEnv(assetMirrorEnvironment)
	if !configured || mirror == "" {
		return nil, 0, nil
	}
	archivePath := filepath.Join(mirror, fmt.Sprintf("firecracker-v%s-%s.tgz", version, architecture))
	archive, err := os.Open(archivePath)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, errs.WrapMsg(errs.CodeBinaryPullFailed, "failed to open exact mirrored release archive", err)
	}
	info, err := archive.Stat()
	if err != nil {
		return nil, 0, errs.WrapMsg(
			errs.CodeBinaryPullFailed,
			"failed to inspect exact mirrored release archive",
			closeWithCause(archive, err),
		)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > trustedReleaseArchiveMaxBytes {
		return nil, 0, errs.WrapMsg(
			errs.CodeBinaryPullFailed,
			"exact mirrored release archive is not a bounded regular file",
			closeWithCause(archive, errors.New("mirrored release archive violates file policy")),
		)
	}
	return archive, uint64(info.Size()), nil
}

func closeWithCause(closer io.Closer, cause error) error {
	return errors.Join(cause, closer.Close())
}

func binaryPairFromTrustedRelease(result jailer.TrustedReleaseInstallResult) *model.BinaryPair {
	now := time.Now().UTC().Format(time.RFC3339)
	ciVersion := binary.CIVersion(result.Slot.Version)
	member := func(typ, name, sha256 string) *model.BinaryItem {
		return &model.BinaryItem{
			ID:           crypto.BinaryReleaseID(typ, result.Slot.Architecture, result.Slot.Version, sha256),
			Type:         typ,
			Architecture: result.Slot.Architecture,
			Version:      result.Slot.Version,
			FullVersion:  "v" + result.Slot.Version,
			CIVersion:    &ciVersion,
			Path: filepath.Join(
				infra.TrustedBinaryRoot,
				result.Slot.Architecture,
				result.Slot.Version,
				name,
			),
			IsPresent: true,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	return &model.BinaryPair{
		Firecracker: member("firecracker", "firecracker", result.Firecracker.SHA256),
		Jailer:      member("jailer", "jailer", result.Jailer.SHA256),
	}
}

func releaseProjectionFailure(operation string, outcome any, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeDatabaseError,
		operation+" succeeded in the root release store but its local projection requires repair",
		cause,
		errs.WithDetails(map[string]any{
			"root_outcome":          outcome,
			"state_repair_required": true,
		}),
	)
}
