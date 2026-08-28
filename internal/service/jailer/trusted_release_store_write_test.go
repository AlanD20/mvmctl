package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: first installation may create only the two fixed managed store components. The write capability must pin
// the exact resulting directories without creating a release architecture or version as an incidental side effect.
func TestOpenTrustedReleaseStoreForWriteCreatesFixedManagedBase(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)

	assertExactMode(t, fixture.mvmctlPath, 0700)
	assertExactMode(t, fixture.binariesPath, 0700)
	assert.NoDirExists(t, fixture.architecturePath)
	var pinned unix.Stat_t
	require.NoError(t, unix.Fstat(lease.store.binariesFD, &pinned))
	assert.Equal(t, fileInode(t, fixture.binariesPath), pinned.Ino)
	require.NoError(t, lease.Release(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestOpenTrustedReleaseStoreForWriteDoesNotCreateAncestors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"var": "var",
		"lib": "lib",
	}
	for name, level := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			require.NoError(t, os.RemoveAll(fixture.pathForLevel(level)))
			lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenTrustedReleaseStoreForWriteRejectsUnsafeManagedBase(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseStoreFixture){
		"mvmctl symlink": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
			target := filepath.Join(fixture.root, "attacker")
			require.NoError(t, os.Mkdir(target, 0700))
			require.NoError(t, os.Symlink(target, fixture.mvmctlPath))
		},
		"binaries symlink": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.RemoveAll(fixture.binariesPath))
			target := filepath.Join(fixture.root, "attacker")
			require.NoError(t, os.Mkdir(target, 0700))
			require.NoError(t, os.Symlink(target, fixture.binariesPath))
		},
		"mvmctl wrong mode": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.Chmod(fixture.mvmctlPath, 0755))
		},
		"binaries wrong mode": func(t *testing.T, fixture *trustedReleaseStoreFixture) {
			require.NoError(t, os.Chmod(fixture.binariesPath, 0750))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			mutate(t, fixture)
			lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenTrustedReleaseStoreForWriteTreatsEEXISTAsConcurrentCreation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	realMkdirAt := fixture.deps.mkdirAt
	fixture.deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		require.NoError(t, realMkdirAt(ctx, parentFD, name, mode))
		return unix.EEXIST
	}
	fixture.deps.fchown = func(context.Context, int, int, int) error {
		return errors.New("must not chown a concurrently created store directory")
	}
	fixture.deps.fchmod = func(context.Context, int, uint32) error {
		return errors.New("must not chmod a concurrently created store directory")
	}

	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	assertExactMode(t, fixture.mvmctlPath, 0700)
	assertExactMode(t, fixture.binariesPath, 0700)
	require.NoError(t, lease.Release(t.Context()))
}

func TestOpenTrustedReleaseStoreForWriteSetsCreatedMetadata(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	realFchown := fixture.deps.fchown
	realFchmod := fixture.deps.fchmod
	var chownCalls atomic.Int32
	var chmodCalls atomic.Int32
	fixture.deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
		chownCalls.Add(1)
		assert.Equal(t, int(fixture.policy.expectedUID), uid)
		assert.Equal(t, int(fixture.policy.expectedGID), gid)
		return realFchown(ctx, fd, uid, gid)
	}
	fixture.deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
		chmodCalls.Add(1)
		assert.Equal(t, trustedReleaseStoreDirectoryMode, mode)
		return realFchmod(ctx, fd, mode)
	}

	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	assert.Equal(t, int32(2), chownCalls.Load())
	assert.Equal(t, int32(2), chmodCalls.Load())
	require.NoError(t, lease.Release(t.Context()))
}

