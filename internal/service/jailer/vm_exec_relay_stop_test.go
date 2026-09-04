package jailer

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type vmExecRelayStopArmCall struct {
	fd    int
	flags int
	value unix.ItimerSpec
}

type fakeVMExecRelayStopEffects struct {
	createFD    int
	createErr   error
	createCalls int
	createClock int
	createFlags int
	armErr      error
	armFn       func(context.Context, int, int, *unix.ItimerSpec) error
	armCalls    []vmExecRelayStopArmCall
	pollFn      func(context.Context, []unix.PollFd, int) (int, error)
	pollCalls   []int
	readFn      func(context.Context, int, []byte) (int, error)
	readCalls   []int
	closeErr    error
	closeCalls  []int
	closeCtxErr []error
	// mu guards syscall observations shared with concurrent Wait and Release calls.
	mu sync.Mutex
}

func (effects *fakeVMExecRelayStopEffects) deps() vmExecRelayStopDeps {
	return vmExecRelayStopDeps{
		create: func(_ context.Context, clockID int, flags int) (int, error) {
			effects.createCalls++
			effects.createClock = clockID
			effects.createFlags = flags
			return effects.createFD, effects.createErr
		},
		arm: func(ctx context.Context, fd int, flags int, value *unix.ItimerSpec) error {
			effects.armCalls = append(effects.armCalls, vmExecRelayStopArmCall{
				fd: fd, flags: flags, value: *value,
			})
			if effects.armFn != nil {
				return effects.armFn(ctx, fd, flags, value)
			}
			return effects.armErr
		},
		poll: func(ctx context.Context, pollFDs []unix.PollFd, timeoutMS int) (int, error) {
			effects.mu.Lock()
			effects.pollCalls = append(effects.pollCalls, timeoutMS)
			effects.mu.Unlock()
			if effects.pollFn != nil {
				return effects.pollFn(ctx, pollFDs, timeoutMS)
			}
			return 0, nil
		},
		read: func(ctx context.Context, fd int, value []byte) (int, error) {
			effects.mu.Lock()
			effects.readCalls = append(effects.readCalls, fd)
			effects.mu.Unlock()
			if effects.readFn != nil {
				return effects.readFn(ctx, fd, value)
			}
			return 0, unix.EAGAIN
		},
		close: func(ctx context.Context, fd int) error {
			effects.mu.Lock()
			defer effects.mu.Unlock()
			effects.closeCalls = append(effects.closeCalls, fd)
			effects.closeCtxErr = append(effects.closeCtxErr, ctx.Err())
			return effects.closeErr
		},
	}
}

