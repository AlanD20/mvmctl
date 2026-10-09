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

	"mvmctl/internal/lib/model"
)

// pciVolumeReadyFixture adds the kernel driver and rescan leaves to the existing exact-disk fixture.
func pciVolumeReadyFixture(t *testing.T) (pciVolumeFS, pciVolumeDisk) {
	t.Helper()
	fs, disk := pciVolumeFixture(t)
	driver := filepath.Join(fs.sys, "bus/virtio/drivers/virtio_blk")
	require.NoError(t, os.MkdirAll(driver, 0755))
	virtio := filepath.Dir(filepath.Dir(disk.path))
	require.NoError(t, os.Symlink(virtio, filepath.Join(disk.path, "device")))
	require.NoError(t, os.Symlink(driver, filepath.Join(virtio, "driver")))
	require.NoError(t, os.WriteFile(filepath.Join(fs.sys, "bus/pci/rescan"), []byte("0\n"), 0644))
	return fs, disk
}

func TestEnsurePCIVolumeReadyRequiresExactBoundDisk(t *testing.T) {
	// Rationale: PCI rescan exit/success does not prove that virtio_blk created the selected disk.
	for name, tc := range map[string]struct {
		change       func(*testing.T, pciVolumeFS, pciVolumeDisk)
		identity     model.PCIVolumeIdentity
		wantError    bool
		wantDeadline bool
	}{
		"successful rescan without selected block": {
			identity:  model.PCIVolumeIdentity{Serial: "999", ExpectedBytes: 4096},
			wantError: true, wantDeadline: true,
		},
		"capacity mismatch": {
			identity: model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 8192}, wantError: true,
		},
		"rescan unavailable": {
			change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
				require.NoError(t, os.Remove(filepath.Join(fs.sys, "bus/pci/rescan")))
			}, wantError: true,
		},
		"rescan write fails": {
			change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
				path := filepath.Join(fs.sys, "bus/pci/rescan")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink("/dev/full", path))
			}, wantError: true,
		},
		"driver absent": {
			change: func(t *testing.T, _ pciVolumeFS, disk pciVolumeDisk) {
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(filepath.Dir(disk.path)), "driver")))
			}, wantError: true, wantDeadline: true,
		},
		"wrong driver": {
			change: func(t *testing.T, fs pciVolumeFS, disk pciVolumeDisk) {
				driver := filepath.Join(fs.sys, "bus/virtio/drivers/other")
				require.NoError(t, os.MkdirAll(driver, 0755))
				link := filepath.Join(filepath.Dir(filepath.Dir(disk.path)), "driver")
				require.NoError(t, os.Remove(link))
				require.NoError(t, os.Symlink(driver, link))
			}, wantError: true,
		},
		"duplicate serial": {
			change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
				require.NoError(t, os.WriteFile(filepath.Join(fs.sys, "block/vda/serial"), []byte("123\n"), 0644))
			}, wantError: true,
		},
		"matching driver-bound disk": {},
		"matching mounted disk": {
			change: func(t *testing.T, fs pciVolumeFS, _ pciVolumeDisk) {
				require.NoError(t, os.WriteFile(filepath.Join(fs.proc, "self/mountinfo"),
					[]byte("1 0 252:0 / / rw - ext4 /dev/vda rw\n2 1 252:16 / /data rw - ext4 /dev/vdb rw\n"), 0644))
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			fs, disk := pciVolumeReadyFixture(t)
			if tc.change != nil {
				tc.change(t, fs, disk)
			}
			identity := tc.identity
			if identity.Serial == "" {
				identity = model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			err := ensurePCIVolumeReady(ctx, identity, fs)
			if tc.wantError {
				assert.Error(t, err)
				if tc.wantDeadline {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
				}
				return
			}
			assert.NoError(t, err)
			rescan, err := os.ReadFile(filepath.Join(fs.sys, "bus/pci/rescan"))
			require.NoError(t, err)
			if diff := cmp.Diff([]byte("1\n"), rescan); diff != "" {
				t.Errorf("checked PCI rescan (-want +got):\n%s", diff)
			}
			remove, err := os.ReadFile(filepath.Join(disk.pci, "remove"))
			require.NoError(t, err)
			assert.Equal(t, "untouched", string(remove), "readiness must not remove the selected device")
		})
	}
}

