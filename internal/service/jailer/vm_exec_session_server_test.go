package jailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const (
	testVMExecServerUID      uint32 = 1000
	testVMExecServerGID      uint32 = 1001
	testVMExecServerDeadline uint64 = 9_876_543_210
)

var testVMExecServerRequest = VMExecSessionOpenRequest{
	SourceVMID:      "11111111111111111111111111111111",
	DestinationVMID: "22222222222222222222222222222222",
}

func TestVMExecSessionServerOpenSourceReservesBindsAndArmsExactDeadline(t *testing.T) {
	events := make([]string, 0, 4)
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
		bindSourceFunc: func(_ context.Context, descriptor int) error {
			events = append(events, fmt.Sprintf("bind_source:%d", descriptor))
			return nil
		},
		admissionFunc: func() {
			events = append(events, "read_deadline")
		},
	}
	stop := &fakeVMExecServerStop{}
	server, err := newVMExecSessionServerWithDeps(vmExecSessionServerDeps{
		reserve: func(
			_ context.Context,
			caller instanceCaller,
			sourceVMID string,
			destinationVMID string,
		) (vmExecSessionServerAuthorityLease, error) {
			events = append(events, "reserve")
			assert.Equal(t, instanceCaller{uid: testVMExecServerUID, gid: testVMExecServerGID}, caller)
			assert.Equal(t, testVMExecServerRequest.SourceVMID, sourceVMID)
			assert.Equal(t, testVMExecServerRequest.DestinationVMID, destinationVMID)
			return authority, nil
		},
		armStop: func(_ context.Context, deadline uint64) (vmExecSessionServerStop, error) {
			events = append(events, fmt.Sprintf("arm:%d", deadline))
			return stop, nil
		},
	})
	require.NoError(t, err)
	connection := fakeVMExecServerConnection{bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
		return binder.Bind(ctx, 47)
	}}

	lease, err := server.OpenSource(
		t.Context(),
		VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
		testVMExecServerRequest,
		connection,
	)

	require.NoError(t, err)
	require.NotNil(t, lease)
	wantEvents := []string{"reserve", "bind_source:47", "read_deadline", "arm:9876543210"}
	if diff := cmp.Diff(wantEvents, events); diff != "" {
		t.Errorf("VM-exec server open order mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecSessionServerRejectsMissingMultipleAndSuppressedSourceBinderFailures(t *testing.T) {
	bindFailure := errors.New("source peer rejected")
	tests := map[string]struct {
		connection   fakeVMExecServerConnection
		bindErr      error
		wantError    string
		wantCause    error
		wantBindings int
	}{
		"missing_binder_call": {
			connection: fakeVMExecServerConnection{bind: func(context.Context, VMExecSessionPeerBinder) error {
				return nil
			}},
			wantError: "was not invoked",
		},
		"multiple_binder_calls_even_when_second_error_is_suppressed": {
			connection: fakeVMExecServerConnection{
				bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
					_ = binder.Bind(ctx, 47)
					_ = binder.Bind(ctx, 48)
					return nil
				},
			},
			wantError:    "invoked more than once",
			wantBindings: 1,
		},
		"binder_failure_suppressed_by_connection": {
			connection: fakeVMExecServerConnection{
				bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
					_ = binder.Bind(ctx, 47)
					return nil
				},
			},
			bindErr:      bindFailure,
			wantCause:    bindFailure,
			wantBindings: 1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &fakeVMExecServerAuthorityLease{
				admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
				bindSourceErr:  tc.bindErr,
			}
			server := mustFakeVMExecSessionServer(t, authority, &fakeVMExecServerStop{})

			lease, err := server.OpenSource(
				t.Context(),
				VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
				testVMExecServerRequest,
				tc.connection,
			)

			require.Error(t, err)
			if tc.wantError != "" {
				assert.ErrorContains(t, err, tc.wantError)
			}
			if tc.wantCause != nil {
				assert.ErrorIs(t, err, tc.wantCause)
			}
			require.NotNil(t, lease)
			assert.Equal(t, tc.wantBindings, authority.sourceBindCalls)
			require.NoError(t, lease.Release(t.Context()))
			assert.Equal(t, 1, authority.releaseCalls)
		})
	}
}

