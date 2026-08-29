package jailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestReleaseAuthorityRemoveInstalledRetriesOnlyNameCollisions(t *testing.T) {
	t.Parallel()

	t.Run("eighth attempt commits", func(t *testing.T) {
		t.Parallel()

		fixture := newTrustedReleaseRemovalFixture(t)
		realRename := fixture.authority.storeDeps.renameInstalledNoReplace
		calls := 0
		fixture.authority.storeDeps.renameInstalledNoReplace = func(
			ctx context.Context,
			parentFD int,
			source string,
			target string,
		) error {
			calls++
			if calls < trustedReleaseRemovalRenameAttempts {
				return unix.EEXIST
			}
			return realRename(ctx, parentFD, source, target)
		}

		removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
		require.NoError(t, err)
		assert.True(t, removed)
		assert.Equal(t, trustedReleaseRemovalRenameAttempts, calls)
		assert.NoDirExists(t, fixture.store.slotPath)
	})

	t.Run("eight collisions exhaust the transaction", func(t *testing.T) {
		t.Parallel()

		fixture := newTrustedReleaseRemovalFixture(t)
		calls := 0
		fixture.authority.storeDeps.renameInstalledNoReplace = func(
			context.Context,
			int,
			string,
			string,
		) error {
			calls++
			return unix.EEXIST
		}

		removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
		require.Error(t, err)
		assert.False(t, removed)
		assert.Equal(t, trustedReleaseRemovalRenameAttempts, calls)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeBinaryRemoveFailed, domainErr.Code)
		assert.Nil(t, domainErr.Details)
		assert.DirExists(t, fixture.store.slotPath)
	})
}

func TestReleaseAuthorityRemoveInstalledDoesNotRetryOtherRenameFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]error{
		"unsupported syscall":   unix.ENOSYS,
		"unsupported operation": unix.EOPNOTSUPP,
		"invalid kernel flags":  unix.EINVAL,
		"cross filesystem":      unix.EXDEV,
		"other failure":         unix.EIO,
	}
	for name, injected := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseRemovalFixture(t)
			calls := 0
			fixture.authority.storeDeps.renameInstalledNoReplace = func(
				context.Context,
				int,
				string,
				string,
			) error {
				calls++
				return injected
			}

			removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
			require.Error(t, err)
			assert.False(t, removed)
			assert.Equal(t, 1, calls)
			assert.ErrorIs(t, err, injected)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeBinaryRemoveFailed, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.Nil(t, domainErr.Details)
			assert.DirExists(t, fixture.store.slotPath)
		})
	}
}

func TestReleaseAuthorityRemoveInstalledRejectsGeneratedNameOutsideReservedGrammar(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	renameCalls := 0
	fixture.authority.storeDeps.candidateName = func(context.Context, releaseSlot) (string, error) {
		return ".mvm-release-wrong-slot-00000000000000000000000000000000.tmp", nil
	}
	fixture.authority.storeDeps.renameInstalledNoReplace = func(context.Context, int, string, string) error {
		renameCalls++
		return nil
	}

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.Error(t, err)
	assert.False(t, removed)
	assert.Equal(t, 0, renameCalls)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.DirExists(t, fixture.store.slotPath)
}

func TestReleaseAuthorityRemoveInstalledDoesNotRetryNameGenerationOrCancellation(t *testing.T) {
	t.Parallel()

	t.Run("name generation error", func(t *testing.T) {
		t.Parallel()

		fixture := newTrustedReleaseRemovalFixture(t)
		nameCalls := 0
		renameCalls := 0
		fixture.authority.storeDeps.candidateName = func(context.Context, releaseSlot) (string, error) {
			nameCalls++
			return "", unix.EIO
		}
		fixture.authority.storeDeps.renameInstalledNoReplace = func(context.Context, int, string, string) error {
			renameCalls++
			return nil
		}

		removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
		require.Error(t, err)
		assert.False(t, removed)
		assert.Equal(t, 1, nameCalls)
		assert.Equal(t, 0, renameCalls)
		assert.ErrorIs(t, err, unix.EIO)
		assert.DirExists(t, fixture.store.slotPath)
	})

	t.Run("cancellation after collision", func(t *testing.T) {
		t.Parallel()

		fixture := newTrustedReleaseRemovalFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		renameCalls := 0
		fixture.authority.storeDeps.renameInstalledNoReplace = func(context.Context, int, string, string) error {
			renameCalls++
			cancel()
			return unix.EEXIST
		}

		removed, err := fixture.authority.removeInstalled(ctx, fixture.store.slot)
		require.Error(t, err)
		assert.False(t, removed)
		assert.Equal(t, 1, renameCalls)
		assert.ErrorIs(t, err, context.Canceled)
		assert.DirExists(t, fixture.store.slotPath)
	})
}

