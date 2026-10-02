package api_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/image"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func TestImageImportCachedDefaultResult(t *testing.T) {
	// Rationale: SQLite updates do not mutate the previously loaded image; the
	// acquisition result must report a committed default change and surface failures.
	if os.Getenv("MVM_IMAGE_DEFAULT_TEST_CHILD") != "1" {
		// Isolate the once-cached image path from other API tests' cache overrides.
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImageImportCachedDefaultResult$")
		command.Env = append(os.Environ(), "MVM_IMAGE_DEFAULT_TEST_CHILD=1", "MVM_CACHE_DIR="+t.TempDir())
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		return
	}
	for name, tc := range map[string]struct {
		setDefault bool
		failWrite  bool
	}{
		"default_write_failure": {setDefault: true, failWrite: true},
		"selects_cached_image":  {setDefault: true},
		"retains_old_default":   {},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := testutil.NewInMemoryDB(t)
			repo := image.NewRepository(db)
			source := filepath.Join(t.TempDir(), "cached.raw")
			require.NoError(t, os.WriteFile(source, []byte("cached image"), 0600))
			require.NoError(t, repo.Upsert(ctx, &model.ImageItem{
				ID: "cached-image", Type: "cached", Version: "1", Path: source, IsPresent: true,
			}))
			require.NoError(t, repo.Upsert(ctx, &model.ImageItem{
				ID: "old-default", Type: "previous", IsDefault: true, IsPresent: true,
			}))
			if tc.failWrite {
				_, err := db.ExecContext(ctx, `CREATE TRIGGER reject_image_default
					BEFORE UPDATE OF is_default ON images WHEN NEW.is_default = 1
					BEGIN SELECT RAISE(ABORT, 'default write rejected'); END`)
				require.NoError(t, err)
			}
			op := &api.Operation{Repos: api.Repos{Image: repo}}
			result, err := op.ImageImport(ctx, inputs.ImageImportInput{
				Name: "cached", Source: source, Format: "raw", Version: "1", SetDefault: tc.setDefault,
			}, nil)
			if tc.failWrite {
				var domainErr *errs.DomainError
				require.ErrorAs(t, err, &domainErr)
				assert.Equal(t, errs.CodeImageImportFailed, domainErr.Code)
				storedDefault, readErr := repo.GetDefault(ctx)
				require.NoError(t, readErr)
				require.NotNil(t, storedDefault)
				assert.Equal(t, "old-default", storedDefault.ID)
				contents, readErr := os.ReadFile(source)
				require.NoError(t, readErr)
				assert.Equal(t, "cached image", string(contents))
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.setDefault, result.IsDefault)
			stored, readErr := repo.Get(ctx, result.ID)
			require.NoError(t, readErr)
			require.NotNil(t, stored)
			assert.Equal(t, tc.setDefault, stored.IsDefault)
			storedDefault, readErr := repo.GetDefault(ctx)
			require.NoError(t, readErr)
			require.NotNil(t, storedDefault)
			wantDefaultID := "old-default"
			if tc.setDefault {
				wantDefaultID = "cached-image"
			}
			assert.Equal(t, wantDefaultID, storedDefault.ID)
		})
	}
}
