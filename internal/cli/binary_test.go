package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/cli"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// --- NewBinaryCmd ---
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

// --- Binary list (via ls subcommand) ---
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
					{ID: "bin-1", Type: "firecracker", Architecture: "x86_64", Version: "1.15.0"},
				}, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		common.BeginOutput([]string{"--json"})
		t.Cleanup(func() { common.BeginOutput(nil) })
		require.NoError(t, listCmd.RunE(listCmd, nil))
		var output bytes.Buffer
		require.NoError(t, common.FinishOutput(&output, nil))
		assert.Contains(t, output.String(), `"schema_version":1`)
		assert.Contains(t, output.String(), `"architecture":"x86_64"`)
	})

	t.Run("table includes architecture column", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryListFunc: func(_ context.Context, _ bool, _ *int, _ event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error) {
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Architecture: "x86_64", Version: "1.15.0"},
				}, nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		output := captureBinaryStdout(t, func() {
			require.NoError(t, listCmd.RunE(listCmd, nil))
		})
		assert.Contains(t, output, "ARCHITECTURE")
		assert.Contains(t, output, "x86_64")
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

func captureBinaryStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	run()
	require.NoError(t, writer.Close())
	os.Stdout = original
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return string(output)
}

// --- Binary pull (via pull subcommand) ---
// Rationale: Binary pull downloads Firecracker binaries. A broken pull command
// prevents users from obtaining binaries needed to run VMs.

func TestNewBinaryPullCmd(t *testing.T) {
	t.Run("exact_version_is_positional", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				assert.Equal(t, "1.15.0", input.Version)
				assert.Equal(t, "firecracker", input.Type)
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", Path: "/tmp/firecracker"},
					{ID: "bin-2", Type: "jailer", Version: "1.15.0", Path: "/tmp/jailer"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		output := captureBinaryStdout(t, func() {
			require.NoError(t, pullCmd.RunE(pullCmd, []string{"1.15.0"}))
		})
		assert.Contains(t, output, "Trusted release ready: firecracker v1.15.0")
		assert.NotContains(t, output, "Installed:")
	})

	t.Run("legacy source and version flags are absent", func(t *testing.T) {
		cmd := cli.NewBinaryCmd(&testutil.MockBinaryAPI{}, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		assert.Nil(t, pullCmd.Flags().Lookup("git-ref"))
		assert.Nil(t, pullCmd.Flags().Lookup("version"))
		assert.Equal(t, "pull <version>", pullCmd.Use)
		assert.NotContains(t, pullCmd.Short, "source")
	})

	t.Run("requires exactly one version", func(t *testing.T) {
		called := false
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, _ inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				called = true
				return nil, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.Args(pullCmd, nil)
		require.Error(t, err)
		assert.False(t, called)
	})

	t.Run("non_interactive_error_propagates", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				return nil, errors.New("download failed: network timeout")
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"1.15.0"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "network timeout")
	})

	t.Run("force_is_forwarded_as_replacement_intent", func(t *testing.T) {
		mock := &testutil.MockBinaryAPI{
			BinaryPullFunc: func(_ context.Context, input inputs.BinaryPullInput, _ event.OnProgressCallback) ([]*model.BinaryItem, error) {
				assert.True(t, input.DownloadOverride)
				return []*model.BinaryItem{
					{ID: "bin-1", Type: "firecracker", Version: "1.15.0", Path: "/tmp/firecracker"},
				}, nil
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		require.NoError(t, pullCmd.Flags().Set("force", "true"))
		err := pullCmd.RunE(pullCmd, []string{"1.15.0"})
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
		err := pullCmd.RunE(pullCmd, []string{"1.15.0"})
		assert.NoError(t, err)
	})
}

func TestNewBinaryRemoveCmdUsesPositionalExactVersions(t *testing.T) {
	t.Run("passes all versions and force once", func(t *testing.T) {
		calls := 0
		mock := &testutil.MockBinaryAPI{
			BinaryRemoveFunc: func(_ context.Context, input inputs.BinaryRemoveInput, force bool) *errs.BatchResult {
				calls++
				assert.Equal(t, []string{"1.16.1", "1.15.2"}, input.Versions)
				assert.True(t, force)
				return &errs.BatchResult{Items: []errs.OperationResult{
					{Status: "success", Message: "removed"},
					{Status: "skipped", Message: "absent"},
				}}
			},
		}
		cmd := cli.NewBinaryCmd(mock, nil)
		removeCmd, _, _ := cmd.Find([]string{"rm"})
		require.NoError(t, removeCmd.Flags().Set("force", "true"))
		require.NoError(t, removeCmd.RunE(removeCmd, []string{"1.16.1", "1.15.2"}))
		assert.Equal(t, 1, calls)
	})

	t.Run("legacy version flag is absent and argument is required", func(t *testing.T) {
		cmd := cli.NewBinaryCmd(&testutil.MockBinaryAPI{}, nil)
		removeCmd, _, _ := cmd.Find([]string{"rm"})
		assert.Nil(t, removeCmd.Flags().Lookup("version"))
		assert.Equal(t, "rm <exact-version>...", removeCmd.Use)
		require.Error(t, removeCmd.Args(removeCmd, nil))
	})
}
