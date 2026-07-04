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
	"mvmctl/pkg/errs"
)

// --- NewCacheCmd ---
// Rationale: NewCacheCmd is the entry point for all cache CLI operations.
// Missing subcommands silently disable cache management without error.

func TestNewCacheCmd(t *testing.T) {
	mock := &testutil.MockCacheAPI{}
	cmd := cli.NewCacheCmd(mock)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "init", hasAlias: false},
		{use: "prune", hasAlias: false},
		{use: "clean", hasAlias: false},
	}

	assert.Equal(t, "cache", cmd.Use, "root command must be 'cache'")
	assert.Equal(t, "Cache management", cmd.Short)

	for _, sc := range expectedSubcommands {
		t.Run("has_subcommand_"+sc.use, func(t *testing.T) {
			sub, _, err := cmd.Find([]string{sc.use})
			require.NoError(t, err, "subcommand %q not found", sc.use)
			require.NotNil(t, sub, "subcommand %q is nil", sc.use)
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

// --- Cache init (via cache init) ---
// Rationale: Cache init creates the cache directory structure. A broken init
// command prevents all cache-backed operations from working.

func TestNewCacheInitCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CacheInitAllFunc: func(_ context.Context, _ event.OnProgressCallback) (*results.CacheInitResult, error) {
				return &results.CacheInitResult{
					CacheDir:    "/home/user/.cache/mvmctl",
					Directories: []string{"/home/user/.cache/mvmctl/vms", "/home/user/.cache/mvmctl/images"},
				}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		err := initCmd.RunE(initCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("error_propagates", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CacheInitAllFunc: func(_ context.Context, _ event.OnProgressCallback) (*results.CacheInitResult, error) {
				return nil, errors.New("permission denied")
			},
		}
		cmd := cli.NewCacheCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		err := initCmd.RunE(initCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("context_cancelled_propagates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gotCancelled := false
		mock := &testutil.MockCacheAPI{
			CacheInitAllFunc: func(c context.Context, _ event.OnProgressCallback) (*results.CacheInitResult, error) {
				if c.Err() != nil {
					gotCancelled = true
				}
				return nil, ctx.Err()
			},
		}
		cmd := cli.NewCacheCmd(mock)
		initCmd, _, _ := cmd.Find([]string{"init"})
		initCmd.SetContext(ctx)
		err := initCmd.RunE(initCmd, nil)
		require.Error(t, err)
		assert.True(t, gotCancelled, "cancelled context should be visible to mock")
	})
}

// --- Cache prune ---
// Rationale: Cache prune removes stale cache resources. A broken prune command
// can leave orphaned resources consuming disk space, or fail to free space.

func TestNewCachePruneCmd(t *testing.T) {
	t.Run("no_resource_specified_returns_error", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		err := pruneCmd.RunE(pruneCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no resource specified")
	})

	t.Run("unknown_resource_returns_error", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		err := pruneCmd.RunE(pruneCmd, []string{"invalid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown resource")
	})

	t.Run("prune_vms_force_no_dryrun", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneVMsFunc: func(_ context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
				return &errs.OperationResult{
					Status: "success",
					Code:   "cache.pruned",
					Item:   []string{"vm-1", "vm-2"},
				}
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"vm"})
		assert.NoError(t, err)
	})

	t.Run("prune_vms_dry_run", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneVMsFunc: func(_ context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
				assert.True(t, dryRun)
				return &errs.OperationResult{
					Status: "success",
					Code:   "cache.pruned",
					Item:   []string{"vm-1"},
				}
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("dry-run", "true"))
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"vm"})
		assert.NoError(t, err)
	})

	t.Run("prune_networks_force", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneNetworksFunc: func(_ context.Context, dryRun bool, includeAll bool) ([]string, error) {
				return []string{"net-default"}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"network"})
		assert.NoError(t, err)
	})

	t.Run("prune_images_force", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneImagesFunc: func(_ context.Context, dryRun bool, includeAll bool) ([]string, error) {
				return []string{"img-ubuntu-22.04"}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"image"})
		assert.NoError(t, err)
	})

	t.Run("prune_kernels_force", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneKernelsFunc: func(_ context.Context, dryRun bool, includeAll bool) ([]string, error) {
				return []string{"kernel-6.1"}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"kernel"})
		assert.NoError(t, err)
	})

	t.Run("prune_binaries_force", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneBinariesFunc: func(_ context.Context, dryRun bool, includeAll bool) ([]string, error) {
				return []string{"firecracker:1.14.0"}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"binary"})
		assert.NoError(t, err)
	})

	t.Run("prune_misc_force", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneMiscFunc: func(_ context.Context, dryRun bool) (map[string]any, error) {
				return map[string]any{"appliance": true, "warm_images": true}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"misc"})
		assert.NoError(t, err)
	})

	t.Run("prune_all_with_flag", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneAllFunc: func(_ context.Context, dryRun bool, includeAll bool) (*model.PruneAllResult, error) {
				assert.True(t, includeAll)
				return &model.PruneAllResult{
					PrunedIDs: []string{"vm-1", "net-default", "img-ubuntu"},
				}, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("all", "true"))
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("prune_vms_error_handling", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneVMsFunc: func(_ context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
				return &errs.OperationResult{
					Status: "error",
					Code:   "cache.prune_failed",
					Item:   []string{},
				}
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"vm"})
		require.Error(t, err)
	})

	t.Run("prune_networks_error_propagates", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneNetworksFunc: func(_ context.Context, dryRun bool, includeAll bool) ([]string, error) {
				return nil, errors.New("network error")
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"network"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network error")
	})

	t.Run("prune_nothing_to_prune", func(t *testing.T) {
		mock := &testutil.MockCacheAPI{
			CachePruneImagesFunc: func(_ context.Context, dryRun bool, includeAll bool) ([]string, error) {
				return nil, nil
			},
		}
		cmd := cli.NewCacheCmd(mock)
		pruneCmd, _, _ := cmd.Find([]string{"prune"})
		require.NoError(t, pruneCmd.Flags().Set("force", "true"))
		err := pruneCmd.RunE(pruneCmd, []string{"image"})
		assert.NoError(t, err)
	})
}

// --- resourceDisplayName / resourceDisplayNamePlural ---
// Rationale: Resource display names appear in all cache prune user messages.
// Incorrect casing or pluralisation creates confusing output.

func TestResourceDisplayName(t *testing.T) {
	tests := []struct {
		resource string
		expected string
	}{
		{"vm", "VM"},
		{"network", "network"},
		{"image", "image"},
		{"kernel", "kernel"},
		{"binary", "binary"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			assert.Equal(t, tt.expected, cli.ResourceDisplayName(tt.resource))
		})
	}
}

func TestResourceDisplayNamePlural(t *testing.T) {
	tests := []struct {
		resource string
		expected string
	}{
		{"vm", "VMs"},
		{"network", "networks"},
		{"image", "images"},
		{"kernel", "kernels"},
		{"binary", "binaries"},
		{"unknown", "unknowns"},
	}
	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			assert.Equal(t, tt.expected, cli.ResourceDisplayNamePlural(tt.resource))
		})
	}
}