func TestVMExecSessionServerNormalizesConnectionAdapterFailure(t *testing.T) {
	connectionFailure := errors.New("connection adapter failed")
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
	}
	server := mustFakeVMExecSessionServer(t, authority, &fakeVMExecServerStop{})
	connection := fakeVMExecServerConnection{bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
		require.NoError(t, binder.Bind(ctx, 47))
		return connectionFailure
	}}

	lease, err := server.OpenSource(
		t.Context(),
		VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
		testVMExecServerRequest,
		connection,
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, connectionFailure)
	require.NotNil(t, errs.AsDomainError(err))
	require.NotNil(t, lease)
	assert.Equal(t, 1, authority.sourceBindCalls)
	require.NoError(t, lease.Release(t.Context()))
}

// Rationale: Copies of the binder must still represent one authority transition. Concurrent attempts may race, but
// only one descriptor can reach the private source authority and the connection cannot suppress the misuse evidence.
func TestVMExecSessionServerRejectsConcurrentCopiedSourceBinderCalls(t *testing.T) {
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
	}
	server := mustFakeVMExecSessionServer(t, authority, &fakeVMExecServerStop{})
	connection := fakeVMExecServerConnection{bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wait sync.WaitGroup
		for descriptor := range 2 {
			copyOfBinder := binder
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				results <- copyOfBinder.Bind(ctx, 47+descriptor)
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		// The connection deliberately suppresses both results. The server must recover the shared violation evidence.
		for range results {
		}
		return nil
	}}

	lease, err := server.OpenSource(
		t.Context(),
		VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
		testVMExecServerRequest,
		connection,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "invoked more than once")
	require.NotNil(t, lease)
	assert.Equal(t, 1, authority.sourceBindCalls)
	require.NoError(t, lease.Release(t.Context()))
}

// Rationale: A connection may return after starting a binder call in another goroutine. Cancellation must not let
// the server return while that borrowed descriptor is still being used by the retained authority.
func TestVMExecSessionServerWaitsForStartedSourceBindAfterCancellation(t *testing.T) {
	bindFailure := errors.New("source bind completed after cancellation")
	bindStarted := make(chan struct{})
	finishBind := make(chan struct{})
	bindFinished := make(chan struct{})
	connectionReturned := make(chan struct{})
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
		bindSourceFunc: func(context.Context, int) error {
			close(bindStarted)
			<-finishBind
			close(bindFinished)
			return bindFailure
		},
	}
	server := mustFakeVMExecSessionServer(t, authority, &fakeVMExecServerStop{})
	ctx, cancel := context.WithCancel(t.Context())
	connection := fakeVMExecServerConnection{
		bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
			go func() {
				_ = binder.Bind(ctx, 47)
			}()
			<-bindStarted
			cancel()
			close(connectionReturned)
			return nil
		},
	}
	type openResult struct {
		lease *VMExecSessionServerLease
		err   error
	}
	results := make(chan openResult, 1)
	go func() {
		lease, err := server.OpenSource(
			ctx,
			VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
			testVMExecServerRequest,
			connection,
		)
		results <- openResult{lease: lease, err: err}
	}()

	<-connectionReturned
	close(finishBind)
	<-bindFinished
	result := <-results

	require.Error(t, result.err)
	assert.ErrorIs(t, result.err, bindFailure)
	require.NotNil(t, errs.AsDomainError(result.err))
	require.NotNil(t, result.lease)
	require.NoError(t, result.lease.Release(t.Context()))
}

