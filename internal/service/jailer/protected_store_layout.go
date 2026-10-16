package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

const protectedStoreLayoutVersion = 2
const protectedSnapshotLockDirectory = "snapshot-locks"

// The fresh layout has one permanent lock namespace outside caller-owned cache data.
// Existing records never recreate missing authority, including during explicit retirement.
func validateProtectedStoreLayout(ctx context.Context, anchor int, record protectedBackingAnchor) error {
	fd, err := pinProtectedSnapshotLockAuthority(ctx, anchor, record)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func pinProtectedSnapshotLockAuthority(ctx context.Context, anchor int, record protectedBackingAnchor) (int, error) {
	if record.Version != protectedStoreLayoutVersion || record.Store.Inode == 0 || record.SnapshotLocks.Inode == 0 {
		return -1, managedVMResourceValidationError("protected store layout authority is unsupported or missing")
	}
	fd, err := openProtectedDirectory(ctx, anchor, protectedSnapshotLockDirectory, false, true)
	if err != nil {
		return -1, err
	}
	identity, err := protectedIdentity(fd)
	if err != nil || !sameProtectedNode(identity, record.SnapshotLocks) {
		return -1, errors.Join(
			managedVMResourceAtomicError("protected snapshot lock authority changed", err),
			unix.Close(fd),
		)
	}
	return fd, nil
}

func openProtectedSnapshotLockAuthority(ctx context.Context, cache *managedCacheLease, anchor int) (int, error) {
	var record protectedBackingAnchor
	if err := readProtectedRecord(ctx, anchor, "store.json", &record); err != nil {
		return -1, err
	}
	if record.Retirement != nil || record.Owner != cache.ownerUID || record.OwnerGID != cache.ownerGID ||
		record.Cache != protectedCacheIdentity(cache.identity) || record.Locator != protectedLocator(cache) {
		return -1, managedVMResourceValidationError("snapshot lock authority differs from admitted cache")
	}
	return pinProtectedSnapshotLockAuthority(ctx, anchor, record)
}
