package privileged

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestInvokingIdentity_RequiresSudoRootContext(t *testing.T) {
	tests := map[string]struct {
		effectiveUID int
		environment  map[string]string
		wantErr      string
	}{
		"rootless": {
			effectiveUID: 1000,
			environment:  map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"},
			wantErr:      "requires effective UID 0",
		},
		"direct_root_without_sudo": {
			environment: map[string]string{},
			wantErr:     "missing SUDO_UID",
		},
		"root_invoker": {
			environment: map[string]string{"SUDO_UID": "0", "SUDO_GID": "1000"},
			wantErr:     "SUDO_UID must identify a non-root user",
		},
		"root_group": {
			environment: map[string]string{"SUDO_UID": "1000", "SUDO_GID": "0"},
			wantErr:     "SUDO_GID must identify a non-root group",
		},
		"negative_uid": {
			environment: map[string]string{"SUDO_UID": "-1", "SUDO_GID": "1000"},
			wantErr:     "invalid SUDO_UID",
		},
		"non_numeric_gid": {
			environment: map[string]string{"SUDO_UID": "1000", "SUDO_GID": "users"},
			wantErr:     "invalid SUDO_GID",
		},
		"overflow_uid": {
			environment: map[string]string{"SUDO_UID": "4294967296", "SUDO_GID": "1000"},
			wantErr:     "invalid SUDO_UID",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := invokingIdentity(identityDeps{
				effectiveUID: func() int { return tc.effectiveUID },
				lookupEnv:    mapLookup(tc.environment),
			})
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, errs.CodePrivilegeRequired, errs.AsDomainError(err).Code)
		})
	}
}

func TestInvokingIdentity_AcceptsNonRootSudoCaller(t *testing.T) {
	got, err := invokingIdentity(identityDeps{
		effectiveUID: func() int { return 0 },
		lookupEnv: mapLookup(map[string]string{
			"SUDO_UID": "1000",
			"SUDO_GID": "1001",
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(1000), got.uid)
	assert.Equal(t, uint32(1001), got.gid)
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
