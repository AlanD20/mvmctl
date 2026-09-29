package kernel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func TestImportKernelUsesConfiguredManagedDirectory(t *testing.T) {
	root := t.TempDir()
	managedDir := filepath.Join(root, "managed", "kernels")
	sourcePath := filepath.Join(root, "source-vmlinux")
	require.NoError(t, os.WriteFile(sourcePath, []byte("custom-kernel"), 0644))
	callerName := "../../escape/custom"
	callerSelectedDir := filepath.Clean(filepath.Join(managedDir, "..", "..", "escape"))
	legacyCallerSelectedPath := filepath.Join(
		managedDir,
		fmt.Sprintf(KernelOutputPattern, callerName, "6.1.0", "x86_64"),
	)
	assert.Equal(t, filepath.Join(callerSelectedDir, "custom-6.1.0-x86_64"), legacyCallerSelectedPath)

	repo := &managedOutputRepository{}
	service := NewService(repo, managedDir)
	service.protectedKernels = &managedOutputKernelStorage{}
	item, err := service.ImportKernel(
		context.Background(),
		callerName,
		sourcePath,
		"6.1.0",
		"x86_64",
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, item)

	wantPath := filepath.Join(managedDir, item.ID)
	assert.Equal(t, wantPath, item.Path)
	assert.Equal(t, callerName+" 6.1.0", item.Name)
	assert.Equal(t, item, repo.item)
	assert.FileExists(t, wantPath)
	storedID, hashErr := crypto.KernelID(wantPath, item.Version, item.Arch)
	require.NoError(t, hashErr)
	assert.Equal(t, item.ID, storedID)
	assert.NoDirExists(t, filepath.Join(managedDir, "kernels"))
	assert.NoDirExists(t, callerSelectedDir)
}

func TestImportKernelHashesReceiverStagedBytes(t *testing.T) {
	root := t.TempDir()
	managedDir := filepath.Join(root, "kernels")
	sourcePath := filepath.Join(root, "caller-source")
	require.NoError(t, os.WriteFile(sourcePath, []byte("original-source"), 0644))

	service := NewService(&managedOutputRepository{}, managedDir)
	service.protectedKernels = &managedOutputKernelStorage{}
	service.copyImportedKernel = func(_ context.Context, _, destination string) error {
		return os.WriteFile(destination, []byte("bytes-copied-to-store"), 0644)
	}

	item, err := service.ImportKernel(
		context.Background(),
		"custom",
		sourcePath,
		"6.1.0",
		"x86_64",
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, item)

	storedID, err := crypto.KernelID(item.Path, item.Version, item.Arch)
	require.NoError(t, err)
	assert.Equal(t, storedID, item.ID)
	sourceID, err := crypto.KernelID(sourcePath, item.Version, item.Arch)
	require.NoError(t, err)
	assert.NotEqual(t, sourceID, item.ID)
}

