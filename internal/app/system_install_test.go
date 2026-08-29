package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestIsHostInstallSystemBinaryInvocation_ReservesSelectedCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "exact", args: []string{"host", "install-system"}, want: true},
		{name: "extra argument", args: []string{"host", "install-system", "unexpected"}, want: true},
		{name: "root flag before command", args: []string{"--debug", "host", "install-system"}, want: true},
		{name: "malformed root flag value", args: []string{"--debug", "false", "host", "install-system"}, want: true},
		{
			name: "repeated malformed root flag values",
			args: []string{"--debug", "false", "false", "host", "install-system"},
			want: true,
		},
		{name: "persistent flag inside command", args: []string{"host", "--verbose", "install-system"}, want: true},
		{
			name: "unknown flag value inside command",
			args: []string{"host", "--unknown", "value", "install-system"},
			want: true,
		},
		{name: "unknown trailing flag", args: []string{"host", "install-system", "--force"}, want: true},
		{name: "argument separator inside command", args: []string{"host", "--", "install-system"}, want: true},
		{name: "leading argument separator", args: []string{"--", "host", "install-system"}, want: true},
		{name: "different host command", args: []string{"host", "init"}, want: false},
		{name: "install words after another command", args: []string{"vm", "host", "install-system"}, want: false},
		{name: "help request remains ordinary", args: []string{"help", "host", "install-system"}, want: false},
		{
			name: "flagged help request remains ordinary",
			args: []string{"--debug", "help", "host", "install-system"},
			want: false,
		},
		{name: "reversed operation order", args: []string{"install-system", "host"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsHostInstallSystemBinaryInvocation(tt.args))
		})
	}
}

func TestHostInstallSystemBinary_ReplacesEnvironmentBeforeInstaller(t *testing.T) {
	store := newSystemInstallEnvironmentStore(map[string]string{
		"PATH":          "/tmp/attacker-bin",
		"HOME":          "/home/attacker",
		"MVM_CACHE_DIR": "/tmp/attacker-cache",
		"LD_PRELOAD":    "/tmp/inject.so",
	})
	installCalls := 0

	changed, err := hostInstallSystemBinary(
		context.Background(),
		[]string{"host", "install-system"},
		systemInstallDeps{
			clearEnvironment: store.clear,
			setEnvironment:   store.set,
			installSystemBinary: func(context.Context) (bool, error) {
				installCalls++
				assert.Equal(t, map[string]string{
					"PATH":   "/usr/sbin:/usr/bin:/sbin:/bin",
					"HOME":   "/root",
					"LANG":   "C",
					"LC_ALL": "C",
				}, store.values)
				return true, nil
			},
		},
	)

	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, store.clearCalls)
	assert.Equal(t, 1, installCalls)
}

func TestHostInstallSystemBinary_FailsClosedBeforeInstallerForMalformedArguments(t *testing.T) {
	tests := [][]string{
		{"host", "install-system", "unexpected"},
		{"--debug", "host", "install-system"},
		{"--debug", "false", "host", "install-system"},
		{"host", "--verbose", "install-system"},
		{"host", "--unknown", "value", "install-system"},
		{"host", "--", "install-system"},
	}

	for _, args := range tests {
		t.Run(args[0], func(t *testing.T) {
			installCalls := 0
			changed, err := hostInstallSystemBinary(context.Background(), args, systemInstallDeps{
				clearEnvironment: func() {},
				setEnvironment:   func(string, string) error { return nil },
				installSystemBinary: func(context.Context) (bool, error) {
					installCalls++
					return true, nil
				},
			})

			require.Error(t, err)
			assert.False(t, changed)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
			assert.Zero(t, installCalls)
		})
	}
}

func TestHostInstallSystemBinary_PreservesPartialSuccess(t *testing.T) {
	sentinel := errs.New(
		errs.CodeInternal,
		"directory sync failed",
		errs.WithDetails(map[string]any{
			"system_binary_replaced": true,
			"durability_uncertain":   true,
		}),
	)

	changed, err := hostInstallSystemBinary(
		context.Background(),
		[]string{"host", "install-system"},
		systemInstallDeps{
			clearEnvironment: func() {},
			setEnvironment:   func(string, string) error { return nil },
			installSystemBinary: func(context.Context) (bool, error) {
				return true, sentinel
			},
		},
	)

	assert.True(t, changed)
	assert.ErrorIs(t, err, sentinel)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["system_binary_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
}

func TestHostInstallSystemBinary_StopsWhenEnvironmentCannotBeSanitized(t *testing.T) {
	sentinel := errors.New("set environment rejected")
	installCalls := 0

	changed, err := hostInstallSystemBinary(
		context.Background(),
		[]string{"host", "install-system"},
		systemInstallDeps{
			clearEnvironment: func() {},
			setEnvironment: func(key, _ string) error {
				if key == "HOME" {
					return sentinel
				}
				return nil
			},
			installSystemBinary: func(context.Context) (bool, error) {
				installCalls++
				return true, nil
			},
		},
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.False(t, changed)
	assert.Equal(t, errs.CodeInternal, errs.AsDomainError(err).Code)
	assert.Zero(t, installCalls)
}

type systemInstallEnvironmentStore struct {
	values     map[string]string
	clearCalls int
}

func newSystemInstallEnvironmentStore(values map[string]string) *systemInstallEnvironmentStore {
	copyValues := make(map[string]string, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return &systemInstallEnvironmentStore{values: copyValues}
}

func (s *systemInstallEnvironmentStore) clear() {
	s.clearCalls++
	s.values = make(map[string]string)
}

func (s *systemInstallEnvironmentStore) set(key, value string) error {
	s.values[key] = value
	return nil
}