func TestVMExecSessionServerReturnsReleasablePartialLeaseAfterReservation(t *testing.T) {
	bindFailure := errors.New("bind failed")
	admissionFailure := errors.New("deadline unavailable")
	armFailure := errors.New("timer arm failed")
	reserveFailure := errors.New("reservation durability uncertain")
	domainFailureCause := errors.New("structured dependency failure")
	domainFailure := errs.WrapMsg(
		errs.CodeUnauthorized,
		"structured dependency rejection",
		domainFailureCause,
		errs.WithDetails(map[string]any{"dependency_evidence": "retained"}),
	)
	tests := map[string]struct {
		reserveErr   error
		bindErr      error
		admissionErr error
		armErr       error
		armStop      *fakeVMExecServerStop
		wantCause    error
		wantDomain   *errs.DomainError
	}{
		"reservation_returns_authority_with_error": {
			reserveErr: reserveFailure,
			wantCause:  reserveFailure,
		},
		"source_binding_fails": {
			bindErr:   bindFailure,
			wantCause: bindFailure,
		},
		"deadline_read_fails": {
			admissionErr: admissionFailure,
			wantCause:    admissionFailure,
		},
		"arm_returns_stop_with_error": {
			armErr:    armFailure,
			armStop:   &fakeVMExecServerStop{},
			wantCause: armFailure,
		},
		"reservation_preserves_domain_error": {
			reserveErr: domainFailure,
			wantCause:  domainFailureCause,
			wantDomain: domainFailure,
		},
		"deadline_read_preserves_domain_error": {
			admissionErr: domainFailure,
			wantCause:    domainFailureCause,
			wantDomain:   domainFailure,
		},
		"arm_preserves_domain_error": {
			armErr:     domainFailure,
			armStop:    &fakeVMExecServerStop{},
			wantCause:  domainFailureCause,
			wantDomain: domainFailure,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &fakeVMExecServerAuthorityLease{
				admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
				bindSourceErr:  tc.bindErr,
				admissionErr:   tc.admissionErr,
			}
			server, err := newVMExecSessionServerWithDeps(vmExecSessionServerDeps{
				reserve: func(
					context.Context,
					instanceCaller,
					string,
					string,
				) (vmExecSessionServerAuthorityLease, error) {
					return authority, tc.reserveErr
				},
				armStop: func(context.Context, uint64) (vmExecSessionServerStop, error) {
					return tc.armStop, tc.armErr
				},
			})
			require.NoError(t, err)
			connection := fakeVMExecServerConnection{
				bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
					return binder.Bind(ctx, 47)
				},
			}

			lease, err := server.OpenSource(
				t.Context(),
				VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
				testVMExecServerRequest,
				connection,
			)

			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantCause)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			if tc.wantDomain != nil {
				assert.Same(t, tc.wantDomain, domainErr)
				assert.Equal(t, "retained", domainErr.Details["dependency_evidence"])
			}
			require.NotNil(t, lease)
			require.NoError(t, lease.Release(t.Context()))
			assert.Equal(t, 1, authority.releaseCalls)
			if tc.armStop != nil {
				assert.Equal(t, 1, tc.armStop.releaseCalls)
			}
		})
	}
}

func TestVMExecSessionServerLeaseBindsDestinationThroughFreshBinder(t *testing.T) {
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
	}
	server := mustFakeVMExecSessionServer(t, authority, &fakeVMExecServerStop{})
	source := fakeVMExecServerConnection{bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
		return binder.Bind(ctx, 47)
	}}
	lease, err := server.OpenSource(
		t.Context(),
		VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
		testVMExecServerRequest,
		source,
	)
	require.NoError(t, err)
	destination := fakeVMExecServerConnection{bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
		return binder.Bind(ctx, 53)
	}}

	err = lease.BindDestination(t.Context(), destination)

	require.NoError(t, err)
	assert.Equal(t, 1, authority.destinationBindCalls)
	assert.Equal(t, 53, authority.destinationDescriptor)
}

