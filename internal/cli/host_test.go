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

	t.Run("json_output", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInfoFunc: func(_ context.Context) (*results.HostInfo, error) {
				return &results.HostInfo{Hostname: "json-host"}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		infoCmd, _, _ := cmd.Find([]string{"info"})
		require.NoError(t, infoCmd.Flags().Set("json", "true"))
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
	t.Run("skip_when_nil_result", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInitFunc: func(_ context.Context, _ event.OnProgressCallback) (any, error) {
				return nil, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		err := initCmd.RunE(initCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("error_propagates", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostInitFunc: func(_ context.Context, _ event.OnProgressCallback) (any, error) {
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
			HostInitFunc: func(_ context.Context, _ event.OnProgressCallback) (any, error) {
				return map[string]any{
					"changes": []*model.HostStateChangeItem{
						{
							Setting:       "ip_forward",
							Mechanism:     "sysctl",
							AppliedValue:  "1",
							OriginalValue: strPtr("0"),
						},
					},
					"user_added_to_group": false,
				}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		err := initCmd.RunE(initCmd, nil)
		assert.NoError(t, err)
	})
}

// --- Host status (via host status) ---
// Rationale: Host status shows current state vs expected. A broken status
// command prevents users from diagnosing host configuration issues.

func TestNewHostStatusCmd(t *testing.T) {
	t.Run("success_returns_status", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostStatusCheckFunc: func(_ context.Context) *results.HostStatusCheck {
				return &results.HostStatusCheck{
					KVMOK:       true,
					IPForwardOK: true,
					IPForward:   "1",
				}
			},
		}
		cmd := cli.NewHostCmd(mock)
		statusCmd, _, _ := cmd.Find([]string{"status"})
		err := statusCmd.RunE(statusCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("json_output", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostStatusCheckFunc: func(_ context.Context) *results.HostStatusCheck {
				return &results.HostStatusCheck{KVMOK: true, IPForwardOK: true, IPForward: "1"}
			},
		}
		cmd := cli.NewHostCmd(mock)
		statusCmd, _, _ := cmd.Find([]string{"status"})
		require.NoError(t, statusCmd.Flags().Set("json", "true"))
		err := statusCmd.RunE(statusCmd, nil)
		assert.NoError(t, err)
	})
}

// --- Host clean (via host clean) ---
// Rationale: Host clean removes network configuration. A broken clean command
// can leave orphaned bridges/TAPs or fail to clean up network state.

func TestNewHostCleanCmd(t *testing.T) {
	t.Run("force_clean_success_no_vms", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, nil
			},
			HostCleanFunc: func(_ context.Context) ([]string, error) {
				return []string{"Removed TAP device 'mvm-tap0'", "Removed firewall chains"}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		cleanCmd, _, _ := cmd.Find([]string{"clean"})
		require.NoError(t, cleanCmd.Flags().Set("force", "true"))
		err := cleanCmd.RunE(cleanCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("aborts_if_vms_running", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return []*model.VMItem{{Name: "vm-1", Status: model.VMStatusRunning}}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		cleanCmd, _, _ := cmd.Find([]string{"clean"})
		require.NoError(t, cleanCmd.Flags().Set("force", "true"))
		err := cleanCmd.RunE(cleanCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VMs still running")
	})

	t.Run("get_running_vms_error_does_not_block", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, errors.New("db error")
			},
			HostCleanFunc: func(_ context.Context) ([]string, error) {
				return []string{"Removed items"}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		cleanCmd, _, _ := cmd.Find([]string{"clean"})
		require.NoError(t, cleanCmd.Flags().Set("force", "true"))
		err := cleanCmd.RunE(cleanCmd, nil)
		assert.NoError(t, err)
	})
}

// --- Host reset (via host reset) ---
// Rationale: Host reset performs a full rollback. A broken reset command can
// leave the host in a partially rolled-back state requiring manual cleanup.

func TestNewHostResetCmd(t *testing.T) {
	t.Run("aborts_if_vms_running", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return []*model.VMItem{{Name: "vm-1", Status: model.VMStatusRunning}}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		require.NoError(t, resetCmd.Flags().Set("force", "true"))
		err := resetCmd.RunE(resetCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VMs still running")
	})

	t.Run("force_reset_success", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, nil
			},
			HostResetFunc: func(_ context.Context) ([]string, error) {
				return []string{"Removed firewall chains", "Reverted sysctl"}, nil
			},
		}
		cmd := cli.NewHostCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		require.NoError(t, resetCmd.Flags().Set("force", "true"))
		err := resetCmd.RunE(resetCmd, nil)
		assert.NoError(t, err)
	})
}

// --- formatChange ---
// Rationale: formatChange produces user-facing change descriptions for host
// init. Incorrect formatting would confuse users about what was modified.

func TestFormatChange(t *testing.T) {
	tests := []struct {
		name          string
		mechanism     string
		setting       string
		appliedValue  string
		originalValue string
		expected      string
	}{
		{
			name:         "iptables_save",
			mechanism:    "iptables_save",
			setting:      "iptables_rules",
			appliedValue: "/etc/iptables/rules.v4",
			expected:     "iptables rules saved \u2192 /etc/iptables/rules.v4",
		},
		{
			name:         "file_create",
			mechanism:    "file_create",
			setting:      "sudoers_dropin",
			appliedValue: "/etc/sudoers.d/mvm",
			expected:     "sudoers_dropin: created /etc/sudoers.d/mvm",
		},
		{
			name:         "groupadd",
			mechanism:    "groupadd",
			appliedValue: "mvm",
			expected:     "group 'mvm' created",
		},
		{
			name:         "usermod_two_parts",
			mechanism:    "usermod",
			appliedValue: "alice:mvm",
			expected:     "user 'alice' added to group 'mvm'",
		},
		{
			name:         "usermod_one_part",
			mechanism:    "usermod",
			appliedValue: "bob",
			expected:     "user 'bob' added to group 'bob'",
		},
		{
			name:          "sysctl_with_original",
			mechanism:     "sysctl",
			setting:       "net.ipv4.ip_forward",
			appliedValue:  "1",
			originalValue: "0",
			expected:      "net.ipv4.ip_forward: 0 \u2192 1",
		},
		{
			name:         "sysctl_without_original",
			mechanism:    "sysctl",
			setting:      "net.ipv4.ip_forward",
			appliedValue: "1",
			expected:     "net.ipv4.ip_forward: 0 \u2192 1",
		},
		{
			name:         "noop_iptables_chains",
			mechanism:    "noop",
			setting:      "iptables_chains",
			appliedValue: "MVM chains already exist",
			expected:     "iptables chains already exist \u2014 keeping existing chain state",
		},
		{
			name:         "modprobe_kernel_module",
			mechanism:    "modprobe",
			setting:      "kernel_module_load",
			appliedValue: "kvm",
			expected:     "loaded kernel module 'kvm'",
		},
		{
			name:         "network_create",
			mechanism:    "network_create",
			setting:      "default_network",
			appliedValue: "default",
			expected:     "Default network 'default' ready",
		},
		{
			name:          "fallback_repr",
			mechanism:     "unknown",
			setting:       "custom_setting",
			appliedValue:  "new_value",
			originalValue: "old_value",
			expected:      "custom_setting: \"old_value\" \u2192 \"new_value\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cli.FormatChange(tt.mechanism, tt.setting, tt.appliedValue, tt.originalValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- abortIfVMsRunning ---
// Rationale: abortIfVMsRunning is a safety check before destructive host
// operations. A broken check could allow cleanup while VMs are running.

func TestAbortIfVMsRunning(t *testing.T) {
	t.Run("no_running_vms_returns_nil", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, nil
			},
		}
		err := cli.AbortIfVMsRunning(context.Background(), mock)
		assert.NoError(t, err)
	})

	t.Run("running_vms_returns_error", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return []*model.VMItem{{Name: "vm-1"}}, nil
			},
		}
		err := cli.AbortIfVMsRunning(context.Background(), mock)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "VMs still running")
	})

	t.Run("api_error_returns_nil", func(t *testing.T) {
		mock := &testutil.MockHostAPI{
			HostGetRunningVMsFunc: func(_ context.Context) ([]*model.VMItem, error) {
				return nil, errors.New("db error")
			},
		}
		err := cli.AbortIfVMsRunning(context.Background(), mock)
		assert.NoError(t, err)
	})
}

// --- Helper ---

func strPtr(s string) *string { return &s }
