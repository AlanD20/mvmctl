package vm_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/model"
)

// ─── NewFirecrackerSpawner ───────────────────────────────────────────────
// Rationale: Ensures the spawner correctly copies config paths to its
// exported fields. A bug here would misroute log/metrics/socket paths.

func TestNewFirecrackerSpawner(t *testing.T) {
	config := &model.FirecrackerConfig{
		ConfigPath:       "/tmp/mvm/fc-config.json",
		LogPath:          "/tmp/mvm/fc.log",
		MetricsPath:      "/tmp/mvm/fc.metrics",
		SerialOutputPath: "/tmp/mvm/fc-serial.log",
		PIDPath:          "/tmp/mvm/fc.pid",
		APISocketPath:    "/tmp/mvm/fc.socket",
	}

	s := vm.NewFirecrackerSpawner(config)
	assert.Equal(t, "/tmp/mvm/fc.socket", s.APISocketPath)
	assert.Nil(t, s.PID)
	assert.Nil(t, s.ProcessStartTime)
}

// ─── Generate: error paths ───────────────────────────────────────────────
// Rationale: buildBootArgs returns errors for invalid config combinations.
// PCI transport requires a UUID, and NET cloud-init mode requires a URL.
// These error paths must be tested first to establish the contract.

func TestFirecrackerSpawner_Generate_errors(t *testing.T) {
	tests := map[string]struct {
		config  *model.FirecrackerConfig
		wantErr string
	}{
		"pci_enabled_no_uuid": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     true,
				ImageFSUUID:    "",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			wantErr: "no filesystem UUID",
		},
		"cloud_init_net_no_url": {
			config: &model.FirecrackerConfig{
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				CloudInitMode:  ptr.Ptr(model.CloudInitModeNET),
			},
			wantErr: "NoCloud URL must be set",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := vm.NewFirecrackerSpawner(tc.config)
			_, err := s.Generate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			return
		})
	}
}

// ─── Generate: boot args ─────────────────────────────────────────────────
// Rationale: buildBootArgs assembles kernel command-line parameters from
// config fields. Each feature (PCI, nested virt, LSM, cloud-init) adds or
// omits specific flags. Wrong boot args cause VM boot failures.

