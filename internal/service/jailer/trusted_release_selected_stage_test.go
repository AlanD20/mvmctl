package jailer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: selected executable bytes must remain private until the complete archive and both ELF objects validate.
// O_TMPFILE without O_EXCL keeps each object anonymous while retaining the later descriptor-relative link capability.
func TestCreateTrustedReleaseSelectedStagesPinsAnonymousStoreObjects(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	archivePolicy := trustedReleaseArchivePolicyForTest(t, fixture.slot)
	before := directoryNamesForTest(t, fixture.binariesPath)
	realOpenAt := lease.store.deps.openAt
	var openFlags []int
	lease.store.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		assert.Equal(t, ".", name)
		assert.Equal(t, trustedReleaseSelectedStageWriteMode, mode)
		openFlags = append(openFlags, flags)
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}

	selected, err := lease.createSelectedExecutableStages(t.Context(), archivePolicy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, selected.Release(context.Background())) })

	if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.binariesPath)); diff != "" {
		t.Errorf("anonymous selected stages changed store entries (-before +after):\n%s", diff)
	}
	assert.Equal(t, trustedReleaseSelectedStagesCreated, selected.state)
	if diff := cmp.Diff(
		[]int{trustedReleaseSelectedStageFlags, trustedReleaseSelectedStageFlags},
		openFlags,
	); diff != "" {
		t.Errorf("selected-stage open flags mismatch (-want +got):\n%s", diff)
	}
	assert.NotZero(t, trustedReleaseSelectedStageFlags&unix.O_TMPFILE)
	assert.Zero(t, trustedReleaseSelectedStageFlags&unix.O_EXCL)
	for _, fd := range []int{selected.firecracker.fd, selected.jailer.fd} {
		var stat unix.Stat_t
		require.NoError(t, unix.Fstat(fd, &stat))
		assert.Equal(t, uint32(unix.S_IFREG), stat.Mode&unix.S_IFMT)
		assert.Equal(t, uint64(0), stat.Nlink)
		assert.Equal(t, fixture.policy.expectedUID, stat.Uid)
		assert.Equal(t, fixture.policy.expectedGID, stat.Gid)
		assert.Equal(t, uint32(trustedReleaseSelectedStageWriteMode), stat.Mode&07777)
		assert.Zero(t, stat.Size)
		offset, seekErr := unix.Seek(fd, 0, unix.SEEK_CUR)
		require.NoError(t, seekErr)
		assert.Zero(t, offset)
		fdFlags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		require.NoError(t, flagErr)
		assert.NotZero(t, fdFlags&unix.FD_CLOEXEC)
	}
}

