package loopmount

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type rootfsReplacementPhase uint8

const (
	rootfsReplaceBeforeOpen rootfsReplacementPhase = iota + 1
	rootfsReplaceAfterOpen
)

type rootfsReplacementCase struct {
	name        string
	selection   rootfsSelectionKind
	components  []string
	targetIndex int
}

// Rationale: Every caller-owned component is mutable while root admits a request. A replacement immediately before
// or after that component's open must either fail closed or retain the original chain through the eventual effect.
func TestRootfsLeasePinsEveryCallerOwnedComponentDuringReplacement(t *testing.T) {
	t.Parallel()

	vmComponents := []string{"owner", "cache", "vms", testVMID, "rootfs.img"}
	imageComponents := []string{"owner", "cache", "images", "staging", testImageID, "rootfs.img"}
	tests := []rootfsReplacementCase{
		{name: "locator ancestor", selection: rootfsVMProvisionSelection, components: vmComponents, targetIndex: 0},
		{name: "cache root", selection: rootfsVMProvisionSelection, components: vmComponents, targetIndex: 1},
		{name: "VM collection", selection: rootfsVMProvisionSelection, components: vmComponents, targetIndex: 2},
		{name: "VM directory", selection: rootfsVMProvisionSelection, components: vmComponents, targetIndex: 3},
		{name: "VM rootfs", selection: rootfsVMProvisionSelection, components: vmComponents, targetIndex: 4},
		{name: "image collection", selection: rootfsImageInspectSelection, components: imageComponents, targetIndex: 2},
		{name: "image staging", selection: rootfsImageInspectSelection, components: imageComponents, targetIndex: 3},
		{name: "image directory", selection: rootfsImageInspectSelection, components: imageComponents, targetIndex: 4},
		{
			name:        "image inspect rootfs",
			selection:   rootfsImageInspectSelection,
			components:  imageComponents,
			targetIndex: 5,
		},
		{
			name:        "image optimize rootfs",
			selection:   rootfsImageOptimizeSelection,
			components:  imageComponents,
			targetIndex: 5,
		},
	}
	phases := []struct {
		name  string
		phase rootfsReplacementPhase
	}{
		{name: "before open", phase: rootfsReplaceBeforeOpen},
		{name: "after open", phase: rootfsReplaceAfterOpen},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, phase := range phases {
				t.Run(phase.name, func(t *testing.T) {
					runRootfsReplacementCase(t, tc, phase.phase)
				})
			}
		})
	}
}

