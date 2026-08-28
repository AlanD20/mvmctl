package jailer

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: archive bytes must be retained on the already trusted filesystem without ever publishing a pathname. The
// O_EXCL form of O_TMPFILE also prevents a later link operation from turning this transport stage into a store leaf.
func TestTrustedReleaseStoreWriteLeaseCreatesAnonymousArchiveStage(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	realOpenAt := writer.store.deps.openAt
	var observedName string
	var observedFlags int
	var observedMode uint32
	writer.store.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		if name == "." {
			observedName = name
			observedFlags = flags
			observedMode = mode
		}
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}
	before := trustedReleaseDirectoryEntryNames(t, fixture.binariesPath)

	stage, err := writer.createArchiveStage(t.Context())
	require.NoError(t, err)
	assert.Equal(t, ".", observedName)
	assert.Equal(t, trustedReleaseArchiveStageFlags, observedFlags)
	assert.Equal(t, trustedReleaseArchiveStageMode, observedMode)
	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(stage.fd, &stat))
	assert.Equal(t, uint32(unix.S_IFREG)|trustedReleaseArchiveStageMode, stat.Mode)
	assert.Equal(t, fixture.policy.expectedUID, stat.Uid)
	assert.Equal(t, fixture.policy.expectedGID, stat.Gid)
	assert.Equal(t, uint64(0), stat.Nlink)
	assert.Equal(t, int64(0), stat.Size)
	flags, err := unix.FcntlInt(uintptr(stage.fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, flags&unix.FD_CLOEXEC)
	offset, err := unix.Seek(stage.fd, 0, unix.SEEK_CUR)
	require.NoError(t, err)
	assert.Equal(t, int64(0), offset)
	after := trustedReleaseDirectoryEntryNames(t, fixture.binariesPath)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("anonymous archive stage changed store entries (-before +after):\n%s", diff)
	}

	require.NoError(t, writer.Release(t.Context()))
	require.NoError(t, unix.Fstat(stage.fd, &stat))
	require.NoError(t, stage.Release(t.Context()))
	require.NoError(t, stage.Release(t.Context()))
}

func TestTrustedReleaseArchiveStageRejectsUnsafeMetadata(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Stat_t){
		"not regular": func(stat *unix.Stat_t) { stat.Mode = uint32(unix.S_IFDIR) | trustedReleaseArchiveStageMode },
		"wrong owner": func(stat *unix.Stat_t) { stat.Uid++ },
		"wrong group": func(stat *unix.Stat_t) { stat.Gid++ },
		"wrong mode":  func(stat *unix.Stat_t) { stat.Mode = uint32(unix.S_IFREG) | 0644 },
		"linked":      func(stat *unix.Stat_t) { stat.Nlink = 1 },
		"not empty":   func(stat *unix.Stat_t) { stat.Size = 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
			realFstat := writer.store.deps.fstat
			writer.store.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				mutate(stat)
				return nil
			}

			stage, err := writer.createArchiveStage(t.Context())
			assert.Nil(t, stage)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestTrustedReleaseArchiveStageRejectsSetupFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseStoreDeps){
		"anonymous open": func(deps *trustedReleaseStoreDeps) {
			realOpenAt := deps.openAt
			deps.openAt = func(ctx context.Context, parentFD int, name string, flags int, mode uint32) (int, error) {
				if name == "." {
					return -1, unix.EOPNOTSUPP
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
		"fstat": func(deps *trustedReleaseStoreDeps) {
			deps.fstat = func(context.Context, int, *unix.Stat_t) error { return unix.EIO }
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
			inject(&writer.store.deps)

			stage, err := writer.createArchiveStage(t.Context())
			assert.Nil(t, stage)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			wantErr := error(unix.EIO)
			if name == "anonymous open" {
				wantErr = unix.EOPNOTSUPP
			}
			assert.ErrorIs(t, err, wantErr)
		})
	}
}

func TestTrustedReleaseArchiveStageHonorsCancellationAfterOpen(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	ctx, cancel := context.WithCancel(t.Context())
	realOpenAt := writer.store.deps.openAt
	writer.store.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, openErr := realOpenAt(ctx, parentFD, name, flags, mode)
		if openErr == nil && name == "." {
			cancel()
		}
		return fd, openErr
	}
	cleanupWasCanceled := false
	realClose := writer.store.deps.close
	writer.store.deps.close = func(closeCtx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || closeCtx.Err() != nil
		return realClose(closeCtx, fd)
	}

	stage, err := writer.createArchiveStage(ctx)
	assert.Nil(t, stage)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.False(t, cleanupWasCanceled)
}

func TestTrustedReleaseArchiveStageRejectsCanceledContextBeforeOpen(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	openCalled := false
	writer.store.deps.openAt = func(context.Context, int, string, int, uint32) (int, error) {
		openCalled = true
		return -1, unix.EIO
	}

	stage, err := writer.createArchiveStage(ctx)
	assert.Nil(t, stage)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.False(t, openCalled)
}

func TestTrustedReleaseArchiveStageRejectsInactiveWriter(t *testing.T) {
	t.Parallel()

	var nilWriter *trustedReleaseStoreWriteLease
	stage, err := nilWriter.createArchiveStage(t.Context())
	assert.Nil(t, stage)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)

	stage, err = (&trustedReleaseStoreWriteLease{}).createArchiveStage(t.Context())
	assert.Nil(t, stage)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
}

func TestTrustedReleaseArchiveStagePreservesPrimaryErrorWhenCloseFails(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	writer.store.deps.fchmod = func(context.Context, int, uint32) error { return unix.EINVAL }
	realClose := writer.store.deps.close
	writer.store.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	stage, err := writer.createArchiveStage(t.Context())
	assert.Nil(t, stage)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.ErrorIs(t, err, unix.EINVAL)
	assert.ErrorIs(t, err, unix.EIO)
	writer.store.deps.close = realClose
}

func TestTrustedReleaseArchiveStageReleaseReportsCloseFailureOnce(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	stage, err := writer.createArchiveStage(t.Context())
	require.NoError(t, err)
	realClose := stage.deps.close
	stage.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	err = stage.Release(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EIO)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, -1, stage.fd)
	assert.NoError(t, stage.Release(t.Context()))
}

// trustedReleaseDirectoryEntryNames returns observable store entries without interpreting any entry metadata.
func trustedReleaseDirectoryEntryNames(t *testing.T, directory string) []string {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
