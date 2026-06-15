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
	"mvmctl/pkg/errs"
)

// ─── NewBinaryCmd ──────────────────────────────────────────────────────────
// Rationale: NewBinaryCmd is the entry point for all binary CLI operations.
// Missing subcommands silently disable binary management without error.

func TestNewBinaryCmd(t *testing.T) {
	mock := &testutil.MockBinaryAPI{}
	cmd := cli.NewBinaryCmd(mock, nil)

	expectedSubcommands := []struct {
		use      string
		hasAlias bool
		alias    string
	}{
		{use: "ls", hasAlias: true, alias: "list"},
		{use: "pull", hasAlias: false},
		{use: "rm", hasAlias: true, alias: "remove"},
		{use: "default", hasAlias: false},
	}

	assert.Equal(t, "bin", cmd.Use, "root command must be 'bin'")
	assert.Equal(t, "Binary management", cmd.Short)

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

// ─── Binary list (via ls subcommand) ───────────────────────────────────────
// Rationale: Binary listing shows users cached binaries and remote versions.
// A broken list command prevents users from verifying or selecting binaries.

func TestNewBinaryListCmd(t *testing.T) {
	t.Run("empty_local_list", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return nil, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("with_local_binaries", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", IsDefault: true},
				}, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("json_output", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0"},
				}, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		require.NoError(t, listCmd.Flags().Set("json", "true"))
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("json_output_with_nil_binaries", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return nil, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		require.NoError(t, listCmd.Flags().Set("json", "true"))
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("remote_list", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, remote bool, limit *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				if !remote {
					return nil, nil, nil
				}
				return nil, []model.VersionInfo{
					{Version: "1.16.0", Type: "firecracker"},
					{Version: "1.15.0", Type: "firecracker"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		require.NoError(t, listCmd.Flags().Set("remote", "true"))
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
	})

	t.Run("remote_list_error", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, remote bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				if remote {
					return nil, nil, errors.New("network error")
				}
				return nil, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		require.NoError(t, listCmd.Flags().Set("remote", "true"))
		err := listCmd.RunE(listCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network error")
	})

	t.Run("local_list_error", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return nil, nil, errors.New("db error")
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db error")
	})

	t.Run("context_is_propagated", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gotCancelled := false
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(c context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				if c.Err() != nil {
					gotCancelled = true
				}
				return nil, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, nil)
		assert.NoError(t, err)
		assert.True(t, gotCancelled, "context cancellation should be visible to the mock")
	})
}

// ─── Binary pull (via pull subcommand) ─────────────────────────────────────
// Rationale: Binary pull downloads Firecracker binaries. A broken pull command
// prevents users from obtaining binaries needed to run VMs.

func TestNewBinaryPullCmd(t *testing.T) {
	t.Run("success_download_firecracker", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", Path: "/tmp/firecracker"},
					{ID: "bin-2", Type: "jailer", Version: "1.15.0", Path: "/tmp/jailer"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		assert.NoError(t, err)
	})

	t.Run("unsupported_binary_type", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"jailer"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported binary")
	})

	t.Run("mutually_exclusive_version_and_gitref", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		require.NoError(t, pullCmd.Flags().Set("version", "1.15.0"))
		require.NoError(t, pullCmd.Flags().Set("git-ref", "main"))
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("version_selector_with_colon", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				assert.Equal(t, "1.15.0", input.Version)
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", Path: "/tmp/firecracker"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"firecracker:1.15.0"})
		assert.NoError(t, err)
	})

	t.Run("non_interactive_error_propagates", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				return nil, errors.New("download failed: network timeout")
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network timeout")
	})

	t.Run("force_override_already_exists", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				if !input.DownloadOverride {
					return nil, errs.AlreadyExists(
						errs.CodeBinaryAlreadyExists,
						"Firecracker v1.15.0 already exists. Use --force to re-download.",
					)
				}
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", Path: "/tmp/firecracker"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		require.NoError(t, pullCmd.Flags().Set("force", "true"))
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		assert.NoError(t, err)
	})

	t.Run("git_ref_build_path", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				assert.NotNil(t, input.GitRef)
				assert.Equal(t, "main", *input.GitRef)
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "dev-main", Path: "/tmp/firecracker"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		require.NoError(t, pullCmd.Flags().Set("git-ref", "main"))
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		assert.NoError(t, err)
	})

	t.Run("download_with_set_default", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				assert.True(t, input.SetDefault)
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", Path: "/tmp/firecracker"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		require.NoError(t, pullCmd.Flags().Set("default", "true"))
		err := pullCmd.RunE(pullCmd, []string{"firecracker"})
		assert.NoError(t, err)
	})
}
