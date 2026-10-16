package privileged

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const (
	testVMExecHandlerSource      = "0123456789abcdef0123456789abcdef"
	testVMExecHandlerDestination = "fedcba9876543210fedcba9876543210"
	testVMExecHandlerSyncTimeout = time.Second
)

type fakeVMExecSessionEndpoint struct {
	mu            sync.Mutex
	response      bytes.Buffer
	deadlines     []time.Time
	writeErr      error
	writeLimit    int
	deadlineErr   error
	deadlineErrAt int
	deadlineFunc  func(time.Time) error
	writeCalls    int
	shutdownCalls int
	shutdownErr   error
	phaseEvents   *[]string
}

type fakeVMExecSessionLease struct {
	events                *[]string
	bindErr               error
	releaseErr            error
	waitStopFunc          func(context.Context, vmExecSessionStopWaitReady) error
	releaseFunc           func(context.Context) error
	sourceConnection      vmExecSessionConnection
	destinationConnection vmExecSessionConnection
	bound                 bool
	released              bool
	releaseCtx            context.Context
}

func TestHandleVMExecSessionExplicitReleaseWinsPendingExpiryAndUsesReverseCleanup(t *testing.T) {
	events := make([]string, 0, 8)
	waitStarted := make(chan struct{})
	waitFinished := make(chan struct{})
	pendingExpiry := make(chan struct{})
	lease := &fakeVMExecSessionLease{
		events: &events,
		waitStopFunc: func(ctx context.Context, ready vmExecSessionStopWaitReady) error {
			close(waitStarted)
			defer close(waitFinished)
			ready()
			select {
			case <-pendingExpiry:
				return nil
			case <-ctx.Done():
				return errs.WrapMsg(
					errs.CodeVMAtomicFailed,
					"wait for VM-exec relay stop",
					ctx.Err(),
					errs.WithClass(errs.ClassRetryable),
				)
			}
		},
		releaseFunc: func(context.Context) error {
			select {
			case <-waitFinished:
				return nil
			default:
				return errors.New("stop waiter was not joined before lease release")
			}
		},
	}
	endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
		t,
		vmExecSessionFrames(t, true, true, true),
		&events,
	)
	caller := callerIdentity{uid: 1000, gid: 1001}
	var gotRequest vmexecsessionwire.OpenSource
	var gotCaller callerIdentity
	var sourceConnection vmExecSessionConnection
	phaseEvents := make([]string, 0, 6)
	endpoint.phaseEvents = &phaseEvents
	effects := vmExecSessionHandlerEffects{
		openSource: func(
			_ context.Context,
			actualCaller callerIdentity,
			request vmexecsessionwire.OpenSource,
			connection vmExecSessionConnection,
		) (vmExecSessionHandlerLease, error) {
			events = append(events, "open_source")
			gotCaller = actualCaller
			gotRequest = request
			sourceConnection = connection
			return lease, nil
		},
	}

	result := make(chan error, 1)
	go func() {
		result <- handleVMExecSession(t.Context(), endpoint, caller, effects, deps)
	}()
	err := requireVMExecSessionHandlerResult(t, result)

	require.NoError(t, err)
	assertVMExecSessionChannelClosed(t, waitStarted)
	assertVMExecSessionChannelClosed(t, waitFinished)
	if diff := cmp.Diff(caller, gotCaller, cmp.AllowUnexported(callerIdentity{})); diff != "" {
		t.Errorf("caller identity mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, testVMExecHandlerSource, gotRequest.SourceVMID)
	assert.Equal(t, testVMExecHandlerDestination, gotRequest.DestinationVMID)
	require.NotNil(t, sourceConnection.retained)
	require.NotNil(t, sourceConnection.retained.owner)
	require.NotNil(t, lease.destinationConnection.retained)
	require.NotNil(t, lease.destinationConnection.retained.owner)
	assert.Equal(t, sourceFD, sourceConnection.retained.owner.fd)
	assert.Equal(t, destinationFD, lease.destinationConnection.retained.owner.fd)
	assert.NotSame(t, sourceConnection.retained.owner, lease.destinationConnection.retained.owner)
	assert.True(t, sourceConnection.retained.owner.closed)
	assert.True(t, lease.destinationConnection.retained.owner.closed)
	assertVMExecSessionEvents(t, []string{
		"read_open_source",
		"write_source_retained",
		"read_bind_destination",
		"write_ready",
		"read_release",
		"write_released",
	}, phaseEvents)
	assertVMExecSessionEvents(t, []string{
		"open_source",
		"bind_destination",
		"shutdown_destination",
		"close_destination",
		"shutdown_source",
		"close_source",
		"release_lease",
	}, events)
	assert.Equal(t, 1, endpoint.shutdownCount())
	assert.True(t, lease.bound)
	assert.True(t, lease.released)
	assert.NoError(t, lease.releaseCtx.Err())
	assertVMExecSessionResponses(t, endpoint.responseBytes(), true, true, true)
	assertFDClosed(t, sourceFD)
	assertFDClosed(t, destinationFD)
}

func TestHandleVMExecSessionArmedExpiryObservedAfterFinishBeatsRelease(t *testing.T) {
	events := make([]string, 0, 8)
	phaseEvents := make([]string, 0, 6)
	waitStarted := make(chan struct{})
	waitFinished := make(chan struct{})
	lease := &fakeVMExecSessionLease{
		events: &events,
		waitStopFunc: func(ctx context.Context, ready vmExecSessionStopWaitReady) error {
			close(waitStarted)
			defer close(waitFinished)
			ready()
			<-ctx.Done()
			return nil
		},
		releaseFunc: func(context.Context) error {
			select {
			case <-waitFinished:
				return nil
			default:
				return errors.New("stop waiter was not joined before lease release")
			}
		},
	}
	endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
		t,
		vmExecSessionFrames(t, true, true, true),
		&events,
	)
	endpoint.phaseEvents = &phaseEvents
	result := make(chan error, 1)
	go func() {
		result <- handleVMExecSession(
			t.Context(),
			endpoint,
			callerIdentity{uid: 1000, gid: 1000},
			vmExecSessionEffectsForLease(lease),
			deps,
		)
	}()

	require.NoError(t, requireVMExecSessionHandlerResult(t, result))

	assertVMExecSessionChannelClosed(t, waitStarted)
	assertVMExecSessionChannelClosed(t, waitFinished)
	responses := bytes.NewReader(endpoint.responseBytes())
	retained, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
	require.NoError(t, err)
	assert.True(t, retained.Retained())
	ready, err := vmexecsessionwire.DecodeReady(t.Context(), responses)
	require.NoError(t, err)
	assert.True(t, ready.Ready())
	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
	require.NoError(t, err)
	assert.False(t, outcome.Released())
	operationErr := outcome.OperationError()
	require.NotNil(t, operationErr)
	assert.Equal(t, errs.CodeProcessError, operationErr.Code)
	assert.Equal(t, errs.ClassInternal, operationErr.Class)
	assert.Zero(t, responses.Len())
	assertVMExecSessionEvents(t, []string{
		"read_open_source",
		"write_source_retained",
		"read_bind_destination",
		"write_ready",
		"read_release",
		"write_failure",
	}, phaseEvents)
	assertVMExecSessionEvents(t, []string{
		"open_source",
		"bind_destination",
		"shutdown_destination",
		"close_destination",
		"shutdown_source",
		"close_source",
		"release_lease",
	}, events)
	assert.Equal(t, 1, endpoint.shutdownCount())
	assert.True(t, lease.bound)
	assert.True(t, lease.released)
	assertFDClosed(t, sourceFD)
	assertFDClosed(t, destinationFD)
}

