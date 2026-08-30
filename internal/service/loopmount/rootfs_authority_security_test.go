package loopmount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Rationale: A caller may rename the admitted leaf or replace its pathname with a symlink immediately after
// admission. The future effect must still see the exact regular file admitted through the retained descriptor.
func TestRootfsLeasePinsEffectVisibleLeafDuringContinuousSymlinkReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	imageDirectory := filepath.Join(root, "cache", "images", "staging", testImageID)
	require.NoError(t, os.MkdirAll(imageDirectory, 0700))
	trustedPath := filepath.Join(imageDirectory, "rootfs.img")
	parkedPath := filepath.Join(imageDirectory, "rootfs.parked")
	attackerPath := filepath.Join(imageDirectory, "attacker.img")
	trustedBytes := []byte("trusted!")
	attackerBytes := []byte("attacker")
	require.NoError(t, os.WriteFile(trustedPath, trustedBytes, 0600))
	require.NoError(t, os.WriteFile(attackerPath, attackerBytes, 0600))

	caller := rootfsCallerForRealFSTest(t, filepath.Join(root, "cache"))
	deps := rootfsRealFSTestDeps(t, root)
	request := ImageInspectRequest{
		CacheLocator:       "/cache",
		ImageID:            testImageID,
		ExpectedImageBytes: uint64(len(trustedBytes)),
		Filesystem:         FilesystemExt4,
	}

	stop := make(chan struct{})
	attackerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				attackerDone <- nil
				return
			default:
			}
			if err := os.Rename(trustedPath, parkedPath); err != nil {
				attackerDone <- err
				return
			}
			if err := os.Symlink(filepath.Base(attackerPath), trustedPath); err != nil {
				attackerDone <- err
				return
			}
			runtime.Gosched()
			if err := os.Remove(trustedPath); err != nil {
				attackerDone <- err
				return
			}
			if err := os.Rename(parkedPath, trustedPath); err != nil {
				attackerDone <- err
				return
			}
		}
	}()

	var lease *rootfsLease
	for range 5000 {
		candidate, err := admitImageInspectRootfs(t.Context(), deps, caller, request)
		if err != nil {
			runtime.Gosched()
			continue
		}
		lease = candidate
		break
	}
	close(stop)
	require.NoError(t, <-attackerDone)
	require.NotNil(t, lease, "continuous replacement never allowed a regular-file admission")
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	// Unlink every possible name for the admitted inode, then leave the attacker symlink at the original pathname.
	for _, path := range []string{trustedPath, parkedPath} {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
	}
	require.NoError(t, os.Symlink(filepath.Base(attackerPath), trustedPath))
	pathBytes, err := os.ReadFile(trustedPath)
	require.NoError(t, err)
	assert.Equal(t, string(attackerBytes), string(pathBytes))

	pinnedBytes := make([]byte, len(trustedBytes))
	readBytes, err := unix.Pread(lease.rootfsFD, pinnedBytes, 0)
	require.NoError(t, err)
	assert.Equal(t, len(trustedBytes), readBytes)
	assert.Equal(t, string(trustedBytes), string(pinnedBytes))
	var pinnedStat unix.Stat_t
	require.NoError(t, unix.Fstat(lease.rootfsFD, &pinnedStat))
	assert.Equal(t, lease.metadata.inode, pinnedStat.Ino)
}

func rootfsRealFSTestDeps(t *testing.T, root string) rootfsAuthorityDeps {
	t.Helper()

	deps := systemRootfsAuthorityDeps()
	realOpenRoot := deps.openRoot
	realStatx := deps.statx
	var mu sync.Mutex
	rootFD := -1
	deps.openRoot = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		assert.Equal(t, "/", path)
		fd, err := realOpenRoot(ctx, root, flags, mode)
		if err == nil {
			mu.Lock()
			rootFD = fd
			mu.Unlock()
		}
		return fd, err
	}
	deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
		if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
			return err
		}
		mu.Lock()
		isRoot := fd == rootFD
		mu.Unlock()
		if isRoot {
			stat.Uid = 0
		}
		return nil
	}
	// The race test exercises real descriptor resolution. Filesystem policy has independent closed-set tests, and
	// common test-container overlay filesystems are deliberately outside the production allowlist.
	deps.statfs = func(_ context.Context, _ int, stat *unix.Statfs_t) error {
		stat.Type = unix.EXT4_SUPER_MAGIC
		return nil
	}
	return deps
}

func rootfsCallerForRealFSTest(t *testing.T, cacheRoot string) Caller {
	t.Helper()

	uid := os.Geteuid()
	gid := os.Getegid()
	if uid != 0 {
		if gid == 0 {
			gid = 1
		}
		return Caller{UID: uint32(uid), GID: uint32(gid)}
	}

	uid = 65534
	if gid == 0 {
		gid = 65534
	}
	err := filepath.Walk(cacheRoot, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chown(path, uid, gid)
	})
	require.NoError(t, err)
	return Caller{UID: uint32(uid), GID: uint32(gid)}
}
