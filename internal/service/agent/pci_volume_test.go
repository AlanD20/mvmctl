package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

func TestSelectPCIVolumeUsesExactIdentity(t *testing.T) {
	// Rationale: A middle disk must be selected without touching a later hotplugged disk.
	identity := model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
	selected := pciVolumeDisk{
		name:   "vdb",
		path:   "/sys/devices/disk-b",
		pci:    "/sys/devices/pci-b",
		serial: "123",
		bytes:  4096,
		device: 2,
	}
	cases := map[string]struct {
		disks  []pciVolumeDisk
		want   *pciVolumeDisk
		reject bool
	}{
		"duplicate serial": {
			disks:  []pciVolumeDisk{selected, {name: "vdc", serial: "123", bytes: 4096}},
			reject: true,
		},
		"capacity changed": {disks: []pciVolumeDisk{{name: "vdb", serial: "123", bytes: 8192}}, reject: true},
		"shared PCI function": {
			disks:  []pciVolumeDisk{selected, {name: "vdc", pci: selected.pci, serial: "999", bytes: 4096}},
			reject: true,
		},
		"middle disk": {
			disks: []pciVolumeDisk{
				{name: "vda", serial: "111", bytes: 4096},
				selected,
				{name: "vdz", serial: "999", bytes: 4096},
			},
			want: &selected,
		},
		"already absent": {disks: []pciVolumeDisk{{name: "vda", serial: "111", bytes: 4096}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := selectPCIVolume(tc.disks, identity)
			if tc.reject {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(pciVolumeDisk{})); diff != "" {
				t.Errorf("selected disk (-want +got):\n%s", diff)
			}
		})
	}
}

// pciVolumeFixture builds a kernel-shaped root disk and one unmounted data disk.
func pciVolumeFixture(t *testing.T) (pciVolumeFS, pciVolumeDisk) {
	t.Helper()
	root := t.TempDir()
	fs := pciVolumeFS{
		sys:  filepath.Join(root, "sys"),
		dev:  filepath.Join(root, "dev"),
		proc: filepath.Join(root, "proc"),
	}
	for _, path := range []string{"block", "bus/pci/devices", "dev/block", "devices"} {
		require.NoError(t, os.MkdirAll(filepath.Join(fs.sys, path), 0755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(fs.proc, "self"), 0755))
	for index, name := range []string{"vda", "vdb"} {
		bdf := []string{"0000:00:01.0", "0000:00:02.0"}[index]
		pci := filepath.Join(fs.sys, "devices", "pci0000:00", bdf)
		path := filepath.Join(pci, "virtio0", "block", name)
		require.NoError(t, os.MkdirAll(filepath.Join(path, "holders"), 0755))
		require.NoError(t, os.Symlink(filepath.Join(fs.sys, "bus/pci"), filepath.Join(pci, "subsystem")))
		require.NoError(t, os.Symlink(pci, filepath.Join(fs.sys, "bus/pci/devices", bdf)))
		require.NoError(t, os.Symlink(path, filepath.Join(fs.sys, "block", name)))
		dev := []string{"252:0", "252:16"}[index]
		require.NoError(t, os.Symlink(path, filepath.Join(fs.sys, "dev/block", dev)))
		require.NoError(t, os.WriteFile(filepath.Join(path, "dev"), []byte(dev+"\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(path, "size"), []byte("8\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(path, "serial"), []byte([]string{"111\n", "123\n"}[index]), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(pci, "remove"), []byte("untouched"), 0644))
	}
	require.NoError(
		t,
		os.WriteFile(filepath.Join(fs.proc, "self/mountinfo"), []byte("1 0 252:0 / / rw - ext4 /dev/vda rw\n"), 0644),
	)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(fs.proc, "swaps"), []byte("Filename Type Size Used Priority\n"), 0644),
	)
	pci := filepath.Join(fs.sys, "devices/pci0000:00/0000:00:02.0")
	return fs, pciVolumeDisk{
		name:   "vdb",
		path:   filepath.Join(pci, "virtio0/block/vdb"),
		pci:    pci,
		serial: "123",
		bytes:  4096,
		device: unix.Mkdev(252, 16),
	}
}