func TestHandleVMExecSessionImmediateExpiryWinsBeforeBufferedPhases(t *testing.T) {
	events := make([]string, 0, 6)
	phaseEvents := make([]string, 0, 2)
	waitCalled := make(chan struct{})
	waitFinished := make(chan struct{})
	lease := &fakeVMExecSessionLease{
		events: &events,
		waitStopFunc: func(context.Context, vmExecSessionStopWaitReady) error {
			close(waitCalled)
			close(waitFinished)
			return nil
		},
		releaseFunc: func(context.Context) error {
			select {
			case <-waitFinished:
				return nil
			default:
				return errors.New("immediate stop was not observed before lease release")
			}
		},
	}
	endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
		t,
		vmExecSessionFrames(t, true, true, true),
		&events,
	)
	endpoint.phaseEvents = &phaseEvents
	result := make(chan error, 1)
	go func() {
		result <- handleVMExecSession(
			t.Context(),
			endpoint,
			callerIdentity{uid: 1000, gid: 1000},
			vmExecSessionEffectsForLease(lease),
			deps,
		)
	}()

	require.NoError(t, requireVMExecSessionHandlerResult(t, result))

	assertVMExecSessionChannelClosed(t, waitCalled)
	assertVMExecSessionChannelClosed(t, waitFinished)
	assertVMExecSessionFailureOnly(t, endpoint.responseBytes())
	assertVMExecSessionEvents(t, []string{"read_open_source", "write_failure"}, phaseEvents)
	assertVMExecSessionEvents(t, []string{
		"open_source", "shutdown_source", "close_source", "release_lease",
	}, events)
	assert.Equal(t, 1, endpoint.shutdownCount())
	assert.False(t, lease.bound)
	assert.True(t, lease.released)
	assertFDClosed(t, sourceFD)
	assertFDOpen(t, destinationFD)
	require.NoError(t, unix.Close(destinationFD))
}

func TestHandleVMExecSessionGatesIntermediateResponsesOnEffects(t *testing.T) {
	t.Run("open source failure sends only terminal failure", func(t *testing.T) {
		events := make([]string, 0, 4)
		endpoint, deps, sourceFD, _ := newVMExecSessionHandlerFixture(
			t,
			vmExecSessionFrames(t, true, false, false),
			&events,
		)
		effects := vmExecSessionHandlerEffects{openSource: func(
			context.Context,
			callerIdentity,
			vmexecsessionwire.OpenSource,
			vmExecSessionConnection,
		) (vmExecSessionHandlerLease, error) {
			return nil, errs.New(errs.CodeProcessError, "source retention failed")
		}}

		err := handleVMExecSession(t.Context(), endpoint, callerIdentity{uid: 1000, gid: 1000}, effects, deps)

		require.NoError(t, err)
		assertVMExecSessionFailureOnly(t, endpoint.responseBytes())
		assertVMExecSessionEvents(t, []string{"shutdown_source", "close_source"}, events)
		assert.Equal(t, 1, endpoint.shutdownCount())
		assertFDClosed(t, sourceFD)
	})

	t.Run("destination bind failure never sends ready", func(t *testing.T) {
		events := make([]string, 0, 8)
		primary := errs.New(
			errs.CodeVMAtomicFailed,
			"destination retention failed",
			errs.WithClass(errs.ClassConflict),
			errs.WithEntity(testVMExecHandlerSource),
			errs.WithDetails(map[string]any{"retention_started": true, "retention_attempt": int64(2)}),
		)
		require.NotEmpty(t, primary.Op)
		lease := &fakeVMExecSessionLease{
			events:     &events,
			bindErr:    primary,
			releaseErr: errors.New("lease cleanup failed"),
		}
		endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
			t,
			vmExecSessionFrames(t, true, true, false),
			&events,
		)
		effects := vmExecSessionEffectsForLease(lease)

		err := handleVMExecSession(t.Context(), endpoint, callerIdentity{uid: 1000, gid: 1000}, effects, deps)

		require.NoError(t, err)
		responses := bytes.NewReader(endpoint.responseBytes())
		retained, decodeErr := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
		require.NoError(t, decodeErr)
		assert.True(t, retained.Retained())
		outcome, decodeErr := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
		require.NoError(t, decodeErr)
		assert.False(t, outcome.Released())
		operationErr := outcome.OperationError()
		require.NotNil(t, operationErr)
		wantOperationErr := &errs.DomainError{
			Code:    errs.CodeVMAtomicFailed,
			Message: "retained VM-exec session transition failed",
			Op:      primary.Op,
			Entity:  testVMExecHandlerSource,
			Class:   errs.ClassConflict,
			Details: map[string]any{
				"retention_started": true,
				"retention_attempt": int64(2),
				"cleanup_failed":    true,
			},
		}
		if diff := cmp.Diff(wantOperationErr, operationErr); diff != "" {
			t.Errorf("projected operation error mismatch (-want +got):\n%s", diff)
		}
		assert.NotContains(t, string(endpoint.responseBytes()), "ready")
		assert.Equal(t, 1, endpoint.shutdownCount())
		assertVMExecSessionEvents(t, []string{
			"open_source",
			"bind_destination",
			"shutdown_destination",
			"close_destination",
			"shutdown_source",
			"close_source",
			"release_lease",
		}, events)
		assertFDClosed(t, sourceFD)
		assertFDClosed(t, destinationFD)
		require.NotNil(t, lease.releaseCtx)
		assert.NoError(t, lease.releaseCtx.Err())
	})
}

