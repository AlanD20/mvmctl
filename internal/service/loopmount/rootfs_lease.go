package loopmount

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"mvmctl/pkg/errs"
)

const maxRootfsCleanupDetailBytes = 512

type rootfsLease struct {
	// mu guards one checked descriptor close and the cached idempotent result.
	mu sync.Mutex

	deps          rootfsAuthorityDeps
	caller        Caller
	cacheIdentity rootfsCacheIdentity
	metadata      rootfsStableMetadata
	// admittedBytes is the stable pre-effect size used to converge an interrupted grow replay.
	admittedBytes uint64
	access        rootfsAccess
	parentFD      int
	stagingFD     int
	rootfsFD      int
	retained      []int
	released      bool
	releaseErr    error
}

// Release closes the exact admitted descriptor chain in reverse order. Caller cancellation cannot retain authority.
func (lease *rootfsLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return lease.releaseErr
	}

	closeErr := closeRootfsDescriptors(context.WithoutCancel(ctx), lease.deps, lease.retained)
	lease.caller = Caller{}
	lease.cacheIdentity = rootfsCacheIdentity{}
	lease.metadata = rootfsStableMetadata{}
	lease.admittedBytes = 0
	lease.access = 0
	lease.parentFD = -1
	lease.stagingFD = -1
	lease.rootfsFD = -1
	lease.retained = nil
	lease.released = true
	if closeErr != nil {
		lease.releaseErr = rootfsCleanupError("release managed rootfs descriptors", closeErr)
	}
	return lease.releaseErr
}

func closeRootfsDescriptors(ctx context.Context, deps rootfsAuthorityDeps, descriptors []int) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	for index := len(descriptors) - 1; index >= 0; index-- {
		if err := deps.close(cleanupCtx, descriptors[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("descriptor %d: %w", descriptors[index], err))
		}
	}
	return result
}

func appendRootfsCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		return rootfsCleanupError(message, cause)
	}
	detail := boundedRootfsCleanupDetail(message, cause)
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + detail
		domainErr.Err = errors.Join(domainErr.Err, cause)
		if domainErr.Details == nil {
			domainErr.Details = make(map[string]any)
		}
		domainErr.Details["loopmount_rootfs_descriptor_cleanup_failed"] = true
		domainErr.Details["loopmount_rootfs_descriptor_cleanup_detail"] = detail
		return domainErr
	}
	return newRootfsCleanupError(detail, errors.Join(primary, cause))
}

func rootfsCleanupError(message string, cause error) *errs.DomainError {
	detail := boundedRootfsCleanupDetail(message, cause)
	return newRootfsCleanupError(detail, cause)
}

func newRootfsCleanupError(detail string, cause error) *errs.DomainError {
	domainErr := rootfsAtomicError(detail, cause)
	domainErr.Details = map[string]any{
		"loopmount_rootfs_descriptor_cleanup_failed": true,
		"loopmount_rootfs_descriptor_cleanup_detail": detail,
	}
	return domainErr
}

func boundedRootfsCleanupDetail(message string, cause error) string {
	detail := message
	if cause != nil {
		detail += ": " + cause.Error()
	}
	if len(detail) <= maxRootfsCleanupDetailBytes {
		return detail
	}
	return detail[:maxRootfsCleanupDetailBytes-3] + "..."
}
