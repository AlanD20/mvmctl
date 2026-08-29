package api

import (
	"context"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

func TestHostInstallSystemBinary_PreservesPartialSuccess(t *testing.T) {
	wantErr := errs.New(
		errs.CodeHostInitFailed,
		"system binary replaced but directory durability is uncertain",
		errs.WithDetails(map[string]any{
			"system_binary_replaced": true,
			"durability_uncertain":   true,
		}),
	)
	installer := &fakeSystemBinaryInstaller{changed: true, err: wantErr}
	op := &Operation{hostSystemBinaryInstaller: installer}

	changed, err := op.HostInstallSystemBinary(context.Background())
	require.ErrorIs(t, err, wantErr)
	assert.True(t, changed)
	assert.Equal(t, 1, installer.calls)
}

// Rationale: HostInit may perform read-only probes before sudoers reconciliation,
// but a sudoers failure must precede group membership or any other host mutation.
func TestHostInitSetup_SudoersFailurePrecedesHostMutations(t *testing.T) {
	originalOS := system.DefaultOS
	originalRunner := system.DefaultRunner
	t.Cleanup(func() {
		system.DefaultOS = originalOS
		system.DefaultRunner = originalRunner
	})
	system.DefaultOS = &testutil.FakeOS{
		CurrentFn: func() (*user.User, error) {
			return &user.User{Username: "operator", Uid: "1000", Gid: "1000"}, nil
		},
	}
	fakeRunner := &testutil.FakeRunner{}
	system.DefaultRunner = fakeRunner

	wantErr := errs.New(errs.CodePrivilegeSudoers, "injected sudoers configuration failure")
	configurer := &fakeHostSudoersConfigurer{err: wantErr}
	op := &Operation{hostSudoersConfigurer: configurer}

	changes, err := op.hostInitSetupEnvironment(context.Background(), "session", nil, "nftables")
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, changes)
	assert.Equal(t, 1, configurer.calls)
	assert.Empty(t, fakeRunner.Calls, "group, user, sysctl, and network mutations must not start")
}

type fakeSystemBinaryInstaller struct {
	changed bool
	err     error
	calls   int
}

func (f *fakeSystemBinaryInstaller) InstallSystemBinary(context.Context) (bool, error) {
	f.calls++
	return f.changed, f.err
}

type fakeHostSudoersConfigurer struct {
	changed bool
	err     error
	calls   int
}

func (f *fakeHostSudoersConfigurer) ConfigureSudoers(context.Context) (bool, error) {
	f.calls++
	return f.changed, f.err
}
