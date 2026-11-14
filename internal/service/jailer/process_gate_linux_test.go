package jailer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestBlockedLaunchGateReadIsCancellationAndDeadlineAware(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		context func() (context.Context, context.CancelFunc)
		trigger func(context.CancelFunc)
		want    error
	}{
		"cancelled stalled peer": {
			context: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(t.Context())
			},
			trigger: func(cancel context.CancelFunc) { cancel() },
			want:    context.Canceled,
		},
		"deadline stalled peer": {
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(t.Context(), 25*time.Millisecond)
			},
			trigger: func(context.CancelFunc) {},
			want:    context.DeadlineExceeded,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			local, peer := newBlockedLaunchGateSocketPair(t)
			ctx, cancel := test.context()
			defer cancel()
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(started)
				result <- readBlockedLaunchGateFull(ctx, local, make([]byte, 1))
			}()
			<-started
			test.trigger(cancel)
			err := <-result
			assert.ErrorIs(t, err, test.want)
			require.NoError(t, unix.Close(peer))
			require.NoError(t, unix.Close(local))
		})
	}
}

func TestBlockedLaunchGateReadTreatsPeerEOFAsClosedGate(t *testing.T) {
	t.Parallel()

	local, peer := newBlockedLaunchGateSocketPair(t)
	require.NoError(t, unix.Close(peer))
	err := readBlockedLaunchGateFull(t.Context(), local, make([]byte, 1))
	assert.ErrorIs(t, err, unix.EPIPE)
	require.NoError(t, unix.Close(local))
}

func TestBlockedLaunchGateWriteHonorsDeadlineWhenPeerStopsReading(t *testing.T) {
	t.Parallel()

	local, peer := newBlockedLaunchGateSocketPair(t)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	err := writeBlockedLaunchGateFull(ctx, local, make([]byte, 8*1024*1024))
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "unexpected error: %v", err)
	require.NoError(t, unix.Close(peer))
	require.NoError(t, unix.Close(local))
}

func newBlockedLaunchGateSocketPair(t *testing.T) (int, int) {
	t.Helper()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	return fds[0], fds[1]
}
