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

// --- NewVolumeCmd ---
// Rationale: NewVolumeCmd is the entry point for all volume CLI operations.
// Missing subcommands silently disable volume management without error.

func TestNewVolumeCmd(t *testing.T) {
	mock := &testutil.MockVolumeAPI{}
	cmd := cli.NewVolumeCmd(mock, nil)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "ls", hasAlias: true, alias: "list"},
		{use: "create", hasAlias: false},
		{use: "rm", hasAlias: true, alias: "remove"},
		{use: "inspect", hasAlias: false},
		{use: "resize", hasAlias: false},
	}

	assert.Equal(t, "volume", cmd.Use, "root command must be 'volume'")
	assert.Equal(t, "Volume management", cmd.Short)
	assert.Equal(t, "Manage persistent volumes — list, create, remove, inspect, resize.", cmd.Long)
	assert.Equal(t, []string{"vol"}, cmd.Aliases)

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

// --- volume ls (via volume ls) ---
// Rationale: Volume listing is the primary way users see available volumes.
// A broken list command prevents users from discovering volumes to attach.

func TestNewVolumeListCmd(t *testing.T) {
	t.Run("empty_list_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{}
		cmd := cli.NewVolumeCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("single_volume_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{
			VolumeListAllFunc: func(ctx context.Context) []*model.VolumeItem {
				return []*model.VolumeItem{
					{ID: "vol-1", Name: "data-vol", SizeBytes: 1073741824, Status: model.VolumeStatusAvailable},
				}
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("multiple_volumes_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{
			VolumeListAllFunc: func(ctx context.Context) []*model.VolumeItem {
				return []*model.VolumeItem{
					{ID: "vol-1", Name: "data-vol", SizeBytes: 1073741824, Status: model.VolumeStatusAvailable},
					{ID: "vol-2", Name: "db-vol", SizeBytes: 2147483648, Status: model.VolumeStatusAttached},
				}
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{
			VolumeListAllFunc: func(ctx context.Context) []*model.VolumeItem {
				return []*model.VolumeItem{
					{ID: "vol-1", Name: "data-vol", SizeBytes: 1073741824, Status: model.VolumeStatusAvailable},
				}
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("context_is_propagated_to_volumeapi", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelled := false
		mock := &testutil.MockVolumeAPI{
			VolumeListAllFunc: func(ctx context.Context) []*model.VolumeItem {
				if ctx.Err() != nil {
					cancelled = true
				}
				return nil
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err, "volume list should not error on cancelled context — list is read-only")
		assert.True(t, cancelled, "mock VolumeListAllFunc should have received the cancelled context")
	})
}

// --- volume create (via volume create) ---
// Rationale: Volume create is the mechanism for adding persistent storage.
// A broken create command prevents users from provisioning storage for VMs.

func TestNewVolumeCreateCmd(t *testing.T) {
	t.Run("success_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{
			VolumeCreateFunc: func(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
				assert.Equal(t, "my-vol", input.Name)
				assert.Equal(t, "10G", input.Size)
				return &model.VolumeItem{
					ID:        "vol-1",
					Name:      "my-vol",
					SizeBytes: 10737418240,
					Status:    model.VolumeStatusAvailable,
				}, nil
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		err := createCmd.RunE(createCmd, []string{"my-vol", "10G"})
		assert.NoError(t, err)
	})

	t.Run("with_format_flag_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{
			VolumeCreateFunc: func(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
				assert.NotNil(t, input.Format)
				if input.Format != nil {
					assert.Equal(t, "qcow2", *input.Format)
				}
				return &model.VolumeItem{
					ID:        "vol-1",
					Name:      "my-vol",
					SizeBytes: 10737418240,
					Status:    model.VolumeStatusAvailable,
				}, nil
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.Flags().Set("format", "qcow2")
		err := createCmd.RunE(createCmd, []string{"my-vol", "10G"})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockVolumeAPI{
			VolumeCreateFunc: func(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
				return nil, errors.New("insufficient disk space")
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		err := createCmd.RunE(createCmd, []string{"my-vol", "10G"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "insufficient disk space")
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockVolumeAPI{
			VolumeCreateFunc: func(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return &model.VolumeItem{ID: "vol-1"}, nil
				}
			},
		}
		cmd := cli.NewVolumeCmd(mock, nil)
		createCmd, _, _ := cmd.Find([]string{"create"})
		createCmd.SetContext(ctx)
		err := createCmd.RunE(createCmd, []string{"my-vol", "10G"})
		require.Error(t, err)
	})
}