func TestHandleVMExecSessionRejectsOutOfOrderPhaseBeforeEffects(t *testing.T) {
	var wrong bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &wrong))
	events := make([]string, 0, 2)
	endpoint, deps, sourceFD, _ := newVMExecSessionHandlerFixture(
		t,
		[]vmExecSessionInputFrame{{frame: wrong.Bytes(), descriptor: true}},
		&events,
	)
	called := false
	effects := vmExecSessionHandlerEffects{openSource: func(
		context.Context,
		callerIdentity,
		vmexecsessionwire.OpenSource,
		vmExecSessionConnection,
	) (vmExecSessionHandlerLease, error) {
		called = true
		return nil, nil
	}}

	err := handleVMExecSession(t.Context(), endpoint, callerIdentity{uid: 1000, gid: 1000}, effects, deps)

	require.NoError(t, err)
	assert.False(t, called)
	assertVMExecSessionFailureOnly(t, endpoint.responseBytes())
	assertVMExecSessionEvents(t, []string{"close_source"}, events)
	assertFDClosed(t, sourceFD)
}

func TestHandleVMExecSessionControlEOFCleansWithoutTerminalClaim(t *testing.T) {
	tests := map[string]struct {
		frames        []vmExecSessionInputFrame
		wantPhases    []string
		wantCleanup   []string
		destinationFD bool
		cleanupFails  bool
	}{
		"after_source_retained": {
			frames: vmExecSessionFrames(t, true, false, false),
			wantPhases: []string{
				"read_open_source", "write_source_retained",
			},
			wantCleanup: []string{
				"open_source", "shutdown_source", "close_source", "release_lease",
			},
			cleanupFails: true,
		},
		"after_ready": {
			frames: vmExecSessionFrames(t, true, true, false),
			wantPhases: []string{
				"read_open_source", "write_source_retained", "read_bind_destination", "write_ready",
			},
			wantCleanup: []string{
				"open_source", "bind_destination", "shutdown_destination", "close_destination",
				"shutdown_source", "close_source", "release_lease",
			},
			destinationFD: true,
			cleanupFails:  true,
		},
		"after_ready_clean_cleanup": {
			frames: vmExecSessionFrames(t, true, true, false),
			wantPhases: []string{
				"read_open_source", "write_source_retained", "read_bind_destination", "write_ready",
			},
			wantCleanup: []string{
				"open_source", "bind_destination", "shutdown_destination", "close_destination",
				"shutdown_source", "close_source", "release_lease",
			},
			destinationFD: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			events := make([]string, 0, 8)
			phaseEvents := make([]string, 0, 4)
			lease := &fakeVMExecSessionLease{events: &events}
			if test.cleanupFails {
				lease.releaseErr = errors.New("EOF cleanup failed")
			}
			endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(t, test.frames, &events)
			endpoint.phaseEvents = &phaseEvents

			err := handleVMExecSession(
				t.Context(),
				endpoint,
				callerIdentity{uid: 1000, gid: 1000},
				vmExecSessionEffectsForLease(lease),
				deps,
			)

			if test.cleanupFails {
				domainErr := requireVMExecDomainError(t, err)
				assert.Equal(t, errs.CodeProcessError, domainErr.Code)
				assert.Equal(t, true, domainErr.Details["cleanup_failed"])
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, 0, endpoint.shutdownCount())
			assertVMExecSessionEvents(t, test.wantPhases, phaseEvents)
			assertVMExecSessionEvents(t, test.wantCleanup, events)
			responses := bytes.NewReader(endpoint.responseBytes())
			sourceResult, decodeErr := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
			require.NoError(t, decodeErr)
			assert.True(t, sourceResult.Retained())
			if test.destinationFD {
				readyResult, readyErr := vmexecsessionwire.DecodeReady(t.Context(), responses)
				require.NoError(t, readyErr)
				assert.True(t, readyResult.Ready())
			}
			assert.Zero(t, responses.Len())
			assert.True(t, lease.released)
			assertFDClosed(t, sourceFD)
			if test.destinationFD {
				assertFDClosed(t, destinationFD)
			} else {
				assertFDOpen(t, destinationFD)
				require.NoError(t, unix.Close(destinationFD))
			}
		})
	}
}

