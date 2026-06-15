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
	"mvmctl/pkg/api/inputs"
)

// ─── NewKernelCmd ─────────────────────────────────────────────────────────────
// Rationale: NewKernelCmd is the entry point for all kernel CLI operations.
// Missing subcommands silently disable kernel management without error.

func TestNewKernelCmd(t *testing.T) {
	mock := &testutil.MockKernelAPI{}
	cmd := cli.NewKernelCmd(mock, nil)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "ls", hasAlias: true, alias: "list"},
		{use: "pull", hasAlias: false},
		{use: "rm", hasAlias: true, alias: "remove"},
		{use: "inspect", hasAlias: false},
		{use: "default", hasAlias: false},
		{use: "import", hasAlias: false},
	}

	assert.Equal(t, "kernel", cmd.Use, "root command must be 'kernel'")
	assert.Equal(t, "Kernel management", cmd.Short)
	assert.Equal(t, "Manage kernels — list, pull, remove, inspect, set default, import.", cmd.Long)

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

// ─── kernel ls (via kernel ls) ────────────────────────────────────────────────
// Rationale: Kernel listing is the primary way users see available kernels.
// A broken list command prevents users from discovering kernels for VM creation.

func TestNewKernelListCmd(t *testing.T) {
	t.Run("empty_list_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("single_kernel_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelListFunc: func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
				return []*model.KernelItem{
					{ID: "k-1", Name: "official-6.19.9", Version: "6.19.9", Type: "official"},
				}, nil, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("multiple_kernels_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelListFunc: func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
				return []*model.KernelItem{
					{ID: "k-1", Name: "official-6.19.9", Version: "6.19.9", Type: "official"},
					{ID: "k-2", Name: "firecracker-6.1", Version: "6.1", Type: "firecracker"},
				}, nil, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelListFunc: func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
				return []*model.KernelItem{
					{ID: "k-1", Name: "official-6.19.9", Version: "6.19.9"},
				}, nil, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("remote_listing_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelListFunc: func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
				return nil, []model.VersionInfo{
					{Version: "6.19.9", DisplayName: "official-6.19.9", Type: "official"},
				}, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("remote", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelListFunc: func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
				return nil, nil, errors.New("upstream unreachable")
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upstream unreachable")
	})

	t.Run("context_is_propagated_to_kernelapi", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelled := false
		mock := &testutil.MockKernelAPI{
			KernelListFunc: func(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error) {
				if ctx.Err() != nil {
					cancelled = true
				}
				return nil, nil, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err, "kernel list should not error on cancelled context — list is read-only")
		assert.True(t, cancelled, "mock KernelListFunc should have received the cancelled context")
	})
}

// ─── kernel pull (via kernel pull) ────────────────────────────────────────────
// Rationale: Kernel pull is the mechanism for downloading or building kernels.
// A broken pull prevents users from obtaining the kernels needed for VMs.

func TestNewKernelPullCmd(t *testing.T) {
	t.Run("pull_by_selector_success", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelPullFunc: func(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error) {
				assert.Equal(t, "official", input.KernelType)
				return &model.KernelItem{ID: "k-1", Name: "official-6.19.9"}, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"official:6.19.9"})
		assert.NoError(t, err)
	})

	t.Run("pull_with_type_flag_success", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelPullFunc: func(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error) {
				assert.Equal(t, "firecracker", input.KernelType)
				assert.Equal(t, "6.1", input.Version)
				return &model.KernelItem{ID: "k-2", Name: "firecracker-6.1"}, nil
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		pullCmd.Flags().Set("type", "firecracker")
		pullCmd.Flags().Set("version", "6.1")
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockKernelAPI{
			KernelPullFunc: func(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error) {
				return nil, errors.New("kernel not found")
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"nonexistent:0.0"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "kernel not found")
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockKernelAPI{
			KernelPullFunc: func(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return &model.KernelItem{ID: "k-1"}, nil
				}
			},
		}
		cmd := cli.NewKernelCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		pullCmd.SetContext(ctx)
		err := pullCmd.RunE(pullCmd, []string{"official:6.19.9"})
		require.Error(t, err)
	})
}