func TestVMExecRelayStopTimespecUsesAbsoluteNanoseconds(t *testing.T) {
	tests := map[string]struct {
		deadline uint64
		want     unix.Timespec
		wantErr  bool
	}{
		"zero is disarmed and rejected": {deadline: 0, wantErr: true},
		"seconds and remainder": {
			deadline: 12_345_678_901,
			want:     unix.Timespec{Sec: 12, Nsec: 345_678_901},
		},
		"largest nanosecond value remains representable": {
			deadline: math.MaxUint64,
			want:     unix.Timespec{Sec: 18_446_744_073, Nsec: 709_551_615},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := vmExecRelayStopTimespec(test.deadline)
			if test.wantErr {
				require.Error(t, err)
				require.NotNil(t, errs.AsDomainError(err))
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("absolute timespec mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVMExecRelayStopFactoryArmsAbsoluteBoottimeDeadline(t *testing.T) {
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
	require.NoError(t, err)

	stop, err := factory.arm(context.Background(), 12_345_678_901)
	require.NoError(t, err)
	require.NotNil(t, stop)

	wantArm := []vmExecRelayStopArmCall{{
		fd:    41,
		flags: unix.TFD_TIMER_ABSTIME,
		value: unix.ItimerSpec{Value: unix.Timespec{Sec: 12, Nsec: 345_678_901}},
	}}
	if diff := cmp.Diff(wantArm, effects.armCalls, cmp.AllowUnexported(vmExecRelayStopArmCall{})); diff != "" {
		t.Errorf("timer arming mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, effects.createCalls)
	assert.Equal(t, unix.CLOCK_BOOTTIME, effects.createClock)
	assert.Equal(t, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK, effects.createFlags)
	assert.Empty(t, effects.closeCalls)

	require.NoError(t, stop.Release(context.Background()))
	if diff := cmp.Diff([]int{41}, effects.closeCalls); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecRelayStopRealTimerExpiresAndCloses(t *testing.T) {
	// Rationale: the fake syscall seam proves exact arguments, while this test proves the wired Linux timerfd can be
	// armed at an already elapsed absolute CLOCK_BOOTTIME value, consumed, and closed through the capability.
	factory, err := newVMExecRelayStopFactory()
	require.NoError(t, err)

	stop, err := factory.arm(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, stop)
	t.Cleanup(func() { require.NoError(t, stop.Release(context.Background())) })

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, stop.Wait(waitCtx))
}

func TestVMExecRelayStopFactoryRejectsInvalidConstructionBeforeEffects(t *testing.T) {
	t.Run("incomplete_dependencies", func(t *testing.T) {
		_, err := newVMExecRelayStopFactoryWithDeps(vmExecRelayStopDeps{})
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
		assert.Equal(t, errs.ClassInternal, domainErr.Class)
	})

	tests := map[string]struct {
		ctx      context.Context
		deadline uint64
	}{
		"nil_context":   {ctx: nil, deadline: 1},
		"zero_deadline": {ctx: context.Background(), deadline: 0},
		"cancelled":     {ctx: cancelledVMExecRelayStopContext(), deadline: 1},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeVMExecRelayStopEffects{createFD: 41}
			factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
			require.NoError(t, err)

			stop, err := factory.arm(test.ctx, test.deadline)
			require.Error(t, err)
			assert.Nil(t, stop)
			require.NotNil(t, errs.AsDomainError(err))
			assert.Equal(t, 0, effects.createCalls)
			assert.Empty(t, effects.armCalls)
			assert.Empty(t, effects.closeCalls)
		})
	}

	t.Run("nil factory", func(t *testing.T) {
		var factory *vmExecRelayStopFactory
		stop, err := factory.arm(context.Background(), 1)
		require.Error(t, err)
		assert.Nil(t, stop)
		require.NotNil(t, errs.AsDomainError(err))
	})

	t.Run("invalid created descriptor", func(t *testing.T) {
		effects := &fakeVMExecRelayStopEffects{createFD: -1}
		factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
		require.NoError(t, err)

		stop, err := factory.arm(context.Background(), 1)
		require.Error(t, err)
		assert.Nil(t, stop)
		assert.Equal(t, 1, effects.createCalls)
		assert.Empty(t, effects.armCalls)
		assert.Empty(t, effects.closeCalls)
	})
}

func TestVMExecRelayStopFactoryClassifiesCreateAndArmFailures(t *testing.T) {
	tests := map[string]struct {
		configure func(*fakeVMExecRelayStopEffects)
		wantClose []int
	}{
		"create_fails_closed": {
			configure: func(effects *fakeVMExecRelayStopEffects) { effects.createErr = unix.ENOSYS },
		},
		"absolute_arm_fails_and_closes": {
			configure: func(effects *fakeVMExecRelayStopEffects) { effects.armErr = unix.EINVAL },
			wantClose: []int{41},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeVMExecRelayStopEffects{createFD: 41}
			test.configure(effects)
			factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
			require.NoError(t, err)

			stop, err := factory.arm(context.Background(), math.MaxUint64)
			require.Error(t, err)
			assert.Nil(t, stop)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			if diff := cmp.Diff(test.wantClose, effects.closeCalls); diff != "" {
				t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVMExecRelayStopFactoryPreservesArmErrorAndCleanupEvidence(t *testing.T) {
	armErr := errors.New("arm failed")
	effects := &fakeVMExecRelayStopEffects{
		createFD: 41,
		armErr:   armErr,
		closeErr: errors.New("close failed"),
	}
	factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
	require.NoError(t, err)

	stop, err := factory.arm(context.Background(), 1)
	require.Error(t, err)
	assert.Nil(t, stop)
	assert.ErrorIs(t, err, armErr)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["vm_exec_relay_stop_cleanup_failed"])
	assert.NotEmpty(t, domainErr.Details["vm_exec_relay_stop_cleanup_detail"])
}

func TestVMExecRelayStopFactoryDetachesArmFailureCleanupFromCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.armFn = func(_ context.Context, _ int, _ int, _ *unix.ItimerSpec) error {
		cancel()
		return errors.New("arm failed")
	}
	factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
	require.NoError(t, err)

	stop, err := factory.arm(ctx, 1)
	require.Error(t, err)
	assert.Nil(t, stop)
	if diff := cmp.Diff([]error{nil}, effects.closeCtxErr); diff != "" {
		t.Errorf("cleanup context errors mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecRelayStopWaitConsumesExpirationAfterTransientSyscalls(t *testing.T) {
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	pollAttempt := 0
	effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
		pollAttempt++
		switch pollAttempt {
		case 1:
			return 0, unix.EINTR
		case 2:
			return 0, unix.EAGAIN
		default:
			pollFDs[0].Revents = unix.POLLIN
			return 1, nil
		}
	}
	readAttempt := 0
	effects.readFn = func(_ context.Context, _ int, value []byte) (int, error) {
		readAttempt++
		if readAttempt == 1 {
			return 0, unix.EINTR
		}
		binary.NativeEndian.PutUint64(value, 1)
		return len(value), nil
	}
	stop := armFakeVMExecRelayStop(t, effects)

	require.NoError(t, stop.Wait(context.Background()))
	if diff := cmp.Diff([]int{41, 41}, effects.readCalls); diff != "" {
		t.Errorf("read descriptors mismatch (-want +got):\n%s", diff)
	}
	assert.Len(t, effects.pollCalls, 3)
	for _, timeoutMS := range effects.pollCalls {
		assert.Positive(t, timeoutMS)
		assert.LessOrEqual(t, timeoutMS, vmExecRelayStopPollSliceMilliseconds)
	}
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitRepollsAfterNonblockingReadWouldBlock(t *testing.T) {
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
		pollFDs[0].Revents = unix.POLLIN
		return 1, nil
	}
	readAttempt := 0
	effects.readFn = func(_ context.Context, _ int, value []byte) (int, error) {
		readAttempt++
		if readAttempt == 1 {
			return 0, unix.EAGAIN
		}
		binary.NativeEndian.PutUint64(value, 2)
		return len(value), nil
	}
	stop := armFakeVMExecRelayStop(t, effects)

	require.NoError(t, stop.Wait(context.Background()))
	assert.Len(t, effects.pollCalls, 2)
	if diff := cmp.Diff([]int{41, 41}, effects.readCalls); diff != "" {
		t.Errorf("read descriptors mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitReadyOmitsReadinessForImmediateResult(t *testing.T) {
	tests := map[string]struct {
		pollFn  func([]unix.PollFd) (int, error)
		readFn  func(context.Context, int, []byte) (int, error)
		wantErr error
	}{
		"expiration": {
			pollFn: func(pollFDs []unix.PollFd) (int, error) {
				pollFDs[0].Revents = unix.POLLIN
				return 1, nil
			},
			readFn: successfulVMExecRelayStopRead,
		},
		"poll error": {
			pollFn:  func([]unix.PollFd) (int, error) { return 0, unix.EIO },
			wantErr: unix.EIO,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeVMExecRelayStopEffects{createFD: 41, readFn: tc.readFn}
			effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, timeoutMS int) (int, error) {
				assert.Equal(t, 0, timeoutMS)
				return tc.pollFn(pollFDs)
			}
			stop := armFakeVMExecRelayStop(t, effects)
			readyCalls := 0

			err := stop.waitReady(context.Background(), func() { readyCalls++ })

			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			}
			assert.Equal(t, 0, readyCalls)
			if diff := cmp.Diff([]int{0}, effects.pollCalls); diff != "" {
				t.Errorf("poll timeouts mismatch (-want +got):\n%s", diff)
			}
			require.NoError(t, stop.Release(context.Background()))
		})
	}
}

func TestVMExecRelayStopWaitReadySignalsOnceBeforePendingResult(t *testing.T) {
	blockingPollEntered := make(chan struct{})
	allowExpiration := make(chan struct{})
	effects := &fakeVMExecRelayStopEffects{createFD: 41, readFn: successfulVMExecRelayStopRead}
	effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, timeoutMS int) (int, error) {
		if timeoutMS == 0 {
			return 0, nil
		}
		close(blockingPollEntered)
		<-allowExpiration
		pollFDs[0].Revents = unix.POLLIN
		return 1, nil
	}
	stop := armFakeVMExecRelayStop(t, effects)
	ready := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- stop.waitReady(context.Background(), func() { close(ready) })
	}()

	<-ready
	<-blockingPollEntered
	select {
	case err := <-result:
		t.Fatalf("pending wait returned before expiration: %v", err)
	default:
	}
	close(allowExpiration)
	require.NoError(t, <-result)
	if diff := cmp.Diff([]int{0, vmExecRelayStopPollSliceMilliseconds}, effects.pollCalls); diff != "" {
		t.Errorf("poll timeouts mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitReadyFinalObservationPreservesExpirationAcrossCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pollCall := 0
	effects := &fakeVMExecRelayStopEffects{createFD: 41, readFn: successfulVMExecRelayStopRead}
	effects.pollFn = func(ctx context.Context, pollFDs []unix.PollFd, timeoutMS int) (int, error) {
		pollCall++
		assert.Equal(t, 0, timeoutMS)
		if pollCall == 1 {
			return 0, nil
		}
		assert.NoError(t, ctx.Err())
		pollFDs[0].Revents = unix.POLLIN
		return 1, nil
	}
	stop := armFakeVMExecRelayStop(t, effects)
	readyCalls := 0

	err := stop.waitReady(ctx, func() {
		readyCalls++
		cancel()
	})

	require.NoError(t, err)
	assert.Equal(t, 1, readyCalls)
	if diff := cmp.Diff([]int{0, 0}, effects.pollCalls); diff != "" {
		t.Errorf("poll timeouts mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitObservesContextCancellationBetweenBoundedPolls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(_ context.Context, _ []unix.PollFd, _ int) (int, error) {
		cancel()
		return 0, nil
	}
	stop := armFakeVMExecRelayStop(t, effects)

	err := stop.Wait(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.ClassRetryable, domainErr.Class)
	assert.Len(t, effects.pollCalls, 1)
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitRejectsInvalidLifecycleWithoutConsumingTimer(t *testing.T) {
	tests := map[string]*vmExecRelayStop{
		"nil capability": nil,
		"missing state":  {},
	}
	for name, stop := range tests {
		t.Run(name, func(t *testing.T) {
			err := stop.Wait(context.Background())
			require.Error(t, err)
			require.NotNil(t, errs.AsDomainError(err))
		})
	}

	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
		pollFDs[0].Revents = unix.POLLIN
		return 1, nil
	}
	effects.readFn = successfulVMExecRelayStopRead
	stop := armFakeVMExecRelayStop(t, effects)

	err := stop.Wait(nil)
	require.Error(t, err)
	assert.Empty(t, effects.pollCalls)

	cancelled := cancelledVMExecRelayStopContext()
	err = stop.Wait(cancelled)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, effects.pollCalls)

	// Rejected contexts do not claim the one waiter or consume the timer.
	require.NoError(t, stop.Wait(context.Background()))
	assert.Len(t, effects.pollCalls, 1)
	require.NoError(t, stop.Release(context.Background()))

	err = stop.Wait(context.Background())
	require.Error(t, err)
	require.NotNil(t, errs.AsDomainError(err))
}

func TestVMExecRelayStopWaitAllowsOnlyOneConcurrentWaiter(t *testing.T) {
	// Rationale: copied capabilities share one timerfd. A second reader could consume the only expiration and strand
	// the retained-sidecar waiter, so concurrent admission must fail before another poll or read begins.
	pollEntered := make(chan struct{})
	allowExpiration := make(chan struct{})
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(ctx context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
		close(pollEntered)
		select {
		case <-allowExpiration:
			pollFDs[0].Revents = unix.POLLIN
			return 1, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	effects.readFn = successfulVMExecRelayStopRead
	stop := armFakeVMExecRelayStop(t, effects)

	firstResult := make(chan error, 1)
	go func() { firstResult <- stop.Wait(context.Background()) }()
	select {
	case <-pollEntered:
	case <-time.After(time.Second):
		require.FailNow(t, "first waiter did not reach poll")
	}

	secondErr := stop.Wait(context.Background())
	require.Error(t, secondErr)
	require.NotNil(t, errs.AsDomainError(secondErr))
	close(allowExpiration)
	select {
	case err := <-firstResult:
		require.NoError(t, err)
	case <-time.After(time.Second):
		require.FailNow(t, "first waiter did not finish")
	}

	assert.Len(t, effects.pollCalls, 1)
	assert.Len(t, effects.readCalls, 1)
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitCachesCompletedExpiration(t *testing.T) {
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
		pollFDs[0].Revents = unix.POLLIN
		return 1, nil
	}
	effects.readFn = successfulVMExecRelayStopRead
	stop := armFakeVMExecRelayStop(t, effects)

	require.NoError(t, stop.Wait(context.Background()))
	require.NoError(t, stop.Wait(context.Background()))
	assert.Len(t, effects.pollCalls, 1)
	assert.Len(t, effects.readCalls, 1)
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitRejectsInvalidPollOutcomes(t *testing.T) {
	tests := map[string]struct {
		pollFn    func([]unix.PollFd) (int, error)
		wantCause error
	}{
		"poll failure": {
			pollFn:    func(_ []unix.PollFd) (int, error) { return 0, unix.EIO },
			wantCause: unix.EIO,
		},
		"flags without event count": {
			pollFn: func(fds []unix.PollFd) (int, error) {
				fds[0].Revents = unix.POLLIN
				return 0, nil
			},
		},
		"unexpected event count": {
			pollFn: func(_ []unix.PollFd) (int, error) { return 2, nil },
		},
		"event count without flags": {
			pollFn: func(_ []unix.PollFd) (int, error) { return 1, nil },
		},
		"closed descriptor": {
			pollFn: func(fds []unix.PollFd) (int, error) {
				fds[0].Revents = unix.POLLNVAL
				return 1, nil
			},
		},
		"poll error flag": {
			pollFn: func(fds []unix.PollFd) (int, error) {
				fds[0].Revents = unix.POLLERR
				return 1, nil
			},
		},
		"unexpected poll flag": {
			pollFn: func(fds []unix.PollFd) (int, error) {
				fds[0].Revents = unix.POLLHUP
				return 1, nil
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeVMExecRelayStopEffects{createFD: 41}
			effects.pollFn = func(_ context.Context, fds []unix.PollFd, _ int) (int, error) {
				return test.pollFn(fds)
			}
			stop := armFakeVMExecRelayStop(t, effects)

			err := stop.Wait(context.Background())
			require.Error(t, err)
			require.NotNil(t, errs.AsDomainError(err))
			if test.wantCause != nil {
				assert.ErrorIs(t, err, test.wantCause)
			}
			require.NoError(t, stop.Release(context.Background()))
		})
	}
}

func TestVMExecRelayStopWaitBoundsTransientPollFailures(t *testing.T) {
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(_ context.Context, _ []unix.PollFd, _ int) (int, error) {
		return 0, unix.EINTR
	}
	stop := armFakeVMExecRelayStop(t, effects)

	err := stop.Wait(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EINTR)
	assert.Len(t, effects.pollCalls, vmExecRelayStopMaxTransientRetries+1)
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopWaitRejectsInvalidExpirationReads(t *testing.T) {
	tests := map[string]struct {
		readFn    func(context.Context, int, []byte) (int, error)
		wantCause error
	}{
		"read failure": {
			readFn:    func(_ context.Context, _ int, _ []byte) (int, error) { return 0, unix.EIO },
			wantCause: unix.EIO,
		},
		"expiration EOF": {
			readFn: func(_ context.Context, _ int, _ []byte) (int, error) { return 0, nil },
		},
		"partial expiration count": {
			readFn: func(_ context.Context, _ int, value []byte) (int, error) {
				binary.NativeEndian.PutUint32(value, 1)
				return 4, nil
			},
		},
		"zero expirations": {
			readFn: func(_ context.Context, _ int, value []byte) (int, error) {
				binary.NativeEndian.PutUint64(value, 0)
				return len(value), nil
			},
		},
		"transient retry bound": {
			readFn:    func(_ context.Context, _ int, _ []byte) (int, error) { return 0, unix.EINTR },
			wantCause: unix.EINTR,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			effects := &fakeVMExecRelayStopEffects{createFD: 41, readFn: test.readFn}
			effects.pollFn = func(_ context.Context, fds []unix.PollFd, _ int) (int, error) {
				fds[0].Revents = unix.POLLIN
				return 1, nil
			}
			stop := armFakeVMExecRelayStop(t, effects)

			err := stop.Wait(context.Background())
			require.Error(t, err)
			require.NotNil(t, errs.AsDomainError(err))
			if test.wantCause != nil {
				assert.ErrorIs(t, err, test.wantCause)
			}
			require.NoError(t, stop.Release(context.Background()))
		})
	}
}

func TestVMExecRelayStopWaitObservesCancellationDuringExpirationRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	effects.pollFn = func(_ context.Context, pollFDs []unix.PollFd, _ int) (int, error) {
		pollFDs[0].Revents = unix.POLLIN
		return 1, nil
	}
	effects.readFn = func(_ context.Context, _ int, _ []byte) (int, error) {
		cancel()
		return 0, unix.EINTR
	}
	stop := armFakeVMExecRelayStop(t, effects)

	err := stop.Wait(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.ClassRetryable, domainErr.Class)
	require.NoError(t, stop.Release(context.Background()))
}

func TestVMExecRelayStopReleaseIsDetachedAndCloseOnceAcrossCopies(t *testing.T) {
	// Rationale: cleanup runs after cancellation and copied capabilities must never retry close because the descriptor
	// number may have been reused after the first ambiguous close result.
	closeErr := errors.New("close failed")
	effects := &fakeVMExecRelayStopEffects{createFD: 41, closeErr: closeErr}
	stop := armFakeVMExecRelayStop(t, effects)
	stopCopy := *stop
	cancelled := cancelledVMExecRelayStopContext()

	firstErr := stop.Release(cancelled)
	require.Error(t, firstErr)
	assert.ErrorIs(t, firstErr, closeErr)
	secondErr := stopCopy.Release(context.Background())
	require.Error(t, secondErr)
	assert.ErrorIs(t, secondErr, closeErr)
	if diff := cmp.Diff([]int{41}, effects.closeCalls); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]error{nil}, effects.closeCtxErr); diff != "" {
		t.Errorf("cleanup context errors mismatch (-want +got):\n%s", diff)
	}
	domainErr := errs.AsDomainError(firstErr)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["vm_exec_relay_stop_cleanup_failed"])
	assert.NotEmpty(t, domainErr.Details["vm_exec_relay_stop_cleanup_detail"])
}

func TestVMExecRelayStopReleaseWithNilContextStillCloses(t *testing.T) {
	effects := &fakeVMExecRelayStopEffects{createFD: 41}
	stop := armFakeVMExecRelayStop(t, effects)

	err := stop.Release(nil)
	require.Error(t, err)
	require.NotNil(t, errs.AsDomainError(err))
	if diff := cmp.Diff([]int{41}, effects.closeCalls); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]error{nil}, effects.closeCtxErr); diff != "" {
		t.Errorf("cleanup context errors mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecRelayStopNilReleaseIsNoop(t *testing.T) {
	var nilStop *vmExecRelayStop
	require.NoError(t, nilStop.Release(context.Background()))
	require.NoError(t, (&vmExecRelayStop{}).Release(context.Background()))
}

func successfulVMExecRelayStopRead(_ context.Context, _ int, value []byte) (int, error) {
	binary.NativeEndian.PutUint64(value, 1)
	return len(value), nil
}

func armFakeVMExecRelayStop(t *testing.T, effects *fakeVMExecRelayStopEffects) *vmExecRelayStop {
	t.Helper()
	factory, err := newVMExecRelayStopFactoryWithDeps(effects.deps())
	require.NoError(t, err)
	stop, err := factory.arm(context.Background(), 1)
	require.NoError(t, err)
	return stop
}

func cancelledVMExecRelayStopContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
