package jailer

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: manifest authority must remain anonymous until it can be linked beside both finalized executables in one
// candidate. No direct manifest pathname may expose an empty or partially written authority record after a crash.
func TestTrustedReleaseArchitectureCreatesAnonymousCanonicalManifestStage(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	manifest := testTrustedReleaseManifest(fixture.slot)
	wantRaw, err := encodeTrustedReleaseManifest(manifest)
	require.NoError(t, err)
	before := directoryNamesForTest(t, fixture.architecturePath)
	realOpenAt := architecture.deps.openAt
	realPwrite := architecture.deps.pwrite
	var openName string
	var openFlags int
	var openMode uint32
	architecture.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		openName, openFlags, openMode = name, flags, mode
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}
	architecture.deps.pwrite = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		if len(value) > 3 {
			value = value[:3]
		}
		return realPwrite(ctx, fd, value, offset)
	}

	stage, err := architecture.createManifestStage(t.Context(), manifest)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stage.Release(context.Background())) })
	if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.architecturePath)); diff != "" {
		t.Errorf("anonymous manifest stage changed architecture entries (-before +after):\n%s", diff)
	}
	assert.Equal(t, ".", openName)
	assert.Equal(t, trustedReleaseManifestStageFlags, openFlags)
	assert.Equal(t, trustedReleaseStoreManifestMode, openMode)
	assert.NotZero(t, openFlags&unix.O_TMPFILE)
	assert.Zero(t, openFlags&unix.O_EXCL)
	fdFlags, err := unix.FcntlInt(uintptr(stage.fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, fdFlags&unix.FD_CLOEXEC)

	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(stage.fd, &stat))
	assert.Equal(t, uint32(unix.S_IFREG), stat.Mode&unix.S_IFMT)
	assert.Equal(t, uint64(0), stat.Nlink)
	assert.Equal(t, fixture.policy.expectedUID, stat.Uid)
	assert.Equal(t, fixture.policy.expectedGID, stat.Gid)
	assert.Equal(t, trustedReleaseStoreManifestMode, stat.Mode&07777)
	assert.Equal(t, int64(len(wantRaw)), stat.Size)
	raw := make([]byte, len(wantRaw))
	count, err := unix.Pread(stage.fd, raw, 0)
	require.NoError(t, err)
	assert.Equal(t, len(raw), count)
	if diff := cmp.Diff(wantRaw, raw); diff != "" {
		t.Errorf("anonymous manifest stage bytes mismatch (-want +got):\n%s", diff)
	}
	offset, err := unix.Seek(stage.fd, 0, unix.SEEK_CUR)
	require.NoError(t, err)
	assert.Zero(t, offset)
	if diff := cmp.Diff(
		manifest,
		stage.manifest,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("anonymous manifest stage authority mismatch (-want +got):\n%s", diff)
	}

	require.NoError(t, stage.Release(t.Context()))
	require.NoError(t, stage.Release(t.Context()))
	assert.Equal(t, -1, stage.fd)
	assert.Nil(t, stage.raw)
	assertEmptyTrustedReleaseManifest(t, stage.manifest)
	if diff := cmp.Diff(unix.Stat_t{}, stage.identity); diff != "" {
		t.Errorf("released manifest stage identity mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseArchitectureRejectsManifestStageAuthorityBeforeAnonymousOpen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutateArchitecture func(*trustedReleaseArchitectureWriteLease)
		mutateManifest     func(*trustedReleaseManifest)
		code               errs.Code
	}{
		"missing architecture lease": {
			mutateArchitecture: func(architecture *trustedReleaseArchitectureWriteLease) {
				architecture.fd = -1
			},
			code: errs.CodeBinaryTrustedInstallFailed,
		},
		"released slot lease": {
			mutateArchitecture: func(architecture *trustedReleaseArchitectureWriteLease) {
				architecture.slotLease.releaseLock.fd = -1
			},
			code: errs.CodeBinaryTrustedInstallFailed,
		},
		"mismatched manifest slot": {
			mutateManifest: func(manifest *trustedReleaseManifest) {
				manifest.slot.version = "1.16.0"
			},
			code: errs.CodeBinaryUntrusted,
		},
		"invalid manifest": {
			mutateManifest: func(manifest *trustedReleaseManifest) {
				manifest.schemaVersion++
			},
			code: errs.CodeBinaryUntrusted,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, architecture := newTrustedReleaseArchitectureFixture(t)
			manifest := testTrustedReleaseManifest(fixture.slot)
			originalArchitectureFD := architecture.fd
			originalReleaseLockFD := architecture.slotLease.releaseLock.fd
			if test.mutateArchitecture != nil {
				test.mutateArchitecture(architecture)
			}
			if test.mutateManifest != nil {
				test.mutateManifest(&manifest)
			}
			openCalled := false
			architecture.deps.openAt = func(context.Context, int, string, int, uint32) (int, error) {
				openCalled = true
				return -1, unix.EIO
			}

			stage, err := architecture.createManifestStage(t.Context(), manifest)
			architecture.fd = originalArchitectureFD
			architecture.slotLease.releaseLock.fd = originalReleaseLockFD
			require.Error(t, err)
			assert.Nil(t, stage)
			assert.Equal(t, test.code, errs.AsDomainError(err).Code)
			assert.False(t, openCalled)
		})
	}
}