func TestPCIVolumeDiscoveryAndIdleAdmission(t *testing.T) {
	// Rationale: Complete discovery and idle admission must refuse uncertain or busy evidence.
	cases := map[string]struct {
		change         func(*testing.T, pciVolumeFS, pciVolumeDisk)
		discoveryError bool
		idleError      bool
	}{
		"missing serial": {change: func(t *testing.T, _ pciVolumeFS, disk pciVolumeDisk) {
			require.NoError(t, os.Remove(filepath.Join(disk.path, "serial")))
		}, discoveryError: true},
		"non PCI ancestry": {change: func(t *testing.T, _ pciVolumeFS, disk pciVolumeDisk) {
			require.NoError(t, os.Remove(filepath.Join(disk.pci, "subsystem")))
		}, discoveryError: true},
		"mounted disk": {change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
			require.NoError(
				t,
				os.WriteFile(
					filepath.Join(fs.proc, "self/mountinfo"),
					[]byte("1 0 252:0 / / rw - ext4 /dev/vda rw\n2 1 252:16 / /data rw - ext4 /dev/vdb rw\n"),
					0644,
				),
			)
		}, idleError: true},
		"partitions": {change: func(t *testing.T, _ pciVolumeFS, disk pciVolumeDisk) {
			require.NoError(t, os.Mkdir(filepath.Join(disk.path, "vdb1"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(disk.path, "vdb1/partition"), []byte("1"), 0644))
		}, idleError: true},
		"holders": {change: func(t *testing.T, _ pciVolumeFS, disk pciVolumeDisk) {
			require.NoError(t, os.WriteFile(filepath.Join(disk.path, "holders/dm-0"), []byte("holder"), 0644))
		}, idleError: true},
		"unknown root": {change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
			require.NoError(t, os.Remove(filepath.Join(fs.sys, "dev/block/252:0")))
		}, idleError: true},
		"incomplete swaps": {change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
			require.NoError(t, os.Remove(filepath.Join(fs.proc, "swaps")))
		}, idleError: true},
		"idle exact disk": {},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs, disk := pciVolumeFixture(t)
			if tc.change != nil {
				tc.change(t, fs, disk)
			}
			disks, err := discoverPCIVolumes(t.Context(), fs)
			if tc.discoveryError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			selected, err := selectPCIVolume(disks, model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096})
			require.NoError(t, err)
			if diff := cmp.Diff(&disk, selected, cmp.AllowUnexported(pciVolumeDisk{})); diff != "" {
				t.Errorf("discovered identity (-want +got):\n%s", diff)
			}
			err = admitIdlePCIVolume(t.Context(), fs, disk)
			if tc.idleError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			raw, err := os.ReadFile(filepath.Join(disk.pci, "remove"))
			require.NoError(t, err)
			assert.Equal(t, "untouched", string(raw))
		})
	}
	t.Run("guest root refused", func(t *testing.T) {
		fs, _ := pciVolumeFixture(t)
		disks, err := discoverPCIVolumes(t.Context(), fs)
		require.NoError(t, err)
		root, err := selectPCIVolume(disks, model.PCIVolumeIdentity{Serial: "111", ExpectedBytes: 4096})
		require.NoError(t, err)
		require.NotNil(t, root)
		assert.Error(t, admitIdlePCIVolume(t.Context(), fs, *root))
	})
	t.Run("cancelled discovery", func(t *testing.T) {
		fs, _ := pciVolumeFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := discoverPCIVolumes(ctx, fs)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestPCIVolumeProtocolRejectsBeforeEffects(t *testing.T) {
	// Rationale: Empty configured credentials must never authorize guest PCI mutation.
	for name, token := range map[string]string{"unconfigured agent": "", "wrong token": "configured"} {
		t.Run(name, func(t *testing.T) {
			a := New(9999, token, "")
			client, server := net.Pipe()
			t.Cleanup(func() { assert.NoError(t, client.Close()) })
			require.NoError(t, client.SetDeadline(time.Now().Add(time.Second)))
			done := make(chan struct{})
			go func() { defer close(done); a.handleConnection(t.Context(), server) }()
			_, err := io.WriteString(client, PCIVolumeMagic)
			require.NoError(t, err)
			identity := model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
			require.NoError(
				t,
				WritePCIVolumeRemoveRequest(
					t.Context(),
					client,
					PCIVolumeRemoveRequest{Token: "incorrect", Volume: identity},
				),
			)
			got, err := ReadPCIVolumeRemoveResult(t.Context(), client)
			require.NoError(t, err)
			want := PCIVolumeRemoveResult{Volume: identity, Error: "guest volume authentication failed"}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("authentication refusal (-want +got):\n%s", diff)
			}
			var trailing [1]byte
			_, err = client.Read(trailing[:])
			assert.ErrorIs(t, err, io.EOF)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("guest handler did not release transport")
			}
		})
	}
	for name, raw := range map[string]string{
		"caller PCI path":    `{"token":"secret","volume":{"serial":"123","expected_bytes":4096,"path":"/sys/bus/pci/devices/0000:00:01.0/remove"}}`,
		"duplicate identity": `{"token":"secret","volume":{"serial":"123","serial":"999","expected_bytes":4096}}`,
		"trailing JSON":      `{"token":"secret","volume":{"serial":"123","expected_bytes":4096}}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
			_, err := ReadPCIVolumeRemoveRequest(t.Context(), bytes.NewReader(append(header[:], raw...)))
			assert.Error(t, err)
		})
	}
}

func TestRemovePCIVolumeAlreadyAbsentRequiresCompleteDiscovery(t *testing.T) {
	// Rationale: Retry can acknowledge absence only after every remaining virtio disk is admitted.
	for name, incomplete := range map[string]bool{"incomplete remaining disk": true, "complete absence": false} {
		t.Run(name, func(t *testing.T) {
			fs, disk := pciVolumeFixture(t)
			if incomplete {
				require.NoError(t, os.Remove(filepath.Join(disk.path, "size")))
			}
			err := removePCIVolume(t.Context(), model.PCIVolumeIdentity{Serial: "999", ExpectedBytes: 4096}, fs)
			if incomplete {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			got, err := os.ReadFile(filepath.Join(disk.pci, "remove"))
			require.NoError(t, err)
			assert.Equal(t, "untouched", string(got))
		})
	}
}