func TestTrustedReleaseSelectedStagesExtractAndFinalizeBothExecutables(t *testing.T) {
	t.Parallel()

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	firecracker := trustedReleaseTestELF("selected Firecracker")
	jailer := trustedReleaseTestELF("selected Jailer")
	setTrustedReleaseSelectedArchiveBytesForTest(t, archiveFixture, firecracker, jailer)
	stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
	realPwrite := selected.deps.pwrite
	selected.deps.pwrite = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		if len(value) > 7 {
			value = value[:7]
		}
		return realPwrite(ctx, fd, value, offset)
	}

	err := selected.extract(t.Context(), stage)
	require.NoError(t, err)
	assert.Equal(t, trustedReleaseSelectedStagesExtracted, selected.state)
	assert.Equal(t, trustedReleaseArchiveStageExtracted, stage.state)
	manifest, err := selected.finalize(t.Context())
	require.NoError(t, err)
	assert.Equal(t, trustedReleaseSelectedStagesFinalized, selected.state)
	wantManifest := trustedReleaseManifest{
		schemaVersion: trustedReleaseManifestSchemaVersion,
		slot:          archiveFixture.policy.source.slot,
		archiveDigest: stage.archiveDigest,
		firecracker: trustedReleaseExecutable{
			digest:    trustedReleaseExecutableDigest(sha256.Sum256(firecracker)),
			sizeBytes: uint64(len(firecracker)),
		},
		jailer: trustedReleaseExecutable{
			digest:    trustedReleaseExecutableDigest(sha256.Sum256(jailer)),
			sizeBytes: uint64(len(jailer)),
		},
	}
	manifestOptions := cmp.AllowUnexported(
		trustedReleaseManifest{},
		releaseSlot{},
		trustedReleaseExecutable{},
	)
	if diff := cmp.Diff(wantManifest, manifest, manifestOptions); diff != "" {
		t.Errorf("finalized trusted release manifest mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, validateTrustedReleaseManifest(manifest))

	assertTrustedReleaseSelectedStageBytes(t, selected.firecracker.fd, firecracker)
	assertTrustedReleaseSelectedStageBytes(t, selected.jailer.fd, jailer)
	for _, fd := range []int{selected.firecracker.fd, selected.jailer.fd} {
		var stat unix.Stat_t
		require.NoError(t, unix.Fstat(fd, &stat))
		assert.Equal(t, uint64(0), stat.Nlink)
		assert.Equal(t, uint32(trustedReleaseStoreExecutableMode), stat.Mode&07777)
		offset, seekErr := unix.Seek(fd, 0, unix.SEEK_CUR)
		require.NoError(t, seekErr)
		assert.Zero(t, offset)
	}

	err = selected.writeFirecracker(t.Context(), []byte("retry"), uint64(len(firecracker)))
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	_, err = selected.finalize(t.Context())
	require.Error(t, err)
}

func TestTrustedReleaseSelectedStagesRejectArchiveFailureAndPoisonPair(t *testing.T) {
	t.Parallel()

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	archiveFixture.members = archiveFixture.members[:len(archiveFixture.members)-1]
	stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)

	err := selected.extract(t.Context(), stage)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
	assert.Equal(t, trustedReleaseSelectedStagesFailed, selected.state)
	_, err = selected.finalize(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
}

func TestTrustedReleaseSelectedStagesRejectUnavailableArchiveBeforeTransition(t *testing.T) {
	t.Parallel()

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)

	err := selected.extract(t.Context(), nil)
	require.Error(t, err)
	assert.Equal(t, trustedReleaseSelectedStagesCreated, selected.state)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = selected.extract(ctx, stage)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, trustedReleaseSelectedStagesCreated, selected.state)
}

func TestTrustedReleaseSelectedStagesRejectEitherInvalidELFBeforeExecutableMode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		firecracker []byte
		jailer      []byte
	}{
		"Firecracker": {
			firecracker: make([]byte, trustedReleaseTestExecutableBytes),
			jailer:      trustedReleaseTestELF("selected Jailer"),
		},
		"Jailer": {
			firecracker: trustedReleaseTestELF("selected Firecracker"),
			jailer:      make([]byte, trustedReleaseTestExecutableBytes),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			archiveFixture := newTrustedReleaseArchiveFixture(t)
			setTrustedReleaseSelectedArchiveBytesForTest(t, archiveFixture, test.firecracker, test.jailer)
			stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
			require.NoError(t, selected.extract(t.Context(), stage))

			_, err := selected.finalize(t.Context())
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseSelectedStagesFailed, selected.state)
			for _, fd := range []int{selected.firecracker.fd, selected.jailer.fd} {
				var stat unix.Stat_t
				require.NoError(t, unix.Fstat(fd, &stat))
				assert.Equal(t, uint64(0), stat.Nlink)
				assert.Equal(t, uint32(trustedReleaseSelectedStageWriteMode), stat.Mode&07777)
			}
		})
	}
}

func TestTrustedReleaseSelectedStagesRejectWriteFailuresAndOffsets(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*trustedReleaseSelectedExecutableStages)
		write  func(*trustedReleaseSelectedExecutableStages) error
		want   error
	}{
		"non-sequential offset": {
			write: func(selected *trustedReleaseSelectedExecutableStages) error {
				return selected.writeFirecracker(t.Context(), []byte("value"), 1)
			},
		},
		"positioned write failure": {
			mutate: func(selected *trustedReleaseSelectedExecutableStages) {
				selected.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
					return 0, unix.EIO
				}
			},
			write: func(selected *trustedReleaseSelectedExecutableStages) error {
				return selected.writeFirecracker(t.Context(), []byte("value"), 0)
			},
			want: unix.EIO,
		},
		"invalid positioned write count": {
			mutate: func(selected *trustedReleaseSelectedExecutableStages) {
				selected.deps.pwrite = func(_ context.Context, _ int, value []byte, _ int64) (int, error) {
					return len(value) + 1, nil
				}
			},
			write: func(selected *trustedReleaseSelectedExecutableStages) error {
				return selected.writeJailer(t.Context(), []byte("value"), 0)
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			archiveFixture := newTrustedReleaseArchiveFixture(t)
			_, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
			selected.state = trustedReleaseSelectedStagesExtracting
			if test.mutate != nil {
				test.mutate(selected)
			}

			err := test.write(selected)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseSelectedStagesFailed, selected.state)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
		})
	}
}

