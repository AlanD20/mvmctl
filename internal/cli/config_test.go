package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

// ─── NewConfigCmd ──────────────────────────────────────────────────────────
// Rationale: NewConfigCmd is the entry point for all config CLI operations.
// Missing subcommands silently disable config management without error.

func TestNewConfigCmd(t *testing.T) {
	mock := &testutil.MockConfigAPI{}
	cmd := cli.NewConfigCmd(mock)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "get", hasAlias: false},
		{use: "set", hasAlias: false},
		{use: "reset", hasAlias: false},
		{use: "ls", hasAlias: true, alias: "list"},
	}

	assert.Equal(t, "config", cmd.Use, "root command must be 'config'")
	assert.Equal(t, "Configuration management", cmd.Short)

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
			if sc.hasAlias {
				expected[sc.alias] = true
			}
		}
		for _, sub := range cmd.Commands() {
			assert.True(t, expected[sub.Name()], "unexpected subcommand: %s", sub.Name())
		}
	})
}

// ─── Config get (via config get) ───────────────────────────────────────────
// Rationale: Config get retrieves settings. A broken get command prevents
// users from inspecting their configuration or debugging misconfiguration.

func TestNewConfigGetCmd(t *testing.T) {
	t.Run("get_category_lists_settings", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigGetFunc: func(_ context.Context, category, key string) (any, error) {
				return map[string]model.SettingInfo{
					"setting1": {Type: "string", Default: "val1"},
					"setting2": {Type: "int", Default: 42},
				}, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		getCmd, _, _ := cmd.Find([]string{"get"})
		err := getCmd.RunE(getCmd, []string{"defaults.vm"})
		assert.NoError(t, err)
	})

	t.Run("get_category_and_key_returns_value", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigGetFunc: func(_ context.Context, category, key string) (any, error) {
				return "some-value", nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		getCmd, _, _ := cmd.Find([]string{"get"})
		err := getCmd.RunE(getCmd, []string{"defaults.vm", "vcpu_count"})
		assert.NoError(t, err)
	})

	t.Run("nil_value_shows_default_note", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigGetFunc: func(_ context.Context, category, key string) (any, error) {
				return nil, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		getCmd, _, _ := cmd.Find([]string{"get"})
		err := getCmd.RunE(getCmd, []string{"defaults.vm", "vcpu_count"})
		assert.NoError(t, err)
	})

	t.Run("api_error_propagates", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigGetFunc: func(_ context.Context, category, key string) (any, error) {
				return nil, errors.New("unknown category")
			},
		}
		cmd := cli.NewConfigCmd(mock)
		getCmd, _, _ := cmd.Find([]string{"get"})
		err := getCmd.RunE(getCmd, []string{"invalid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown category")
	})

	t.Run("context_cancelled_propagates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gotCancelled := false
		mock := &testutil.MockConfigAPI{
			ConfigGetFunc: func(c context.Context, category, key string) (any, error) {
				if c.Err() != nil {
					gotCancelled = true
				}
				return nil, ctx.Err()
			},
		}
		cmd := cli.NewConfigCmd(mock)
		getCmd, _, _ := cmd.Find([]string{"get"})
		getCmd.SetContext(ctx)
		err := getCmd.RunE(getCmd, []string{"defaults.vm"})
		require.Error(t, err)
		assert.True(t, gotCancelled, "cancelled context should be visible to mock")
	})
}

// ─── Config set (via config set) ───────────────────────────────────────────
// Rationale: Config set modifies user configuration. A broken set command can
// silently corrupt configuration or fail to apply user preferences.

func TestNewConfigSetCmd(t *testing.T) {
	t.Run("set_value_success", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigSetFunc: func(_ context.Context, category, key string, value any) error {
				return nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		setCmd, _, _ := cmd.Find([]string{"set"})
		err := setCmd.RunE(setCmd, []string{"defaults.vm", "vcpu_count", "4"})
		assert.NoError(t, err)
	})

	t.Run("api_error_propagates", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigSetFunc: func(_ context.Context, category, key string, value any) error {
				return errors.New("invalid value")
			},
		}
		cmd := cli.NewConfigCmd(mock)
		setCmd, _, _ := cmd.Find([]string{"set"})
		err := setCmd.RunE(setCmd, []string{"defaults.vm", "vcpu_count", "999"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid value")
	})
}

// ─── Config list (via config ls) ───────────────────────────────────────────
// Rationale: Config list shows all available settings. A broken list command
// prevents users from discovering what settings can be configured.

func TestNewConfigListCmd(t *testing.T) {
	t.Run("list_all_settings", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigListAllFunc: func(_ context.Context) (map[string]map[string]model.SettingInfo, error) {
				return map[string]map[string]model.SettingInfo{
					"defaults.vm": {
						"vcpu_count": {Type: "int", Default: 2},
					},
					"settings": {
						"firewall_backend": {Type: "string", Default: "nftables"},
					},
				}, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("api_error_propagates", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigListAllFunc: func(_ context.Context) (map[string]map[string]model.SettingInfo, error) {
				return nil, errors.New("db error")
			},
		}
		cmd := cli.NewConfigCmd(mock)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db error")
	})
}

// ─── Config reset (via config reset) ───────────────────────────────────────
// Rationale: Config reset restores defaults. A broken reset command can leave
// stale overrides in place or fail to revert misconfiguration.

func TestNewConfigResetCmd(t *testing.T) {
	t.Run("reset_all_overrides_with_force", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigResetFunc: func(_ context.Context, category, key string, allOverrides bool) (int, error) {
				return 3, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		require.NoError(t, resetCmd.Flags().Set("all", "true"))
		require.NoError(t, resetCmd.Flags().Set("force", "true"))
		err := resetCmd.RunE(resetCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("reset_category", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigResetFunc: func(_ context.Context, category, key string, allOverrides bool) (int, error) {
				return 2, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		err := resetCmd.RunE(resetCmd, []string{"defaults.vm"})
		assert.NoError(t, err)
	})

	t.Run("reset_single_key", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigResetFunc: func(_ context.Context, category, key string, allOverrides bool) (int, error) {
				return 1, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		err := resetCmd.RunE(resetCmd, []string{"defaults.vm", "vcpu_count"})
		assert.NoError(t, err)
	})

	t.Run("already_at_default", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigResetFunc: func(_ context.Context, category, key string, allOverrides bool) (int, error) {
				return 0, nil
			},
		}
		cmd := cli.NewConfigCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		err := resetCmd.RunE(resetCmd, []string{"defaults.vm", "vcpu_count"})
		assert.NoError(t, err)
	})

	t.Run("reset_error_propagates", func(t *testing.T) {
		mock := &testutil.MockConfigAPI{
			ConfigResetFunc: func(_ context.Context, category, key string, allOverrides bool) (int, error) {
				return 0, errors.New("reset failed")
			},
		}
		cmd := cli.NewConfigCmd(mock)
		resetCmd, _, _ := cmd.Find([]string{"reset"})
		err := resetCmd.RunE(resetCmd, []string{"defaults.vm", "vcpu_count"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reset failed")
	})
}