func TestEnsurePCIVolumeReadyWaitsForDriverBinding(t *testing.T) {
	fs, disk := pciVolumeReadyFixture(t)
	link := filepath.Join(filepath.Dir(filepath.Dir(disk.path)), "driver")
	require.NoError(t, os.Remove(link))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ensurePCIVolumeReady(ctx, model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}, fs)
	}()
	// Kernel binding can follow the rescan; synchronize on the rescan effect, not a scheduling delay.
	for {
		raw, err := os.ReadFile(filepath.Join(fs.sys, "bus/pci/rescan"))
		require.NoError(t, err)
		if string(raw) == "1\n" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("readiness completed before driver binding: %v", err)
		case <-ctx.Done():
			t.Fatal("PCI rescan was not observed")
		case <-time.After(time.Millisecond):
		}
	}
	require.NoError(t, os.Symlink(filepath.Join(fs.sys, "bus/virtio/drivers/virtio_blk"), link))
	assert.NoError(t, <-done)
}

func TestPCIVolumeLockWaitHonorsCancellationWithoutEffects(t *testing.T) {
	// Rationale: Another PCI operation must not make the request outlive its finite guest budget.
	for _, operation := range []string{"readiness", "removal"} {
		t.Run(operation, func(t *testing.T) {
			fs, _ := pciVolumeReadyFixture(t)
			pciVolumeMu.Lock()
			defer pciVolumeMu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			identity := model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
			var err error
			if operation == "readiness" {
				err = ensurePCIVolumeReady(ctx, identity, fs)
			} else {
				err = removePCIVolume(ctx, identity, fs)
			}
			assert.ErrorIs(t, err, context.DeadlineExceeded)
			raw, readErr := os.ReadFile(filepath.Join(fs.sys, "bus/pci/rescan"))
			require.NoError(t, readErr)
			assert.Equal(t, "0\n", string(raw), "cancelled lock waiter must leave PCI unchanged")
		})
	}
}

func TestPCIVolumeReadyProtocolRejectsBeforeEffects(t *testing.T) {
	for _, token := range []string{"", "configured"} {
		t.Run("configured_token_"+token, func(t *testing.T) {
			client, server := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); (&Agent{token: token}).handleConnection(ctx, server) }()
			_, err := io.WriteString(client, PCIVolumeReadyMagic)
			require.NoError(t, err)
			identity := model.PCIVolumeIdentity{Serial: "123", ExpectedBytes: 4096}
			require.NoError(t, WritePCIVolumeReadyRequest(ctx, client,
				PCIVolumeReadyRequest{Token: "incorrect", Volume: identity}))
			got, err := ReadPCIVolumeReadyResult(ctx, client)
			require.NoError(t, err)
			want := PCIVolumeReadyResult{Volume: identity, Error: "guest volume authentication failed"}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("unauthenticated readiness (-want +got):\n%s", diff)
			}
			var trailing [1]byte
			n, err := client.Read(trailing[:])
			assert.Zero(t, n)
			assert.ErrorIs(t, err, io.EOF)
			<-done
		})
	}
	for name, raw := range map[string]string{
		"caller device path":  `{"token":"secret","volume":{"serial":"123","expected_bytes":4096,"path":"/dev/vdb"}}`,
		"generic operation":   `{"token":"secret","volume":{"serial":"123","expected_bytes":4096},"operation":"rescan"}`,
		"duplicate readiness": `{"volume":{"serial":"123","expected_bytes":4096},"ready":true,"ready":false,"error":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
			frame := bytes.NewReader(append(header[:], raw...))
			var err error
			if name == "duplicate readiness" {
				_, err = ReadPCIVolumeReadyResult(t.Context(), frame)
			} else {
				_, err = ReadPCIVolumeReadyRequest(t.Context(), frame)
			}
			assert.Error(t, err)
		})
	}
}