func TestHandleVMExecSessionCleanupFailureProducesFailureAndContinues(t *testing.T) {
	events := make([]string, 0, 8)
	lease := &fakeVMExecSessionLease{events: &events, releaseErr: errors.New("release failed")}
	endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
		t,
		vmExecSessionFrames(t, true, true, true),
		&events,
	)
	deps.connectionReader.shutdownFD = func(ctx context.Context, fd int, how int) error {
		appendVMExecSessionEvent(&events, vmExecSessionFDName(fd, sourceFD, destinationFD, "shutdown"))
		assert.NoError(t, ctx.Err())
		assert.Equal(t, unix.SHUT_RDWR, how)
		return errors.New("shutdown failed")
	}

	err := handleVMExecSession(
		t.Context(),
		endpoint,
		callerIdentity{uid: 1000, gid: 1000},
		vmExecSessionEffectsForLease(lease),
		deps,
	)

	require.NoError(t, err)
	assertVMExecSessionEvents(t, []string{
		"open_source",
		"bind_destination",
		"shutdown_destination",
		"close_destination",
		"shutdown_source",
		"close_source",
		"release_lease",
	}, events)
	responses := bytes.NewReader(endpoint.responseBytes())
	retained, decodeErr := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.True(t, retained.Retained())
	ready, decodeErr := vmexecsessionwire.DecodeReady(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.True(t, ready.Ready())
	outcome, decodeErr := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.False(t, outcome.Released())
	require.NotNil(t, outcome.OperationError())
	assert.Equal(t, true, outcome.OperationError().Details["cleanup_failed"])
}

func TestHandleVMExecSessionResponseTransportFailureDoesNotStartSecondFrame(t *testing.T) {
	tests := map[string]struct {
		mutate        func(*fakeVMExecSessionEndpoint)
		wantDeadlines int
		wantWrites    int
		wantShutdowns int
		wantFrames    int
		cleanupFails  bool
	}{
		"deadline": {
			mutate: func(endpoint *fakeVMExecSessionEndpoint) {
				endpoint.deadlineErr = io.ErrClosedPipe
			},
			wantDeadlines: 1,
			cleanupFails:  true,
		},
		"ready deadline": {
			mutate: func(endpoint *fakeVMExecSessionEndpoint) {
				endpoint.deadlineErr = io.ErrClosedPipe
				endpoint.deadlineErrAt = 2
			},
			wantDeadlines: 2,
			wantWrites:    2,
			wantFrames:    1,
			cleanupFails:  true,
		},
		"partial write": {
			mutate: func(endpoint *fakeVMExecSessionEndpoint) {
				endpoint.writeLimit = 5
				endpoint.writeErr = io.ErrClosedPipe
			},
			wantDeadlines: 1,
			wantWrites:    1,
		},
		"terminal shutdown": {
			mutate: func(endpoint *fakeVMExecSessionEndpoint) {
				endpoint.shutdownErr = io.ErrClosedPipe
			},
			wantDeadlines: 3,
			wantWrites:    6,
			wantShutdowns: 1,
			wantFrames:    3,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			events := make([]string, 0, 8)
			lease := &fakeVMExecSessionLease{events: &events}
			if test.cleanupFails {
				lease.releaseErr = errors.New("transport cleanup failed")
			}
			endpoint, deps, _, _ := newVMExecSessionHandlerFixture(
				t,
				vmExecSessionFrames(t, true, true, true),
				&events,
			)
			test.mutate(endpoint)

			err := handleVMExecSession(
				t.Context(),
				endpoint,
				callerIdentity{uid: 1000, gid: 1000},
				vmExecSessionEffectsForLease(lease),
				deps,
			)

			domainErr := requireVMExecDomainError(t, err)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			if test.cleanupFails {
				assert.Equal(t, true, domainErr.Details["cleanup_failed"])
			}
			assert.Equal(t, test.wantDeadlines, endpoint.deadlineCount())
			assert.Equal(t, test.wantWrites, endpoint.writeCount())
			assert.Equal(t, test.wantShutdowns, endpoint.shutdownCount())
			assert.Equal(t, test.wantFrames, bytes.Count(endpoint.responseBytes(), []byte("MVMXSES1")))
		})
	}
}

func TestHandleVMExecSessionClassifiesParentCancellationAndUsesDetachedCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make([]string, 0, 8)
	phaseEvents := make([]string, 0, 6)
	lease := &fakeVMExecSessionLease{events: &events}
	endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
		t,
		vmExecSessionFrames(t, true, true, false),
		&events,
	)
	endpoint.phaseEvents = &phaseEvents
	effects := vmExecSessionEffectsForLease(lease)
	recvmsg := deps.connectionReader.recvmsg
	deps.connectionReader.recvmsg = func(
		readCtx context.Context,
		fd int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		count, controlCount, receiveFlags, from, err := recvmsg(readCtx, fd, payload, control, flags)
		if count == 0 && controlCount == 0 && receiveFlags == 0 && from == nil && err == nil {
			cancel()
			return 0, 0, 0, nil, unix.EAGAIN
		}
		return count, controlCount, receiveFlags, from, err
	}

	err := handleVMExecSession(ctx, endpoint, callerIdentity{uid: 1000, gid: 1000}, effects, deps)

	require.NoError(t, err)
	responses := bytes.NewReader(endpoint.responseBytes())
	sourceResult, decodeErr := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.True(t, sourceResult.Retained())
	readyResult, decodeErr := vmexecsessionwire.DecodeReady(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.True(t, readyResult.Ready())
	outcome, decodeErr := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.False(t, outcome.Released())
	operationErr := outcome.OperationError()
	require.NotNil(t, operationErr)
	assert.Equal(t, errs.CodeProcessError, operationErr.Code)
	assert.Equal(t, errs.ClassInternal, operationErr.Class)
	assertVMExecSessionEvents(t, []string{
		"read_open_source",
		"write_source_retained",
		"read_bind_destination",
		"write_ready",
		"write_failure",
	}, phaseEvents)
	assert.Equal(t, 1, endpoint.shutdownCount())
	assert.True(t, lease.released)
	require.NotNil(t, lease.releaseCtx)
	assert.NoError(t, lease.releaseCtx.Err())
	assertFDClosed(t, sourceFD)
	assertFDClosed(t, destinationFD)
}

