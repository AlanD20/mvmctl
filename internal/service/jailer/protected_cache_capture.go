package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

const protectedCacheQuarantineChild = ".retirement-quarantine"
const protectedCacheCapturedStore = "store"

// These named effects are also used by failure tests; production supplies the fixed syscalls.
type protectedCacheCaptureDeps struct {
	captureStore         func(context.Context, int, int) error
	syncCacheParent      func(context.Context, int) error
	syncRetirementIntent func(context.Context, int) error
}

func realProtectedCacheCaptureDeps() protectedCacheCaptureDeps {
	return protectedCacheCaptureDeps{
		captureStore: func(ctx context.Context, cache, quarantine int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Renameat2(
				cache,
				protectedBackingDirectory,
				quarantine,
				protectedCacheCapturedStore,
				unix.RENAME_NOREPLACE,
			)
		},
		syncCacheParent:      syncProtectedCacheDirectory,
		syncRetirementIntent: syncProtectedCacheDirectory,
	}
}

func syncProtectedCacheDirectory(ctx context.Context, fd int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := unix.Openat2(fd, ".", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: protectedBackingResolve,
	})
	if err != nil {
		return err
	}
	return errors.Join(unix.Fsync(reader), unix.Close(reader))
}

func (plan *protectedCacheCleanupPlan) openNode(ctx context.Context, parent int, name string,
	expected *protectedBackingIdentity) (int, error) {
	fd, err := openProtectedDirectory(ctx, parent, name, false, true)
	if err != nil {
		return -1, err
	}
	plan.retained = append(plan.retained, fd)
	actual, err := protectedIdentity(fd)
	if err != nil || expected != nil && !sameProtectedNode(actual, *expected) {
		return -1, managedVMResourceAtomicError("protected cleanup namespace identity changed", err)
	}
	return fd, nil
}

func protectedCacheNameAbsent(ctx context.Context, parent int, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	err := unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return managedVMResourceAtomicError("protected cleanup namespace unexpectedly present", err)
}

// locate admits ONLY the recorded fixed locations. It never searches for a renamed store.
func (plan *protectedCacheCleanupPlan) locate(ctx context.Context) error {
	retirement := plan.record.Retirement
	if retirement == nil || retirement.Phase == "preparing" || retirement.Phase == "promoting" {
		store, err := plan.openNode(ctx, plan.cache.cacheFD, protectedBackingDirectory, &plan.record.Store)
		if err != nil {
			return err
		}
		plan.store = store
		if retirement == nil || retirement.Phase == "preparing" {
			return protectedCacheNameAbsent(ctx, plan.cache.cacheFD, ProtectedCacheQuarantineDirectory)
		}
		inside, insideErr := plan.openNode(ctx, store, protectedCacheQuarantineChild, retirement.Quarantine)
		sibling, siblingErr := plan.openNode(
			ctx,
			plan.cache.cacheFD,
			ProtectedCacheQuarantineDirectory,
			retirement.Quarantine,
		)
		if insideErr == nil && errors.Is(siblingErr, unix.ENOENT) {
			plan.quarantine = inside
			return plan.requireEmptyQuarantine(ctx)
		}
		if siblingErr == nil && errors.Is(insideErr, unix.ENOENT) {
			plan.quarantine = sibling
			return plan.requireEmptyQuarantine(ctx)
		}
		return managedVMResourceValidationError("protected cleanup promotion locations are ambiguous")
	}
	q, err := plan.openNode(ctx, plan.cache.cacheFD, ProtectedCacheQuarantineDirectory, retirement.Quarantine)
	if errors.Is(err, unix.ENOENT) && retirement.Phase == "removed" {
		if err := protectedCacheNameAbsent(ctx, plan.cache.cacheFD, protectedBackingDirectory); err != nil {
			return err
		}
		plan.completed = true // The already durable completed authority is not rewritten.
		return nil
	}
	if err != nil {
		return err
	}
	plan.quarantine = q
	store, storeErr := plan.openNode(ctx, q, protectedCacheCapturedStore, &plan.record.Store)
	if storeErr == nil {
		if retirement.Phase == "removed" {
			return managedVMResourceValidationError("completed protected cleanup store reappeared")
		}
		names, err := protectedCleanupNames(ctx, q)
		if err != nil {
			return err
		}
		if len(names) != 1 || names[0] != protectedCacheCapturedStore {
			return managedVMResourceValidationError("protected cleanup quarantine contains unknown entries")
		}
		plan.store = store
		return protectedCacheNameAbsent(ctx, plan.cache.cacheFD, protectedBackingDirectory)
	}
	if !errors.Is(storeErr, unix.ENOENT) {
		return storeErr
	}
	if err := plan.requireEmptyQuarantine(ctx); err != nil {
		return err
	}
	if retirement.Phase == "capturing" {
		store, err = plan.openNode(ctx, plan.cache.cacheFD, protectedBackingDirectory, &plan.record.Store)
		if err != nil {
			return err
		}
		plan.store = store
		return nil
	}
	return protectedCacheNameAbsent(ctx, plan.cache.cacheFD, protectedBackingDirectory)
}

