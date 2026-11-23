package jailer

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// restoreProtectedLockContention accepts only an actual flock refusal with successful
// descriptor cleanup. A joined close, ownership or I/O failure must never become a retry.
func restoreProtectedLockContention(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		return len(causes) == 1 && restoreProtectedLockContention(causes[0])
	}
	domain, ok := err.(*errs.DomainError)
	return ok && domain.Code == errs.CodeProcessError && domain.Class == errs.ClassRetryable &&
		(domain.Err == unix.EAGAIN || domain.Err == unix.EWOULDBLOCK)
}

func waitRestoreAuthority(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return instanceLockContextError("snapshot restore authority", ctx.Err())
	case <-timer.C:
		return nil
	}
}

type restoreBackingSelection struct {
	kind, id, artifact string
	volumeIndex        int
	sourceFD           int
	bytes              uint64
}

func acquireRestoredBackingSet(
	ctx context.Context, cache *managedCacheLease, items []restoreBackingSelection,
) (admissions []*protectedBackingAdmission, returnErr error) {
	anchor, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := closeProtectedFDs(retained); closeErr != nil {
			returnErr = errors.Join(
				returnErr, closeErr, releaseRestoredBackingAdmissions(context.WithoutCancel(ctx), admissions),
			)
			admissions = nil
		}
	}()
	return acquireRestoredBackingSetInAnchor(ctx, cache, items, retained[len(retained)-2], anchor)
}

// CALLER MUST HOLD: admitted owner/cache anchor descriptors and the target VM lock.
// Each attempt freshly admits the fixed store and canonical logical/object set. Never
// wait with a partial set, and never start a copy or grant before complete acquisition.
func acquireRestoredBackingSetInAnchor(
	ctx context.Context, cache *managedCacheLease, items []restoreBackingSelection, owner, anchor int,
) ([]*protectedBackingAdmission, error) {
	for {
		var admissions []*protectedBackingAdmission
		var contended bool
		var attemptErr error
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				attemptErr = err
				break
			}
			ownerCopy, err := unix.FcntlInt(uintptr(owner), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				attemptErr = err
				break
			}
			anchorCopy, err := unix.FcntlInt(uintptr(anchor), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				attemptErr = errors.Join(err, unix.Close(ownerCopy))
				break
			}
			admission, busy, err := tryAdmitProtectedBackingInAnchor(
				ctx, cache, item.kind, item.id, true, anchorCopy, []int{ownerCopy, anchorCopy},
			)
			if err != nil {
				attemptErr, contended = err, busy
				break
			}
			admissions = append(admissions, admission)
		}
		if attemptErr == nil {
			return admissions, nil
		}
		closeErr := releaseRestoredBackingAdmissions(context.WithoutCancel(ctx), admissions)
		if !contended || closeErr != nil {
			return nil, errors.Join(attemptErr, closeErr)
		}
		if err := waitRestoreAuthority(ctx); err != nil {
			return nil, err
		}
	}
}

func releaseRestoredBackingAdmissions(ctx context.Context, admissions []*protectedBackingAdmission) error {
	var result error
	for index := len(admissions) - 1; index >= 0; index-- {
		result = errors.Join(result, admissions[index].Release(ctx))
	}
	return result
}

// Initial capture admission waits before publishing a restore reference. Once admitted,
// metadata/artifact reads and reference creation execute once under the retained entry lock.
func openRestoreSnapshotEntry(
	ctx context.Context, cache *managedCacheLease, snapshotID string,
) (*protectedSnapshotStoreLease, *snapshotStoreEntry, error) {
	for {
		store, busy, err := tryOpenProtectedStoreBranch(ctx, cache, "snapshots", false)
		if err == nil {
			var entry *snapshotStoreEntry
			entry, busy, err = tryLockSnapshotStoreEntry(ctx, store, snapshotID, false)
			if err == nil {
				return store, entry, nil
			}
			if closeErr := store.Release(context.WithoutCancel(ctx)); closeErr != nil {
				return nil, nil, errors.Join(err, closeErr)
			}
		}
		if !busy {
			return nil, nil, err
		}
		if err := waitRestoreAuthority(ctx); err != nil {
			return nil, nil, err
		}
	}
}