func TestHandleVMExecSessionStopWaiterFailsClosedBeforeRelease(t *testing.T) {
	stopFault := errs.New(
		errs.CodeVMAtomicFailed,
		"wait for retained VM-exec relay stop",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(testVMExecHandlerSource),
	)
	tests := map[string]struct {
		frames      []vmExecSessionInputFrame
		stopErr     error
		wantReady   bool
		wantCode    errs.Code
		wantClass   errs.Class
		wantCleanup []string
	}{
		"deadline while waiting for destination": {
			frames:    vmExecSessionFrames(t, true, false, false),
			wantCode:  errs.CodeProcessError,
			wantClass: errs.ClassInternal,
			wantCleanup: []string{
				"open_source", "shutdown_source", "close_source", "release_lease",
			},
		},
		"stop fault while waiting for release": {
			frames:    vmExecSessionFrames(t, true, true, false),
			stopErr:   stopFault,
			wantReady: true,
			wantCode:  errs.CodeVMAtomicFailed,
			wantClass: errs.ClassConflict,
			wantCleanup: []string{
				"open_source", "bind_destination", "shutdown_destination", "close_destination",
				"shutdown_source", "close_source", "release_lease",
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			events := make([]string, 0, 8)
			phaseEvents := make([]string, 0, 6)
			waitStarted := make(chan struct{})
			waitFinished := make(chan struct{})
			triggerStop := make(chan struct{})
			readBlocked := make(chan struct{})
			lease := &fakeVMExecSessionLease{
				events: &events,
				waitStopFunc: func(ctx context.Context, ready vmExecSessionStopWaitReady) error {
					close(waitStarted)
					defer close(waitFinished)
					ready()
					select {
					case <-triggerStop:
						return test.stopErr
					case <-ctx.Done():
						return ctx.Err()
					}
				},
				releaseFunc: func(context.Context) error {
					select {
					case <-waitFinished:
						return nil
					default:
						return errors.New("stop waiter was not joined before lease release")
					}
				},
			}
			endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(t, test.frames, &events)
			endpoint.phaseEvents = &phaseEvents
			recvmsg := deps.connectionReader.recvmsg
			var blockRead sync.Once
			deps.connectionReader.recvmsg = func(
				ctx context.Context,
				fd int,
				payload []byte,
				control []byte,
				flags int,
			) (int, int, int, unix.Sockaddr, error) {
				count, controlCount, receiveFlags, from, err := recvmsg(ctx, fd, payload, control, flags)
				if count != 0 || controlCount != 0 || receiveFlags != 0 || from != nil || err != nil {
					return count, controlCount, receiveFlags, from, err
				}
				blockRead.Do(func() { close(readBlocked) })
				<-ctx.Done()
				return 0, 0, 0, nil, ctx.Err()
			}
			result := make(chan error, 1)
			go func() {
				result <- handleVMExecSession(
					t.Context(),
					endpoint,
					callerIdentity{uid: 1000, gid: 1000},
					vmExecSessionEffectsForLease(lease),
					deps,
				)
			}()

			assertVMExecSessionChannelClosed(t, waitStarted)
			assertVMExecSessionChannelClosed(t, readBlocked)
			close(triggerStop)
			require.NoError(t, requireVMExecSessionHandlerResult(t, result))

			assertVMExecSessionChannelClosed(t, waitFinished)
			responses := bytes.NewReader(endpoint.responseBytes())
			retained, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
			require.NoError(t, err)
			assert.True(t, retained.Retained())
			wantPhases := []string{"read_open_source", "write_source_retained"}
			if test.wantReady {
				ready, readyErr := vmexecsessionwire.DecodeReady(t.Context(), responses)
				require.NoError(t, readyErr)
				assert.True(t, ready.Ready())
				wantPhases = append(wantPhases, "read_bind_destination", "write_ready")
			}
			outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
			require.NoError(t, err)
			assert.False(t, outcome.Released())
			operationErr := outcome.OperationError()
			require.NotNil(t, operationErr)
			assert.Equal(t, test.wantCode, operationErr.Code)
			assert.Equal(t, test.wantClass, operationErr.Class)
			assert.Zero(t, responses.Len())
			assertVMExecSessionEvents(t, append(wantPhases, "write_failure"), phaseEvents)
			assertVMExecSessionEvents(t, test.wantCleanup, events)
			assert.Equal(t, 1, endpoint.shutdownCount())
			assert.True(t, lease.released)
			assertFDClosed(t, sourceFD)
			if test.wantReady {
				assertFDClosed(t, destinationFD)
			} else {
				assertFDOpen(t, destinationFD)
				require.NoError(t, unix.Close(destinationFD))
			}
		})
	}
}