func TestTrustedReleaseSelectedStagesRejectFinalizationFailuresWithoutPublication(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject func(*testing.T, *trustedReleaseSelectedExecutableStages)
		want   error
	}{
		"nonzero descriptor offset": {
			inject: func(t *testing.T, selected *trustedReleaseSelectedExecutableStages) {
				_, err := unix.Seek(selected.firecracker.fd, 1, unix.SEEK_SET)
				require.NoError(t, err)
			},
		},
		"mode change failure": {
			inject: func(_ *testing.T, selected *trustedReleaseSelectedExecutableStages) {
				selected.deps.fchmod = func(context.Context, int, uint32) error { return unix.EIO }
			},
			want: unix.EIO,
		},
		"sync failure": {
			inject: func(_ *testing.T, selected *trustedReleaseSelectedExecutableStages) {
				selected.deps.fsync = func(context.Context, int) error { return unix.ENOSPC }
			},
			want: unix.ENOSPC,
		},
		"identity change during hash": {
			inject: func(_ *testing.T, selected *trustedReleaseSelectedExecutableStages) {
				realFstat := selected.deps.fstat
				firecrackerCalls := 0
				selected.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					err := realFstat(ctx, fd, stat)
					if err == nil && fd == selected.firecracker.fd {
						firecrackerCalls++
						if firecrackerCalls == 2 {
							stat.Ino++
						}
					}
					return err
				}
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			archiveFixture := newTrustedReleaseArchiveFixture(t)
			setTrustedReleaseSelectedArchiveBytesForTest(
				t,
				archiveFixture,
				trustedReleaseTestELF("selected Firecracker"),
				trustedReleaseTestELF("selected Jailer"),
			)
			stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
			require.NoError(t, selected.extract(t.Context(), stage))
			test.inject(t, selected)

			_, err := selected.finalize(t.Context())
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseSelectedStagesFailed, selected.state)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
			for _, fd := range []int{selected.firecracker.fd, selected.jailer.fd} {
				var stat unix.Stat_t
				require.NoError(t, unix.Fstat(fd, &stat))
				assert.Equal(t, uint64(0), stat.Nlink)
			}
		})
	}
}

func TestTrustedReleaseSelectedStagesAllowRetryAfterPreFinalizationCancellation(t *testing.T) {
	t.Parallel()

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	setTrustedReleaseSelectedArchiveBytesForTest(
		t,
		archiveFixture,
		trustedReleaseTestELF("selected Firecracker"),
		trustedReleaseTestELF("selected Jailer"),
	)
	stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
	require.NoError(t, selected.extract(t.Context(), stage))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := selected.finalize(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, trustedReleaseSelectedStagesExtracted, selected.state)
	_, err = selected.finalize(t.Context())
	require.NoError(t, err)
}

func TestCreateTrustedReleaseSelectedStagesRejectsFailuresWithCheckedCleanup(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	lease, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	policy := trustedReleaseArchivePolicyForTest(t, fixture.slot)
	realOpenAt := lease.store.deps.openAt
	openCalls := 0
	lease.store.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		openCalls++
		if openCalls == 2 {
			return -1, unix.EIO
		}
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}
	realClose := lease.store.deps.close
	lease.store.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.ENOSPC)
	}

	selected, err := lease.createSelectedExecutableStages(t.Context(), policy)
	assert.Nil(t, selected)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, domainErr.Code)
	assert.ErrorIs(t, err, unix.EIO)
	assert.ErrorIs(t, err, unix.ENOSPC)
	lease.store.deps.close = realClose
}

func TestTrustedReleaseSelectedStagesPreserveFinalizationErrorDuringRelease(t *testing.T) {
	t.Parallel()

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	invalidFirecracker := make([]byte, trustedReleaseTestExecutableBytes)
	setTrustedReleaseSelectedArchiveBytesForTest(
		t,
		archiveFixture,
		invalidFirecracker,
		trustedReleaseTestELF("selected Jailer"),
	)
	stage, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
	require.NoError(t, selected.extract(t.Context(), stage))
	realClose := selected.deps.close
	selected.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	_, primary := selected.finalize(t.Context())
	require.Error(t, primary)
	domainErr := errs.AsDomainError(primary)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	combined := releaseRejectedTrustedReleaseSelectedStages(t.Context(), primary, selected)
	require.Error(t, combined)
	assert.Same(t, domainErr, errs.AsDomainError(combined))
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.ErrorIs(t, combined, unix.EIO)
}

