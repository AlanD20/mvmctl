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
	"mvmctl/pkg/api/inputs"
)

// --- NewNetworkCmd ---
// Rationale: NewNetworkCmd is the entry point for all network CLI operations.
// Missing subcommands silently disable network management without error.

func TestNewNetworkCmd(t *testing.T) {
	mock := &testutil.MockNetworkAPI{}
	cmd := cli.NewNetworkCmd(mock, nil)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "ls", hasAlias: true, alias: "list"},
		{use: "create", hasAlias: false},
		{use: "rm", hasAlias: true, alias: "remove"},
		{use: "inspect", hasAlias: false},
		{use: "sync", hasAlias: false},
		{use: "default", hasAlias: false},
	}

	assert.Equal(t, "network", cmd.Use, "root command must be 'network'")
	assert.Equal(t, "Network management", cmd.Short)
	assert.Equal(t, []string{"net"}, cmd.Aliases)

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

// --- network ls (via network ls) ---
// Rationale: Network listing shows users their configured networks. A broken
// list command prevents users from seeing available networks for VM creation.

func TestNewNetworkListCmd(t *testing.T) {
	t.Run("empty_list_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{}
		cmd := cli.NewNetworkCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("single_network_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{
			NetworkListAllFunc: func(ctx context.Context) ([]*model.NetworkItem, error) {
				return []*model.NetworkItem{
					{ID: "net-1", Name: "default", Subnet: "192.168.100.0/24"},
				}, nil
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("multiple_networks_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{
			NetworkListAllFunc: func(ctx context.Context) ([]*model.NetworkItem, error) {
				return []*model.NetworkItem{
					{ID: "net-1", Name: "default", Subnet: "192.168.100.0/24"},
					{ID: "net-2", Name: "dmz", Subnet: "10.0.0.0/24"},
				}, nil
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{
			NetworkListAllFunc: func(ctx context.Context) ([]*model.NetworkItem, error) {
				return []*model.NetworkItem{
					{ID: "net-1", Name: "default", Subnet: "192.168.100.0/24"},
				}, nil
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{
			NetworkListAllFunc: func(ctx context.Context) ([]*model.NetworkItem, error) {
				return nil, errors.New("database locked")
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database locked")
	})

	t.Run("context_is_propagated_to_networkapi", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelled := false
		mock := &testutil.MockNetworkAPI{
			NetworkListAllFunc: func(ctx context.Context) ([]*model.NetworkItem, error) {
				if ctx.Err() != nil {
					cancelled = true
				}
				return nil, nil
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err, "network list should not error on cancelled context — list is read-only")
		assert.True(t, cancelled, "mock NetworkListAllFunc should have received the cancelled context")
	})
}

// --- network create (via network create) ---
// Rationale: Network create is how users add new networks. A broken create
// command prevents users from setting up networking for their VMs.

func TestNewNetworkCreateCmd(t *testing.T) {
	t.Run("success_with_minimal_flags", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{
			NetworkCreateFunc: func(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error) {
				assert.Equal(t, "test-net", input.Name)
				assert.Equal(t, "192.168.200.0/24", input.Subnet)
				return &model.NetworkItem{ID: "net-1", Name: "test-net", Subnet: "192.168.200.0/24"}, nil
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("subnet", "192.168.200.0/24")
		createCmd.Flags().Set("no-nat", "true")
		err := createCmd.RunE(createCmd, []string{"test-net"})
		assert.NoError(t, err)
	})

	t.Run("missing_subnet_returns_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{}
		cmd := cli.NewNetworkCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("no-nat", "true")
		err := createCmd.RunE(createCmd, []string{"test-net"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Missing required option '--subnet'")
	})

	t.Run("missing_name_returns_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{}
		cmd := cli.NewNetworkCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		err := createCmd.RunE(createCmd, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required argument")
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockNetworkAPI{
			NetworkCreateFunc: func(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error) {
				return nil, errors.New("bridge creation failed")
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("subnet", "192.168.200.0/24")
		createCmd.Flags().Set("no-nat", "true")
		err := createCmd.RunE(createCmd, []string{"test-net"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create network failed")
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockNetworkAPI{
			NetworkCreateFunc: func(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return &model.NetworkItem{ID: "net-1", Name: "test-net"}, nil
				}
			},
		}
		cmd := cli.NewNetworkCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.SetContext(ctx)
		createCmd.Flags().Set("subnet", "192.168.200.0/24")
		createCmd.Flags().Set("no-nat", "true")
		err := createCmd.RunE(createCmd, []string{"test-net"})
		require.Error(t, err)
	})
}