func TestOpenTrustedReleaseStoreForWriteRejectsCreationFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseStoreDeps){
		"mkdir": func(deps *trustedReleaseStoreDeps) {
			deps.mkdirAt = func(context.Context, int, string, uint32) error { return unix.EIO }
		},
		"open created directory": func(deps *trustedReleaseStoreDeps) {
			realOpenAt := deps.openAt
			var mvmctlCalls int
			deps.openAt = func(ctx context.Context, parentFD int, name string, flags int, mode uint32) (int, error) {
				if name == "mvmctl" {
					mvmctlCalls++
					if mvmctlCalls == 2 {
						return -1, unix.EIO
					}
				}
				return realOpenAt(ctx, parentFD, name, flags, mode)
			}
		},
		"fchown": func(deps *trustedReleaseStoreDeps) {
			deps.fchown = func(context.Context, int, int, int) error { return unix.EIO }
		},
		"fchmod": func(deps *trustedReleaseStoreDeps) {
			deps.fchmod = func(context.Context, int, uint32) error { return unix.EIO }
		},
		"fsync": func(deps *trustedReleaseStoreDeps) {
			deps.fsync = func(context.Context, int) error { return unix.EIO }
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
			inject(&fixture.deps)
			lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.ErrorIs(t, err, unix.EIO)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenTrustedReleaseStoreForWriteDoesNotRepairConcurrentUnsafeDirectory(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	realMkdirAt := fixture.deps.mkdirAt
	fixture.deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		require.NoError(t, realMkdirAt(ctx, parentFD, name, mode))
		if name == "mvmctl" {
			require.NoError(t, os.Chmod(fixture.mvmctlPath, 0755))
		}
		return unix.EEXIST
	}

	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	assertExactMode(t, fixture.mvmctlPath, 0755)
}

func TestOpenTrustedReleaseStoreForWriteSyncsNewChildAndParent(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	fdNames := make(map[int]string)
	realOpen := fixture.deps.open
	fixture.deps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, name, flags, mode)
		if err == nil {
			fdNames[fd] = "root"
		}
		return fd, err
	}
	realOpenAt := fixture.deps.openAt
	fixture.deps.openAt = func(
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
	realFsync := fixture.deps.fsync
	synced := make([]string, 0, 4)
	fixture.deps.fsync = func(ctx context.Context, fd int) error {
		synced = append(synced, fdNames[fd])
		return realFsync(ctx, fd)
	}

	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"mvmctl", "lib", "binaries", "mvmctl"}, synced); diff != "" {
		t.Errorf("trusted store creation sync order mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.Release(t.Context()))
}

func TestOpenTrustedReleaseStoreForWriteRejectsReplacementAfterMkdir(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	realMkdirAt := fixture.deps.mkdirAt
	fixture.deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		err := realMkdirAt(ctx, parentFD, name, mode)
		if err == nil && name == "mvmctl" {
			require.NoError(t, os.Rename(fixture.mvmctlPath, fixture.mvmctlPath+".created"))
			require.NoError(t, os.Symlink(fixture.mvmctlPath+".created", fixture.mvmctlPath))
		}
		return err
	}

	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
}

func TestOpenTrustedReleaseStoreForWriteHonorsCancellationAfterCreation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.RemoveAll(fixture.mvmctlPath))
	ctx, cancel := context.WithCancel(t.Context())
	realMkdirAt := fixture.deps.mkdirAt
	fixture.deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		err := realMkdirAt(ctx, parentFD, name, mode)
		cancel()
		return err
	}
	cleanupWasCanceled := false
	realClose := fixture.deps.close
	fixture.deps.close = func(closeCtx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || closeCtx.Err() != nil
		return realClose(closeCtx, fd)
	}

	lease, err := openTrustedReleaseStoreForWrite(ctx, fixture.deps, fixture.policy)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.False(t, cleanupWasCanceled)
}

func TestOpenTrustedReleaseStoreForWritePreservesPrimaryErrorWhenCleanupFails(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.Chmod(fixture.mvmctlPath, 0755))
	realClose := fixture.deps.close
	fixture.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	assert.Nil(t, lease)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.ErrorIs(t, err, unix.EIO)
}
