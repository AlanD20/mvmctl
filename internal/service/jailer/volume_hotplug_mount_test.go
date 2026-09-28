package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

// This root-only family is excluded from unprivileged CI by the existing
// TestLaunchManaged selector. It exercises the real kernel mount boundary,
// without replacing process verification or privileged syscall results.
func TestLaunchManagedVolumeHotplugMountTransition(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("volume namespace transitions require root in the disposable runner")
	}
	cases := []struct {
		name     string
		readOnly bool
		cancel   bool
		badNS    bool
		changed  bool
	}{
		{name: "cancelled", cancel: true},
		{name: "rejected_namespace", badNS: true},
		{name: "changed_source", changed: true},
		{name: "read_write"},
		{name: "read_only", readOnly: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := t.TempDir()
			volumes := filepath.Join(root, "volumes")
			journal := filepath.Join(volumes, volumeHotplugJournalDirectory)
			require.NoError(t, os.Mkdir(volumes, 0755))
			require.NoError(t, os.Mkdir(journal, 0700))
			payload := strings.Repeat("retained volume bytes", 128)
			sourcePath := filepath.Join(root, "source.raw")
			require.NoError(t, os.WriteFile(sourcePath, []byte(payload), 0600))
			source, err := unix.Open(sourcePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, unix.Close(source)) })
			var sourceStat unix.Stat_t
			require.NoError(t, unix.Fstat(source, &sourceStat))
			boot, err := readCurrentProcessBootID(ctx)
			require.NoError(t, err)
			namespace, directory, journalFD := pinHotplugTestNamespace(t, volumes, journal)
			transaction := &volumeHotplugTransaction{
				directory: directory, journal: journalFD, source: source, attach: true,
				record: volumeHotplugRecord{
					Schema: 1, VMID: testVMID, OwnerUID: 1000, BootID: boot, LaunchGeneration: 1,
					State: "mounting", SourceMajor: unix.Major(sourceStat.Dev),
					SourceMinor: unix.Minor(sourceStat.Dev), SourceInode: sourceStat.Ino,
					SourceUID: sourceStat.Uid, SourceGID: sourceStat.Gid, SourceMode: uint16(sourceStat.Mode),
					Volume: VMLaunchVolume{
						ID: strings.Repeat("a", 64), Format: model.VolumeFormatRaw,
						ExpectedBytes: uint64(len(payload)), ReadOnly: tc.readOnly, CacheType: model.CacheTypeUnsafe,
					},
				},
			}
			require.NoError(t, transaction.write(ctx))
			initialRecord := transaction.record
			if tc.cancel {
				cancel()
			}
			if tc.changed {
				require.NoError(t, os.Truncate(sourcePath, int64(len(payload)-1)))
			}
			selectedNS := namespace
			if tc.badNS {
				selectedNS = -1
			}
			beforeFDs := hotplugTestDescriptors(t)
			err = runVolumeHotplugMountWorker(ctx, selectedNS, transaction)
			if tc.cancel || tc.badNS || tc.changed {
				require.Error(t, err)
				if tc.cancel {
					assert.ErrorIs(t, err, context.Canceled)
				}
				if tc.badNS {
					assert.ErrorIs(t, err, unix.EBADF)
				}
				if tc.changed {
					assert.Contains(t, err.Error(), "source changed")
				}
				_, statErr := os.Stat(filepath.Join(volumes, transaction.record.Volume.private().basename()))
				assert.ErrorIs(t, statErr, os.ErrNotExist)
				assertHotplugTestDescriptors(t, beforeFDs)
				recorded, present, readErr := readVolumeHotplugRecord(
					context.Background(), journalFD, transaction.record.Volume.ID+".json",
				)
				require.NoError(t, readErr)
				require.True(t, present)
				if diff := cmp.Diff(initialRecord, recorded); diff != "" {
					t.Errorf("rejected mount changed durable intent (-want +got):\n%s", diff)
				}
				return
			}
			require.NoError(t, err)
			assertHotplugTestDescriptors(t, beforeFDs)
			name := transaction.record.Volume.private().basename()
			target, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			require.NoError(t, err)
			t.Cleanup(func() {
				if target >= 0 {
					assert.NoError(t, unix.Close(target))
				}
			})
			mountedBytes, err := readBoundedProcessDescriptor(ctx, target, len(payload)+1)
			require.NoError(t, err)
			assert.Equal(t, payload, string(mountedBytes))
			var mounted unix.Stat_t
			require.NoError(t, unix.Fstat(target, &mounted))
			assert.Equal(t, sourceStat.Ino, mounted.Ino)
			assert.Equal(t, sourceStat.Dev, mounted.Dev)
			var filesystem unix.Statfs_t
			require.NoError(t, unix.Fstatfs(target, &filesystem))
			wantFlags := int64(unix.ST_NOSUID | unix.ST_NODEV | unix.ST_NOEXEC)
			if tc.readOnly {
				wantFlags |= unix.ST_RDONLY
			}
			mask := int64(unix.ST_NOSUID | unix.ST_NODEV | unix.ST_NOEXEC | unix.ST_RDONLY)
			assert.Equal(t, wantFlags, filesystem.Flags&mask)
			require.NoError(t, unix.Close(target))
			target = -1
			// The host sees only the empty exclusive placeholder, never the guest mount.
			hostBytes, err := os.ReadFile(filepath.Join(volumes, name))
			require.NoError(t, err)
			assert.Empty(t, hostBytes)
			recorded, present, err := readVolumeHotplugRecord(ctx, journalFD, transaction.record.Volume.ID+".json")
			require.NoError(t, err)
			require.True(t, present)
			if diff := cmp.Diff(transaction.record, recorded); diff != "" {
				t.Errorf("durable mount intent (-want +got):\n%s", diff)
			}
			inspection, mountedIdentity, err := transaction.inspectTarget(ctx)
			require.NoError(t, err)
			require.NoError(t, unix.Close(inspection))
			assert.Equal(t, recorded.MountID, mountedIdentity.Mnt_id)
			require.NoError(t, runVolumeHotplugMountWorker(ctx, namespace, transaction))
			assertHotplugTestDescriptors(t, beforeFDs)
			transaction.record.State, transaction.attach = "detaching", false
			require.NoError(t, transaction.write(ctx))
			require.NoError(t, runVolumeHotplugMountWorker(ctx, namespace, transaction))
			assertHotplugTestDescriptors(t, beforeFDs)
			_, err = os.Stat(filepath.Join(volumes, name))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func pinHotplugTestNamespace(t *testing.T, volumes, journal string) (int, int, int) {
	t.Helper()
	type pins struct {
		namespace, directory, journal int
		err                           error
	}
	done := make(chan pins, 1)
	go func() {
		runtime.LockOSThread()
		result := pins{namespace: -1, directory: -1, journal: -1}
		defer func() {
			if result.err != nil {
				for _, fd := range []int{result.journal, result.directory, result.namespace} {
					if fd >= 0 {
						result.err = errors.Join(result.err, unix.Close(fd))
					}
				}
			}
			done <- result
		}()
		if result.err = unix.Unshare(unix.CLONE_FS | unix.CLONE_NEWNS); result.err != nil {
			runtime.UnlockOSThread()
			return
		}
		// Retire this thread after unsharing; private propagation prevents host effects.
		if result.err = unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); result.err != nil {
			return
		}
		result.namespace, result.err = unix.Open("/proc/thread-self/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if result.err != nil {
			return
		}
		flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		result.directory, result.err = unix.Open(volumes, flags, 0)
		if result.err != nil {
			return
		}
		result.journal, result.err = unix.Open(journal, flags, 0)
	}()
	result := <-done
	require.NoError(t, result.err)
	t.Cleanup(func() {
		for _, fd := range []int{result.journal, result.directory, result.namespace} {
			assert.NoError(t, unix.Close(fd))
		}
	})
	return result.namespace, result.directory, result.journal
}

func hotplugTestDescriptors(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	result := make(map[string]string)
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // ReadDir's own descriptor has already closed.
		}
		require.NoError(t, err)
		result[entry.Name()] = target
	}
	return result
}

func assertHotplugTestDescriptors(t *testing.T, want map[string]string) {
	t.Helper()
	if diff := cmp.Diff(want, hotplugTestDescriptors(t)); diff != "" {
		t.Errorf("retained descriptors after mount transition (-want +got):\n%s", diff)
	}
}
