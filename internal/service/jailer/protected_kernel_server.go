package jailer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// ProtectedKernelServer owns root kernel retirement and explicit reimport authority.
type ProtectedKernelServer struct{}

// NewProtectedKernelServer constructs the closed receiver without effects.
func NewProtectedKernelServer() *ProtectedKernelServer { return &ProtectedKernelServer{} }

// Import admits initial content or identical bytes under a completed root retirement record.
func (server *ProtectedKernelServer) Import(
	ctx context.Context, uid, gid uint32, request ProtectedKernelImportRequest,
) (_ ProtectedKernelImportResult, returnErr error) {
	defer func() { returnErr = protectedKernelOperationError("import", request.KernelID, returnErr) }()
	if err := request.Validate(); err != nil {
		return ProtectedKernelImportResult{}, err
	}
	cache, err := pinSnapshotCache(ctx, instanceCaller{uid: uid, gid: gid}, request.CacheRoot)
	if err != nil {
		return ProtectedKernelImportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	lease, err := protectBackingWithKernelImport(
		ctx,
		cache,
		protectedBackingKernel,
		request.KernelID,
		0,
		true,
		-1,
		true,
	)
	if err != nil {
		return ProtectedKernelImportResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	if err := lease.active(ctx); err != nil {
		return ProtectedKernelImportResult{}, err
	}
	return ProtectedKernelImportResult{KernelID: request.KernelID, Imported: true}, nil
}

// Remove pins and hashes actual protected bytes before committing durable unlink intent.
func (server *ProtectedKernelServer) Remove(
	ctx context.Context, uid, gid uint32, request ProtectedKernelRemoveRequest,
) (_ ProtectedKernelRemoveResult, returnErr error) {
	defer func() { returnErr = protectedKernelOperationError("remove", request.KernelID, returnErr) }()
	if err := request.Validate(); err != nil {
		return ProtectedKernelRemoveResult{}, err
	}
	cache, err := pinSnapshotCache(ctx, instanceCaller{uid: uid, gid: gid}, request.CacheRoot)
	if err != nil {
		return ProtectedKernelRemoveResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	lease, _, err := lookupProtectedBacking(ctx, cache, protectedBackingKernel, request.KernelID)
	if err != nil {
		return ProtectedKernelRemoveResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	if len(lease.record.References) != 0 || len(lease.record.NextReferences) != 0 {
		return ProtectedKernelRemoveResult{}, managedVMResourceValidationError("kernel retains root VM references")
	}
	if err := requireKernelSnapshotReferencesAbsent(ctx, cache, request.KernelID); err != nil {
		return ProtectedKernelRemoveResult{}, err
	}
	if lease.record.Phase == "absent" {
		// There are no historical protected bytes to hash. Only explicit import may leave this state.
		lease.record.Phase = "kernel-unprotected-removed"
		if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
			return ProtectedKernelRemoveResult{}, err
		}
	} else if lease.record.Phase != "kernel-unprotected-removed" {
		if err := lease.Remove(ctx); err != nil {
			return ProtectedKernelRemoveResult{}, err
		}
	}
	if lease.record.Phase == "kernel-unprotected-removed" {
		if err := markProtectedLogicalInitialized(ctx, lease.logicalFD); err != nil {
			return ProtectedKernelRemoveResult{}, err
		}
	}
	return ProtectedKernelRemoveResult{KernelID: request.KernelID, Removed: true}, nil
}

// CALLER MUST HOLD: the kernel logical-object lock. Capture retains that lock until publication,
// so no new snapshot can gain this kernel while the authoritative store is being inspected.
func requireKernelSnapshotReferencesAbsent(
	ctx context.Context,
	cache *managedCacheLease,
	kernelID string,
) (returnErr error) {
	store, err := openProtectedSnapshotStore(ctx, cache, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.Release(context.WithoutCancel(ctx))) }()
	return requireKernelSnapshotStoreReferencesAbsent(ctx, store, cache.ownerUID, kernelID)
}

func requireKernelSnapshotStoreReferencesAbsent(
	ctx context.Context, store *protectedSnapshotStoreLease, ownerUID uint32, kernelID string,
) (returnErr error) {
	reader, err := unix.Openat(store.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(reader), "kernel-snapshot-inventory")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	for {
		names, readErr := file.Readdirnames(128)
		for _, name := range names {
			if !isCanonicalManagedResourceID(name, 64) {
				return managedVMResourceValidationError("snapshot inventory contains an unknown entry")
			}
			entry, err := lockSnapshotStoreEntry(ctx, store, name, false)
			if errors.Is(err, unix.ENOENT) {
				continue // A concurrent completed snapshot removal cannot add a reference.
			}
			if err != nil {
				return err
			}
			record, recordErr := entry.readRecord(ctx, ownerUID)
			closeErr := entry.Release(context.WithoutCancel(ctx))
			if err := errors.Join(recordErr, closeErr); err != nil {
				return err // An interrupted capture is unknown authority, never an empty reference set.
			}
			if record.Source.KernelID == kernelID {
				return managedVMResourceValidationError("kernel retains a root snapshot reference")
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func protectedKernelOperationError(operation, id string, cause error) error {
	if cause == nil {
		return nil
	}
	slog.Error("authoritative kernel operation failed", "operation", operation, "kernel_id", id, "error", cause)
	if domainErr := errs.AsDomainError(cause); domainErr != nil && domainErr.Code == errs.CodeKernelImportFailed {
		return cause
	}
	return errs.WrapMsg(
		errs.CodeKernelBuildFailed,
		operation+" authoritative kernel backing",
		cause,
		errs.WithEntity(id),
	)
}
