package privileged

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

type fakeVMExecSessionServer struct {
	caller     jailer.VMExecSessionCaller
	request    jailer.VMExecSessionOpenRequest
	connection jailer.VMExecSessionConnection
	lease      *jailer.VMExecSessionServerLease
	err        error
}

func (server *fakeVMExecSessionServer) OpenSource(
	_ context.Context,
	caller jailer.VMExecSessionCaller,
	request jailer.VMExecSessionOpenRequest,
	connection jailer.VMExecSessionConnection,
) (*jailer.VMExecSessionServerLease, error) {
	server.caller = caller
	server.request = request
	server.connection = connection
	return server.lease, server.err
}

type fakeVMExecSessionStopWait struct {
	pending bool
	err     error
	events  *[]string
}

func (wait *fakeVMExecSessionStopWait) Pending() bool { return wait.pending }

func (wait *fakeVMExecSessionStopWait) Wait() error {
	*wait.events = append(*wait.events, "wait")
	return wait.err
}

func TestVMExecSessionEffectsMapAuthenticatedCallerAndClosedRequest(t *testing.T) {
	sentinel := errs.New(errs.CodeProcessError, "open source sentinel")
	server := &fakeVMExecSessionServer{
		lease: &jailer.VMExecSessionServerLease{},
		err:   sentinel,
	}
	effects := newVMExecSessionHandlerEffects(server)
	connection := vmExecSessionConnection{retained: &vmExecRetainedConnection{
		owner: &vmExecReceivedConnectionOwner{fd: 41},
	}}

	lease, err := effects.openSource(
		t.Context(),
		callerIdentity{uid: 1001, gid: 1002},
		vmexecsessionwire.OpenSource{
			SourceVMID:      testVMExecHandlerSource,
			DestinationVMID: testVMExecHandlerDestination,
		},
		connection,
	)

	assert.Same(t, sentinel, err)
	require.NotNil(t, lease)
	wantCaller := jailer.VMExecSessionCaller{UID: 1001, GID: 1002}
	if diff := cmp.Diff(wantCaller, server.caller); diff != "" {
		t.Errorf("VM-exec caller mismatch (-want +got):\n%s", diff)
	}
	wantRequest := jailer.VMExecSessionOpenRequest{
		SourceVMID:      testVMExecHandlerSource,
		DestinationVMID: testVMExecHandlerDestination,
	}
	if diff := cmp.Diff(wantRequest, server.request); diff != "" {
		t.Errorf("VM-exec request mismatch (-want +got):\n%s", diff)
	}
	gotConnection, ok := server.connection.(vmExecSessionConnection)
	require.True(t, ok)
	assert.Same(t, connection.retained.owner, gotConnection.retained.owner)
}

func TestFinishVMExecSessionStopWaitSignalsOnlyPendingWait(t *testing.T) {
	sentinel := errs.New(errs.CodeProcessError, "stop sentinel")
	tests := map[string]struct {
		pending   bool
		wantReady int
	}{
		"immediate result is joined without readiness":      {pending: false, wantReady: 0},
		"pending result signals readiness once before join": {pending: true, wantReady: 1},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			events := make([]string, 0, 2)
			readyCalls := 0
			wait := &fakeVMExecSessionStopWait{pending: test.pending, err: sentinel, events: &events}

			err := finishVMExecSessionStopWait(wait, func() {
				readyCalls++
				events = append(events, "ready")
			})

			assert.Same(t, sentinel, err)
			assert.Equal(t, test.wantReady, readyCalls)
			wantEvents := []string{"wait"}
			if test.pending {
				wantEvents = []string{"ready", "wait"}
			}
			if diff := cmp.Diff(wantEvents, events); diff != "" {
				t.Errorf("stop wait events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVMExecSessionLeaseAdapterRejectsMissingReadyBeforeStartingWait(t *testing.T) {
	adapter := &vmExecSessionServerLeaseAdapter{lease: &jailer.VMExecSessionServerLease{}}

	err := adapter.waitStop(t.Context(), nil)

	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeInternal, domainErr.Code)
	assert.ErrorContains(t, domainErr, "readiness handler is unavailable")
}

func TestVMExecSessionConnectionFailsClosed(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := map[string]struct {
		ctx        context.Context
		connection vmExecSessionConnection
		wantCode   errs.Code
		wantErr    string
	}{
		"missing owner": {
			ctx:        t.Context(),
			connection: vmExecSessionConnection{},
			wantCode:   errs.CodeValidationFailed,
			wantErr:    "connection is required",
		},
		"nil context": {
			connection: vmExecSessionConnection{retained: &vmExecRetainedConnection{
				owner: &vmExecReceivedConnectionOwner{fd: 42},
			}},
			wantCode: errs.CodeValidationFailed,
			wantErr:  "context is required",
		},
		"cancelled context": {
			ctx: cancelled,
			connection: vmExecSessionConnection{retained: &vmExecRetainedConnection{
				owner: &vmExecReceivedConnectionOwner{fd: 43},
			}},
			wantCode: errs.CodeProcessError,
			wantErr:  "cancelled",
		},
		"closed owner": {
			ctx: t.Context(),
			connection: vmExecSessionConnection{retained: &vmExecRetainedConnection{
				owner: &vmExecReceivedConnectionOwner{fd: 44, closed: true},
			}},
			wantCode: errs.CodeValidationFailed,
			wantErr:  "closed",
		},
		"invalid descriptor": {
			ctx: t.Context(),
			connection: vmExecSessionConnection{retained: &vmExecRetainedConnection{
				owner: &vmExecReceivedConnectionOwner{fd: -1},
			}},
			wantCode: errs.CodeValidationFailed,
			wantErr:  "descriptor is invalid",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.connection.BindVMExecSessionPeer(test.ctx, jailer.VMExecSessionPeerBinder{})

			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, test.wantCode, domainErr.Code)
			assert.ErrorContains(t, domainErr, test.wantErr)
		})
	}
}
