package api_test

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

func TestVolumeCreateUsesImmutableIDForManagedPath(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MVM_CACHE_DIR", cacheDir)

	tests := map[string]struct {
		name       string
		format     *string
		wantSuffix string
		pathArg    int
	}{
		"raw_default": {
			name:       "customer-backups",
			wantSuffix: ".raw",
			pathArg:    3,
		},
		"qcow2": {
			name:       "database-volume",
			format:     ptr.Ptr("qcow2"),
			wantSuffix: ".qcow2",
			pathArg:    4,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			fakeRunner := &testutil.FakeRunner{}
			originalRunner := system.DefaultRunner
			system.DefaultRunner = fakeRunner
			t.Cleanup(func() { system.DefaultRunner = originalRunner })

			op := &api.Operation{
				Repos:    api.Repos{Volume: repo},
				Services: api.Services{Volume: volume.NewService(repo)},
				AuditLog: &logging.AuditLog{},
			}
			writeback := false

			created, err := op.VolumeCreate(context.Background(), inputs.VolumeCreateInput{
				Name: tc.name, Size: "1MB", Format: tc.format, Writeback: &writeback,
			})
			require.NoError(t, err)
			require.NotNil(t, created)
			assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), created.ID)

			wantPath := filepath.Join(cacheDir, "volumes", created.ID+tc.wantSuffix)
			assert.Equal(t, wantPath, created.Path)
			assert.NotContains(t, filepath.Base(created.Path), tc.name)

			stored, repoErr := repo.Get(context.Background(), created.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, stored)
			assert.Equal(t, wantPath, stored.Path)

			require.Len(t, fakeRunner.Calls, 1)
			require.Greater(t, len(fakeRunner.Calls[0].Args), tc.pathArg)
			assert.Equal(t, wantPath, fakeRunner.Calls[0].Args[tc.pathArg])
		})
	}
}

func TestVolumeCreateRejectsPathLikeDisplayNamesBeforeDiskCreation(t *testing.T) {
	tests := []string{"../escape", "nested/escape"}
	for _, displayName := range tests {
		t.Run(displayName, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			fakeRunner := &testutil.FakeRunner{}
			originalRunner := system.DefaultRunner
			system.DefaultRunner = fakeRunner
			t.Cleanup(func() { system.DefaultRunner = originalRunner })

			op := &api.Operation{
				Repos:    api.Repos{Volume: repo},
				Services: api.Services{Volume: volume.NewService(repo)},
				AuditLog: &logging.AuditLog{},
			}
			writeback := false

			created, err := op.VolumeCreate(context.Background(), inputs.VolumeCreateInput{
				Name: displayName, Size: "1MB", Writeback: &writeback,
			})
			require.Error(t, err)
			assert.Nil(t, created)
			assert.Empty(t, fakeRunner.Calls)
		})
	}
}