func runRootfsReplacementCase(t *testing.T, tc rootfsReplacementCase, phase rootfsReplacementPhase) {
	t.Helper()

	root := t.TempDir()
	trustedPrefix := []byte("trusted!")
	attackerPrefix := []byte("attacker")
	expectedBytes := uint64(len(trustedPrefix))
	if tc.selection == rootfsVMProvisionSelection {
		expectedBytes = minimumRootfsBytes
	}

	trustedLeaf := filepath.Join(append([]string{root}, tc.components...)...)
	require.NoError(t, os.MkdirAll(filepath.Dir(trustedLeaf), 0700))
	writeRootfsReplacementImage(t, trustedLeaf, trustedPrefix, expectedBytes)

	targetPath := filepath.Join(append([]string{root}, tc.components[:tc.targetIndex+1]...)...)
	exchangePath := targetPath + ".attacker-link"
	attackerObject := targetPath + ".attacker-object"
	attackerLeaf := attackerObject
	if tc.targetIndex < len(tc.components)-1 {
		attackerLeaf = filepath.Join(append([]string{attackerObject}, tc.components[tc.targetIndex+1:]...)...)
		require.NoError(t, os.MkdirAll(filepath.Dir(attackerLeaf), 0700))
	}
	writeRootfsReplacementImage(t, attackerLeaf, attackerPrefix, expectedBytes)
	require.NoError(t, os.Symlink(filepath.Base(attackerObject), exchangePath))

	caller := rootfsCallerForRealFSTest(t, root)
	trustedInode := rootfsReplacementInode(t, trustedLeaf)
	attackerInode := rootfsReplacementInode(t, attackerLeaf)
	require.NotEqual(t, trustedInode, attackerInode)

	deps := rootfsRealFSTestDeps(t, root)
	realOpenAt2 := deps.openAt2
	trigger := make(chan struct{})
	swapped := make(chan error, 1)
	restore := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		select {
		case <-trigger:
		case <-stop:
			done <- nil
			return
		}
		err := unix.Renameat2(
			unix.AT_FDCWD,
			targetPath,
			unix.AT_FDCWD,
			exchangePath,
			unix.RENAME_EXCHANGE,
		)
		swapped <- err
		if err != nil {
			done <- nil
			return
		}
		<-restore
		done <- unix.Renameat2(
			unix.AT_FDCWD,
			targetPath,
			unix.AT_FDCWD,
			exchangePath,
			unix.RENAME_EXCHANGE,
		)
	}()

	openIndex := 0
	replacementTriggered := false
	var replacementErr error
	var replacementCloseErr error
	deps.openAt2 = func(
		ctx context.Context,
		parentFD int,
		component string,
		how *unix.OpenHow,
	) (int, error) {
		index := openIndex
		openIndex++
		if index != tc.targetIndex {
			return realOpenAt2(ctx, parentFD, component, how)
		}

		replacementTriggered = true
		if phase == rootfsReplaceBeforeOpen {
			trigger <- struct{}{}
			replacementErr = <-swapped
			if replacementErr != nil {
				return -1, replacementErr
			}
		}
		fd, err := realOpenAt2(ctx, parentFD, component, how)
		if err != nil || phase == rootfsReplaceBeforeOpen {
			return fd, err
		}
		trigger <- struct{}{}
		replacementErr = <-swapped
		if replacementErr != nil {
			replacementCloseErr = unix.Close(fd)
			return -1, replacementErr
		}
		return fd, nil
	}

	lease, admitErr := admitRootfsReplacementSelection(t.Context(), deps, caller, tc.selection, expectedBytes)
	if replacementTriggered {
		close(restore)
	} else {
		close(stop)
	}
	restoreErr := <-done
	require.True(t, replacementTriggered, "admission never opened the selected component")
	require.NoError(t, replacementErr)
	require.NoError(t, replacementCloseErr)
	require.NoError(t, restoreErr)
	require.Equal(t, trustedInode, rootfsReplacementInode(t, trustedLeaf))

	if admitErr != nil {
		assert.Nil(t, lease)
		return
	}
	require.NotNil(t, lease)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	pinnedPrefix := make([]byte, len(trustedPrefix))
	readBytes, err := unix.Pread(lease.rootfsFD, pinnedPrefix, 0)
	require.NoError(t, err)
	assert.Equal(t, len(trustedPrefix), readBytes)
	assert.Equal(t, trustedPrefix, pinnedPrefix)
	var pinnedStat unix.Stat_t
	require.NoError(t, unix.Fstat(lease.rootfsFD, &pinnedStat))
	assert.Equal(t, trustedInode, pinnedStat.Ino)
	assert.NotEqual(t, attackerInode, pinnedStat.Ino)
	assert.Equal(t, trustedInode, lease.metadata.inode)
}

func admitRootfsReplacementSelection(
	ctx context.Context,
	deps rootfsAuthorityDeps,
	caller Caller,
	selection rootfsSelectionKind,
	expectedBytes uint64,
) (*rootfsLease, error) {
	switch selection {
	case rootfsVMProvisionSelection:
		request := testVMProvisionRequest()
		request.CacheLocator = "/owner/cache"
		request.ExpectedRootfsBytes = expectedBytes
		return admitVMProvisionRootfs(ctx, deps, caller, request)
	case rootfsImageInspectSelection:
		request := testImageInspectRequest()
		request.CacheLocator = "/owner/cache"
		request.ExpectedImageBytes = expectedBytes
		return admitImageInspectRootfs(ctx, deps, caller, request)
	case rootfsImageOptimizeSelection:
		request := testImageOptimizeRequest()
		request.CacheLocator = "/owner/cache"
		request.ExpectedImageBytes = expectedBytes
		return admitImageOptimizeRootfs(ctx, deps, caller, request)
	default:
		return nil, rootfsAtomicError("replacement test selection is invalid", nil)
	}
}

func writeRootfsReplacementImage(t *testing.T, path string, prefix []byte, size uint64) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	written, writeErr := file.Write(prefix)
	require.NoError(t, writeErr)
	require.Equal(t, len(prefix), written)
	require.NoError(t, file.Truncate(int64(size)))
	require.NoError(t, file.Close())
}

func rootfsReplacementInode(t *testing.T, path string) uint64 {
	t.Helper()

	var stat unix.Stat_t
	require.NoError(t, unix.Stat(path, &stat))
	return stat.Ino
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
