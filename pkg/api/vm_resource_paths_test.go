package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/image"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
)

// Rationale: the managed rootfs leaf is part of the privileged resource
// contract. The producer must not derive it from the image filesystem type.
func TestVMCreateBuilderCloneImageUsesFixedRootfsLeaf(t *testing.T) {
	t.Setenv(infra.EnvKey("TEMP_DIR"), t.TempDir())

	sourcePath := filepath.Join(t.TempDir(), "source.ext4")
	require.NoError(t, os.WriteFile(sourcePath, []byte("rootfs"), 0600))

	resolved := &inputs.ResolvedVMCreateInput{Image: &model.ImageItem{
		ID:     "fixed-rootfs-test",
		Path:   sourcePath,
		FSType: "ext4",
	}}
	vmDir := t.TempDir()
	builder := &VMCreateBuilder{vmDir: vmDir}

	err := builder.cloneImage(t.Context(), image.NewService(nil), resolved)
	require.NoError(t, err)

	wantPath := filepath.Join(vmDir, infra.VMRootfsFilename)
	assert.Equal(t, wantPath, builder.rootfsPath)
	data, err := os.ReadFile(wantPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("rootfs"), data)
	assert.NoFileExists(t, filepath.Join(vmDir, "rootfs.ext4"))
}

// Rationale: every Firecracker artifact path passed into the launch boundary
// must be derived from the VM directory and the closed filename vocabulary.
func TestVMCreateBuilderUsesFixedFirecrackerLeaves(t *testing.T) {
	vmDir := t.TempDir()
	builder := &VMCreateBuilder{
		vmDir: vmDir,
		resolved: &inputs.ResolvedVMCreateInput{
			Binary:  &model.BinaryItem{},
			Jailer:  &model.BinaryItem{},
			Kernel:  &model.KernelItem{},
			Image:   &model.ImageItem{},
			Network: &model.NetworkItem{},
		},
	}

	config := builder.buildFirecrackerConfig()
	require.NotNil(t, config)

	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerConfigFilename), config.ConfigPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerAPISocketFilename), config.APISocketPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerLogFilename), config.LogPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerConsoleLogFilename), config.SerialOutputPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerMetricsFilename), config.MetricsPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerPIDFilename), config.PIDPath)
}

// Rationale: persisted path columns are display metadata, not launch
// authority. Relaunch must reconstruct every managed path from the VM ID.
func TestBuildRespawnFirecrackerConfigIgnoresPersistedPaths(t *testing.T) {
	vmDir := t.TempDir()
	logPath := "/user-selected/firecracker.log"
	serialPath := "/user-selected/console.log"
	vmItem := &model.VMItem{
		RootfsPath:       "/user-selected/rootfs.img",
		ConfigPath:       "/user-selected/config.json",
		APISocketPath:    "/user-selected/api.socket",
		LogPath:          &logPath,
		SerialOutputPath: &serialPath,
		EnableMetrics:    true,
		Image:            &model.ImageItem{},
		Binary:           &model.BinaryItem{},
		Kernel:           &model.KernelItem{},
		Network:          &model.NetworkItem{},
		Vsock: &model.VsockConfigItem{
			GuestCID: 42,
			UDSPath:  "/user-selected/vsock.socket",
		},
	}
	pair := &model.BinaryPair{
		Firecracker: &model.BinaryItem{Version: "1.16.0"},
		Jailer:      &model.BinaryItem{},
	}

	config := buildRespawnFirecrackerConfig(vmItem, pair, vmDir, false, "", "Info")
	require.NotNil(t, config)

	assert.Equal(t, filepath.Join(vmDir, infra.VMRootfsFilename), config.RootfsPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerConfigFilename), config.ConfigPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerAPISocketFilename), config.APISocketPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerLogFilename), config.LogPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerConsoleLogFilename), config.SerialOutputPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerMetricsFilename), config.MetricsPath)
	assert.Equal(t, filepath.Join(vmDir, infra.VMFirecrackerPIDFilename), config.PIDPath)
	require.NotNil(t, config.Vsock)
	assert.Equal(t, filepath.Join(vmDir, infra.VMVsockSocketFilename), config.Vsock.UDSPath)
}

// Rationale: user-owned snapshot metadata must not select an artifact path;
// exact leaves are derived from the validated snapshot identity.
func TestResolveManagedSnapshotArtifacts(t *testing.T) {
	cacheDir := t.TempDir()
	snapshotID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	paths, err := resolveManagedSnapshotArtifacts(cacheDir, snapshotID)
	require.NoError(t, err)

	wantDir := filepath.Join(cacheDir, "snapshots", snapshotID)
	assert.Equal(t, wantDir, paths.dir)
	assert.Equal(t, filepath.Join(wantDir, infra.SnapshotRootfsFilename), paths.rootfs)
	assert.Equal(t, filepath.Join(wantDir, infra.SnapshotMemoryFilename), paths.memory)
	assert.Equal(t, filepath.Join(wantDir, infra.SnapshotStateFilename), paths.state)
}

func TestResolveManagedSnapshotArtifactsRejectsInvalidIdentity(t *testing.T) {
	for name, snapshotID := range map[string]string{
		"path traversal": "../selected-by-database",
		"uppercase hex":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveManagedSnapshotArtifacts(t.TempDir(), snapshotID)
			require.ErrorContains(t, err, "invalid snapshot identity")
		})
	}
}
