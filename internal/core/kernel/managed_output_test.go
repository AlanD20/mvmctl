package kernel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
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

	root := t.TempDir()
	managedDir := filepath.Join(root, "managed-kernels")
	buildDir := filepath.Join(root, "build", "linux")
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
	cacheDir := filepath.Dir(buildDir)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cacheDir, fmt.Sprintf(KernelCacheMarker, cacheKey)),
		[]byte(cacheKey),
		0644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(cacheDir, fmt.Sprintf(KernelCachePath, cacheKey)),
		[]byte("official-kernel"),
		0755,
	))

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

	assert.Equal(t, managedDir, filepath.Dir(result.Path))
	assert.NotContains(t, filepath.Base(result.Path), "caller-selected-output")
	assert.NotContains(t, filepath.Base(result.Path), spec.Version)
	assert.NotContains(t, filepath.Base(result.Path), "x86_64")
	assert.FileExists(t, result.Path)
	assert.NoDirExists(t, filepath.Join(managedDir, "caller-selected-output"))
}

type managedOutputRepository struct {
	Repository
	item *model.KernelItem
}

func (repo *managedOutputRepository) Upsert(_ context.Context, item *model.KernelItem) error {
	repo.item = item
	return nil
}

func (repo *managedOutputRepository) SetDefault(_ context.Context, _ string) error {
	return nil
}