func TestReleaseAuthorityRemoveInstalledDoesNotHideSafeAbsenceCleanupFailure(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	require.NoError(t, os.RemoveAll(fixture.store.mvmctlPath))
	realClose := fixture.authority.storeDeps.close
	failed := false
	fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if !failed {
			failed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.Error(t, err)
	assert.False(t, removed)
	assert.ErrorIs(t, err, unix.EIO)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryNotFound, domainErr.Code)
}

func TestTrustedReleaseRemovalTargetRefusesPrecommitRetirementAndReleaseIsCloseOnly(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	slotLease, err := fixture.authority.instances.lockReleaseSlot(t.Context(), fixture.store.slot)
	require.NoError(t, err)
	store, err := openTrustedReleaseStoreForRead(
		t.Context(),
		fixture.authority.storeDeps,
		fixture.authority.storePolicy,
	)
	require.NoError(t, err)
	architecture, err := store.openExistingArchitecture(t.Context(), slotLease)
	require.NoError(t, err)
	target, err := architecture.openRemovalTarget(t.Context())
	require.NoError(t, err)
	require.NotNil(t, target)
	assert.Equal(t, trustedReleaseRemovalTargetAdmitted, target.state)
	before := installedReleaseInodesForTest(t, fixture.store.slotPath)
	realUnlinkAt := architecture.deps.unlinkAt
	unlinks := 0
	architecture.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		unlinks++
		return realUnlinkAt(ctx, parentFD, name, flags)
	}

	retired, err := target.retire(t.Context(), architecture)
	require.Error(t, err)
	assert.False(t, retired)
	assert.Equal(t, 0, unlinks)
	assert.Equal(t, trustedReleaseRemovalTargetAdmitted, target.state)
	if diff := cmp.Diff(before, installedReleaseInodesForTest(t, fixture.store.slotPath)); diff != "" {
		t.Errorf("precommit retirement mutated canonical release (-before +after):\n%s", diff)
	}

	require.NoError(t, target.Release(t.Context()))
	assert.Equal(t, trustedReleaseRemovalTargetReleased, target.state)
	assert.Equal(t, 0, unlinks)
	if diff := cmp.Diff(before, installedReleaseInodesForTest(t, fixture.store.slotPath)); diff != "" {
		t.Errorf("close-only target release mutated canonical release (-before +after):\n%s", diff)
	}
	require.NoError(t, target.Release(t.Context()))
	require.NoError(t, architecture.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
	require.NoError(t, slotLease.Release(t.Context()))
}

func TestReleaseAuthorityRemoveInstalledRejectsCanonicalBindingReplacement(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	attacker := filepath.Join(fixture.store.architecturePath, "attacker")
	require.NoError(t, os.Mkdir(attacker, 0700))
	realOpenAt := fixture.authority.storeDeps.openAt
	canonicalOpens := 0
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		if name == fixture.store.slot.version {
			canonicalOpens++
			if canonicalOpens == 2 {
				return unix.Open(attacker, flags, mode)
			}
		}
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}
	renameCalls := 0
	fixture.authority.storeDeps.renameInstalledNoReplace = func(context.Context, int, string, string) error {
		renameCalls++
		return nil
	}

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.Error(t, err)
	assert.False(t, removed)
	assert.Equal(t, 0, renameCalls)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.DirExists(t, fixture.store.slotPath)
	assert.DirExists(t, attacker)
}

func TestReleaseAuthorityRemoveInstalledRejectsCrossFilesystemCanonical(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	realOpenAt := fixture.authority.storeDeps.openAt
	realFstat := fixture.authority.storeDeps.fstat
	canonicalFD := -1
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == fixture.store.slot.version && canonicalFD < 0 {
			canonicalFD = fd
		}
		return fd, err
	}
	fixture.authority.storeDeps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		if fd == canonicalFD {
			stat.Dev++
		}
		return nil
	}

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.Error(t, err)
	assert.False(t, removed)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.DirExists(t, fixture.store.slotPath)
}