func (plan *protectedCacheCleanupPlan) requireEmptyQuarantine(ctx context.Context) error {
	names, err := protectedCleanupNames(ctx, plan.quarantine)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		return managedVMResourceValidationError("protected cleanup quarantine is not empty")
	}
	return nil
}

func (plan *protectedCacheCleanupPlan) admitPreparingQuarantine(ctx context.Context) error {
	fd, err := plan.openNode(ctx, plan.store, protectedCacheQuarantineChild, plan.record.Retirement.Quarantine)
	if err != nil {
		return err
	}
	plan.quarantine = fd
	return plan.requireEmptyQuarantine(ctx)
}

func (plan *protectedCacheCleanupPlan) persistPhase(ctx context.Context, phase string) error {
	plan.record.Retirement.Phase = phase
	return writeProtectedRecord(ctx, plan.anchor, "store.json", plan.record)
}

func (plan *protectedCacheCleanupPlan) capture(ctx context.Context) error {
	if plan.record.Retirement == nil {
		plan.record.Retirement = &protectedCacheRetirement{Phase: "preparing", Directories: plan.directories}
		if err := plan.persistPhase(ctx, "preparing"); err != nil {
			return err
		}
	}
	if plan.record.Retirement.Phase == "preparing" {
		if plan.quarantine < 0 {
			if plan.record.Retirement.Quarantine != nil {
				return managedVMResourceValidationError("recorded preparing quarantine is missing")
			}
			// Creation and pinning are beneath the admitted root store, never the caller parent.
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := unix.Mkdirat(plan.store, protectedCacheQuarantineChild, 0700); err != nil {
				return err
			}
			if err := plan.admitPreparingQuarantine(ctx); err != nil {
				return err
			}
		}
		identity, err := protectedIdentity(plan.quarantine)
		if err != nil {
			return err
		}
		plan.record.Retirement.Quarantine = &identity
		if err := syncProtectedCacheDirectory(ctx, plan.store); err != nil {
			return err
		}
		if err := plan.persistPhase(ctx, "promoting"); err != nil {
			return err
		}
	}
	if plan.record.Retirement.Phase == "promoting" {
		if err := protectedCacheNameAbsent(ctx, plan.store, protectedCacheQuarantineChild); err != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := unix.Renameat2(plan.store, protectedCacheQuarantineChild, plan.cache.cacheFD,
				ProtectedCacheQuarantineDirectory, unix.RENAME_NOREPLACE); err != nil {
				return err
			}
		}
		if err := plan.requireSiblingQuarantine(ctx); err != nil {
			return err
		}
		if err := errors.Join(syncProtectedCacheDirectory(ctx, plan.store), plan.deps.syncCacheParent(ctx, plan.cache.cacheFD)); err != nil {
			return err
		}
		if err := plan.persistPhase(ctx, "capturing"); err != nil {
			return err
		}
	}
	if plan.record.Retirement.Phase == "capturing" {
		if err := protectedCacheNameAbsent(ctx, plan.quarantine, protectedCacheCapturedStore); err == nil {
			if err := plan.deps.captureStore(ctx, plan.cache.cacheFD, plan.quarantine); err != nil {
				return err
			}
		}
		// Atomic capture may take a caller replacement: verify BEFORE deleting any data.
		if _, err := plan.openNode(ctx, plan.quarantine, protectedCacheCapturedStore, &plan.record.Store); err != nil {
			return err
		}
		if err := plan.requireCapturedNamespace(ctx); err != nil {
			return err
		}
		if err := errors.Join(syncProtectedCacheDirectory(ctx, plan.quarantine), plan.deps.syncCacheParent(ctx, plan.cache.cacheFD)); err != nil {
			return err
		}
		return plan.persistPhase(ctx, "removing")
	}
	return nil
}

func (plan *protectedCacheCleanupPlan) requireSiblingQuarantine(ctx context.Context) error {
	_, err := plan.openNode(
		ctx,
		plan.cache.cacheFD,
		ProtectedCacheQuarantineDirectory,
		plan.record.Retirement.Quarantine,
	)
	return err
}

func (plan *protectedCacheCleanupPlan) requireCapturedNamespace(ctx context.Context) error {
	if err := plan.requireSiblingQuarantine(ctx); err != nil {
		return err
	}
	return protectedCacheNameAbsent(ctx, plan.cache.cacheFD, protectedBackingDirectory)
}
