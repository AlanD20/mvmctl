package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: one value must own every authority object selected after the release lock, including the identity derived
// from the verified manifest. It must not expose a state where hashes and retained executable descriptors can diverge.
func TestReleaseAuthorityPreparesInstalledReleaseUnderSlotLease(t *testing.T) {
	t.Parallel()

	fixture := newPreparedReleaseFixture(t)
	prepared, err := fixture.authority().prepareInstalled(t.Context(), fixture.release.store.slot)
	require.NoError(t, err)
	wantIdentity, err := fixture.release.manifest.releaseIdentity()
	require.NoError(t, err)
	if diff := cmp.Diff(
		fixture.release.manifest,
		prepared.manifest,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("prepared manifest mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantIdentity, prepared.identity, cmp.AllowUnexported(releaseIdentity{})); diff != "" {
		t.Errorf("prepared release identity mismatch (-want +got):\n%s", diff)
	}
	assert.NotNil(t, prepared.slotLease)
	assert.NotNil(t, prepared.store)
	assert.NotNil(t, prepared.directory)
	assert.NotNil(t, prepared.executables)
	assert.GreaterOrEqual(t, prepared.executables.firecrackerFD, 0)
	assert.GreaterOrEqual(t, prepared.executables.jailerFD, 0)

	require.NoError(t, prepared.Release(t.Context()))
	require.NoError(t, prepared.Release(t.Context()))
	assert.Nil(t, prepared.slotLease)
	assert.Nil(t, prepared.store)
	assert.Nil(t, prepared.directory)
	assert.Nil(t, prepared.executables)
	if diff := cmp.Diff(
		trustedReleaseManifest{},
		prepared.manifest,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("released manifest was not cleared (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(releaseIdentity{}, prepared.identity, cmp.AllowUnexported(releaseIdentity{})); diff != "" {
		t.Errorf("released identity was not cleared (-want +got):\n%s", diff)
	}
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.release.store.slot)
}

func TestReleaseAuthorityLocksSlotBeforeTrustedStoreResolution(t *testing.T) {
	t.Parallel()

	fixture := newPreparedReleaseFixture(t)
	lockHeld := false
	realFlock := fixture.instances.deps.flock
	fixture.instances.deps.flock = func(ctx context.Context, fd int, how int) error {
		err := realFlock(ctx, fd, how)
		if err == nil && how == unix.LOCK_EX|unix.LOCK_NB {
			lockHeld = true
		}
		if err == nil && how == unix.LOCK_UN {
			lockHeld = false
		}
		return err
	}
	storeOpenedWhileLocked := false
	realOpen := fixture.release.store.deps.open
	fixture.release.store.deps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
		storeOpenedWhileLocked = lockHeld
		return realOpen(ctx, name, flags, mode)
	}

	prepared, err := fixture.authority().prepareInstalled(t.Context(), fixture.release.store.slot)
	require.NoError(t, err)
	assert.True(t, storeOpenedWhileLocked)
	assert.True(t, lockHeld)
	require.NoError(t, prepared.Release(t.Context()))
	assert.False(t, lockHeld)
}

func TestReleaseAuthorityPreparationFailureReleasesSlotLease(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate   func(*testing.T, *preparedReleaseFixture)
		wantCode errs.Code
	}{
		"missing manifest": {
			mutate: func(t *testing.T, fixture *preparedReleaseFixture) {
				require.NoError(t, os.Remove(filepath.Join(
					fixture.release.store.slotPath,
					trustedReleaseManifestLeaf,
				)))
			},
			wantCode: errs.CodeBinaryUntrusted,
		},
		"corrupt manifest": {
			mutate: func(t *testing.T, fixture *preparedReleaseFixture) {
				require.NoError(t, os.WriteFile(
					filepath.Join(fixture.release.store.slotPath, trustedReleaseManifestLeaf),
					[]byte(`{"invalid":true}`),
					0600,
				))
			},
			wantCode: errs.CodeBinaryUntrusted,
		},
		"unsafe executable": {
			mutate: func(t *testing.T, fixture *preparedReleaseFixture) {
				require.NoError(t, os.Chmod(fixture.release.firecrackerPath, 0700))
			},
			wantCode: errs.CodeBinaryUntrusted,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newPreparedReleaseFixture(t)
			tc.mutate(t, fixture)
			prepared, err := fixture.authority().prepareInstalled(t.Context(), fixture.release.store.slot)
			assert.Nil(t, prepared)
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, errs.AsDomainError(err).Code)
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.release.store.slot)
		})
	}
}