func TestReleaseAuthorityRemoveInstalledFinishesCleanupAfterCommitCancellation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	realRename := fixture.authority.storeDeps.renameInstalledNoReplace
	fixture.authority.storeDeps.renameInstalledNoReplace = func(
		renameCtx context.Context,
		parentFD int,
		source string,
		target string,
	) error {
		if err := realRename(renameCtx, parentFD, source, target); err != nil {
			return err
		}
		cancel()
		return nil
	}

	removed, err := fixture.authority.removeInstalled(ctx, fixture.store.slot)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	entries, err := os.ReadDir(fixture.store.architecturePath)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{}, trustedReleaseEntryNamesForRemoveTest(entries)); diff != "" {
		t.Errorf("release architecture entries mismatch (-want +got):\n%s", diff)
	}
}

func TestReleaseAuthorityRemoveInstalledHoldsSlotLeaseThroughFinalCleanup(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	realOpenAt := fixture.authority.storeDeps.openAt
	realFsync := fixture.authority.storeDeps.fsync
	realWaitLock := fixture.authority.instances.deps.waitLock
	var architectureFD atomic.Int64
	architectureFD.Store(-1)
	var architectureFsyncs atomic.Int32
	finalCleanupEntered := make(chan struct{})
	releaseFinalCleanup := make(chan struct{})
	contentionObserved := make(chan struct{})
	var finalOnce sync.Once
	var releaseOnce sync.Once
	var contentionOnce sync.Once
	releaseCleanup := func() { releaseOnce.Do(func() { close(releaseFinalCleanup) }) }
	defer releaseCleanup()
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == fixture.store.slot.architecture {
			architectureFD.CompareAndSwap(-1, int64(fd))
		}
		return fd, err
	}
	fixture.authority.storeDeps.fsync = func(ctx context.Context, fd int) error {
		if int64(fd) == architectureFD.Load() {
			if architectureFsyncs.Add(1) == 3 {
				finalOnce.Do(func() { close(finalCleanupEntered) })
				select {
				case <-releaseFinalCleanup:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return realFsync(ctx, fd)
	}
	fixture.authority.instances.deps.waitLock = func(ctx context.Context) error {
		contentionOnce.Do(func() { close(contentionObserved) })
		return realWaitLock(ctx)
	}

	type result struct {
		removed bool
		err     error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		removed, err := fixture.authority.removeInstalled(ctx, fixture.store.slot)
		firstResult <- result{removed: removed, err: err}
	}()
	select {
	case <-finalCleanupEntered:
	case <-ctx.Done():
		require.Failf(t, "first removal did not reach final cleanup", "error: %v", ctx.Err())
	}
	go func() {
		removed, err := fixture.authority.removeInstalled(ctx, fixture.store.slot)
		secondResult <- result{removed: removed, err: err}
	}()
	select {
	case <-contentionObserved:
	case <-ctx.Done():
		require.Failf(t, "second removal did not contend on the slot lease", "error: %v", ctx.Err())
	}
	releaseCleanup()

	first := <-firstResult
	second := <-secondResult
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.True(t, first.removed)
	assert.False(t, second.removed)
}

func TestReleaseAuthorityRemoveInstalledRetainsCommittedResultAcrossOuterCleanupFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject   func(*testing.T, *trustedReleaseRemovalFixture)
		wantCode errs.Code
	}{
		"target directory close": {
			inject:   injectTrustedReleaseRemovalTargetClose,
			wantCode: errs.CodeBinaryTrustedInstallFailed,
		},
		"architecture close": {
			inject:   injectTrustedReleaseRemovalArchitectureClose,
			wantCode: errs.CodeBinaryTrustedInstallFailed,
		},
		"store close": {
			inject:   injectTrustedReleaseRemovalStoreClose,
			wantCode: errs.CodeBinaryTrustedInstallFailed,
		},
		"slot lease unlock": {
			inject:   injectTrustedReleaseRemovalSlotUnlock,
			wantCode: errs.CodeProcessError,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseRemovalFixture(t)
			test.inject(t, fixture)

			removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
			require.Error(t, err)
			assert.True(t, removed)
			assert.ErrorIs(t, err, unix.EIO)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, test.wantCode, domainErr.Code)
			wantDetails := map[string]any{"release_removed": true}
			if diff := cmp.Diff(wantDetails, domainErr.Details); diff != "" {
				t.Errorf("postcommit cleanup details mismatch (-want +got):\n%s", diff)
			}
			assert.NoDirExists(t, fixture.store.slotPath)
			assert.Empty(t, trustedReleaseReservedPathsForRemoveTest(t, fixture.store.architecturePath))
		})
	}
}

