package privileged

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

func TestRunLeavesVMExecSessionActionUnregisteredAndRequestUnread(t *testing.T) {
	requestBytes := []byte("must remain unread")
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(requestBytes)
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = validControlDeps(endpoint, nil)

	err := run(
		t.Context(),
		[]string{Marker, vmexecsessionwire.Action},
		endpoint,
		runtime,
	)

	require.NoError(t, err)
	assert.Zero(t, endpoint.readCalls)
	assert.Equal(t, int64(0), endpoint.input.Size()-int64(endpoint.input.Len()))
	operationErr := decodeReceiverError(t, endpoint.output.Bytes(), vmexecsessionwire.Action)
	assert.ErrorContains(t, operationErr, `unknown privileged action "`+vmexecsessionwire.Action+`"`)
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
