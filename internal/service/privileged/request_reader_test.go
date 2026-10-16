package privileged

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRequestReaderUsesRawNonblockingReceiveForDataAndEOF(t *testing.T) {
	input := []byte("request")
	receiveCalls := 0
	deps := controlDeps{
		recvmsg: func(_ context.Context, fd int, target []byte, flags int) (int, error) {
			receiveCalls++
			assert.Equal(t, 0, fd)
			assert.Equal(t, unix.MSG_DONTWAIT, flags)
			if len(input) == 0 {
				return 0, nil
			}
			copied := copy(target, input)
			input = input[copied:]
			return copied, nil
		},
		poll: func(context.Context, []unix.PollFd, int) (int, error) {
			t.Fatal("poll must not run while data or EOF is immediately available")
			return 0, nil
		},
	}
	reader := newRequestReader(context.Background(), 0, time.Now().Add(time.Second), deps)

	got, err := io.ReadAll(reader)

	require.NoError(t, err)
	assert.Equal(t, []byte("request"), got)
	assert.Equal(t, 2, receiveCalls)
}

// Rationale: A duplicate descriptor may mutate O_NONBLOCK after admission. Per-call MSG_DONTWAIT plus bounded poll
// remains correct without reading or changing shared descriptor status flags.
func TestRequestReaderPollsEAGAINAndCapsCancellableWait(t *testing.T) {
	receiveCalls := 0
	var pollTimeout int
	deps := controlDeps{
		recvmsg: func(_ context.Context, _ int, target []byte, flags int) (int, error) {
			receiveCalls++
			assert.Equal(t, unix.MSG_DONTWAIT, flags)
			if receiveCalls == 1 {
				return 0, unix.EAGAIN
			}
			target[0] = 'x'
			return 1, nil
		},
		poll: func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
			pollTimeout = timeoutMillis
			assert.Equal(t, []unix.PollFd{{Fd: 0, Events: unix.POLLIN}}, pollFDs)
			pollFDs[0].Revents = unix.POLLIN
			return 1, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := newRequestReader(ctx, 0, time.Now().Add(5*time.Minute), deps)
	var value [1]byte

	read, err := reader.Read(value[:])

	require.NoError(t, err)
	assert.Equal(t, 1, read)
	assert.Equal(t, byte('x'), value[0])
	assert.Positive(t, pollTimeout)
	assert.LessOrEqual(t, pollTimeout, 100)
}

func TestRequestReaderUncancellableWaitMayUseRemainingDeadline(t *testing.T) {
	var pollTimeout int
	receiveCalls := 0
	deps := controlDeps{
		recvmsg: func(context.Context, int, []byte, int) (int, error) {
			receiveCalls++
			if receiveCalls == 1 {
				return 0, unix.EAGAIN
			}
			return 0, nil
		},
		poll: func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
			pollTimeout = timeoutMillis
			pollFDs[0].Revents = unix.POLLHUP
			return 1, nil
		},
	}
	reader := newRequestReader(context.Background(), 0, time.Now().Add(250*time.Millisecond), deps)
	var value [1]byte

	_, err := reader.Read(value[:])

	require.ErrorIs(t, err, io.EOF)
	assert.Greater(t, pollTimeout, 100)
}

func TestRequestReaderCancellationInterruptsBlockedPollCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	deps := controlDeps{
		recvmsg: func(context.Context, int, []byte, int) (int, error) { return 0, unix.EAGAIN },
		poll: func(context.Context, []unix.PollFd, int) (int, error) {
			cancel()
			return 0, nil
		},
	}
	reader := newRequestReader(ctx, 0, time.Now().Add(5*time.Minute), deps)
	var value [1]byte

	_, err := reader.Read(value[:])

	require.ErrorIs(t, err, context.Canceled)
}

func TestRequestReaderClassifiesDeadlineAndPollFailures(t *testing.T) {
	sentinel := errors.New("poll sentinel")
	tests := map[string]struct {
		deadline time.Time
		poll     func(context.Context, []unix.PollFd, int) (int, error)
		want     error
	}{
		"deadline": {
			deadline: time.Now().Add(-time.Millisecond),
			poll:     func(context.Context, []unix.PollFd, int) (int, error) { return 0, nil },
			want:     os.ErrDeadlineExceeded,
		},
		"poll_error": {
			deadline: time.Now().Add(time.Second),
			poll:     func(context.Context, []unix.PollFd, int) (int, error) { return 0, sentinel },
			want:     sentinel,
		},
		"invalid_descriptor": {
			deadline: time.Now().Add(time.Second),
			poll: func(_ context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
				pollFDs[0].Revents = unix.POLLNVAL
				return 1, nil
			},
			want: unix.EBADF,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := controlDeps{
				recvmsg: func(context.Context, int, []byte, int) (int, error) { return 0, unix.EAGAIN },
				poll:    tc.poll,
			}
			reader := newRequestReader(context.Background(), 0, tc.deadline, deps)
			var value [1]byte
			_, err := reader.Read(value[:])
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}