func TestImportKernelRemovesFailedReceiverStaging(t *testing.T) {
	root := t.TempDir()
	managedDir := filepath.Join(root, "kernels")
	sourcePath := filepath.Join(root, "caller-source")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0644))

	copyErr := errors.New("copy failed")
	service := NewService(&managedOutputRepository{}, managedDir)
	service.protectedKernels = &managedOutputKernelStorage{}
	service.copyImportedKernel = func(_ context.Context, _, destination string) error {
		require.NoError(t, os.WriteFile(destination, []byte("partial"), 0644))
		return copyErr
	}

	item, err := service.ImportKernel(
		context.Background(),
		"custom",
		sourcePath,
		"6.1.0",
		"x86_64",
		false,
	)
	require.Error(t, err)
	assert.Nil(t, item)
	assert.ErrorIs(t, err, copyErr)
	entries, readErr := os.ReadDir(managedDir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestAppendKernelStagingCleanupError(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")

	t.Run("cleanup is the only failure", func(t *testing.T) {
		err := appendKernelStagingCleanupError(nil, cleanupErr)
		require.Error(t, err)
		assert.ErrorIs(t, err, cleanupErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeKernelBuildFailed, domainErr.Code)
	})

	t.Run("cleanup does not hide primary failure", func(t *testing.T) {
		primaryErr := errors.New("download failed")
		err := appendKernelStagingCleanupError(primaryErr, cleanupErr)
		require.Error(t, err)
		assert.ErrorIs(t, err, primaryErr)
		assert.ErrorIs(t, err, cleanupErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeKernelBuildFailed, domainErr.Code)
	})

	t.Run("cleanup preserves primary domain metadata", func(t *testing.T) {
		primaryCause := errors.New("primary cause")
		primaryErr := &errs.DomainError{
			Code:    errs.CodeVMStateInvalid,
			Message: "primary failed",
			Op:      "custom-operation",
			Entity:  "vm-id",
			Class:   errs.ClassConflict,
			Err:     primaryCause,
			Details: map[string]any{"partial": true},
		}

		err := appendKernelStagingCleanupError(primaryErr, cleanupErr)
		require.Error(t, err)
		assert.ErrorIs(t, err, primaryCause)
		assert.ErrorIs(t, err, cleanupErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Same(t, primaryErr, domainErr)
		assert.Equal(t, primaryErr.Code, domainErr.Code)
		assert.Equal(t, primaryErr.Class, domainErr.Class)
		assert.Equal(t, primaryErr.Op, domainErr.Op)
		assert.Equal(t, primaryErr.Entity, domainErr.Entity)
		assert.Equal(t, map[string]any{"partial": true}, domainErr.Details)
	})
}

func TestFetchFirecrackerKernelUsesReceiverStagingLeaf(t *testing.T) {
	t.Setenv("MVM_ASSET_MIRROR", "")

	const (
		ciVersion     = "v1.15.0"
		kernelVersion = "6.1.0"
		arch          = "x86_64"
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/list":
			_, _ = fmt.Fprintf(
				writer,
				"<ListBucketResult><Key>firecracker-ci/%s/%s/vmlinux-%s</Key></ListBucketResult>",
				ciVersion,
				arch,
				kernelVersion,
			)
		case "/firecracker-ci/" + ciVersion + "/" + arch + "/vmlinux-" + kernelVersion:
			_, _ = writer.Write([]byte("firecracker-kernel"))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	managedDir := filepath.Join(root, "managed-kernels")
	listURL := server.URL + "/list?ci_version={ci_version}&arch={arch}"
	service := NewService(&managedOutputRepository{}, managedDir)
	result, err := service.FetchFirecrackerKernel(
		context.Background(),
		&model.KernelSpec{
			ListURLTemplate: &listURL,
			Source:          server.URL,
			OutputName:      "../caller-selected-output",
		},
		ciVersion,
		arch,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, managedDir, filepath.Dir(result.Path))
	assert.NotContains(t, filepath.Base(result.Path), "caller-selected-output")
	assert.NotContains(t, filepath.Base(result.Path), kernelVersion)
	assert.NotContains(t, filepath.Base(result.Path), arch)
	assert.FileExists(t, result.Path)
	assert.NoDirExists(t, filepath.Join(managedDir, "caller-selected-output"))
}

func TestBuildOfficialKernelUsesReceiverStagingLeaf(t *testing.T) {
	originalCommands := KernelBuildCommands
	originalLibraries := KernelBuildLibraries
	KernelBuildCommands = nil
	KernelBuildLibraries = nil
	t.Cleanup(func() {
		KernelBuildCommands = originalCommands
		KernelBuildLibraries = originalLibraries
	})

	// Rationale: A selected scratch directory must also select its cached kernel,
	// while an unset or empty override must preserve the spec's build location.
	cases := map[string]struct {
		unsetTemp     bool
		selectScratch bool
		wantKernel    string
	}{
		"unset_temp_preserves_spec":    {unsetTemp: true, wantKernel: "spec-kernel"},
		"empty_temp_preserves_spec":    {wantKernel: "spec-kernel"},
		"selected_temp_overrides_spec": {selectScratch: true, wantKernel: "scratch-kernel"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			managedDir := filepath.Join(root, "managed-kernels")
			buildDir := filepath.Join(root, "build", "linux")
			scratchDir := filepath.Join(root, "scratch")
			t.Setenv(infra.EnvKey("TEMP_DIR"), "")
			if tc.unsetTemp {
				require.NoError(t, os.Unsetenv(infra.EnvKey("TEMP_DIR")))
			} else if tc.selectScratch {
				t.Setenv(infra.EnvKey("TEMP_DIR"), scratchDir)
			}
			spec := &model.KernelSpec{
				Version:        "6.1.0",
				Source:         "https://example.invalid/linux.tar.xz",
				OutputName:     "../caller-selected-output",
				BuildDir:       buildDir,
				DefaultConfigs: map[string]string{},
			}
			service := NewService(&managedOutputRepository{}, managedDir)
			configHash := service.computeConfigHash(spec, spec.Version, nil, nil)
			cacheKey := fmt.Sprintf("%s-%s", spec.Version, configHash)
			for cacheDir, payload := range map[string]string{
				filepath.Dir(buildDir): "spec-kernel",
				scratchDir:             "scratch-kernel",
			} {
				require.NoError(t, os.MkdirAll(cacheDir, 0755))
				require.NoError(t, os.WriteFile(
					filepath.Join(cacheDir, fmt.Sprintf(KernelCacheMarker, cacheKey)),
					[]byte(cacheKey),
					0644,
				))
				require.NoError(t, os.WriteFile(
					filepath.Join(cacheDir, fmt.Sprintf(KernelCachePath, cacheKey)),
					[]byte(payload),
					0755,
				))
			}

			result, err := service.BuildOfficialKernel(
				context.Background(),
				spec,
				"x86_64",
				1,
				false,
				true,
				false,
				nil,
				nil,
				nil,
				nil,
			)
			require.NoError(t, err)
			require.NotNil(t, result)

			payload, err := os.ReadFile(result.Path)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKernel, string(payload))
			assert.Equal(t, buildDir, spec.BuildDir)
			assert.Equal(t, managedDir, filepath.Dir(result.Path))
			assert.NotContains(t, filepath.Base(result.Path), "caller-selected-output")
			assert.NotContains(t, filepath.Base(result.Path), spec.Version)
			assert.NotContains(t, filepath.Base(result.Path), "x86_64")
			assert.NoDirExists(t, filepath.Join(managedDir, "caller-selected-output"))
		})
	}
}

type managedOutputRepository struct {
	Repository
	item        *model.KernelItem
	softDeleted string
	deleted     string
}

func (repo *managedOutputRepository) Upsert(_ context.Context, item *model.KernelItem) error {
	repo.item = item
	return nil
}

func (repo *managedOutputRepository) SetDefault(_ context.Context, _ string) error {
	return nil
}

func (repo *managedOutputRepository) SoftDelete(_ context.Context, id string) error {
	repo.softDeleted = id
	return nil
}

func (repo *managedOutputRepository) Delete(_ context.Context, id string) error {
	repo.deleted = id
	return nil
}

type managedOutputKernelStorage struct {
	importErr error
	removeErr error
}

func (storage *managedOutputKernelStorage) Import(
	_ context.Context,
	request jailer.ProtectedKernelImportRequest,
) (jailer.ProtectedKernelImportResult, error) {
	return jailer.ProtectedKernelImportResult{
		KernelID: request.KernelID,
		Imported: storage.importErr == nil,
	}, storage.importErr
}

func (storage *managedOutputKernelStorage) Remove(
	_ context.Context,
	request jailer.ProtectedKernelRemoveRequest,
) (jailer.ProtectedKernelRemoveResult, error) {
	return jailer.ProtectedKernelRemoveResult{
		KernelID: request.KernelID,
		Removed:  storage.removeErr == nil,
	}, storage.removeErr
}

func TestImportKernelRootRefusalPrecedesProjection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	content := []byte("imported kernel")
	require.NoError(t, os.WriteFile(source, content, 0600))
	repo := &managedOutputRepository{}
	service := NewService(repo, filepath.Join(root, "kernels"))
	refusal := errors.New("retired digest mismatch")
	service.protectedKernels = &managedOutputKernelStorage{importErr: refusal}
	_, err := service.ImportKernel(t.Context(), "custom", source, "6.1", "x86_64", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, refusal)
	assert.Nil(t, repo.item, "root refusal must not project imported/default kernel")
	id, err := crypto.KernelID(source, "6.1", "x86_64")
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(root, "kernels", id))
	require.NoError(t, err)
	if diff := cmp.Diff(content, got); diff != "" {
		t.Errorf("published import bytes (-want +got):\n%s", diff)
	}
}

