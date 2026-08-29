package privileged

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

func TestRun_FailsClosedBeforeDispatch(t *testing.T) {
	tests := map[string]struct {
		args         []string
		effectiveUID int
		environment  map[string]string
		wantCode     errs.Code
		wantErr      string
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
		"rootless_unknown_action": {
			args:         []string{Marker, "run-command"},
			effectiveUID: 1000,
			wantCode:     errs.CodePrivilegeRequired,
			wantErr:      "requires effective UID 0",
		},
		"direct_root_unknown_action": {
			args:     []string{Marker, "run-command"},
			wantCode: errs.CodePrivilegeRequired,
			wantErr:  "missing SUDO_UID",
		},
		"sudo_unknown_action": {
			args:        []string{Marker, "run-command"},
			environment: map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"},
			wantCode:    errs.CodeValidationFailed,
			wantErr:     `unknown privileged action "run-command"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := run(
				context.Background(),
				tc.args,
				strings.NewReader(`{"command":"id"}`),
				testRuntimeDeps(tc.effectiveUID, tc.environment),
			)
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

	err := run(context.Background(), []string{Marker, "run-command"}, strings.NewReader("{}"), runtime)
	require.Error(t, err)
	assert.ErrorContains(t, err, "must run from "+infra.SystemBinaryPath)
	assert.NotContains(t, err.Error(), "unknown privileged action")
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