// Rationale: ownership unwinds opposite acquisition. The release lock is last so no competing install, removal, or
// launch can observe the slot while any pinned release object from this preparation remains active.
func TestPreparedReleaseReleasesResourcesInReverseWithoutCancellation(t *testing.T) {
	t.Parallel()

	fixture := newPreparedReleaseFixture(t)
	events := make([]string, 0, 10)
	fdNames := make(map[int]string)
	cleanupWasCanceled := false
	realOpen := fixture.release.store.deps.open
	fixture.release.store.deps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, name, flags, mode)
		if err == nil {
			fdNames[fd] = "root"
		}
		return fd, err
	}
	realOpenAt := fixture.release.store.deps.openAt
	fixture.release.store.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil {
			fdNames[fd] = name
		}
		return fd, err
	}
	realStoreClose := fixture.release.store.deps.close
	fixture.release.store.deps.close = func(ctx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || ctx.Err() != nil
		events = append(events, "store:"+fdNames[fd])
		return realStoreClose(ctx, fd)
	}
	realFlock := fixture.instances.deps.flock
	fixture.instances.deps.flock = func(ctx context.Context, fd int, how int) error {
		if how == unix.LOCK_UN {
			cleanupWasCanceled = cleanupWasCanceled || ctx.Err() != nil
			events = append(events, "instance:release-lock")
		}
		return realFlock(ctx, fd, how)
	}

	prepared, err := fixture.authority().prepareInstalled(t.Context(), fixture.release.store.slot)
	require.NoError(t, err)
	events = events[:0]
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, prepared.Release(ctx))

	want := []string{
		"store:jailer",
		"store:firecracker",
		"store:1.16.1",
		"store:x86_64",
		"store:binaries",
		"store:mvmctl",
		"store:lib",
		"store:var",
		"store:root",
		"instance:release-lock",
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("prepared release cleanup order mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, cleanupWasCanceled)
}

func TestReleaseAuthorityPreparationPreservesPrimaryErrorWhenCleanupFails(t *testing.T) {
	t.Parallel()

	fixture := newPreparedReleaseFixture(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.release.store.slotPath, trustedReleaseManifestLeaf),
		[]byte(`{"invalid":true}`),
		0600,
	))
	realClose := fixture.release.store.deps.close
	fixture.release.store.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	prepared, err := fixture.authority().prepareInstalled(t.Context(), fixture.release.store.slot)
	assert.Nil(t, prepared)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.ErrorIs(t, err, unix.EIO)
	assert.Contains(t, domainErr.Message, "release rejected prepared trusted release")
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.release.store.slot)
}

func TestReleaseAuthorityPreparationHonorsCancellationAndCleansUp(t *testing.T) {
	t.Parallel()

	fixture := newPreparedReleaseFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	realPread := fixture.release.store.deps.pread
	fixture.release.store.deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		count, err := realPread(ctx, fd, value, offset)
		cancel()
		return count, err
	}
	cleanupWasCanceled := false
	realClose := fixture.release.store.deps.close
	fixture.release.store.deps.close = func(ctx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || ctx.Err() != nil
		return realClose(ctx, fd)
	}

	prepared, err := fixture.authority().prepareInstalled(ctx, fixture.release.store.slot)
	assert.Nil(t, prepared)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.False(t, cleanupWasCanceled)
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.release.store.slot)
}

func TestPreparedReleaseContinuesCleanupAfterExecutableCloseFailure(t *testing.T) {
	t.Parallel()

	fixture := newPreparedReleaseFixture(t)
	prepared, err := fixture.authority().prepareInstalled(t.Context(), fixture.release.store.slot)
	require.NoError(t, err)
	realClose := prepared.executables.deps.close
	prepared.executables.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	err = prepared.Release(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EIO)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Nil(t, prepared.slotLease)
	assert.Nil(t, prepared.store)
	assert.Nil(t, prepared.directory)
	assert.Nil(t, prepared.executables)
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.release.store.slot)
}

type preparedReleaseFixture struct {
	instances *instanceAuthority
	release   *trustedReleaseExecutableFixture
}

// newPreparedReleaseFixture builds independent instance-lock and trusted-store roots and writes one complete manifest.
func newPreparedReleaseFixture(t *testing.T) *preparedReleaseFixture {
	t.Helper()

	instances, _ := newTestInstanceAuthority(t)
	release := newTrustedReleaseExecutableFixture(t)
	writeTrustedReleaseManifestFile(t, release.store.slotPath, release.manifest)
	return &preparedReleaseFixture{instances: instances, release: release}
}

// authority snapshots the fixture's injected dependencies after a test has installed any failure hooks.
func (fixture *preparedReleaseFixture) authority() *releaseAuthority {
	return newReleaseAuthorityWithPolicy(
		fixture.instances,
		fixture.release.store.deps,
		fixture.release.store.policy,
	)
}

// assertPreparedReleaseSlotReacquirable proves failure and release paths do not strand the ordering lock.
func assertPreparedReleaseSlotReacquirable(
	t *testing.T,
	authority *instanceAuthority,
	slot releaseSlot,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := authority.lockReleaseSlot(ctx, slot)
	require.NoError(t, err)
	require.NoError(t, lease.Release(context.Background()))
}
