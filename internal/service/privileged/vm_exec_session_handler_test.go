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
)

type fakeVMExecSessionEndpoint struct {
	mu            sync.Mutex
	response      bytes.Buffer
	deadlines     []time.Time
	writeErr      error
	writeLimit    int
	deadlineErr   error
	deadlineErrAt int
	writeCalls    int
	shutdownCalls int
	shutdownErr   error
	phaseEvents   *[]string
}

type fakeVMExecSessionLease struct {
	events                *[]string
	bindErr               error
	releaseErr            error
	sourceConnection      vmExecSessionConnection
	destinationConnection vmExecSessionConnection
	bound                 bool
	released              bool
	releaseCtx            context.Context
}

func TestHandleVMExecSessionSuccessUsesExactPhasesAndReverseCleanup(t *testing.T) {
	events := make([]string, 0, 8)
	lease := &fakeVMExecSessionLease{events: &events}
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

	err := handleVMExecSession(t.Context(), endpoint, caller, effects, deps)

	require.NoError(t, err)
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
		lease := &fakeVMExecSessionLease{
			events: &events,
			bindErr: errs.New(
				errs.CodeVMAtomicFailed,
				"destination retention failed",
				errs.WithClass(errs.ClassConflict),
			),
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
		assert.Equal(t, errs.CodeVMAtomicFailed, operationErr.Code)
		assert.Equal(t, errs.ClassConflict, operationErr.Class)
		assert.Equal(t, true, operationErr.Details["cleanup_failed"])
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

func TestHandleVMExecSessionCancellationUsesDetachedCleanup(t *testing.T) {
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
	assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
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

func (lease *fakeVMExecSessionLease) release(ctx context.Context) error {
	appendVMExecSessionEvent(lease.events, "release_lease")
	lease.released = true
	lease.releaseCtx = ctx
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
	defer endpoint.mu.Unlock()
	endpoint.deadlines = append(endpoint.deadlines, deadline)
	if endpoint.deadlineErrAt == 0 || endpoint.deadlineErrAt == len(endpoint.deadlines) {
		return endpoint.deadlineErr
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
