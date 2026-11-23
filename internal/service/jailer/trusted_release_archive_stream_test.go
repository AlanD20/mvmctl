package jailer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseArchiveStageReceivesExactVerifiedStream(t *testing.T) {
	t.Parallel()

	stage, writer := newTrustedReleaseArchiveStreamFixture(t)
	require.NoError(t, writer.Release(t.Context()))
	payload := bytes.Repeat([]byte("trusted archive bytes"), 4097)
	digest := trustedReleaseArchiveDigestForTest(payload)
	reader := &trustedReleaseArchiveTrackingReader{reader: bytes.NewReader(payload)}
	realPwrite := stage.deps.pwrite
	var offsets []int64
	var writeSizes []int
	stage.deps.pwrite = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		offsets = append(offsets, offset)
		writeSizes = append(writeSizes, len(value))
		return realPwrite(ctx, fd, value, offset)
	}
	realFsync := stage.deps.fsync
	fsyncCalls := 0
	stage.deps.fsync = func(ctx context.Context, fd int) error {
		fsyncCalls++
		return realFsync(ctx, fd)
	}

	err := stage.receive(t.Context(), reader, uint64(len(payload)), digest)
	require.NoError(t, err)
	assert.Equal(t, trustedReleaseArchiveStageReady, stage.state)
	assert.Equal(t, uint64(len(payload)), stage.sizeBytes)
	assert.Equal(t, digest, stage.archiveDigest)
	assert.LessOrEqual(t, reader.maxRequested, trustedReleaseArchiveStreamBufferBytes)
	assert.Equal(t, 1, fsyncCalls)

	wantOffsets := make([]int64, 0, len(offsets))
	wantWriteSizes := make([]int, 0, len(writeSizes))
	for offset := 0; offset < len(payload); offset += trustedReleaseArchiveStreamBufferBytes {
		wantOffsets = append(wantOffsets, int64(offset))
		remaining := len(payload) - offset
		if remaining > trustedReleaseArchiveStreamBufferBytes {
			remaining = trustedReleaseArchiveStreamBufferBytes
		}
		wantWriteSizes = append(wantWriteSizes, remaining)
	}
	if diff := cmp.Diff(wantOffsets, offsets); diff != "" {
		t.Errorf("positioned-write offsets mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantWriteSizes, writeSizes); diff != "" {
		t.Errorf("positioned-write sizes mismatch (-want +got):\n%s", diff)
	}

	stored := make([]byte, len(payload))
	count, err := unix.Pread(stage.fd, stored, 0)
	require.NoError(t, err)
	assert.Equal(t, len(payload), count)
	if diff := cmp.Diff(payload, stored); diff != "" {
		t.Errorf("stored archive mismatch (-want +got):\n%s", diff)
	}
	offset, err := unix.Seek(stage.fd, 0, unix.SEEK_CUR)
	require.NoError(t, err)
	assert.Equal(t, int64(0), offset)
	retryReader := &trustedReleaseArchiveTrackingReader{reader: bytes.NewReader(payload)}
	err = stage.receive(t.Context(), retryReader, uint64(len(payload)), digest)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 0, retryReader.calls)
}

func TestTrustedReleaseArchiveStageRejectsStreamSizeOutsidePolicyBeforeRead(t *testing.T) {
	t.Parallel()

	tests := map[string]uint64{
		"empty":     0,
		"too large": trustedReleaseArchiveMaxBytes + 1,
		"overflow":  ^uint64(0),
	}
	for name, declaredBytes := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stage, _ := newTrustedReleaseArchiveStreamFixture(t)
			reader := &trustedReleaseArchiveTrackingReader{reader: bytes.NewReader([]byte("ignored"))}
			pwriteCalled := false
			stage.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
				pwriteCalled = true
				return 0, unix.EIO
			}

			err := stage.receive(t.Context(), reader, declaredBytes, trustedReleaseArchiveDigest{})
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, 0, reader.calls)
			assert.False(t, pwriteCalled)
			assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
		})
	}
}

