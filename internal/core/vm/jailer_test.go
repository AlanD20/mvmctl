package vm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
)

const testDriveID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestFirecrackerSpawner_WriteJailerManifestSelectsOnlyVMResources(t *testing.T) {
	vmDir := t.TempDir()
	config := jailerTestConfig(vmDir)
	spawner := NewFirecrackerSpawner(config)

	require.NoError(t, spawner.writeJailerManifest())
	data, err := os.ReadFile(filepath.Join(vmDir, infra.JailerManifestFilename))
	require.NoError(t, err)
	var got jailer.LaunchManifest
	require.NoError(t, json.Unmarshal(data, &got))

	want := jailer.LaunchManifest{
		VMID: filepath.Base(vmDir), VMDir: vmDir, Version: "1.16.0",
		ConfigPath: config.ConfigPath, PIDPath: config.PIDPath,
		KernelPath: config.KernelPath, RootfsPath: config.RootfsPath,
		ISOPath: *config.CloudInitISOPath, SnapshotDir: config.SnapshotDir,
		Volumes: []jailer.VolumeMount{{
			DriveID: testDriveID, HostPath: config.ExtraDrives[0].PathOnHost, ReadOnly: true,
		}},
		APISocket: config.APISocketPath, PCIEnabled: true, SnapshotMode: true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LaunchManifest mismatch (-want +got):\n%s", diff)
	}
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	wantKeys := map[string]bool{
		"vm_id": true, "vm_dir": true, "version": true, "config_path": true,
		"pid_path": true, "kernel_path": true, "rootfs_path": true, "iso_path": true,
		"snapshot_dir": true, "volumes": true, "api_socket": true,
		"pci_enabled": true, "snapshot_mode": true,
	}
	gotKeys := make(map[string]bool, len(raw))
	for key := range raw {
		gotKeys[key] = true
	}
	if diff := cmp.Diff(wantKeys, gotKeys); diff != "" {
		t.Errorf("manifest field set mismatch (-want +got):\n%s", diff)
	}
}

func TestFirecrackerSpawner_WriteToFileUsesJailedPathsWithoutMutatingHostConfig(t *testing.T) {
	vmDir := t.TempDir()
	config := jailerTestConfig(vmDir)
	config.SnapshotMode = false
	spawner := NewFirecrackerSpawner(config)

	hostConfig, err := spawner.Generate()
	require.NoError(t, err)
	require.NoError(t, spawner.WriteToFile())
	data, err := os.ReadFile(config.ConfigPath)
	require.NoError(t, err)
	var jailed model.FirecrackerVMConfig
	require.NoError(t, json.Unmarshal(data, &jailed))

	assert.Equal(t, config.KernelPath, hostConfig.BootSource.KernelImagePath)
	assert.Equal(t, "/kernel", jailed.BootSource.KernelImagePath)
	require.Len(t, hostConfig.Drives, 3)
	require.Len(t, jailed.Drives, 3)
	wantHostPaths := []string{config.RootfsPath, *config.CloudInitISOPath, config.ExtraDrives[0].PathOnHost}
	wantJailedPaths := []string{"/rootfs", "/cloud-init.iso", "/volumes/" + testDriveID}
	for i := range hostConfig.Drives {
		assert.Equal(t, wantHostPaths[i], hostConfig.Drives[i].PathOnHost)
		assert.Equal(t, wantJailedPaths[i], jailed.Drives[i].PathOnHost)
	}
	require.NotNil(t, hostConfig.Logger)
	require.NotNil(t, jailed.Logger)
	assert.Equal(t, config.LogPath, hostConfig.Logger.LogPath)
	assert.Equal(t, "/run/mvm/"+filepath.Base(config.LogPath), jailed.Logger.LogPath)
	require.NotNil(t, hostConfig.Metrics)
	require.NotNil(t, jailed.Metrics)
	assert.Equal(t, config.MetricsPath, hostConfig.Metrics.MetricsPath)
	assert.Equal(t, "/run/mvm/"+filepath.Base(config.MetricsPath), jailed.Metrics.MetricsPath)
	require.NotNil(t, hostConfig.Vsock)
	require.NotNil(t, jailed.Vsock)
	assert.Equal(t, config.Vsock.UDSPath, hostConfig.Vsock.UDSPath)
	assert.Equal(t, "/run/mvm/"+filepath.Base(config.Vsock.UDSPath), jailed.Vsock.UDSPath)
}

// Rationale: Mount destinations form a privileged filesystem boundary and
// must remain absolute, normalized, and independent of attacker path segments.
func TestJailedPathCalculation(t *testing.T) {
	tests := map[string]struct {
		got  string
		want string
	}{
		"runtime_uses_basename": {got: jailedRuntimePath("/host/vms/id/api.socket"), want: "/run/mvm/api.socket"},
		"runtime_cleans_parent_segments": {
			got: jailedRuntimePath("/host/vms/id/../metrics.fifo"), want: "/run/mvm/metrics.fifo",
		},
		"volume_uses_validated_id": {got: jailedVolumePath(testDriveID), want: "/volumes/" + testDriveID},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.got)
			assert.Equal(t, tc.got, filepath.Clean(tc.got))
		})
	}
}

func jailerTestConfig(vmDir string) *model.FirecrackerConfig {
	cloudInitMode := model.CloudInitModeISO
	isoPath := filepath.Join(vmDir, "cloud-init.iso")
	return &model.FirecrackerConfig{
		VMDir: vmDir, RootfsPath: filepath.Join(vmDir, "rootfs.ext4"),
		BinaryPath: "/var/lib/mvmctl/binaries/1.16.0/firecracker",
		JailerPath: "/var/lib/mvmctl/binaries/1.16.0/jailer", BinaryVersion: "1.16.0",
		KernelPath: filepath.Join(filepath.Dir(vmDir), "kernels", "vmlinux"),
		VCPUCount:  2, MemSizeMiB: 512, GuestIP: "10.0.0.2", GuestMAC: "02:00:00:00:00:02",
		TapName: "tap-test", NetworkGateway: "10.0.0.1", NetworkNetmask: "255.255.255.0",
		PCIEnabled: true, ImageFSUUID: "11111111-2222-3333-4444-555555555555", ImageFSType: "ext4",
		EnableLogging: true, EnableMetrics: true, LogLevel: "Info",
		LogPath: filepath.Join(vmDir, "firecracker.log"), MetricsPath: filepath.Join(vmDir, "metrics.fifo"),
		SerialOutputPath: filepath.Join(vmDir, "serial.log"),
		APISocketPath:    filepath.Join(vmDir, "api.socket"), PIDPath: filepath.Join(vmDir, "firecracker.pid"),
		ConfigPath: filepath.Join(vmDir, "config.json"), CloudInitMode: &cloudInitMode,
		CloudInitISOPath: &isoPath,
		ExtraDrives: []model.DriveConfig{{
			DriveID: testDriveID, PathOnHost: filepath.Join(filepath.Dir(vmDir), "volumes", "selected.raw"),
			IsReadOnly: true, CacheType: model.CacheTypeUnsafe, IOEngine: "Sync",
		}},
		SnapshotMode: true, SnapshotDir: filepath.Join(filepath.Dir(vmDir), "snapshots", testDriveID),
		Vsock:     &model.VsockConfig{GuestCID: 42, UDSPath: filepath.Join(vmDir, "vsock.socket")},
		CPUVendor: ptr.Ptr("Intel"),
	}
}