func TestHandleVMExecSessionDeadlineCancelsIntermediateSuccessResponses(t *testing.T) {
	tests := map[string]struct {
		frames              []vmExecSessionInputFrame
		blockedDeadlineCall int
		wantSourceRetained  bool
		wantDestination     bool
		wantPhases          []string
		wantCleanup         []string
	}{
		"before source retained": {
			frames:              vmExecSessionFrames(t, true, false, false),
			blockedDeadlineCall: 1,
			wantPhases:          []string{"read_open_source"},
			wantCleanup: []string{
				"open_source", "shutdown_source", "close_source", "release_lease",
			},
		},
		"before ready": {
			frames:              vmExecSessionFrames(t, true, true, false),
			blockedDeadlineCall: 2,
			wantSourceRetained:  true,
			wantDestination:     true,
			wantPhases: []string{
				"read_open_source", "write_source_retained", "read_bind_destination",
			},
			wantCleanup: []string{
				"open_source", "bind_destination", "shutdown_destination", "close_destination",
				"shutdown_source", "close_source", "release_lease",
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			events := make([]string, 0, 8)
			phaseEvents := make([]string, 0, 4)
			triggerStop := make(chan struct{})
			waitFinished := make(chan struct{})
			waitContext := make(chan context.Context, 1)
			responseBlocked := make(chan struct{})
			releaseResponse := make(chan struct{})
			lease := &fakeVMExecSessionLease{
				events: &events,
				waitStopFunc: func(ctx context.Context, ready vmExecSessionStopWaitReady) error {
					waitContext <- ctx
					defer close(waitFinished)
					ready()
					select {
					case <-triggerStop:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
				releaseFunc: func(context.Context) error {
					select {
					case <-waitFinished:
						return nil
					default:
						return errors.New("stop waiter was not joined before lease release")
					}
				},
			}
			endpoint, deps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(t, test.frames, &events)
			endpoint.phaseEvents = &phaseEvents
			deadlineCall := 0
			endpoint.deadlineFunc = func(time.Time) error {
				deadlineCall++
				if deadlineCall == test.blockedDeadlineCall {
					close(responseBlocked)
					<-releaseResponse
				}
				return nil
			}
			result := make(chan error, 1)
			go func() {
				result <- handleVMExecSession(
					t.Context(),
					endpoint,
					callerIdentity{uid: 1000, gid: 1000},
					vmExecSessionEffectsForLease(lease),
					deps,
				)
			}()

			stopCtx := requireVMExecSessionStopContext(t, waitContext)
			assertVMExecSessionChannelClosed(t, responseBlocked)
			close(triggerStop)
			assertVMExecSessionChannelClosed(t, stopCtx.Done())
			close(releaseResponse)
			handlerErr := requireVMExecSessionHandlerResult(t, result)

			domainErr := requireVMExecDomainError(t, handlerErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.Equal(t, false, domainErr.Details["response_started"])
			assertVMExecSessionChannelClosed(t, waitFinished)
			assertVMExecSessionEvents(t, test.wantPhases, phaseEvents)
			assertVMExecSessionEvents(t, test.wantCleanup, events)
			responses := bytes.NewReader(endpoint.responseBytes())
			if test.wantSourceRetained {
				retained, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), responses)
				require.NoError(t, err)
				assert.True(t, retained.Retained())
			}
			assert.Zero(t, responses.Len())
			assert.Equal(t, 0, endpoint.shutdownCount())
			assert.True(t, lease.released)
			assertFDClosed(t, sourceFD)
			if test.wantDestination {
				assertFDClosed(t, destinationFD)
			} else {
				assertFDOpen(t, destinationFD)
				require.NoError(t, unix.Close(destinationFD))
			}
		})
	}
}

func TestVMExecSessionStopErrorWrapsInfrastructureFailure(t *testing.T) {
	stopFailure := errors.New("timerfd poll failed")

	stopErr := vmExecSessionStopError(stopFailure)

	assert.Equal(t, errs.CodeProcessError, stopErr.Code)
	assert.Equal(t, errs.ClassInternal, stopErr.Class)
	assert.ErrorIs(t, stopErr, stopFailure)
}

func TestVMExecSessionStopPrimaryPreservesPartialResponseEvidence(t *testing.T) {
	stopErr := errs.New(errs.CodeVMAtomicFailed, "retained VM-exec relay stopped")
	responseErr := vmExecSessionResponseTransportError(io.ErrClosedPipe, true)

	primary := withVMExecSessionResponseEvidence(stopErr, responseErr)

	assert.Equal(t, stopErr.Code, primary.Code)
	assert.Equal(t, true, primary.Details["response_started"])
	assert.ErrorIs(t, primary, stopErr)
	assert.ErrorIs(t, primary, io.ErrClosedPipe)
}

func TestVMExecSessionStopScopeLocalFinishWinsLateParentCancellation(t *testing.T) {
	parentCtx, cancelParent := context.WithCancel(t.Context())
	waitStarted := make(chan struct{})
	waitFinished := make(chan struct{})
	lease := &fakeVMExecSessionLease{waitStopFunc: func(ctx context.Context, ready vmExecSessionStopWaitReady) error {
		close(waitStarted)
		ready()
		<-ctx.Done()
		close(waitFinished)
		return ctx.Err()
	}}
	scope := startVMExecSessionStopWaiter(parentCtx, lease)
	assertVMExecSessionChannelClosed(t, waitStarted)

	finishCompleted := make(chan struct{})
	go func() {
		scope.finish()
		close(finishCompleted)
	}()
	assertVMExecSessionChannelClosed(t, finishCompleted)
	assertVMExecSessionChannelClosed(t, waitFinished)
	cancelParent()

	assert.NoError(t, scope.finishAndPrimary(nil))
}

func TestVMExecSessionIntermediateResponsesHonorCancellation(t *testing.T) {
	tests := map[string]func(context.Context, controlEndpoint, controlDeps) error{
		"source retained": deliverVMExecSourceRetained,
		"ready":           deliverVMExecReady,
	}
	for name, deliver := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			endpoint := &fakeVMExecSessionEndpoint{}
			deps := controlDeps{
				now:           time.Now,
				shutdownWrite: func(context.Context, int) error { return nil },
			}

			err := deliver(ctx, endpoint, deps)

			require.Error(t, err)
			assert.Equal(t, 1, endpoint.deadlineCount())
			assert.Equal(t, 0, endpoint.writeCount())
		})
	}
}

