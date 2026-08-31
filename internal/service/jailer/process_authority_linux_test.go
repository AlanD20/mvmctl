package jailer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestProcessPIDFDAliveRetriesInterruptAndAcceptsOnlyExitReadiness(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		results []processPIDFDPollResult
		want    bool
		wantErr string
	}{
		"live": {results: []processPIDFDPollResult{{count: 0}}, want: true},
		"exit": {results: []processPIDFDPollResult{{count: 1, events: unix.POLLIN}}},
		"interrupt then live": {
			results: []processPIDFDPollResult{{err: unix.EINTR}, {count: 0}}, want: true,
		},
		"poll error": {
			results: []processPIDFDPollResult{{count: 1, events: unix.POLLERR}},
			wantErr: "poll error",
		},
		"invalid fd": {results: []processPIDFDPollResult{{count: 1, events: unix.POLLNVAL}}, wantErr: "invalid"},
		"unexpected hangup": {
			results: []processPIDFDPollResult{{count: 1, events: unix.POLLHUP}},
			wantErr: "unexpected",
		},
		"mixed exit event": {
			results: []processPIDFDPollResult{{count: 1, events: unix.POLLIN | unix.POLLHUP}}, wantErr: "unexpected",
		},
		"count without flags": {results: []processPIDFDPollResult{{count: 1}}, wantErr: "without flags"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			index := 0
			alive, err := processPIDFDAliveWithPoll(t.Context(), 17, func(fds []unix.PollFd, timeout int) (int, error) {
				assert.Equal(t, 0, timeout)
				assert.Equal(t, int32(17), fds[0].Fd)
				assert.Equal(t, int16(unix.POLLIN), fds[0].Events)
				result := test.results[index]
				index++
				fds[0].Revents = result.events
				return result.count, result.err
			})
			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, alive)
		})
	}
}

func TestProcessPIDFDAliveHonorsCancellationBetweenInterruptedPolls(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := processPIDFDAliveWithPoll(ctx, 17, func([]unix.PollFd, int) (int, error) {
		calls++
		cancel()
		return 0, unix.EINTR
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, 1, calls)
}

type processPIDFDPollResult struct {
	count  int
	events int16
	err    error
}
