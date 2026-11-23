package privileged

import (
	"context"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

// Rationale: Authorization lookups must not observe caller-controlled locale,
// loader, NSS, Kerberos, tool, HOME, PATH, or mvmctl variables.
func TestRun_SanitizesBeforeExecutableAndUIDBoundAuthorization(t *testing.T) {
	var calls []string
	environment := newEnvironmentStore(map[string]string{
		"HOME":          "/home/alice",
		"PATH":          "/tmp/tools",
		"LD_PRELOAD":    "/tmp/inject.so",
		"MVM_CACHE_DIR": "/tmp/state",
	})
	environment.calls = &calls

	executable := trustedExecutableDeps()
	originalPin := executable.pin
	executable.pin = func(ctx context.Context) (*system.PinnedExecutable, error) {
		calls = append(calls, "executable.pin")
		return originalPin(ctx)
	}
	authorization := memberAuthorizationDeps()
	originalLookupUser := authorization.lookupUserID
	authorization.lookupUserID = func(uid string) (*user.User, error) {
		calls = append(calls, "authorization.lookup_user")
		return originalLookupUser(uid)
	}
	admission := admissionDeps{acquire: func(context.Context) (operationAdmissionLease, error) {
		calls = append(calls, "admission.acquire")
		return &fakeOperationAdmissionLease{release: func(context.Context) error {
			calls = append(calls, "admission.release")
			return nil
		}}, nil
	}}

	endpoint := newReceiverEndpoint()
	err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtimeDeps{
		identity: identityDeps{
			effectiveUID: func() int {
				calls = append(calls, "identity.euid")
				return 0
			},
			lookupEnv: func(key string) (string, bool) {
				calls = append(calls, "identity."+key)
				return "1000", true
			},
		},
		environment:   environment.deps(),
		executable:    executable,
		admission:     admission,
		authorization: authorization,
		control:       validControlDeps(endpoint, &calls),
	})
	require.NoError(t, err)
	operationErr := decodeReceiverError(t, endpoint.output.Bytes(), "unknown")
	assert.ErrorContains(t, operationErr, `unknown privileged action "unknown"`)
	identityIndex := callIndex(calls, "identity.SUDO_GID")
	sanitizeStartIndex := callIndex(calls, "environment.clear")
	sanitizeEndIndex := callIndex(calls, "environment.set.LC_ALL")
	executableIndex := callIndex(calls, "executable.pin")
	admissionIndex := callIndex(calls, "admission.acquire")
	authorizationIndex := callIndex(calls, "authorization.lookup_user")
	releaseIndex := callIndex(calls, "admission.release")
	for _, index := range []int{
		identityIndex, sanitizeStartIndex, sanitizeEndIndex, executableIndex, admissionIndex, authorizationIndex,
		releaseIndex,
	} {
		require.GreaterOrEqual(t, index, 0)
	}
	assert.Less(t, identityIndex, sanitizeStartIndex)
	assert.Less(t, sanitizeEndIndex, executableIndex)
	assert.Less(t, executableIndex, admissionIndex)
	assert.Less(t, admissionIndex, authorizationIndex)
	assert.Less(t, authorizationIndex, releaseIndex)
	assert.Equal(t, map[string]string{
		"PATH":   "/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":   privilegedHOME,
		"LANG":   privilegedLocale,
		"LC_ALL": privilegedLocale,
	}, environment.values)
}

// Rationale: A valid sudo context and trusted executable cannot substitute for
// current membership in the mvm authorization group.
func TestRun_RejectsNonMemberBeforeActionDispatch(t *testing.T) {
	authorization := memberAuthorizationDeps()
	admissionHeld := false
	authorization.groupIDs = func(*user.User) ([]string, error) {
		assert.True(t, admissionHeld)
		return []string{"1000"}, nil
	}

	endpoint := newReceiverEndpoint()
	err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtimeDeps{
		identity: identityDeps{
			effectiveUID: func() int { return 0 },
			lookupEnv:    mapLookup(map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"}),
		},
		environment: newEnvironmentStore(nil).deps(),
		executable:  trustedExecutableDeps(),
		admission: admissionDeps{acquire: func(context.Context) (operationAdmissionLease, error) {
			admissionHeld = true
			return &fakeOperationAdmissionLease{release: func(context.Context) error {
				admissionHeld = false
				return nil
			}}, nil
		}},
		authorization: authorization,
		control:       validControlDeps(endpoint, nil),
	})
	require.NoError(t, err)
	operationErr := decodeReceiverError(t, endpoint.output.Bytes(), "unknown")
	assert.ErrorContains(t, operationErr, "is not a current member of the "+infra.MVMUnixGroup+" group")
	assert.NotContains(t, operationErr.Error(), "unknown privileged action")
	assert.False(t, admissionHeld)
}

func callIndex(calls []string, target string) int {
	for index, call := range calls {
		if call == target {
			return index
		}
	}
	return -1
}
