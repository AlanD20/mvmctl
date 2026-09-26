package jailer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

// Rationale: The protected record is read by hotplug, snapshot, and stopped-device archival.
// A variant mismatch must fail before any of those readers can act on device authority.
func TestLaunchDeviceAuthorityAdmitsOnlyMatchingPlacementAndConfig(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*vmLaunchDeviceAuthority)
		wantErr bool
	}{
		"schema two": {
			mutate: func(value *vmLaunchDeviceAuthority) { value.Schema = 2 }, wantErr: true,
		},
		"omitted placement": {
			mutate: func(value *vmLaunchDeviceAuthority) { value.NetworkPlacement = "" }, wantErr: true,
		},
		"unknown placement": {
			mutate: func(value *vmLaunchDeviceAuthority) { value.NetworkPlacement = "other" }, wantErr: true,
		},
		"networkless retains endpoint": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				value.NetworkPlacement = model.VMNetworkPlacementNetworkless
				value.Config.NetworkInterfaces = []managedFirecrackerNetworkInterface{}
			},
			wantErr: true,
		},
		"networkless retains NIC": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				value.NetworkPlacement = model.VMNetworkPlacementNetworkless
				value.NetworkID, value.GuestIPv4, value.GuestMAC = "", "", ""
			},
			wantErr: true,
		},
		"networkless retains IP boot argument": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				makeNetworklessLaunchDeviceAuthority(value)
				value.BootArgs += " ip=dhcp"
				value.Config.BootSource.BootArgs = value.BootArgs
			},
			wantErr: true,
		},
		"networkless retains NoCloud NET boot argument": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				makeNetworklessLaunchDeviceAuthority(value)
				value.BootArgs += " ds=nocloud-net;s=http://169.254.0.1/"
				value.Config.BootSource.BootArgs = value.BootArgs
			},
			wantErr: true,
		},
		"managed missing NIC": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				value.Config.NetworkInterfaces = []managedFirecrackerNetworkInterface{}
			},
			wantErr: true,
		},
		"managed wrong guest MAC": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				value.Config.NetworkInterfaces[0].GuestMAC = "06:00:00:00:00:02"
			},
			wantErr: true,
		},
		"managed wrong interface": {
			mutate:  func(value *vmLaunchDeviceAuthority) { value.Config.NetworkInterfaces[0].IfaceID = "eth1" },
			wantErr: true,
		},
		"managed invalid TAP": {
			mutate: func(value *vmLaunchDeviceAuthority) {
				value.Config.NetworkInterfaces[0].HostDevName = "tap/escape"
			},
			wantErr: true,
		},
		"managed": {},
		"networkless": {
			mutate: makeNetworklessLaunchDeviceAuthority,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			value := testManagedLaunchDeviceAuthority()
			if tc.mutate != nil {
				tc.mutate(&value)
			}
			err := value.validate()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// Rationale: A protected device record can be internally valid while belonging to a different
// launch placement. The read boundary must bind it to the durable instance before device effects.
func TestReadVMLaunchDeviceAuthorityRejectsPlacementDifferentFromInstance(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("protected launch device records require root ownership")
	}
	metadata := testManagedLaunchDeviceAuthority()
	for _, binding := range []*protectedBackingBinding{&metadata.Rootfs, &metadata.KernelBacking} {
		binding.cacheIdentity.inode = 11
		binding.identity = managedVMConfigStat{
			inode: 12, mode: unix.S_IFREG | 0400, nlink: 1, size: binding.bytes,
		}
	}
	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0755))
	path := filepath.Join(root, launchDeviceAuthorityLeaf)
	require.NoError(t, os.WriteFile(path, encoded, 0400))
	require.NoError(t, os.Chmod(path, 0400))
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unix.Close(rootFD)) })
	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(rootFD, &stat))
	record := instanceRecord{
		ownerUID: metadata.OwnerUID, vmID: metadata.VMID, lifecycle: instanceLifecycleActive,
		launchGeneration: metadata.LaunchGeneration,
		launch: &instanceLaunchRecord{
			networkPlacement:  model.VMNetworkPlacementManaged,
			executionIdentity: vmExecutionIdentity{uid: metadata.ExecutionUID, gid: metadata.ExecutionGID},
			hostBootID:        metadata.HostBootID,
			release: releaseIdentity{
				version: metadata.Release.Version, architecture: metadata.Release.Architecture,
				firecrackerSHA256: metadata.FirecrackerSHA256, jailerSHA256: metadata.JailerSHA256,
			},
		},
		active: &instanceActiveIdentity{jailRoot: instanceObjectIdentity{device: uint64(stat.Dev), inode: stat.Ino}},
	}

	got, err := readVMLaunchDeviceAuthority(context.Background(), record, rootFD)
	require.NoError(t, err)
	assert.Equal(t, model.VMNetworkPlacementManaged, got.NetworkPlacement)
	if diff := cmp.Diff(metadata.Config.NetworkInterfaces, got.Config.NetworkInterfaces); diff != "" {
		t.Errorf("protected NIC inventory mismatch (-want +got):\n%s", diff)
	}

	record.launch.networkPlacement = model.VMNetworkPlacementNetworkless
	_, err = readVMLaunchDeviceAuthority(context.Background(), record, rootFD)
	require.Error(t, err)
	assert.ErrorContains(t, err, "launch device authority binding differs")
}