func TestVMExecSessionServerLeaseRejectsSuppressedAndMultipleDestinationBinderFailures(t *testing.T) {
	bindFailure := errors.New("destination peer rejected")
	tests := map[string]struct {
		connection fakeVMExecServerConnection
		bindErr    error
		wantError  string
		wantCause  error
	}{
		"multiple_binder_calls": {
			connection: fakeVMExecServerConnection{
				bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
					_ = binder.Bind(ctx, 53)
					_ = binder.Bind(ctx, 54)
					return nil
				},
			},
			wantError: "invoked more than once",
		},
		"binder_failure_suppressed_by_connection": {
			connection: fakeVMExecServerConnection{
				bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
					_ = binder.Bind(ctx, 53)
					return nil
				},
			},
			bindErr:   bindFailure,
			wantCause: bindFailure,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &fakeVMExecServerAuthorityLease{
				admissionValue:     vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
				bindDestinationErr: tc.bindErr,
			}
			server := mustFakeVMExecSessionServer(t, authority, &fakeVMExecServerStop{})
			lease := openFakeVMExecServerLease(t, server)

			err := lease.BindDestination(t.Context(), tc.connection)

			require.Error(t, err)
			if tc.wantError != "" {
				assert.ErrorContains(t, err, tc.wantError)
			}
			if tc.wantCause != nil {
				assert.ErrorIs(t, err, tc.wantCause)
			}
			assert.Equal(t, 1, authority.destinationBindCalls)
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

func TestVMExecSessionServerLeaseStartStopWaitDelegatesToArmedStop(t *testing.T) {
	waitFailure := errors.New("wait stopped")
	domainFailure := errs.WrapMsg(
		errs.CodeUnauthorized,
		"structured wait rejection",
		waitFailure,
		errs.WithDetails(map[string]any{"wait_evidence": "retained"}),
	)
	tests := map[string]struct {
		waitErr    error
		wantDomain *errs.DomainError
	}{
		"raw_error_is_normalized": {waitErr: waitFailure},
		"domain_error_is_preserved": {
			waitErr:    domainFailure,
			wantDomain: domainFailure,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stop := &fakeVMExecServerStop{waitErr: tc.waitErr}
			server := mustFakeVMExecSessionServer(t, &fakeVMExecServerAuthorityLease{
				admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
			}, stop)
			lease := openFakeVMExecServerLease(t, server)

			wait, err := lease.StartStopWait(t.Context())
			require.NoError(t, err)
			assert.False(t, wait.Pending())
			err = wait.Wait()

			require.Error(t, err)
			assert.ErrorIs(t, err, waitFailure)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			if tc.wantDomain != nil {
				assert.Same(t, tc.wantDomain, domainErr)
				assert.Equal(t, "retained", domainErr.Details["wait_evidence"])
			}
			assert.Equal(t, 1, stop.waitCalls)
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

func TestVMExecSessionServerLeaseStartStopWaitPublishesPendingReadiness(t *testing.T) {
	stop := &fakeVMExecServerStop{
		waitPending: true,
	}
	server := mustFakeVMExecSessionServer(t, &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
	}, stop)
	lease := openFakeVMExecServerLease(t, server)

	wait, err := lease.StartStopWait(t.Context())
	require.NoError(t, err)
	assert.True(t, wait.Pending())
	err = wait.Wait()

	require.NoError(t, err)
	assert.Equal(t, 1, stop.waitCalls)
	require.NoError(t, lease.Release(t.Context()))
}

func TestCopiedVMExecSessionServerLeaseReleasesStopThenAuthorityOnce(t *testing.T) {
	stopFailure := errors.New("stop cleanup failed")
	authorityFailure := errors.New("authority cleanup failed")
	events := make([]string, 0, 2)
	var eventsMu sync.Mutex
	stop := &fakeVMExecServerStop{releaseErr: stopFailure, releaseFunc: func(ctx context.Context) {
		assert.NoError(t, ctx.Err())
		eventsMu.Lock()
		events = append(events, "stop")
		eventsMu.Unlock()
	}}
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
		releaseErr:     authorityFailure,
		releaseFunc: func(ctx context.Context) {
			assert.NoError(t, ctx.Err())
			eventsMu.Lock()
			events = append(events, "authority")
			eventsMu.Unlock()
		},
	}
	server := mustFakeVMExecSessionServer(t, authority, stop)
	lease := openFakeVMExecServerLease(t, server)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	const callers = 12
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		copyOfLease := *lease
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByCaller <- copyOfLease.Release(cancelled)
		}()
	}
	wait.Wait()
	close(errorsByCaller)

	for err := range errorsByCaller {
		require.Error(t, err)
		assert.ErrorIs(t, err, stopFailure)
		assert.ErrorIs(t, err, authorityFailure)
		require.NotNil(t, errs.AsDomainError(err))
	}
	assert.Equal(t, 1, stop.releaseCalls)
	assert.Equal(t, 1, authority.releaseCalls)
	wantEvents := []string{"stop", "authority"}
	if diff := cmp.Diff(wantEvents, events); diff != "" {
		t.Errorf("VM-exec server cleanup order mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecSessionServerLeaseNilReleaseContextRecordsEvidenceAndStillCleans(t *testing.T) {
	stopFailure := errors.New("stop cleanup failed")
	authorityFailure := errors.New("authority cleanup failed")
	stop := &fakeVMExecServerStop{
		releaseErr: stopFailure,
		releaseFunc: func(ctx context.Context) {
			assert.NoError(t, ctx.Err())
		},
	}
	authority := &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
		releaseErr:     authorityFailure,
		releaseFunc: func(ctx context.Context) {
			assert.NoError(t, ctx.Err())
		},
	}
	server := mustFakeVMExecSessionServer(t, authority, stop)
	lease := openFakeVMExecServerLease(t, server)

	err := lease.Release(nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "release context is required")
	assert.ErrorIs(t, err, stopFailure)
	assert.ErrorIs(t, err, authorityFailure)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["vm_exec_session_cleanup_failed"])
	assert.Equal(t, 1, stop.releaseCalls)
	assert.Equal(t, 1, authority.releaseCalls)
}

