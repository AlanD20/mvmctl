package privileged

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestSanitizeEnvironment_ReplacesInheritedEnvironmentWithFixedAllowlist(t *testing.T) {
	store := newEnvironmentStore(map[string]string{
		"PATH":                   "/home/alice/bin:/tmp/tools",
		"HOME":                   "/home/alice",
		"MVM_CACHE_DIR":          "/tmp/attacker-cache",
		"LD_PRELOAD":             "/tmp/inject.so",
		"LD_LIBRARY_PATH":        "/tmp/libraries",
		"PYTHONPATH":             "/tmp/python",
		"GIT_CONFIG_GLOBAL":      "/tmp/gitconfig",
		"SYSTEMD_CONFIG_FILE":    "/tmp/systemd",
		"SUDO_UID":               "1000",
		"SUDO_GID":               "1000",
		"UNRELATED_CALLER_VALUE": "present",
	})

	err := sanitizeEnvironment(store.deps())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"PATH":   "/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME":   privilegedHOME,
		"LANG":   privilegedLocale,
		"LC_ALL": privilegedLocale,
	}, store.values)
	assert.Equal(t, 1, store.clearCalls)
}

func TestSanitizeEnvironment_FailsClosedWhenFixedValueCannotBeSet(t *testing.T) {
	store := newEnvironmentStore(map[string]string{"LD_PRELOAD": "/tmp/inject.so"})
	store.setErrorKey = "HOME"

	err := sanitizeEnvironment(store.deps())
	require.Error(t, err)
	assert.ErrorContains(t, err, "set fixed privileged environment HOME")
	assert.Equal(t, errs.CodeInternal, errs.AsDomainError(err).Code)
	assert.NotContains(t, store.values, "LD_PRELOAD")
}

type environmentStore struct {
	values      map[string]string
	clearCalls  int
	setErrorKey string
	calls       *[]string
}

func newEnvironmentStore(values map[string]string) *environmentStore {
	copyValues := make(map[string]string, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return &environmentStore{values: copyValues}
}

func (s *environmentStore) deps() environmentDeps {
	return environmentDeps{
		clear: func() {
			s.clearCalls++
			s.values = make(map[string]string)
			if s.calls != nil {
				*s.calls = append(*s.calls, "environment.clear")
			}
		},
		set: func(key, value string) error {
			if s.calls != nil {
				*s.calls = append(*s.calls, "environment.set."+key)
			}
			if key == s.setErrorKey {
				return errors.New("set rejected")
			}
			s.values[key] = value
			return nil
		},
	}
}