func TestReleaseAuthorityRemoveInstalledRetirementEventOrderUnderSlotLease(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseRemovalFixture(t)
	storeDeps := &fixture.authority.storeDeps
	realStoreOpenAt := storeDeps.openAt
	realClose := storeDeps.close
	realFsync := storeDeps.fsync
	realUnlinkAt := storeDeps.unlinkAt
	targetFD := -1
	architectureFD := -1
	executableFDs := make(map[int]struct{}, 2)
	executableCloses := 0
	architectureFsyncs := 0
	reservedBindings := 0
	events := make([]string, 0, 8)
	storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realStoreOpenAt(ctx, parentFD, name, flags, mode)
		if err != nil {
			return fd, err
		}
		switch name {
		case fixture.store.slot.architecture:
			if architectureFD < 0 {
				architectureFD = fd
			}
		case fixture.store.slot.version:
			if targetFD < 0 {
				targetFD = fd
			}
		case trustedReleaseFirecrackerLeaf, trustedReleaseJailerLeaf:
			executableFDs[fd] = struct{}{}
		default:
			if strings.HasPrefix(name, trustedReleaseCandidateNamePrefix) {
				reservedBindings++
				events = append(events, fmt.Sprintf("reserved-binding-%d", reservedBindings))
			}
		}
		return fd, nil
	}
	storeDeps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if _, executable := executableFDs[fd]; executable && executableCloses < len(executableFDs) {
			executableCloses++
			if executableCloses == len(executableFDs) {
				events = append(events, "admission-close")
			}
		}
		return err
	}
	storeDeps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		err := realUnlinkAt(ctx, parentFD, name, flags)
		if err != nil {
			return err
		}
		if flags == unix.AT_REMOVEDIR {
			events = append(events, "rmdir")
		} else {
			events = append(events, "unlink-"+name)
		}
		return nil
	}
	storeDeps.fsync = func(ctx context.Context, fd int) error {
		err := realFsync(ctx, fd)
		if err != nil {
			return err
		}
		switch fd {
		case targetFD:
			events = append(events, "retired-directory-fsync")
		case architectureFD:
			architectureFsyncs++
			if architectureFsyncs == 2 {
				events = append(events, "first-architecture-fsync")
			} else if architectureFsyncs == 3 {
				events = append(events, "final-architecture-fsync")
			}
		}
		return nil
	}

	instanceDeps := &fixture.authority.instances.deps
	realInstanceOpenAt := instanceDeps.openAt
	realFlock := instanceDeps.flock
	releaseLockFD := -1
	unlockObserved := false
	unlockBeforeFinal := false
	wantReleaseLock := releaseLockName(fixture.store.slot)
	instanceDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realInstanceOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == wantReleaseLock {
			releaseLockFD = fd
		}
		return fd, err
	}
	instanceDeps.flock = func(ctx context.Context, fd int, how int) error {
		if fd == releaseLockFD && how == unix.LOCK_UN {
			unlockObserved = true
			unlockBeforeFinal = len(events) == 0 || events[len(events)-1] != "final-architecture-fsync"
		}
		return realFlock(ctx, fd, how)
	}

	removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
	require.NoError(t, err)
	assert.True(t, removed)
	wantEvents := []string{
		"first-architecture-fsync",
		"admission-close",
		"reserved-binding-1",
		"unlink-" + trustedReleaseManifestLeaf,
		"unlink-" + trustedReleaseJailerLeaf,
		"unlink-" + trustedReleaseFirecrackerLeaf,
		"retired-directory-fsync",
		"reserved-binding-2",
		"rmdir",
		"final-architecture-fsync",
	}
	if diff := cmp.Diff(wantEvents, events); diff != "" {
		t.Errorf("trusted release removal event order mismatch (-want +got):\n%s", diff)
	}
	assert.True(t, unlockObserved)
	assert.False(t, unlockBeforeFinal)
}

