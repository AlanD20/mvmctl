package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/results"
)

// --- NewHostCmd ---
// Rationale: NewHostCmd is the entry point for all host CLI operations.
// Missing subcommands silently disable host management without error.

func TestNewHostCmd(t *testing.T) {
	mock := &testutil.MockHostAPI{}
	cmd := cli.NewHostCmd(mock)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "install-system", hasAlias: false},
		{use: "init", hasAlias: false},
		{use: "status", hasAlias: false},
		{use: "info", hasAlias: false},
		{use: "clean", hasAlias: false},
		{use: "reset", hasAlias: false},
	}

	assert.Equal(t, "host", cmd.Use, "root command must be 'host'")
	assert.Equal(t, "Host configuration", cmd.Short)

	for _, sc := range expectedSubcommands {
		t.Run("has_subcommand_"+sc.use, func(t *testing.T) {
			sub, _, err := cmd.Find([]string{sc.use})
			require.NoError(t, err, "subcommand %q not found", sc.use)
			require.NotNil(t, sub, "subcommand %q is nil", sc.use)
			if sc.hasAlias {
				aliasCmd, _, aliasErr := cmd.Find([]string{sc.alias})
				require.NoError(t, aliasErr, "alias %q not found for %q", sc.alias, sc.use)
				require.NotNil(t, aliasCmd, "alias %q is nil for %q", sc.alias, sc.use)
			}
		})
	}

	t.Run("no_extra_subcommands", func(t *testing.T) {
		expected := make(map[string]bool)
		for _, sc := range expectedSubcommands {
			expected[sc.use] = true
		}
		for _, sub := range cmd.Commands() {
			assert.True(t, expected[sub.Name()], "unexpected subcommand: %s", sub.Name())
		}
	})
}

// Rationale: Installing the trusted system image is an explicit administrator
// bootstrap operation, not an implicit side effect of host initialization.
func TestNewHostCmd_InstallSystemBinary(t *testing.T) {
	wantErr := errors.New("durability uncertain")
	hostAPI := &installSystemHostAPI{
		MockHostAPI: &testutil.MockHostAPI{},
		changed:     true,
		err:         wantErr,
	}
	cmd := cli.NewHostCmd(hostAPI)

	installCmd, _, err := cmd.Find([]string{"install-system"})
	require.NoError(t, err)
	require.Equal(t, "install-system", installCmd.Use)
	err = installCmd.RunE(installCmd, nil)
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, hostAPI.calls)
}

type installSystemHostAPI struct {
	*testutil.MockHostAPI
	changed bool
	err     error
	calls   int
}

func (f *installSystemHostAPI) HostInstallSystemBinary(context.Context) (bool, error) {
	f.calls++
	return f.changed, f.err
}

// --- Host info (via host info) ---
// Rationale: Host info shows hardware and capacity. A broken info command
// prevents users from seeing available resources for VM placement.