func TestTrustedReleaseArchiveStageRejectsUntrustedStreamContent(t *testing.T) {
	t.Parallel()

	payload := []byte("complete trusted release archive")
	digest := trustedReleaseArchiveDigestForTest(payload)
	tests := map[string]struct {
		body          []byte
		declaredBytes uint64
		digest        trustedReleaseArchiveDigest
	}{
		"truncated": {
			body:          payload,
			declaredBytes: uint64(len(payload) + 1),
			digest:        digest,
		},
		"trailing": {
			body:          append(append([]byte(nil), payload...), '!'),
			declaredBytes: uint64(len(payload)),
			digest:        digest,
		},
		"digest mismatch": {
			body:          payload,
			declaredBytes: uint64(len(payload)),
			digest:        trustedReleaseArchiveDigest{},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stage, _ := newTrustedReleaseArchiveStreamFixture(t)
			fsyncCalled := false
			stage.deps.fsync = func(context.Context, int) error {
				fsyncCalled = true
				return nil
			}

			err := stage.receive(t.Context(), bytes.NewReader(test.body), test.declaredBytes, test.digest)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
			assert.False(t, fsyncCalled)

			err = stage.receive(
				t.Context(),
				bytes.NewReader(payload),
				uint64(len(payload)),
				digest,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestTrustedReleaseArchiveStageHandlesPartialPositionedWrites(t *testing.T) {
	t.Parallel()

	stage, _ := newTrustedReleaseArchiveStreamFixture(t)
	payload := bytes.Repeat([]byte("partial write"), 100)
	realPwrite := stage.deps.pwrite
	stage.deps.pwrite = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		if len(value) > 7 {
			value = value[:7]
		}
		return realPwrite(ctx, fd, value, offset)
	}

	err := stage.receive(
		t.Context(),
		bytes.NewReader(payload),
		uint64(len(payload)),
		trustedReleaseArchiveDigestForTest(payload),
	)
	require.NoError(t, err)
	assert.Equal(t, trustedReleaseArchiveStageReady, stage.state)

	stored := make([]byte, len(payload))
	count, err := unix.Pread(stage.fd, stored, 0)
	require.NoError(t, err)
	assert.Equal(t, len(payload), count)
	if diff := cmp.Diff(payload, stored); diff != "" {
		t.Errorf("stored archive mismatch after partial writes (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseArchiveStageRejectsStreamAndWriteFailures(t *testing.T) {
	t.Parallel()

	payload := []byte("trusted archive bytes")
	digest := trustedReleaseArchiveDigestForTest(payload)
	tests := map[string]struct {
		reader func(context.CancelFunc) io.Reader
		inject func(*trustedReleaseArchiveStage)
		want   error
	}{
		"reader error": {
			reader: func(context.CancelFunc) io.Reader {
				return io.MultiReader(bytes.NewReader(payload[:5]), trustedReleaseArchiveReaderFunc(
					func([]byte) (int, error) { return 0, unix.EIO },
				))
			},
			want: unix.EIO,
		},
		"reader invalid count": {
			reader: func(context.CancelFunc) io.Reader {
				return trustedReleaseArchiveReaderFunc(func(value []byte) (int, error) {
					return len(value) + 1, nil
				})
			},
		},
		"reader no progress": {
			reader: func(context.CancelFunc) io.Reader {
				return trustedReleaseArchiveReaderFunc(func([]byte) (int, error) { return 0, nil })
			},
			want: io.ErrNoProgress,
		},
		"EOF probe no progress": {
			reader: func(context.CancelFunc) io.Reader {
				firstRead := true
				return trustedReleaseArchiveReaderFunc(func(value []byte) (int, error) {
					if firstRead {
						firstRead = false
						return copy(value, payload), nil
					}
					return 0, nil
				})
			},
			want: io.ErrNoProgress,
		},
		"canceled after read": {
			reader: func(cancel context.CancelFunc) io.Reader {
				return trustedReleaseArchiveReaderFunc(func(value []byte) (int, error) {
					count := copy(value, payload)
					cancel()
					return count, nil
				})
			},
			inject: func(stage *trustedReleaseArchiveStage) {
				stage.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
					return 0, unix.EFAULT
				}
			},
			want: context.Canceled,
		},
		"positioned write error": {
			inject: func(stage *trustedReleaseArchiveStage) {
				stage.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
					return 0, unix.ENOSPC
				}
			},
			want: unix.ENOSPC,
		},
		"positioned write invalid count": {
			inject: func(stage *trustedReleaseArchiveStage) {
				stage.deps.pwrite = func(_ context.Context, _ int, value []byte, _ int64) (int, error) {
					return len(value) + 1, nil
				}
			},
		},
		"positioned write no progress": {
			inject: func(stage *trustedReleaseArchiveStage) {
				stage.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) { return 0, nil }
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stage, _ := newTrustedReleaseArchiveStreamFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.inject != nil {
				test.inject(stage)
			}
			reader := io.Reader(bytes.NewReader(payload))
			if test.reader != nil {
				reader = test.reader(cancel)
			}

			err := stage.receive(ctx, reader, uint64(len(payload)), digest)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
		})
	}
}

func TestTrustedReleaseArchiveStageRejectsAcceptanceFailures(t *testing.T) {
	t.Parallel()

	payload := []byte("trusted archive bytes")
	digest := trustedReleaseArchiveDigestForTest(payload)
	tests := map[string]struct {
		inject func(*trustedReleaseArchiveStage)
		want   error
	}{
		"fsync": {
			inject: func(stage *trustedReleaseArchiveStage) {
				stage.deps.fsync = func(context.Context, int) error { return unix.EIO }
			},
			want: unix.EIO,
		},
		"final fstat": {
			inject: func(stage *trustedReleaseArchiveStage) {
				realFstat := stage.deps.fstat
				calls := 0
				stage.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						return unix.EIO
					}
					return realFstat(ctx, fd, stat)
				}
			},
			want: unix.EIO,
		},
		"changed identity": {
			inject: func(stage *trustedReleaseArchiveStage) {
				realFstat := stage.deps.fstat
				calls := 0
				stage.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					calls++
					if err := realFstat(ctx, fd, stat); err != nil {
						return err
					}
					if calls == 2 {
						stat.Ino++
					}
					return nil
				}
			},
		},
		"wrong final size": {
			inject: func(stage *trustedReleaseArchiveStage) {
				realFstat := stage.deps.fstat
				calls := 0
				stage.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					calls++
					if err := realFstat(ctx, fd, stat); err != nil {
						return err
					}
					if calls == 2 {
						stat.Size++
					}
					return nil
				}
			},
		},
		"changed offset": {
			inject: func(stage *trustedReleaseArchiveStage) {
				realPwrite := stage.deps.pwrite
				stage.deps.pwrite = func(
					ctx context.Context,
					fd int,
					value []byte,
					offset int64,
				) (int, error) {
					count, err := realPwrite(ctx, fd, value, offset)
					if err == nil {
						_, err = unix.Seek(fd, 1, unix.SEEK_SET)
					}
					return count, err
				}
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stage, _ := newTrustedReleaseArchiveStreamFixture(t)
			test.inject(stage)

			err := stage.receive(t.Context(), bytes.NewReader(payload), uint64(len(payload)), digest)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
		})
	}
}

