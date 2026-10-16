package privileged

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestVMExecReleaseFrameReaderReadsOrdinaryBytesAndCleanEOF(t *testing.T) {
	reader := newScriptedVMExecReleaseFrameReader(t, []fakeVMExecReceive{
		{payload: []byte("release")},
		{},
	}, nil)
	t.Cleanup(func() { assert.NoError(t, reader.Close()) })

	got, err := io.ReadAll(reader)

	require.NoError(t, err)
	if diff := cmp.Diff([]byte("release"), got); diff != "" {
		t.Errorf("release frame mismatch (-want +got):\n%s", diff)
	}
	assert.NoError(t, reader.Close())
}

func TestVMExecReleaseFrameReaderRejectsProtocolViolationsAndClosesAncillary(t *testing.T) {
	tests := map[string]struct {
		script        []fakeVMExecReceive
		carriesRights bool
	}{
		"initial ancillary": {
			script: []fakeVMExecReceive{{payload: []byte("r")}}, carriesRights: true,
		},
		"late ancillary": {
			script: []fakeVMExecReceive{{payload: []byte("r")}, {payload: []byte("e")}}, carriesRights: true,
		},
		"sender address": {
			script: []fakeVMExecReceive{{payload: []byte("r"), from: &unix.SockaddrUnix{Name: "sender"}}},
		},
		"unexpected flags": {
			script: []fakeVMExecReceive{{payload: []byte("r"), flags: unix.MSG_OOB}},
		},
		"payload truncation": {
			script: []fakeVMExecReceive{{payload: []byte("r"), flags: unix.MSG_TRUNC}},
		},
		"control truncation": {
			script: []fakeVMExecReceive{{payload: []byte("r"), flags: unix.MSG_CTRUNC}},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fd := -1
			if test.carriesRights {
				fd = requireRawTestFD(t)
				test.script[len(test.script)-1].control = unix.UnixRights(fd)
			}
			tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
			reader := newScriptedVMExecReleaseFrameReader(t, test.script, tracker)

			var err error
			for range test.script {
				_, err = reader.Read(make([]byte, 8))
				if err != nil {
					break
				}
			}

			domainErr := requireVMExecDomainError(t, err)
			assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
			assert.Equal(t, errs.ClassValidation, domainErr.Class)
			if test.carriesRights {
				assertVMExecFDClosedOnce(t, tracker, fd)
			} else {
				assert.Empty(t, tracker.calls)
			}
			assert.Zero(t, tracker.shutdownCount(fd))
			_, repeatedErr := reader.Read(make([]byte, 1))
			assert.Error(t, repeatedErr)
			assert.NoError(t, reader.Close())
			assert.NoError(t, reader.Close())
		})
	}
}

func TestVMExecReleaseFrameReaderBoundsCancellationAndDeadline(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reader := newVMExecReleaseFrameReaderWithDeps(
			ctx, 23, time.Now().Add(time.Second), successfulVMExecReaderDeps(nil),
		)

		_, err := reader.Read(make([]byte, 1))

		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, errs.CodeProcessError, requireVMExecDomainError(t, err).Code)
	})

	t.Run("deadline", func(t *testing.T) {
		reader := newVMExecReleaseFrameReaderWithDeps(
			context.Background(), 23, time.Now().Add(-time.Second), successfulVMExecReaderDeps(nil),
		)

		_, err := reader.Read(make([]byte, 1))

		assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
		assert.Equal(t, errs.CodeProcessError, requireVMExecDomainError(t, err).Code)
	})
}

func newScriptedVMExecReleaseFrameReader(
	t *testing.T,
	script []fakeVMExecReceive,
	tracker *fakeVMExecCloseTracker,
) *vmExecReleaseFrameReader {
	t.Helper()
	deps := successfulVMExecReaderDeps(tracker)
	index := 0
	deps.recvmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		if index >= len(script) {
			return 0, 0, 0, nil, nil
		}
		step := script[index]
		index++
		return copy(payload, step.payload), copy(control, step.control), step.flags, step.from, step.err
	}
	return newVMExecReleaseFrameReaderWithDeps(context.Background(), 23, time.Now().Add(time.Second), deps)
}