func TestNewHostInfoCmd(t *testing.T) {
	t.Run("success_returns_host_info", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInfoFunc: func(_ context.Context) (*results.HostInfo, error) {
				return &results.HostInfo{
					Hostname: "test-host",
				}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		infoCmd, _, _ := cmd.Find([]string{"info"})
		err := infoCmd.RunE(infoCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("refresh_flag", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostRefreshCapacityFunc: func(_ context.Context) (*results.HostInfo, error) {
				return &results.HostInfo{
					Hostname: "refreshed-host",
				}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		infoCmd, _, _ := cmd.Find([]string{"info"})
		require.NoError(t, infoCmd.Flags().Set("refresh", "true"))
		err := infoCmd.RunE(infoCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("api_error_propagates", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInfoFunc: func(_ context.Context) (*results.HostInfo, error) {
				return nil, errors.New("host detection failed")
			},
		}
		cmd := cli.NewHostCmd(mock)
		infoCmd, _, _ := cmd.Find([]string{"info"})
		err := infoCmd.RunE(infoCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host detection failed")
	})

	t.Run("context_cancelled_propagates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gotCancelled := false
		mock := &testutil.MockHostAPI{
			HostInfoFunc: func(c context.Context) (*results.HostInfo, error) {
				if c.Err() != nil {
					gotCancelled = true
				}
				return nil, ctx.Err()
			},
		}
		cmd := cli.NewHostCmd(mock)
		infoCmd, _, _ := cmd.Find([]string{"info"})
		infoCmd.SetContext(ctx)
		err := infoCmd.RunE(infoCmd, nil)
		require.Error(t, err)
		assert.True(t, gotCancelled, "cancelled context should be visible to mock")
	})
}

// --- Host init (via host init) ---
// Rationale: Host init applies host configuration changes. A broken init
// command leaves the host unconfigured and VMs unable to start.

func TestNewHostInitCmd(t *testing.T) {
	t.Run("empty_result_returns_success", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInitFunc: func(_ context.Context, _ event.OnProgressCallback) (*results.HostInit, error) {
				return &results.HostInit{}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		err := initCmd.RunE(initCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("error_propagates", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInitFunc: func(_ context.Context, _ event.OnProgressCallback) (*results.HostInit, error) {
				return nil, errors.New("privilege check failed")
			},
		}
		cmd := cli.NewHostCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		err := initCmd.RunE(initCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "privilege check failed")
	})

	t.Run("with_changes_returns_success", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInitFunc: func(_ context.Context, _ event.OnProgressCallback) (*results.HostInit, error) {
				return &results.HostInit{
					Changes: []*model.HostStateChangeItem{
						{
							Setting:       "ip_forward",
							Mechanism:     "sysctl",
							AppliedValue:  "1",
							OriginalValue: strPtr("0"),
						},
					},
					UserAddedToGroup: false,
				}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		output := capturePolicyStdout(t, func() error { return initCmd.RunE(initCmd, nil) })
		assert.Contains(t, output, "ip_forward: 0 → 1")
	})
}

// --- Host status (via host status) ---
// Rationale: Host status shows current state vs expected. A broken status
// command prevents users from diagnosing host configuration issues.

func TestNewHostStatusCmd(t *testing.T) {
	t.Run("unavailable_policy_returns_error_before_rendering", func(t *testing.T) {
		policyErr := errors.New("root policy observation unavailable")
		mock := &testutil.MockHostAPI{
			HostStatusCheckFunc: func(context.Context) (*results.HostStatusCheck, error) {
				return nil, policyErr
			},
		}
		cmd := cli.NewHostCmd(mock)
		statusCmd, _, err := cmd.Find([]string{"status"})
		require.NoError(t, err)
		assert.ErrorIs(t, statusCmd.RunE(statusCmd, nil), policyErr)
	})
	t.Run("success_returns_status", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostStatusCheckFunc: func(_ context.Context) (*results.HostStatusCheck, error) {
				return &results.HostStatusCheck{
					KVMOK:       true,
					IPForwardOK: true,
					IPForward:   "1",
				}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		statusCmd, _, _ := cmd.Find([]string{"status"})
		err := statusCmd.RunE(statusCmd, nil)
		assert.NoError(t, err)
	})
}

// --- Host clean (via host clean) ---
// Rationale: Host clean removes network configuration. A broken clean command
// can leave orphaned bridges/TAPs or fail to clean up network state.

func TestNewHostCleanCmd(t *testing.T) {
	t.Run("force_clean_success_no_vms", func(t *testing.T) {
		cleaned := false
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, nil
			},
			HostCleanFunc: func(_ context.Context) ([]string, error) {
				cleaned = true
				return []string{"Removed TAP device 'mvm-tap0'", "Removed firewall chains"}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		cleanCmd, _, _ := cmd.Find([]string{"clean"})
		require.NoError(t, cleanCmd.Flags().Set("force", "true"))
		err := cleanCmd.RunE(cleanCmd, nil)
		assert.NoError(t, err)
		assert.True(t, cleaned)
	})

	t.Run("aborts_if_vms_running", func(t *testing.T) {
		cleaned := false
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return []*model.VMItem{{Name: "vm-1", Status: model.VMStatusRunning}}, nil
			},
			HostCleanFunc: func(_ context.Context) ([]string, error) {
				cleaned = true
				return nil, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		cleanCmd, _, _ := cmd.Find([]string{"clean"})
		require.NoError(t, cleanCmd.Flags().Set("force", "true"))
		err := cleanCmd.RunE(cleanCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VMs still running")
		assert.False(t, cleaned, "clean must not run while a VM is running")
	})

	t.Run("get_running_vms_error_blocks_clean", func(t *testing.T) {
		wantErr := errors.New("db error")
		cleaned := false
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, wantErr
			},
			HostCleanFunc: func(_ context.Context) ([]string, error) {
				cleaned = true
				return nil, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		cleanCmd, _, _ := cmd.Find([]string{"clean"})
		require.NoError(t, cleanCmd.Flags().Set("force", "true"))
		err := cleanCmd.RunE(cleanCmd, nil)
		require.ErrorIs(t, err, wantErr)
		assert.False(t, cleaned, "clean must not run when VM state is unknown")
	})
}

// --- Host reset (via host reset) ---
// Rationale: Host reset performs a full rollback. A broken reset command can
// leave the host in a partially rolled-back state requiring manual cleanup.

func TestNewHostResetCmd(t *testing.T) {
	t.Run("aborts_if_vms_running", func(t *testing.T) {
		reset := false
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return []*model.VMItem{{Name: "vm-1", Status: model.VMStatusRunning}}, nil
			},
			HostResetFunc: func(_ context.Context) ([]string, error) {
				reset = true
				return nil, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		require.NoError(t, resetCmd.Flags().Set("force", "true"))
		err := resetCmd.RunE(resetCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VMs still running")
		assert.False(t, reset, "reset must not run while a VM is running")
	})

	t.Run("force_reset_success", func(t *testing.T) {
		reset := false
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, nil
			},
			HostResetFunc: func(_ context.Context) ([]string, error) {
				reset = true
				return []string{"Removed firewall chains", "Reverted sysctl"}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		require.NoError(t, resetCmd.Flags().Set("force", "true"))
		err := resetCmd.RunE(resetCmd, nil)
		assert.NoError(t, err)
		assert.True(t, reset)
	})
}

// --- Helper ---

func strPtr(s string) *string { return &s }