func TestTrustedReleaseArchiveStageRejectsCanceledContextBeforeStreamRead(t *testing.T) {
	t.Parallel()

	stage, _ := newTrustedReleaseArchiveStreamFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &trustedReleaseArchiveTrackingReader{reader: bytes.NewReader([]byte("ignored"))}

	err := stage.receive(ctx, reader, 1, trustedReleaseArchiveDigest{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 0, reader.calls)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func TestTrustedReleaseArchiveStageRejectsNilStreamBeforeTransition(t *testing.T) {
	t.Parallel()

	stage, _ := newTrustedReleaseArchiveStreamFixture(t)
	err := stage.receive(t.Context(), nil, 1, trustedReleaseArchiveDigest{})
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func TestTrustedReleaseArchiveStageRejectsNonzeroInitialOffsetBeforeStreamRead(t *testing.T) {
	t.Parallel()

	stage, _ := newTrustedReleaseArchiveStreamFixture(t)
	_, err := unix.Seek(stage.fd, 1, unix.SEEK_SET)
	require.NoError(t, err)
	reader := &trustedReleaseArchiveTrackingReader{reader: bytes.NewReader([]byte("ignored"))}

	err = stage.receive(t.Context(), reader, 1, trustedReleaseArchiveDigest{})
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 0, reader.calls)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func newTrustedReleaseArchiveStreamFixture(
	t *testing.T,
) (*trustedReleaseArchiveStage, *trustedReleaseStoreWriteLease) {
	t.Helper()

	fixture := newTrustedReleaseStoreFixture(t)
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	stage, err := writer.createArchiveStage(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stage.Release(context.Background())) })
	return stage, writer
}

func trustedReleaseArchiveDigestForTest(value []byte) trustedReleaseArchiveDigest {
	return trustedReleaseArchiveDigest(sha256.Sum256(value))
}

type trustedReleaseArchiveTrackingReader struct {
	reader       io.Reader
	calls        int
	maxRequested int
}

func (reader *trustedReleaseArchiveTrackingReader) Read(value []byte) (int, error) {
	reader.calls++
	if len(value) > reader.maxRequested {
		reader.maxRequested = len(value)
	}
	return reader.reader.Read(value)
}

type trustedReleaseArchiveReaderFunc func([]byte) (int, error)

func (function trustedReleaseArchiveReaderFunc) Read(value []byte) (int, error) {
	return function(value)
}

var _ io.Reader = (*trustedReleaseArchiveTrackingReader)(nil)
var _ io.Reader = trustedReleaseArchiveReaderFunc(nil)