func TestVMExecSessionServerLeasePreservesStructuredCleanupEvidence(t *testing.T) {
	cleanupCause := errors.New("structured cleanup cause")
	stopFailure := errs.WrapMsg(
		errs.CodeVMAtomicFailed,
		"structured stop cleanup failure",
		cleanupCause,
		errs.WithDetails(map[string]any{"stop_evidence": "retained", "shared_evidence": "stop"}),
	)
	authorityFailure := errs.WrapMsg(
		errs.CodeUnauthorized,
		"structured authority cleanup failure",
		cleanupCause,
		errs.WithDetails(map[string]any{"authority_evidence": "retained", "shared_evidence": "authority"}),
	)
	tests := map[string]struct {
		stopFailure      error
		authorityFailure error
		wantFailures     []*errs.DomainError
		wantEvidenceKeys []string
		wantShared       string
	}{
		"stop": {
			stopFailure:      stopFailure,
			wantFailures:     []*errs.DomainError{stopFailure},
			wantEvidenceKeys: []string{"stop_evidence"},
			wantShared:       "stop",
		},
		"authority": {
			authorityFailure: authorityFailure,
			wantFailures:     []*errs.DomainError{authorityFailure},
			wantEvidenceKeys: []string{"authority_evidence"},
			wantShared:       "authority",
		},
		"simultaneous failures merge details with stop precedence": {
			stopFailure:      stopFailure,
			authorityFailure: authorityFailure,
			wantFailures:     []*errs.DomainError{stopFailure, authorityFailure},
			wantEvidenceKeys: []string{"stop_evidence", "authority_evidence"},
			wantShared:       "stop",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			stop := &fakeVMExecServerStop{releaseErr: tc.stopFailure}
			authority := &fakeVMExecServerAuthorityLease{
				admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
				releaseErr:     tc.authorityFailure,
			}
			lease := openFakeVMExecServerLease(t, mustFakeVMExecSessionServer(t, authority, stop))

			err := lease.Release(t.Context())

			require.Error(t, err)
			assert.ErrorIs(t, err, cleanupCause)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			for _, evidenceKey := range tc.wantEvidenceKeys {
				assert.Equal(t, "retained", domainErr.Details[evidenceKey])
			}
			assert.Equal(t, tc.wantShared, domainErr.Details["shared_evidence"])
			assert.Equal(t, true, domainErr.Details["vm_exec_session_cleanup_failed"])
			for _, wantFailure := range tc.wantFailures {
				assert.ErrorIs(t, err, wantFailure)
			}
		})
	}
}

