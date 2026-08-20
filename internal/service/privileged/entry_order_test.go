package privileged

import (
	"context"
	"os/user"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
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

	executable := newExecutableFixture()
	originalOpen := executable.deps.open
	executable.deps.open = func(path string, flags int, mode uint32) (int, error) {
		calls = append(calls, "executable.open")
		return originalOpen(path, flags, mode)
	}
	authorization := memberAuthorizationDeps()
	originalLookupUser := authorization.lookupUserID
	authorization.lookupUserID = func(uid string) (*user.User, error) {
		calls = append(calls, "authorization.lookup_user")
		return originalLookupUser(uid)
	}

	err := run(context.Background(), []string{Marker, "unknown"}, strings.NewReader("{}"), runtimeDeps{
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
		executable:    executable.deps,
		authorization: authorization,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, `unknown privileged action "unknown"`)
	identityIndex := callIndex(calls, "identity.SUDO_GID")
	sanitizeStartIndex := callIndex(calls, "environment.clear")
	sanitizeEndIndex := callIndex(calls, "environment.set.LC_ALL")
	executableIndex := callIndex(calls, "executable.open")
	authorizationIndex := callIndex(calls, "authorization.lookup_user")
	for _, index := range []int{
		identityIndex, sanitizeStartIndex, sanitizeEndIndex, executableIndex, authorizationIndex,
	} {
		require.GreaterOrEqual(t, index, 0)
	}
	assert.Less(t, identityIndex, sanitizeStartIndex)
	assert.Less(t, sanitizeEndIndex, executableIndex)
	assert.Less(t, executableIndex, authorizationIndex)
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
	authorization.groupIDs = func(*user.User) ([]string, error) { return []string{"1000"}, nil }

	err := run(context.Background(), []string{Marker, "unknown"}, strings.NewReader("{}"), runtimeDeps{
		identity: identityDeps{
			effectiveUID: func() int { return 0 },
			lookupEnv:    mapLookup(map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"}),
		},
		environment:   newEnvironmentStore(nil).deps(),
		executable:    trustedExecutableDeps(),
		authorization: authorization,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not a current member of the "+infra.MVMUnixGroup+" group")
	assert.NotContains(t, err.Error(), "unknown privileged action")
}

func callIndex(calls []string, target string) int {
	for index, call := range calls {
		if call == target {
			return index
		}
	}
	return -1
}