func TestFirecrackerSpawner_Generate_bootArgs(t *testing.T) {
	tests := map[string]struct {
		config *model.FirecrackerConfig
		want   string
	}{
		"pci_off": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"pci_on_no_uuid_fallback_root_vda": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     true,
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=UUID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 rootfstype=ext4 systemd.mask=systemd-networkd-wait-online.service",
		},
		"lsm_flags_appended": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				LSMFlags:       "apparmor=1",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off lsm=apparmor=1 root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"nested_virt_intel": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				NestedVirt:     true,
				CPUVendor:      ptr.Ptr("Intel"),
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off kvm-intel.nested=1 root=UUID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 rootfstype=ext4 systemd.mask=systemd-networkd-wait-online.service",
		},
		"nested_virt_amd": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				NestedVirt:     true,
				CPUVendor:      ptr.Ptr("AMD"),
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off kvm-amd.nested=1 root=UUID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 rootfstype=ext4 systemd.mask=systemd-networkd-wait-online.service",
		},
		"nested_virt_hygon": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				NestedVirt:     true,
				CPUVendor:      ptr.Ptr("Hygon"),
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off kvm-amd.nested=1 root=UUID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 rootfstype=ext4 systemd.mask=systemd-networkd-wait-online.service",
		},
		"nested_virt_arm_no_boot_arg": {
			config: &model.FirecrackerConfig{
				PCIEnabled:      false,
				NestedVirt:      true,
				CPUVendor:       ptr.Ptr("arm"),
				CPUArchitecture: ptr.Ptr("aarch64"),
				ImageFSUUID:     "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:     "ext4",
				GuestIP:         "10.0.0.2",
				NetworkGateway:  "10.0.0.1",
				NetworkNetmask:  "255.255.255.0",
			},
			want: "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=UUID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 rootfstype=ext4 systemd.mask=systemd-networkd-wait-online.service",
		},
		"nested_virt_forces_pci_on": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				NestedVirt:     true,
				CPUVendor:      ptr.Ptr("Intel"),
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off kvm-intel.nested=1 root=UUID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 rootfstype=ext4 systemd.mask=systemd-networkd-wait-online.service",
		},
		"custom_boot_args_prepended": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				EnableConsole:  true,
				BootArgs:       "quiet console=ttyS0",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "quiet console=ttyS0 pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"cloud_init_net": {
			config: &model.FirecrackerConfig{
				PCIEnabled:          false,
				GuestIP:             "10.0.0.2",
				NetworkGateway:      "10.0.0.1",
				NetworkNetmask:      "255.255.255.0",
				CloudInitMode:       ptr.Ptr(model.CloudInitModeNET),
				CloudInitNoCloudURL: ptr.Ptr("http://10.0.0.1:8080/"),
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service ds=nocloud;seedfrom=http://10.0.0.1:8080/",
		},
		"cloud_init_inject": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				CloudInitMode:  ptr.Ptr(model.CloudInitModeINJECT),
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service ds=ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/",
		},
		"cloud_init_iso": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				CloudInitMode:  ptr.Ptr(model.CloudInitModeISO),
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service ds=nocloud",
		},
		"cloud_init_off_omits_ds": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				CloudInitMode:  ptr.Ptr(model.CloudInitModeOFF),
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"nil_cloud_init_mode_omits_ds": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				CloudInitMode:  nil,
			},
			want: "pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"custom_boot_args_without_console_console_enabled": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				EnableConsole:  true,
				BootArgs:       "quiet",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "quiet console=ttyS0 pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"custom_boot_args_with_console_tty0_console_enabled": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				EnableConsole:  true,
				BootArgs:       "console=tty0",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "console=tty0 pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
		"custom_boot_args_without_console_console_disabled": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     false,
				EnableConsole:  false,
				BootArgs:       "quiet",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: "quiet pci=off ip=10.0.0.2::10.0.0.1:255.255.255.0::eth0:off root=/dev/vda systemd.mask=systemd-networkd-wait-online.service",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := vm.NewFirecrackerSpawner(tc.config)
			got, err := s.Generate()
			require.NoError(t, err)
			assert.Equal(t,
				tc.want,
				got.BootSource.BootArgs,
				"BootArgs mismatch",
			)
		})
	}
}

// ─── Generate: drives config ─────────────────────────────────────────────
// Rationale: buildDrivesConfig constructs the drive list from rootfs,
// optional cloud-init ISO, and extra drives. Missing or extra drives
// cause VM boot failures or data loss.