func TestReleaseAuthorityRemoveInstalledPostCommitFaultsAreRecoverable(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject func(*testing.T, *trustedReleaseRemovalFixture)
		want   trustedReleaseRemovalFaultState
	}{
		"first architecture fsync": {
			inject: func(t *testing.T, fixture *trustedReleaseRemovalFixture) {
				injectTrustedReleaseRemovalArchitectureFsync(t, fixture, 2)
			},
			want: trustedReleaseRemovalFaultState{durabilityUncertain: true, retiredReleaseRetained: true},
		},
		"admission close": {
			inject: injectTrustedReleaseRemovalAdmissionClose,
			want:   trustedReleaseRemovalFaultState{},
		},
		"manifest unlink": {
			inject: func(t *testing.T, fixture *trustedReleaseRemovalFixture) {
				injectTrustedReleaseRemovalLeafUnlink(t, fixture, trustedReleaseManifestLeaf)
			},
			want: trustedReleaseRemovalFaultState{retiredReleaseRetained: true},
		},
		"jailer unlink": {
			inject: func(t *testing.T, fixture *trustedReleaseRemovalFixture) {
				injectTrustedReleaseRemovalLeafUnlink(t, fixture, trustedReleaseJailerLeaf)
			},
			want: trustedReleaseRemovalFaultState{retiredReleaseRetained: true},
		},
		"firecracker unlink": {
			inject: func(t *testing.T, fixture *trustedReleaseRemovalFixture) {
				injectTrustedReleaseRemovalLeafUnlink(t, fixture, trustedReleaseFirecrackerLeaf)
			},
			want: trustedReleaseRemovalFaultState{retiredReleaseRetained: true},
		},
		"retired directory fsync": {
			inject: injectTrustedReleaseRemovalTargetFsync,
			want:   trustedReleaseRemovalFaultState{retiredReleaseRetained: true},
		},
		"retired directory removal": {
			inject: injectTrustedReleaseRemovalRmdir,
			want:   trustedReleaseRemovalFaultState{retiredReleaseRetained: true},
		},
		"final architecture fsync": {
			inject: func(t *testing.T, fixture *trustedReleaseRemovalFixture) {
				injectTrustedReleaseRemovalArchitectureFsync(t, fixture, 3)
			},
			want: trustedReleaseRemovalFaultState{durabilityUncertain: true},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseRemovalFixture(t)
			originalDeps := fixture.authority.storeDeps
			test.inject(t, fixture)

			removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
			require.Error(t, err)
			assert.True(t, removed)
			assert.ErrorIs(t, err, unix.EIO)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["release_removed"])
			assert.Equal(t, test.want.durabilityUncertain, detailBoolForRemoveTest(domainErr, "durability_uncertain"))
			assert.Equal(
				t,
				test.want.retiredReleaseRetained,
				detailBoolForRemoveTest(domainErr, "retired_release_retained"),
			)
			assert.NoDirExists(t, fixture.store.slotPath)
			reserved := trustedReleaseReservedPathsForRemoveTest(t, fixture.store.architecturePath)
			if test.want.retiredReleaseRetained {
				require.Len(t, reserved, 1)
			} else {
				assert.Empty(t, reserved)
			}
			if name == "admission close" {
				assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, domainErr.Code)
			}

			fixture.authority.storeDeps = originalDeps
			removed, err = fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
			require.NoError(t, err)
			assert.False(t, removed)
			entries, err := os.ReadDir(fixture.store.architecturePath)
			require.NoError(t, err)
			if diff := cmp.Diff([]string{}, trustedReleaseEntryNamesForRemoveTest(entries)); diff != "" {
				t.Errorf("recovered architecture entries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReleaseAuthorityRemoveInstalledReservedBindingChecksPreventWrongDirectoryDeletion(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		bindingCall int
		wantLeaves  bool
	}{
		"before first leaf unlink": {bindingCall: 1, wantLeaves: true},
		"before rmdir":             {bindingCall: 2, wantLeaves: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseRemovalFixture(t)
			attacker := filepath.Join(fixture.store.architecturePath, "attacker")
			require.NoError(t, os.Mkdir(attacker, 0700))
			realOpenAt := fixture.authority.storeDeps.openAt
			reservedBindingCalls := 0
			fixture.authority.storeDeps.openAt = func(
				ctx context.Context,
				parentFD int,
				name string,
				flags int,
				mode uint32,
			) (int, error) {
				if strings.HasPrefix(name, trustedReleaseCandidateNamePrefix) {
					reservedBindingCalls++
					if reservedBindingCalls == test.bindingCall {
						return unix.Open(attacker, flags, mode)
					}
				}
				return realOpenAt(ctx, parentFD, name, flags, mode)
			}

			removed, err := fixture.authority.removeInstalled(t.Context(), fixture.store.slot)
			require.Error(t, err)
			assert.True(t, removed)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
			assert.Equal(t, true, domainErr.Details["release_removed"])
			assert.Equal(t, true, domainErr.Details["retired_release_retained"])
			assert.DirExists(t, attacker)
			reserved := trustedReleaseReservedPathsForRemoveTest(t, fixture.store.architecturePath)
			require.Len(t, reserved, 1)
			for _, leaf := range []string{
				trustedReleaseManifestLeaf,
				trustedReleaseJailerLeaf,
				trustedReleaseFirecrackerLeaf,
			} {
				if test.wantLeaves {
					assert.FileExists(t, filepath.Join(reserved[0], leaf))
				} else {
					assert.NoFileExists(t, filepath.Join(reserved[0], leaf))
				}
			}
		})
	}
}

