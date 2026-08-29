package privileged

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

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
	deps := trustedExecutableDeps()
	deps.executable = func() (string, error) { return "/tmp/user-owned-mvm", nil }
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