func TestVMExecSessionServerBoundariesFailClosed(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	validCaller := VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID}
	validConnection := fakeVMExecServerConnection{
		bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
			return binder.Bind(ctx, 47)
		},
	}
	active := mustFakeVMExecSessionServer(t, &fakeVMExecServerAuthorityLease{
		admissionValue: vmExecSessionAdmission{relayStopBoottimeNS: testVMExecServerDeadline},
	}, &fakeVMExecServerStop{})
	var nilServer *VMExecSessionServer
	tests := map[string]struct {
		server     *VMExecSessionServer
		ctx        context.Context
		caller     VMExecSessionCaller
		request    VMExecSessionOpenRequest
		connection VMExecSessionConnection
		wantCause  error
	}{
		"nil_server": {
			server: nilServer, ctx: t.Context(), caller: validCaller,
			request: testVMExecServerRequest, connection: validConnection,
		},
		"nil_context": {
			server: active, caller: validCaller, request: testVMExecServerRequest, connection: validConnection,
		},
		"cancelled": {
			server: active, ctx: cancelled, caller: validCaller,
			request: testVMExecServerRequest, connection: validConnection, wantCause: context.Canceled,
		},
		"root_caller": {
			server: active, ctx: t.Context(), caller: VMExecSessionCaller{UID: 0, GID: testVMExecServerGID},
			request: testVMExecServerRequest, connection: validConnection,
		},
		"missing_gid": {
			server: active, ctx: t.Context(), caller: VMExecSessionCaller{UID: testVMExecServerUID},
			request: testVMExecServerRequest, connection: validConnection,
		},
		"invalid_request": {
			server: active, ctx: t.Context(), caller: validCaller,
			request: VMExecSessionOpenRequest{}, connection: validConnection,
		},
		"nil_connection": {server: active, ctx: t.Context(), caller: validCaller, request: testVMExecServerRequest},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			lease, err := tc.server.OpenSource(tc.ctx, tc.caller, tc.request, tc.connection)

			require.Error(t, err)
			assert.Nil(t, lease)
			if tc.wantCause != nil {
				assert.ErrorIs(t, err, tc.wantCause)
			}
		})
	}

	_, err := newVMExecSessionServerWithDeps(vmExecSessionServerDeps{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "dependencies are incomplete")
}

func TestVMExecSessionServerLeaseBoundariesFailClosedAndPartialReleaseIsSafe(t *testing.T) {
	var nilLease *VMExecSessionServerLease
	require.NoError(t, nilLease.Release(t.Context()))
	require.NoError(t, (&VMExecSessionServerLease{}).Release(t.Context()))

	assert.Error(t, nilLease.BindDestination(t.Context(), fakeVMExecServerConnection{}))
	_, err := nilLease.StartStopWait(t.Context())
	assert.Error(t, err)

	partial := &VMExecSessionServerLease{state: &vmExecSessionServerLeaseState{}}
	assert.Error(t, partial.Release(nil))
}

type fakeVMExecServerConnection struct {
	bind func(context.Context, VMExecSessionPeerBinder) error
}