func TestTrustedReleaseArchitectureRecoveryBindingChecksPreventWrongDirectoryDeletion(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		bindingOpen int
		wantLeaves  bool
	}{
		"before first leaf unlink": {bindingOpen: 2, wantLeaves: true},
		"before rmdir":             {bindingOpen: 3, wantLeaves: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, architecture := newTrustedReleaseArchitectureFixture(t)
			prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
			require.NoError(t, err)
			candidateName := prefix + "33333333333333333333333333333333.tmp"
			candidate := writeTrustedReleaseRecoveryCandidate(t, fixture.architecturePath, candidateName, []string{
				trustedReleaseManifestLeaf,
				trustedReleaseJailerLeaf,
			})
			attacker := filepath.Join(fixture.architecturePath, "attacker")
			require.NoError(t, os.Mkdir(attacker, 0700))
			realOpenAt := architecture.deps.openAt
			candidateOpens := 0
			architecture.deps.openAt = func(
				ctx context.Context,
				parentFD int,
				name string,
				flags int,
				mode uint32,
			) (int, error) {
				if name == candidateName {
					candidateOpens++
					if candidateOpens == test.bindingOpen {
						return unix.Open(attacker, flags, mode)
					}
				}
				return realOpenAt(ctx, parentFD, name, flags, mode)
			}

			err = architecture.recoverCandidates(t.Context())
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
			assert.DirExists(t, attacker)
			assert.DirExists(t, candidate)
			for _, leaf := range []string{trustedReleaseManifestLeaf, trustedReleaseJailerLeaf} {
				if test.wantLeaves {
					assert.FileExists(t, filepath.Join(candidate, leaf))
				} else {
					assert.NoFileExists(t, filepath.Join(candidate, leaf))
				}
			}
		})
	}
}

func TestAppendTrustedReleaseRemovalErrorPreservesFirstDomainError(t *testing.T) {
	t.Parallel()

	primary := errs.New(
		errs.CodeValidationFailed,
		"distinctive removal failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("trusted-release"),
		errs.WithDetails(map[string]any{"existing_detail": true}),
	)

	combined := appendTrustedReleaseRemovalError(primary, "later removal cleanup", unix.EIO)
	domainErr := errs.AsDomainError(combined)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, "trusted-release", domainErr.Entity)
	assert.Equal(t, true, domainErr.Details["existing_detail"])
	assert.ErrorIs(t, combined, unix.EIO)
}

type trustedReleaseRemovalFaultState struct {
	durabilityUncertain    bool
	retiredReleaseRetained bool
}

func injectTrustedReleaseRemovalArchitectureFsync(
	t *testing.T,
	fixture *trustedReleaseRemovalFixture,
	failCall int,
) {
	t.Helper()

	realOpenAt := fixture.authority.storeDeps.openAt
	realFsync := fixture.authority.storeDeps.fsync
	architectureFD := -1
	architectureFsyncs := 0
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == fixture.store.slot.architecture && architectureFD < 0 {
			architectureFD = fd
		}
		return fd, err
	}
	fixture.authority.storeDeps.fsync = func(ctx context.Context, fd int) error {
		if fd == architectureFD {
			architectureFsyncs++
			if architectureFsyncs == failCall {
				return unix.EIO
			}
		}
		return realFsync(ctx, fd)
	}
}