func TestTrustedReleaseSelectedStagesReleaseClosesBothInReverseWithUncanceledCleanup(t *testing.T) {
	t.Parallel()

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	_, selected := newTrustedReleaseSelectedStagesFixture(t, archiveFixture)
	firecrackerFD := selected.firecracker.fd
	jailerFD := selected.jailer.fd
	realClose := selected.deps.close
	var closed []int
	cleanupWasCanceled := false
	selected.deps.close = func(ctx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || ctx.Err() != nil
		closed = append(closed, fd)
		return realClose(ctx, fd)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, selected.Release(ctx))
	if diff := cmp.Diff([]int{jailerFD, firecrackerFD}, closed); diff != "" {
		t.Errorf("selected-stage close order mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, cleanupWasCanceled)
	assert.Equal(t, -1, selected.firecracker.fd)
	assert.Equal(t, -1, selected.jailer.fd)
	assert.Zero(t, selected.firecracker.sizeBytes)
	assert.Zero(t, selected.jailer.sizeBytes)
	assert.Equal(t, trustedReleaseSelectedStagesFailed, selected.state)
	if diff := cmp.Diff(trustedReleaseArchiveDigest{}, selected.archiveDigest); diff != "" {
		t.Errorf("released selected-stage archive digest mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(unix.Stat_t{}, selected.firecracker.identity); diff != "" {
		t.Errorf("released Firecracker identity mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(unix.Stat_t{}, selected.jailer.identity); diff != "" {
		t.Errorf("released Jailer identity mismatch (-want +got):\n%s", diff)
	}

	require.NoError(t, selected.Release(t.Context()))
	if diff := cmp.Diff([]int{jailerFD, firecrackerFD}, closed); diff != "" {
		t.Errorf("idempotent selected-stage release closed extra descriptors (-want +got):\n%s", diff)
	}
}

func newTrustedReleaseSelectedStagesFixture(
	t *testing.T,
	archiveFixture *trustedReleaseArchiveFixture,
) (*trustedReleaseArchiveStage, *trustedReleaseSelectedExecutableStages) {
	t.Helper()

	storeFixture := newTrustedReleaseStoreFixture(t)
	lease, err := openTrustedReleaseStoreForWrite(t.Context(), storeFixture.deps, storeFixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	archiveStage, err := lease.createArchiveStage(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, archiveStage.Release(context.Background())) })
	compressed := archiveFixture.compressed(t)
	require.NoError(
		t,
		archiveStage.receive(
			t.Context(),
			bytes.NewReader(compressed),
			uint64(len(compressed)),
			trustedReleaseArchiveDigestForTest(compressed),
		),
	)
	selected, err := lease.createSelectedExecutableStages(t.Context(), archiveFixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, selected.Release(context.Background())) })
	return archiveStage, selected
}

func setTrustedReleaseSelectedArchiveBytesForTest(
	t *testing.T,
	fixture *trustedReleaseArchiveFixture,
	firecracker []byte,
	jailer []byte,
) {
	t.Helper()

	fixture.firecrackerBytes = slices.Clone(firecracker)
	fixture.jailerBytes = slices.Clone(jailer)
	for index := range fixture.members {
		memberPolicy := fixture.policy.members[fixture.members[index].name]
		switch memberPolicy.selected {
		case trustedReleaseArchiveFirecracker:
			fixture.members[index].data = slices.Clone(firecracker)
		case trustedReleaseArchiveJailer:
			fixture.members[index].data = slices.Clone(jailer)
		}
	}
}

func trustedReleaseArchivePolicyForTest(t *testing.T, slot releaseSlot) trustedReleaseArchivePolicy {
	t.Helper()

	source, err := newTrustedReleaseSource(slot)
	require.NoError(t, err)
	policy, err := newTrustedReleaseArchivePolicy(source)
	require.NoError(t, err)
	return policy
}

func assertTrustedReleaseSelectedStageBytes(t *testing.T, fd int, want []byte) {
	t.Helper()

	got := make([]byte, len(want))
	count, err := unix.Pread(fd, got, 0)
	require.NoError(t, err)
	assert.Equal(t, len(want), count)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("selected stage bytes mismatch (-want +got):\n%s", diff)
	}
}

func directoryNamesForTest(t *testing.T, directory string) []string {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	return result
}
