package privileged

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

func TestRunDispatchesVMExecSessionWithoutOuterResponse(t *testing.T) {
	events := make([]string, 0, 3)
	endpoint, handlerDeps, sourceFD, destinationFD := newVMExecSessionHandlerFixture(
		t,
		vmExecSessionFrames(t, true, false, false),
		&events,
	)
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = vmExecSessionRunControlDeps(handlerDeps.control)
	runtime.vmExecSession = vmExecSessionHandlerEffects{openSource: func(
		_ context.Context,
		caller callerIdentity,
		request vmexecsessionwire.OpenSource,
		_ vmExecSessionConnection,
	) (vmExecSessionHandlerLease, error) {
		assert.Equal(t, uint32(1000), caller.uid)
		assert.Equal(t, uint32(1000), caller.gid)
		assert.Equal(t, testVMExecHandlerSource, request.SourceVMID)
		assert.Equal(t, testVMExecHandlerDestination, request.DestinationVMID)
		return nil, errs.New(errs.CodeProcessError, "registered VM-exec effect stopped")
	}}
	runtime.vmExecConnectionReader = handlerDeps.connectionReader

	err := run(
		t.Context(),
		[]string{Marker, vmexecsessionwire.Action},
		endpoint,
		runtime,
	)

	require.NoError(t, err)
	responses := bytes.NewReader(endpoint.responseBytes())
	outcome, decodeErr := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
	require.NoError(t, decodeErr)
	assert.False(t, outcome.Released())
	operationErr := outcome.OperationError()
	require.NotNil(t, operationErr)
	assert.Equal(t, errs.CodeProcessError, operationErr.Code)
	assert.Equal(t, errs.ClassInternal, operationErr.Class)
	assert.Zero(t, responses.Len())
	assert.Equal(t, 1, bytes.Count(endpoint.responseBytes(), []byte("MVMXSES1")))
	assert.NotContains(t, string(endpoint.responseBytes()), "MVMRES01")
	assertFDClosed(t, sourceFD)
	assertFDOpen(t, destinationFD)
	require.NoError(t, unix.Close(destinationFD))
}

func vmExecSessionRunControlDeps(handler controlDeps) controlDeps {
	return controlDeps{
		descriptorFlags:       func(context.Context, uintptr) (int, error) { return unix.FD_CLOEXEC, nil },
		setDescriptorFlags:    func(context.Context, uintptr, int) error { return nil },
		descriptorStatusFlags: func(context.Context, uintptr) (int, error) { return 0, nil },
		socketDomain:          func(context.Context, int) (int, error) { return unix.AF_UNIX, nil },
		socketType:            func(context.Context, int) (int, error) { return unix.SOCK_STREAM, nil },
		peerCredentials: func(context.Context, int) (*unix.Ucred, error) {
			return &unix.Ucred{Pid: 1, Uid: 1000, Gid: 1000}, nil
		},
		shutdownWrite: handler.shutdownWrite,
		now:           handler.now,
	}
}

func TestRunReleasesPinnedExecutableAndPreservesDeliveryFailure(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("deliver response failed")
	cleanupCause := errors.New("release executable failed")
	ctx, cancel := context.WithCancel(t.Context())
	endpoint := newReceiverEndpoint()
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = validControlDeps(endpoint, nil)
	live := false
	runtime.executable.identity = func(context.Context, *system.PinnedExecutable) (
		system.PinnedExecutableIdentity,
		error,
	) {
		live = true
		return system.PinnedExecutableIdentity{SHA256: "test-authority"}, nil
	}
	endpoint.writeFunc = func([]byte) (int, error) {
		assert.True(t, live)
		cancel()
		return 0, primaryCause
	}
	releaseCalls := 0
	runtime.executable.release = func(cleanupCtx context.Context, _ *system.PinnedExecutable) error {
		releaseCalls++
		assert.NoError(t, cleanupCtx.Err())
		live = false
		return cleanupCause
	}

	err := run(ctx, []string{Marker, "unknown"}, endpoint, runtime)
	require.Error(t, err)
	assert.Equal(t, 1, releaseCalls)
	assert.False(t, live)
	assert.ErrorIs(t, err, primaryCause)
	assert.ErrorIs(t, err, cleanupCause)
}

func TestRun_FailsClosedBeforeDispatch(t *testing.T) {
	tests := map[string]struct {
		args     []string
		wantCode errs.Code
		wantErr  string
	}{
		"unsupported_protocol": {
			args:     []string{markerPrefix + "2", "vm-cleanup"},
			wantCode: errs.CodeValidationFailed,
			wantErr:  "unsupported privileged protocol",
		},
		"extra_argv_cannot_reach_public_cli": {
			args:     []string{Marker, "vm-cleanup", "host", "reset"},
			wantCode: errs.CodeValidationFailed,
			wantErr:  "requires exactly one action",
		},
		"invalid_action_grammar": {
			args:     []string{Marker, "run/command"},
			wantCode: errs.CodeValidationFailed,
			wantErr:  "1-64 byte ASCII identifier",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			runtime := testRuntimeDeps(0, nil)
			runtime.control = validControlDeps(endpoint, nil)
			err := run(context.Background(), tc.args, endpoint, runtime)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, tc.wantCode, errs.AsDomainError(err).Code)
		})
	}
}

// Rationale: Caller identity alone does not establish code integrity; a root
// dispatcher reached through a user-replaceable executable remains root code execution.
func TestRun_VerifiesSystemExecutableBeforeActionDispatch(t *testing.T) {
	deps := rejectedExecutableDeps("privileged protocol must run from " + infra.SystemBinaryPath)
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.executable = deps
	endpoint := newReceiverEndpoint()
	runtime.control = validControlDeps(endpoint, nil)

	err := run(context.Background(), []string{Marker, "run-command"}, endpoint, runtime)
	require.NoError(t, err)
	operationErr := decodeReceiverError(t, endpoint.output.Bytes(), "run-command")
	assert.ErrorContains(t, operationErr, "must run from "+infra.SystemBinaryPath)
	assert.NotContains(t, operationErr.Error(), "unknown privileged action")
}

func testRuntimeDeps(effectiveUID int, identityEnvironment map[string]string) runtimeDeps {
	return runtimeDeps{
		identity: identityDeps{
			effectiveUID: func() int { return effectiveUID },
			lookupEnv:    mapLookup(identityEnvironment),
		},
		environment:   newEnvironmentStore(nil).deps(),
		executable:    trustedExecutableDeps(),
		authorization: memberAuthorizationDeps(),
	}
}