func injectTrustedReleaseRemovalAdmissionClose(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	realOpenAt := fixture.authority.storeDeps.openAt
	realClose := fixture.authority.storeDeps.close
	executableFD := -1
	failed := false
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == trustedReleaseJailerLeaf {
			executableFD = fd
		}
		return fd, err
	}
	fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if fd == executableFD && !failed {
			failed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}
}

func injectTrustedReleaseRemovalLeafUnlink(
	t *testing.T,
	fixture *trustedReleaseRemovalFixture,
	failLeaf string,
) {
	t.Helper()

	realUnlinkAt := fixture.authority.storeDeps.unlinkAt
	failed := false
	fixture.authority.storeDeps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if flags == 0 && name == failLeaf && !failed {
			failed = true
			return unix.EIO
		}
		return realUnlinkAt(ctx, parentFD, name, flags)
	}
}

func injectTrustedReleaseRemovalTargetFsync(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	realOpenAt := fixture.authority.storeDeps.openAt
	realFsync := fixture.authority.storeDeps.fsync
	targetFD := -1
	failed := false
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == fixture.store.slot.version && targetFD < 0 {
			targetFD = fd
		}
		return fd, err
	}
	fixture.authority.storeDeps.fsync = func(ctx context.Context, fd int) error {
		if fd == targetFD && !failed {
			failed = true
			return unix.EIO
		}
		return realFsync(ctx, fd)
	}
}

func injectTrustedReleaseRemovalRmdir(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	realUnlinkAt := fixture.authority.storeDeps.unlinkAt
	failed := false
	fixture.authority.storeDeps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if flags == unix.AT_REMOVEDIR && strings.HasPrefix(name, trustedReleaseCandidateNamePrefix) && !failed {
			failed = true
			return unix.EIO
		}
		return realUnlinkAt(ctx, parentFD, name, flags)
	}
}

func injectTrustedReleaseRemovalTargetClose(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	deps := &fixture.authority.storeDeps
	realOpenAt := deps.openAt
	realClose := deps.close
	targetFD := -1
	failed := false
	deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == fixture.store.slot.version && targetFD < 0 {
			targetFD = fd
		}
		return fd, err
	}
	deps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if fd == targetFD && !failed {
			failed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}
}

func injectTrustedReleaseRemovalArchitectureClose(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	deps := &fixture.authority.storeDeps
	realOpenAt := deps.openAt
	realClose := deps.close
	architectureFD := -1
	failed := false
	deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == fixture.store.slot.architecture && architectureFD < 0 {
			architectureFD = fd
		}
		return fd, err
	}
	deps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if fd == architectureFD && !failed {
			failed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}
}

func injectTrustedReleaseRemovalStoreClose(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	deps := &fixture.authority.storeDeps
	realOpenAt := deps.openAt
	realClose := deps.close
	binariesFD := -1
	failed := false
	deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == "binaries" && binariesFD < 0 {
			binariesFD = fd
		}
		return fd, err
	}
	deps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if fd == binariesFD && !failed {
			failed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}
}

func injectTrustedReleaseRemovalSlotUnlock(t *testing.T, fixture *trustedReleaseRemovalFixture) {
	t.Helper()

	deps := &fixture.authority.instances.deps
	realOpenAt := deps.openAt
	realFlock := deps.flock
	releaseLockFD := -1
	failed := false
	wantName := releaseLockName(fixture.store.slot)
	deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && name == wantName {
			releaseLockFD = fd
		}
		return fd, err
	}
	deps.flock = func(ctx context.Context, fd int, how int) error {
		err := realFlock(ctx, fd, how)
		if fd == releaseLockFD && how == unix.LOCK_UN && !failed {
			failed = true
			return errors.Join(err, unix.EIO)
		}
		return err
	}
}

func detailBoolForRemoveTest(domainErr *errs.DomainError, key string) bool {
	value, _ := domainErr.Details[key].(bool)
	return value
}

func trustedReleaseReservedPathsForRemoveTest(t *testing.T, architecturePath string) []string {
	t.Helper()

	entries, err := os.ReadDir(architecturePath)
	require.NoError(t, err)
	paths := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), trustedReleaseCandidateNamePrefix) {
			paths = append(paths, filepath.Join(architecturePath, entry.Name()))
		}
	}
	return paths
}