func TestHandleVMExecSessionRejectsInactiveSeamsWithoutPanic(t *testing.T) {
	tests := map[string]struct {
		ctx      context.Context
		endpoint controlEndpoint
		deps     vmExecSessionHandlerDeps
		wantCode errs.Code
	}{
		"nil context": {
			ctx: nil, endpoint: &fakeVMExecSessionEndpoint{}, wantCode: errs.CodeValidationFailed,
		},
		"nil endpoint": {
			ctx: t.Context(), endpoint: nil, wantCode: errs.CodeValidationFailed,
		},
		"inactive dependencies": {
			ctx: t.Context(), endpoint: &fakeVMExecSessionEndpoint{}, wantCode: errs.CodeInternal,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := handleVMExecSession(
				test.ctx,
				test.endpoint,
				callerIdentity{uid: 1000, gid: 1000},
				vmExecSessionHandlerEffects{},
				test.deps,
			)

			assert.Equal(t, test.wantCode, requireVMExecDomainError(t, err).Code)
		})
	}
}

func (lease *fakeVMExecSessionLease) bindDestination(
	ctx context.Context,
	connection vmExecSessionConnection,
) error {
	appendVMExecSessionEvent(lease.events, "bind_destination")
	lease.destinationConnection = connection
	lease.bound = true
	return lease.bindErr
}

func (lease *fakeVMExecSessionLease) waitStop(ctx context.Context, ready vmExecSessionStopWaitReady) error {
	if lease.waitStopFunc != nil {
		return lease.waitStopFunc(ctx, ready)
	}
	ready()
	<-ctx.Done()
	return ctx.Err()
}

func (lease *fakeVMExecSessionLease) release(ctx context.Context) error {
	appendVMExecSessionEvent(lease.events, "release_lease")
	lease.released = true
	lease.releaseCtx = ctx
	if lease.releaseFunc != nil {
		return lease.releaseFunc(ctx)
	}
	return lease.releaseErr
}

type vmExecSessionInputFrame struct {
	frame      []byte
	descriptor bool
	phase      string
}

func vmExecSessionFrames(
	t *testing.T,
	includeSource bool,
	includeDestination bool,
	includeRelease bool,
) []vmExecSessionInputFrame {
	t.Helper()
	frames := make([]vmExecSessionInputFrame, 0, 3)
	if includeSource {
		var frame bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeOpenSource(t.Context(), &frame, vmexecsessionwire.OpenSource{
			SourceVMID:      testVMExecHandlerSource,
			DestinationVMID: testVMExecHandlerDestination,
		}))
		frames = append(frames, vmExecSessionInputFrame{
			frame: frame.Bytes(), descriptor: true, phase: "open_source",
		})
	}
	if includeDestination {
		var frame bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &frame))
		frames = append(frames, vmExecSessionInputFrame{
			frame: frame.Bytes(), descriptor: true, phase: "bind_destination",
		})
	}
	if includeRelease {
		var frame bytes.Buffer
		require.NoError(t, vmexecsessionwire.EncodeRelease(t.Context(), &frame))
		frames = append(frames, vmExecSessionInputFrame{frame: frame.Bytes(), phase: "release"})
	}
	return frames
}

func newVMExecSessionHandlerFixture(
	t *testing.T,
	frames []vmExecSessionInputFrame,
	events *[]string,
) (*fakeVMExecSessionEndpoint, vmExecSessionHandlerDeps, int, int) {
	t.Helper()
	endpoint := &fakeVMExecSessionEndpoint{}
	sourceFD := requireRawTestFD(t)
	destinationFD := requireRawTestFD(t)
	descriptorIndex := 0
	frameIndex := 0
	offset := 0
	readerDeps := successfulVMExecReaderDeps(nil)
	readerDeps.recvmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		if frameIndex >= len(frames) {
			return 0, 0, 0, nil, nil
		}
		frame := frames[frameIndex]
		start := offset == 0
		if start {
			endpoint.recordPhase("read_" + frame.phase)
		}
		count := copy(payload, frame.frame[offset:])
		offset += count
		controlCount := 0
		if start && frame.descriptor {
			fd := sourceFD
			if descriptorIndex == 1 {
				fd = destinationFD
			}
			descriptorIndex++
			controlCount = copy(control, unix.UnixRights(fd))
		}
		if offset == len(frame.frame) {
			frameIndex++
			offset = 0
		}
		return count, controlCount, 0, nil, nil
	}
	readerDeps.shutdownFD = func(ctx context.Context, fd int, how int) error {
		assert.NoError(t, ctx.Err())
		assert.Equal(t, unix.SHUT_RDWR, how)
		appendVMExecSessionEvent(events, vmExecSessionFDName(fd, sourceFD, destinationFD, "shutdown"))
		return nil
	}
	readerDeps.closeFD = func(ctx context.Context, fd int) error {
		assert.NoError(t, ctx.Err())
		appendVMExecSessionEvent(events, vmExecSessionFDName(fd, sourceFD, destinationFD, "close"))
		return unix.Close(fd)
	}
	control := controlDeps{
		now: func() time.Time { return time.Now() },
		shutdownWrite: func(_ context.Context, _ int) error {
			endpoint.mu.Lock()
			defer endpoint.mu.Unlock()
			endpoint.shutdownCalls++
			return endpoint.shutdownErr
		},
	}
	return endpoint, vmExecSessionHandlerDeps{control: control, connectionReader: readerDeps}, sourceFD, destinationFD
}