func TestTrustedReleaseArchitectureRejectsManifestStageFailuresWithCheckedCleanup(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject func(*trustedReleaseManifestStageFailureFixture)
		want   error
	}{
		"open": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.openAt = func(context.Context, int, string, int, uint32) (int, error) {
					return -1, unix.EIO
				}
			},
			want: unix.EIO,
		},
		"initial metadata": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				realFstat := fixture.architecture.deps.fstat
				calls := 0
				fixture.architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					if err := realFstat(ctx, fd, stat); err != nil {
						return err
					}
					calls++
					if calls == 2 {
						stat.Nlink = 1
					}
					return nil
				}
			},
		},
		"architecture stat": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.fstat = func(context.Context, int, *unix.Stat_t) error {
					return unix.EIO
				}
			},
			want: unix.EIO,
		},
		"stage stat": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				realFstat := fixture.architecture.deps.fstat
				calls := 0
				fixture.architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						return unix.EIO
					}
					return realFstat(ctx, fd, stat)
				}
			},
			want: unix.EIO,
		},
		"owner": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.fchown = func(context.Context, int, int, int) error { return unix.EPERM }
			},
			want: unix.EPERM,
		},
		"mode": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.fchmod = func(context.Context, int, uint32) error { return unix.EPERM }
			},
			want: unix.EPERM,
		},
		"positioned write": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
					return 0, unix.ENOSPC
				}
			},
			want: unix.ENOSPC,
		},
		"invalid positioned write count": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.pwrite = func(_ context.Context, _ int, value []byte, _ int64) (int, error) {
					return len(value) + 1, nil
				}
			},
		},
		"zero positioned write": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
					return 0, nil
				}
			},
			want: io.ErrNoProgress,
		},
		"sync": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.fsync = func(context.Context, int) error { return unix.ENOSPC }
			},
			want: unix.ENOSPC,
		},
		"final identity": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) { stat.Ino++ })
			},
		},
		"final device": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) { stat.Dev++ })
			},
		},
		"final type": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) {
					stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFDIR
				})
			},
		},
		"final link count": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) { stat.Nlink++ })
			},
		},
		"final owner UID": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) { stat.Uid++ })
			},
		},
		"final owner GID": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) { stat.Gid++ })
			},
		},
		"final mode": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) {
					stat.Mode = stat.Mode&^07777 | 0644
				})
			},
		},
		"final size": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				injectTrustedReleaseManifestStageFinalStat(fixture, func(stat *unix.Stat_t) { stat.Size++ })
			},
		},
		"nonzero offset": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.seek = func(context.Context, int, int64, int) (int64, error) {
					return 1, nil
				}
			},
		},
		"offset inspection": {
			inject: func(fixture *trustedReleaseManifestStageFailureFixture) {
				fixture.architecture.deps.seek = func(context.Context, int, int64, int) (int64, error) {
					return 0, unix.EIO
				}
			},
			want: unix.EIO,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, architecture := newTrustedReleaseArchitectureFixture(t)
			fixture := &trustedReleaseManifestStageFailureFixture{architecture: architecture}
			before := directoryNamesForTest(t, store.architecturePath)
			test.inject(fixture)

			stage, err := architecture.createManifestStage(t.Context(), testTrustedReleaseManifest(store.slot))
			require.Error(t, err)
			assert.Nil(t, stage)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
			if diff := cmp.Diff(before, directoryNamesForTest(t, store.architecturePath)); diff != "" {
				t.Errorf("rejected manifest stage changed architecture entries (-before +after):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseArchitectureManifestStageCancellationCleansWithoutCanceledEffects(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	before := directoryNamesForTest(t, fixture.architecturePath)
	ctx, cancel := context.WithCancel(t.Context())
	realPwrite := architecture.deps.pwrite
	architecture.deps.pwrite = func(writeCtx context.Context, fd int, value []byte, offset int64) (int, error) {
		if len(value) > 1 {
			value = value[:1]
		}
		count, err := realPwrite(writeCtx, fd, value, offset)
		cancel()
		return count, err
	}
	realClose := architecture.deps.close
	cleanupWasCanceled := false
	architecture.deps.close = func(closeCtx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || closeCtx.Err() != nil
		return realClose(closeCtx, fd)
	}

	stage, err := architecture.createManifestStage(ctx, testTrustedReleaseManifest(fixture.slot))
	require.Error(t, err)
	assert.Nil(t, stage)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, cleanupWasCanceled)
	if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.architecturePath)); diff != "" {
		t.Errorf("canceled manifest stage changed architecture entries (-before +after):\n%s", diff)
	}
}

func TestTrustedReleaseArchitectureManifestStagePreservesPrimaryAndCleanupErrors(t *testing.T) {
	t.Parallel()

	fixture, architecture := newTrustedReleaseArchitectureFixture(t)
	architecture.deps.fchmod = func(context.Context, int, uint32) error { return unix.EIO }
	realClose := architecture.deps.close
	architecture.deps.close = func(closeCtx context.Context, fd int) error {
		return errors.Join(realClose(closeCtx, fd), unix.ENOSPC)
	}

	stage, err := architecture.createManifestStage(t.Context(), testTrustedReleaseManifest(fixture.slot))
	architecture.deps.close = realClose
	require.Error(t, err)
	assert.Nil(t, stage)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, domainErr.Code)
	assert.ErrorIs(t, err, unix.EIO)
	assert.ErrorIs(t, err, unix.ENOSPC)
}

type trustedReleaseManifestStageFailureFixture struct {
	architecture *trustedReleaseArchitectureWriteLease
}

func injectTrustedReleaseManifestStageFinalStat(
	fixture *trustedReleaseManifestStageFailureFixture,
	mutate func(*unix.Stat_t),
) {
	realFstat := fixture.architecture.deps.fstat
	calls := 0
	fixture.architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		calls++
		if calls == 3 {
			mutate(stat)
		}
		return nil
	}
}
