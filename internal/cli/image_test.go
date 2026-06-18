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

// --- NewImageCmd ---
// Rationale: NewImageCmd is the entry point for all image CLI operations.
// Missing subcommands silently disable image operations without error.

func TestNewImageCmd(t *testing.T) {
	mock := &testutil.MockImageAPI{}
	cmd := cli.NewImageCmd(mock, nil)

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
		{use: "warm", hasAlias: false},
	}

	assert.Equal(t, "image", cmd.Use, "root command must be 'image'")
	assert.Equal(t, "Image management", cmd.Short)
	assert.Equal(t, []string{"img"}, cmd.Aliases)

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

// --- image ls (via image ls) ---
// Rationale: Image listing is the primary user-facing output for image
// operations. A broken list command prevents users from discovering available
// images and selecting them for VM creation.

func TestNewImageListCmd(t *testing.T) {
	t.Run("empty_list_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("single_image_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImageListAllFunc: func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error) {
				return []*model.ImageItem{
					{ID: "img-1", Name: "ubuntu-24.04", Type: "ubuntu"},
				}, nil, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("multiple_images_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImageListAllFunc: func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error) {
				return []*model.ImageItem{
					{ID: "img-1", Name: "ubuntu-24.04", Type: "ubuntu"},
					{ID: "img-2", Name: "debian-12", Type: "debian"},
				}, nil, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("json_output_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImageListAllFunc: func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error) {
				return []*model.ImageItem{
					{ID: "img-1", Name: "ubuntu-24.04", Type: "ubuntu"},
				}, nil, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("json", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("remote_listing_returns_no_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImageListAllFunc: func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error) {
				return nil, []model.VersionInfo{
					{Version: "24.04", DisplayName: "ubuntu-24.04", Type: "ubuntu"},
				}, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.Flags().Set("remote", "true")
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImageListAllFunc: func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error) {
				return nil, nil, errors.New("connection refused")
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		err := listCmd.RunE(listCmd, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection refused")
	})

	t.Run("context_is_propagated_to_imageapi", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelled := false
		mock := &testutil.MockImageAPI{
			ImageListAllFunc: func(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error) {
				if ctx.Err() != nil {
					cancelled = true
				}
				return nil, nil, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		listCmd, _, _ := cmd.Find([]string{"ls"})
		listCmd.SetContext(ctx)
		err := listCmd.RunE(listCmd, []string{})
		assert.NoError(t, err, "image list should not error on cancelled context — list is read-only")
		assert.True(t, cancelled, "mock ImageListAllFunc should have received the cancelled context")
	})
}

// --- image pull (via image pull) ---
// Rationale: Pull is the mechanism for downloading new images. A broken pull
// prevents users from fetching images needed for VM creation.

func TestNewImagePullCmd(t *testing.T) {
	t.Run("pull_by_selector_success", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImagePullFunc: func(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error) {
				return &model.ImageItem{ID: "img-1", Name: "ubuntu-24.04"}, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"ubuntu"})
		assert.NoError(t, err)
	})

	t.Run("pull_by_type_version_success", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImagePullFunc: func(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error) {
				assert.Equal(t, "ubuntu", input.Type)
				assert.Equal(t, "24.04", input.Version)
				return &model.ImageItem{ID: "img-1", Name: "ubuntu-24.04"}, nil
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"ubuntu:24.04"})
		assert.NoError(t, err)
	})

	t.Run("api_error_returns_error", func(t *testing.T) {
		mock := &testutil.MockImageAPI{
			ImagePullFunc: func(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error) {
				return nil, errors.New("pull failed: image not found")
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		err := pullCmd.RunE(pullCmd, []string{"nonexistent"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pull failed")
	})

	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &testutil.MockImageAPI{
			ImagePullFunc: func(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return &model.ImageItem{ID: "img-1"}, nil
				}
			},
		}
		cmd := cli.NewImageCmd(mock, nil)
		pullCmd, _, _ := cmd.Find([]string{"pull"})
		pullCmd.SetContext(ctx)
		err := pullCmd.RunE(pullCmd, []string{"ubuntu"})
		require.Error(t, err)
	})
}