// testManagedLaunchDeviceAuthority supplies a valid protected record so each mutation isolates one placement rule.
func testManagedLaunchDeviceAuthority() vmLaunchDeviceAuthority {
	vmID := strings.Repeat("a", 32)
	kernelID := strings.Repeat("c", 64)
	bootArgs := "console=ttyS0 reboot=k panic=1"
	return vmLaunchDeviceAuthority{
		Schema: 3, VMID: vmID, OwnerUID: 1000, ExecutionUID: 2000, ExecutionGID: 2000,
		Rootfs: protectedBackingBinding{
			kind: protectedBackingRootfs, logicalID: vmID, ownerUID: 1000, bytes: managedVMRootfsMinBytes,
		},
		KernelID: kernelID,
		KernelBacking: protectedBackingBinding{
			kind: protectedBackingKernel, logicalID: kernelID, ownerUID: 1000, bytes: 1,
		},
		NetworkPlacement: model.VMNetworkPlacementManaged,
		NetworkID:        strings.Repeat("b", 64), GuestIPv4: "192.0.2.10", GuestMAC: "06:00:00:00:00:01",
		HostBootID: "boot-id", LaunchGeneration: 1,
		Release:           ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
		FirecrackerSHA256: strings.Repeat("d", 64), JailerSHA256: strings.Repeat("e", 64),
		BootArgs: bootArgs, ConfigSHA256: strings.Repeat("f", 64),
		Volumes: []launchDeviceVolume{},
		Config: managedFirecrackerConfig{
			BootSource: managedFirecrackerBootSource{BootArgs: bootArgs, KernelImagePath: "/kernel"},
			Drives: []managedFirecrackerDrive{{
				DriveID: "rootfs", PathOnHost: "/rootfs", IsRootDevice: true, CacheType: model.CacheTypeWriteback,
				IOEngine: "Sync",
			}},
			NetworkInterfaces: []managedFirecrackerNetworkInterface{{
				IfaceID: "eth0", GuestMAC: "06:00:00:00:00:01", HostDevName: "mvtap012345678",
			}},
		},
	}
}

func makeNetworklessLaunchDeviceAuthority(value *vmLaunchDeviceAuthority) {
	value.NetworkPlacement = model.VMNetworkPlacementNetworkless
	value.NetworkID, value.GuestIPv4, value.GuestMAC = "", "", ""
	value.Config.NetworkInterfaces = []managedFirecrackerNetworkInterface{}
}