func TestKernelRemoveRetainsReferencedBackingAndRootRefusal(t *testing.T) {
	for _, references := range []string{"vm", "snapshot", "none"} {
		t.Run(references, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "kernel")
			content := []byte("retained kernel")
			require.NoError(t, os.WriteFile(path, content, 0600))
			repo := &managedOutputRepository{}
			service := NewService(repo, filepath.Join(root, "kernels"))
			refusal := errors.New("root snapshot reference")
			service.protectedKernels = &managedOutputKernelStorage{removeErr: refusal}
			item := &model.KernelItem{ID: strings.Repeat("a", 64), Path: path}
			if references == "vm" {
				item.VMs = []*model.VMItem{{ID: "retained-vm"}}
			} else if references == "snapshot" {
				item.Snapshots = []*model.SnapshotItem{{ID: "retained-snapshot"}}
			}
			_, err := service.Remove(t.Context(), item, true)
			if references == "none" {
				require.Error(t, err)
				assert.ErrorIs(t, err, refusal)
				assert.Empty(t, repo.softDeleted)
			} else {
				require.NoError(t, err)
				assert.Equal(t, item.ID, repo.softDeleted)
			}
			assert.Empty(t, repo.deleted)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			if diff := cmp.Diff(content, got); diff != "" {
				t.Errorf("retained kernel bytes (-want +got):\n%s", diff)
			}
		})
	}
}