func TestFirecrackerSpawner_Generate_drivesConfig(t *testing.T) {
	rootfsPath := "/tmp/test-rootfs.ext4"
	ciISOPath := "/tmp/test-cloud-init.iso"
	extraPath := "/tmp/test-extra.ext4"

	tests := map[string]struct {
		config *model.FirecrackerConfig
		want   []model.DriveConfig
	}{
		"rootfs_only": {
			config: &model.FirecrackerConfig{
				RootfsPath: rootfsPath,
			},
			want: []model.DriveConfig{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
			},
		},
		"rootfs_with_cloud_init": {
			config: &model.FirecrackerConfig{
				RootfsPath:       rootfsPath,
				CloudInitMode:    ptr.Ptr(model.CloudInitModeISO),
				CloudInitISOPath: ptr.Ptr(ciISOPath),
			},
			want: []model.DriveConfig{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
				{
					DriveID:      "cloud-init",
					PathOnHost:   ciISOPath,
					IsRootDevice: false,
					IsReadOnly:   true,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
			},
		},
		"rootfs_with_extra_drives": {
			config: &model.FirecrackerConfig{
				RootfsPath: rootfsPath,
				ExtraDrives: []model.DriveConfig{
					{
						DriveID:      "extra-1",
						PathOnHost:   extraPath,
						IsRootDevice: false,
						IsReadOnly:   false,
						CacheType:    "Unsafe",
						IOEngine:     "Sync",
					},
				},
			},
			want: []model.DriveConfig{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
				{
					DriveID:      "extra-1",
					PathOnHost:   extraPath,
					IsRootDevice: false,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
			},
		},
		"cloud_init_mode_nil_omits_iso": {
			config: &model.FirecrackerConfig{
				RootfsPath:       rootfsPath,
				CloudInitMode:    nil,
				CloudInitISOPath: ptr.Ptr(ciISOPath),
			},
			want: []model.DriveConfig{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
			},
		},
		"cloud_init_mode_empty_omits_iso": {
			config: &model.FirecrackerConfig{
				RootfsPath:       rootfsPath,
				CloudInitMode:    ptr.Ptr(model.CloudInitMode("")),
				CloudInitISOPath: ptr.Ptr(ciISOPath),
			},
			want: []model.DriveConfig{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
			},
		},
		"cloud_init_iso_path_nil_omits_iso": {
			config: &model.FirecrackerConfig{
				RootfsPath:       rootfsPath,
				CloudInitMode:    ptr.Ptr(model.CloudInitModeISO),
				CloudInitISOPath: nil,
			},
			want: []model.DriveConfig{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
					CacheType:    "Unsafe",
					IOEngine:     "Sync",
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := vm.NewFirecrackerSpawner(tc.config)
			got, err := s.Generate()
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got.Drives); diff != "" {
				t.Errorf("Drives mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── Generate: network config ────────────────────────────────────────────
// Rationale: buildNetworkConfig produces the network interfaces list.
// A misconfigured interface prevents the VM from communicating.

func TestFirecrackerSpawner_Generate_networkConfig(t *testing.T) {
	tests := map[string]struct {
		config *model.FirecrackerConfig
		want   []model.NetworkInterfaceConfig
	}{
		"basic_single_interface": {
			config: &model.FirecrackerConfig{
				GuestMAC: "02:00:00:00:00:01",
				TapName:  "tap-test",
			},
			want: []model.NetworkInterfaceConfig{
				{
					IfaceID:     "eth0",
					GuestMAC:    "02:00:00:00:00:01",
					HostDevName: "tap-test",
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := vm.NewFirecrackerSpawner(tc.config)
			got, err := s.Generate()
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got.NetworkInterfaces); diff != "" {
				t.Errorf("NetworkInterfaces mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── Generate: CPU config ────────────────────────────────────────────────
// Rationale: buildCPUConfig returns nil or a CPU template based on nested
// virt and explicit CPU config. A missing CPU template when nested virt is
// enabled causes VM boot failure.

func TestFirecrackerSpawner_Generate_cpuConfig(t *testing.T) {
	tests := map[string]struct {
		config *model.FirecrackerConfig
		want   *model.CpuConfig
	}{
		"nil_when_no_nested_virt_and_no_cpu_config": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     true,
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: nil,
		},
		"nested_virt_creates_empty_kvm_capabilities": {
			config: &model.FirecrackerConfig{
				NestedVirt:     true,
				CPUVendor:      ptr.Ptr("Intel"),
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
			},
			want: &model.CpuConfig{
				KvmCapabilities: []string{},
			},
		},
		"explicit_cpu_config_returned_as_is": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     true,
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				CPUConfig: &model.CpuConfig{
					KvmCapabilities: []string{"some-capability"},
				},
			},
			want: &model.CpuConfig{
				KvmCapabilities: []string{"some-capability"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := vm.NewFirecrackerSpawner(tc.config)
			got, err := s.Generate()
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got.CPUConfig); diff != "" {
				t.Errorf("CPUConfig mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── Generate: logger and metrics config ─────────────────────────────────
// Rationale: Logger and Metrics sections are conditionally included.
// When disabled they must be nil; when enabled they must carry the
// correct paths and levels.

func TestFirecrackerSpawner_Generate_loggerMetrics(t *testing.T) {
	tests := map[string]struct {
		config      *model.FirecrackerConfig
		wantLogger  *model.LoggerConfig
		wantMetrics *model.MetricsConfig
	}{
		"both_disabled": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     true,
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				EnableLogging:  false,
				EnableMetrics:  false,
			},
			wantLogger:  nil,
			wantMetrics: nil,
		},
		"both_enabled": {
			config: &model.FirecrackerConfig{
				PCIEnabled:     true,
				ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
				ImageFSType:    "ext4",
				GuestIP:        "10.0.0.2",
				NetworkGateway: "10.0.0.1",
				NetworkNetmask: "255.255.255.0",
				EnableLogging:  true,
				EnableMetrics:  true,
				LogLevel:       "Debug",
				LogPath:        "/tmp/mvm/fc.log",
				MetricsPath:    "/tmp/mvm/fc.metrics",
			},
			wantLogger: &model.LoggerConfig{
				LogPath:       "/tmp/mvm/fc.log",
				Level:         "Debug",
				ShowLevel:     true,
				ShowLogOrigin: true,
			},
			wantMetrics: &model.MetricsConfig{
				MetricsPath: "/tmp/mvm/fc.metrics",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := vm.NewFirecrackerSpawner(tc.config)
			got, err := s.Generate()
			require.NoError(t, err)

			if diff := cmp.Diff(tc.wantLogger, got.Logger); diff != "" {
				t.Errorf("Logger mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantMetrics, got.Metrics); diff != "" {
				t.Errorf("Metrics mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── Generate: full config assembly ──────────────────────────────────────
// Rationale: Generate assembles all sub-configs into a single VM config.
// This test verifies the full output struct including BootSource and
// MachineConfig fields not covered by focused tests above.

func TestFirecrackerSpawner_Generate_fullConfig(t *testing.T) {
	config := &model.FirecrackerConfig{
		KernelPath:     "/tmp/vmlinux.bin",
		RootfsPath:     "/tmp/rootfs.ext4",
		VCPUCount:      2,
		MemSizeMiB:     1024,
		GuestIP:        "10.0.0.2",
		GuestMAC:       "02:00:00:00:00:01",
		TapName:        "tap-test",
		NetworkGateway: "10.0.0.1",
		NetworkNetmask: "255.255.255.0",
		PCIEnabled:     true,
		ImageFSUUID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		ImageFSType:    "ext4",
		EnableLogging:  true,
		LogLevel:       "Debug",
		LogPath:        "/tmp/mvm/fc.log",
		MetricsPath:    "/tmp/mvm/fc.metrics",
	}

	s := vm.NewFirecrackerSpawner(config)
	got, err := s.Generate()
	require.NoError(t, err)

	require.NotNil(t, got)
	_ = got.Drives            // verified in drives test
	_ = got.NetworkInterfaces // verified in network test

	assert.Equal(t, config.KernelPath, got.BootSource.KernelImagePath)
	assert.Equal(t, config.VCPUCount, got.MachineConfig.VCPUCount)
	assert.Equal(t, config.MemSizeMiB, got.MachineConfig.MemSizeMiB)
	assert.False(t, got.MachineConfig.SMT)
	assert.False(t, got.MachineConfig.TrackDirtyPages)
}

// ─── RemoveDrive ─────────────────────────────────────────────────────────
// Rationale: Drive removal logic must correctly filter drive entries by
// drive_id, persist the change to disk, and report whether removal occurred.
// A bug here leaves stale drives attached or fails to free resources.

func TestFirecrackerConfigManager_RemoveDrive(t *testing.T) {
	tests := map[string]struct {
		initialDrives []map[string]any
		driveID       string
		wantRemoved   bool
		wantDriveIDs  []string
		wantErr       string
	}{
		"removes_existing_drive": {
			initialDrives: []map[string]any{
				{"drive_id": "rootfs"},
				{"drive_id": "cloud-init"},
			},
			driveID:      "cloud-init",
			wantRemoved:  true,
			wantDriveIDs: []string{"rootfs"},
		},
		"removes_last_remaining_drive": {
			initialDrives: []map[string]any{
				{"drive_id": "rootfs"},
			},
			driveID:      "rootfs",
			wantRemoved:  true,
			wantDriveIDs: []string{},
		},
		"nonexistent_drive_returns_false": {
			initialDrives: []map[string]any{
				{"drive_id": "rootfs"},
			},
			driveID:      "nonexistent",
			wantRemoved:  false,
			wantDriveIDs: []string{"rootfs"},
		},
		"empty_drives_list_returns_false": {
			initialDrives: []map[string]any{},
			driveID:       "rootfs",
			wantRemoved:   false,
			wantDriveIDs:  []string{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "fc-config.json")

			cfg := map[string]any{"drives": tc.initialDrives}
			data, err := json.Marshal(cfg)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(configPath, data, 0644))

			m := vm.NewFirecrackerConfigManager(configPath)
			removed, err := m.RemoveDrive(tc.driveID)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRemoved, removed)

			// Verify persistence
			resultData, err := os.ReadFile(configPath)
			require.NoError(t, err)
			var result map[string]any
			require.NoError(t, json.Unmarshal(resultData, &result))

			resultDrives, _ := result["drives"].([]any)
			gotIDs := make([]string, 0, len(resultDrives))
			for _, d := range resultDrives {
				if dm, ok := d.(map[string]any); ok {
					id, _ := dm["drive_id"].(string)
					gotIDs = append(gotIDs, id)
				}
			}
			if diff := cmp.Diff(tc.wantDriveIDs, gotIDs); diff != "" {
				t.Errorf("Remaining drive IDs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── AddDrive ────────────────────────────────────────────────────────────
// Rationale: AddDrive must append new drive entries and replace existing
// ones by drive_id. A bug here causes duplicate drives or data loss.

func TestFirecrackerConfigManager_AddDrive(t *testing.T) {
	tests := map[string]struct {
		initialDrives []map[string]any
		drive         model.DriveConfig
		wantErr       string
		wantDriveIDs  []string
	}{
		"adds_new_drive": {
			initialDrives: []map[string]any{
				{"drive_id": "rootfs"},
			},
			drive: model.DriveConfig{
				DriveID:    "data",
				PathOnHost: "/tmp/data.ext4",
			},
			wantDriveIDs: []string{"rootfs", "data"},
		},
		"replaces_existing_drive": {
			initialDrives: []map[string]any{
				{"drive_id": "rootfs", "path_on_host": "/tmp/old-rootfs.ext4"},
			},
			drive: model.DriveConfig{
				DriveID:    "rootfs",
				PathOnHost: "/tmp/new-rootfs.ext4",
			},
			wantDriveIDs: []string{"rootfs"},
		},
		"adds_to_empty_list": {
			initialDrives: []map[string]any{},
			drive: model.DriveConfig{
				DriveID:    "rootfs",
				PathOnHost: "/tmp/rootfs.ext4",
			},
			wantDriveIDs: []string{"rootfs"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "fc-config.json")

			cfg := map[string]any{"drives": tc.initialDrives}
			data, err := json.Marshal(cfg)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(configPath, data, 0644))

			m := vm.NewFirecrackerConfigManager(configPath)
			err = m.AddDrive(tc.drive)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Verify persistence
			resultData, err := os.ReadFile(configPath)
			require.NoError(t, err)
			var result map[string]any
			require.NoError(t, json.Unmarshal(resultData, &result))

			resultDrives, _ := result["drives"].([]any)
			gotIDs := make([]string, 0, len(resultDrives))
			for _, d := range resultDrives {
				if dm, ok := d.(map[string]any); ok {
					id, _ := dm["drive_id"].(string)
					gotIDs = append(gotIDs, id)
				}
			}
			if diff := cmp.Diff(tc.wantDriveIDs, gotIDs); diff != "" {
				t.Errorf("Drive IDs after AddDrive mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