func vmExecSessionEffectsForLease(lease *fakeVMExecSessionLease) vmExecSessionHandlerEffects {
	return vmExecSessionHandlerEffects{openSource: func(
		_ context.Context,
		_ callerIdentity,
		_ vmexecsessionwire.OpenSource,
		connection vmExecSessionConnection,
	) (vmExecSessionHandlerLease, error) {
		appendVMExecSessionEvent(lease.events, "open_source")
		lease.sourceConnection = connection
		return lease, nil
	}}
}

func (endpoint *fakeVMExecSessionEndpoint) Write(value []byte) (int, error) {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	endpoint.writeCalls++
	for _, phase := range []string{"source_retained", "ready", "released", "failure"} {
		if strings.Contains(string(value), `"phase":"`+phase+`"`) {
			endpoint.recordPhaseLocked("write_" + phase)
		}
	}
	if endpoint.writeErr != nil {
		limit := endpoint.writeLimit
		if limit > len(value) {
			limit = len(value)
		}
		if limit > 0 {
			_, _ = endpoint.response.Write(value[:limit])
		}
		return limit, endpoint.writeErr
	}
	return endpoint.response.Write(value)
}

func (*fakeVMExecSessionEndpoint) Fd() uintptr { return 0 }

func (endpoint *fakeVMExecSessionEndpoint) SetWriteDeadline(deadline time.Time) error {
	endpoint.mu.Lock()
	endpoint.deadlines = append(endpoint.deadlines, deadline)
	deadlineFunc := endpoint.deadlineFunc
	deadlineErr := endpoint.deadlineErr
	deadlineErrAt := endpoint.deadlineErrAt
	deadlineCount := len(endpoint.deadlines)
	endpoint.mu.Unlock()
	if deadlineFunc != nil {
		return deadlineFunc(deadline)
	}
	if deadlineErrAt == 0 || deadlineErrAt == deadlineCount {
		return deadlineErr
	}
	return nil
}

func (endpoint *fakeVMExecSessionEndpoint) responseBytes() []byte {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	return append([]byte(nil), endpoint.response.Bytes()...)
}

func (endpoint *fakeVMExecSessionEndpoint) shutdownCount() int {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	return endpoint.shutdownCalls
}

func (endpoint *fakeVMExecSessionEndpoint) writeCount() int {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	return endpoint.writeCalls
}

func (endpoint *fakeVMExecSessionEndpoint) deadlineCount() int {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	return len(endpoint.deadlines)
}

func (endpoint *fakeVMExecSessionEndpoint) recordPhase(event string) {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	endpoint.recordPhaseLocked(event)
}

func (endpoint *fakeVMExecSessionEndpoint) recordPhaseLocked(event string) {
	if endpoint.phaseEvents != nil {
		*endpoint.phaseEvents = append(*endpoint.phaseEvents, event)
	}
}

func assertVMExecSessionResponses(
	t *testing.T,
	raw []byte,
	wantSource bool,
	wantReady bool,
	wantReleased bool,
) {
	t.Helper()
	reader := bytes.NewReader(raw)
	if wantSource {
		result, err := vmexecsessionwire.DecodeSourceRetained(t.Context(), reader)
		require.NoError(t, err)
		assert.True(t, result.Retained())
	}
	if wantReady {
		result, err := vmexecsessionwire.DecodeReady(t.Context(), reader)
		require.NoError(t, err)
		assert.True(t, result.Ready())
	}
	if wantReleased {
		result, err := vmexecsessionwire.DecodeOutcome(t.Context(), reader)
		require.NoError(t, err)
		assert.True(t, result.Released())
	}
}

func assertVMExecSessionFailureOnly(t *testing.T, raw []byte) {
	t.Helper()
	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), bytes.NewReader(raw))
	require.NoError(t, err)
	assert.False(t, outcome.Released())
	assert.NotNil(t, outcome.OperationError())
}

func appendVMExecSessionEvent(events *[]string, event string) {
	*events = append(*events, event)
}

func vmExecSessionFDName(fd int, sourceFD int, destinationFD int, operation string) string {
	switch fd {
	case sourceFD:
		return operation + "_source"
	case destinationFD:
		return operation + "_destination"
	default:
		return operation + "_unknown"
	}
}

func assertVMExecSessionEvents(t *testing.T, want []string, got []string) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VM-exec session event order mismatch (-want +got):\n%s", diff)
	}
}

func assertVMExecSessionChannelClosed(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(testVMExecHandlerSyncTimeout)
	defer timer.Stop()
	select {
	case <-channel:
	case <-timer.C:
		t.Fatal("VM-exec session synchronization channel did not close")
	}
}

func requireVMExecSessionHandlerResult(t *testing.T, result <-chan error) error {
	t.Helper()
	timer := time.NewTimer(testVMExecHandlerSyncTimeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatal("VM-exec session handler did not return")
		return nil
	}
}

func requireVMExecSessionStopContext(t *testing.T, contexts <-chan context.Context) context.Context {
	t.Helper()
	timer := time.NewTimer(testVMExecHandlerSyncTimeout)
	defer timer.Stop()
	select {
	case ctx := <-contexts:
		return ctx
	case <-timer.C:
		t.Fatal("VM-exec session stop waiter did not publish its context")
		return nil
	}
}

func assertFDClosed(t *testing.T, fd int) {
	t.Helper()
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	assert.ErrorIs(t, err, unix.EBADF)
}

func assertFDOpen(t *testing.T, fd int) {
	t.Helper()
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	assert.NoError(t, err)
}
