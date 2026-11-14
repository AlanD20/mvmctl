package loopmount

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// The immutable Btrfs snapshot must not replay its tree log even on a read-only mount.
// If the kernel refuses that mount, its source capabilities must still be released.
func TestImageConversionSnapshotMountPreservesReadOnlyAuthorityOnFailure(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	fixture.source.state.filesystem = FilesystemBtrfs
	inspect := fixture.deps()
	mountErr := errors.New("snapshot mount refused")
	var mountOptions string
	deps := rootfsVMProvisionDeps{
		lockOSThread: inspect.lockOSThread, unlockOSThread: inspect.unlockOSThread,
		unshare: inspect.unshare, unmount: inspect.unmount,
		open: inspect.open, openAt2: inspect.openAt2, mkdirAt: inspect.mkdirAt,
		fstat: inspect.fstat, close: inspect.close,
		discoverPartitions: inspect.discoverPartitions, probeFilesystem: inspect.probeFilesystem,
		attachLoop: func(ctx context.Context, lease *rootfsLease) (*rootfsLoopDevice, error) {
			assert.Equal(t, fixture.lease.rootfsFD, lease.rootfsFD)
			assert.Equal(t, rootfsReadOnly, lease.access)
			return inspect.attachLoop(ctx, lease)
		},
		mount: func(ctx context.Context, source, target, filesystem string, flags uintptr, data string) error {
			if target == "/" {
				return inspect.mount(ctx, source, target, filesystem, flags, data)
			}
			assert.Equal(t, "/dev/loop3p1", source)
			assert.Equal(t, rootfsInspectMountPath, target)
			assert.Equal(t, "btrfs", filesystem)
			assert.Equal(t, uintptr(unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC), flags)
			mountOptions = data
			return mountErr
		},
	}

	err := constructImageConversionCandidate(t.Context(), deps, &imageConversionTransaction{
		snapshotFD: fixture.lease.rootfsFD,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, mountErr)
	assert.Equal(t, "rescue=nologreplay", mountOptions)
	assert.True(t, fixture.source.state.released)
	assert.True(t, fixture.partitions.state.released)
	assert.True(t, fixture.loop.state.released)
	assert.NotContains(t, fixture.events, "unmount", "a rejected mount must not be unmounted")
	assert.False(t, fixture.unlocked, "a thread with a private mount namespace cannot rejoin the runtime pool")
}