func (connection fakeVMExecServerConnection) BindVMExecSessionPeer(
	ctx context.Context,
	binder VMExecSessionPeerBinder,
) error {
	if connection.bind == nil {
		return errors.New("connection binder is unavailable")
	}
	return connection.bind(ctx, binder)
}

type fakeVMExecServerAuthorityLease struct {
	// mu guards configured behavior, captured arguments, and call counts used by concurrent façade tests.
	mu                    sync.Mutex
	admissionValue        vmExecSessionAdmission
	admissionErr          error
	bindSourceErr         error
	bindSourceFunc        func(context.Context, int) error
	bindDestinationErr    error
	admissionFunc         func()
	destinationDescriptor int
	sourceBindCalls       int
	destinationBindCalls  int
	releaseCalls          int
	releaseErr            error
	releaseFunc           func(context.Context)
}

func (lease *fakeVMExecServerAuthorityLease) bindSource(ctx context.Context, descriptor int) error {
	lease.mu.Lock()
	lease.sourceBindCalls++
	bind := lease.bindSourceFunc
	err := lease.bindSourceErr
	lease.mu.Unlock()
	if bind != nil {
		return bind(ctx, descriptor)
	}
	return err
}

func (lease *fakeVMExecServerAuthorityLease) bindDestination(_ context.Context, descriptor int) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.destinationBindCalls++
	lease.destinationDescriptor = descriptor
	return lease.bindDestinationErr
}

func (lease *fakeVMExecServerAuthorityLease) admission() (vmExecSessionAdmission, error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.admissionFunc != nil {
		lease.admissionFunc()
	}
	return lease.admissionValue, lease.admissionErr
}

func (lease *fakeVMExecServerAuthorityLease) Release(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.releaseCalls++
	if lease.releaseFunc != nil {
		lease.releaseFunc(ctx)
	}
	return lease.releaseErr
}

type fakeVMExecServerStop struct {
	// mu guards configured behavior and call counts used by concurrent façade tests.
	mu           sync.Mutex
	waitCalls    int
	waitErr      error
	waitPending  bool
	releaseCalls int
	releaseErr   error
	releaseFunc  func(context.Context)
}

func (stop *fakeVMExecServerStop) startWait(context.Context) vmExecSessionServerStopWait {
	stop.mu.Lock()
	defer stop.mu.Unlock()
	stop.waitCalls++
	return newVMExecSessionServerStopWait(stop.waitPending, stop.waitErr)
}

func (stop *fakeVMExecServerStop) Release(ctx context.Context) error {
	stop.mu.Lock()
	defer stop.mu.Unlock()
	stop.releaseCalls++
	if stop.releaseFunc != nil {
		stop.releaseFunc(ctx)
	}
	return stop.releaseErr
}

func mustFakeVMExecSessionServer(
	t *testing.T,
	authority vmExecSessionServerAuthorityLease,
	stop vmExecSessionServerStop,
) *VMExecSessionServer {
	t.Helper()
	server, err := newVMExecSessionServerWithDeps(vmExecSessionServerDeps{
		reserve: func(
			context.Context,
			instanceCaller,
			string,
			string,
		) (vmExecSessionServerAuthorityLease, error) {
			return authority, nil
		},
		armStop: func(context.Context, uint64) (vmExecSessionServerStop, error) {
			return stop, nil
		},
	})
	require.NoError(t, err)
	return server
}

func openFakeVMExecServerLease(t *testing.T, server *VMExecSessionServer) *VMExecSessionServerLease {
	t.Helper()
	connection := fakeVMExecServerConnection{bind: func(ctx context.Context, binder VMExecSessionPeerBinder) error {
		return binder.Bind(ctx, 47)
	}}
	lease, err := server.OpenSource(
		t.Context(),
		VMExecSessionCaller{UID: testVMExecServerUID, GID: testVMExecServerGID},
		testVMExecServerRequest,
		connection,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	return lease
}
