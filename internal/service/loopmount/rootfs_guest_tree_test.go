package loopmount

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRootfsGuestTreeAtomicallyReplacesAndReplaysWithoutFollowingSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "etc"), 0755))
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("outside\n"), 0600))
	require.NoError(t, os.Symlink(victim, filepath.Join(root, "etc", "hostname")))
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(rootFD)) })

	var sequence atomic.Uint32
	tracker := &rootfsProvisionTracker{}
	tree := rootfsGuestTree{rootFD: rootFD, tracker: tracker, deps: testSystemRootfsGuestTreeDeps(&sequence)}
	file := rootfsProvisionFile{
		path: "/etc/hostname", content: []byte("vm-a\n"), mode: 0640,
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()),
	}
	changed, err := tree.replaceFile(t.Context(), file)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tracker.state())
	got, err := os.ReadFile(filepath.Join(root, "etc", "hostname"))
	require.NoError(t, err)
	if diff := cmp.Diff(file.content, got); diff != "" {
		t.Errorf("published guest file mismatch (-want +got):\n%s", diff)
	}
	victimContent, err := os.ReadFile(victim)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("outside\n"), victimContent); diff != "" {
		t.Errorf("symlink victim content mismatch (-want +got):\n%s", diff)
	}

	replayTracker := &rootfsProvisionTracker{}
	tree.tracker = replayTracker
	changed, err = tree.replaceFile(t.Context(), file)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, vmProvisionEffectStateUnchanged, replayTracker.state())

	require.NoError(t, os.Symlink(root, filepath.Join(root, "etc", "escape")))
	_, err = tree.replaceFile(t.Context(), rootfsProvisionFile{
		path: "/etc/escape/file", content: []byte("blocked"), mode: 0600,
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "parent")
}

// Rationale: Once an ancestor close is attempted its numeric descriptor is consumed, while the newly opened child
// remains owned and must still be closed exactly once on the error path.
func TestRootfsGuestTreeEnsureDirectoryTransfersChildOwnershipBeforeAncestorClose(t *testing.T) {
	t.Parallel()

	opened := []int{10, 11}
	var closes []int
	tree := rootfsGuestTree{
		rootFD:  1,
		tracker: &rootfsProvisionTracker{},
		deps: rootfsGuestTreeDeps{
			openAt2: func(context.Context, int, string, *unix.OpenHow) (int, error) {
				fd := opened[0]
				opened = opened[1:]
				return fd, nil
			},
			close: func(_ context.Context, fd int) error {
				closes = append(closes, fd)
				if fd == 10 {
					return errors.New("ambiguous ancestor close")
				}
				return nil
			},
		},
	}

	_, err := tree.ensureDirectory(t.Context(), "/etc/mvmctl", 0700, 0, 0)
	require.Error(t, err)
	if diff := cmp.Diff([]int{10, 11}, closes); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Parent traversal has the same ownership transfer boundary as directory creation and must not leak the
// newly opened child or retry an ambiguous close against a descriptor number the kernel may already have reused.
func TestRootfsGuestTreeOpenParentTransfersChildOwnershipBeforeAncestorClose(t *testing.T) {
	t.Parallel()

	opened := []int{10, 11}
	var closes []int
	tree := rootfsGuestTree{
		rootFD: 1,
		deps: rootfsGuestTreeDeps{
			openAt2: func(context.Context, int, string, *unix.OpenHow) (int, error) {
				fd := opened[0]
				opened = opened[1:]
				return fd, nil
			},
			close: func(_ context.Context, fd int) error {
				closes = append(closes, fd)
				if fd == 10 {
					return errors.New("ambiguous ancestor close")
				}
				return nil
			},
		},
	}

	_, _, err := tree.openParent(t.Context(), "/etc/systemd/system/mvm-agent.service")
	require.Error(t, err)
	if diff := cmp.Diff([]int{10, 11}, closes); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
}

// testSystemRootfsGuestTreeDeps returns real descriptor-relative guest-tree effects with deterministic temp names.
func testSystemRootfsGuestTreeDeps(sequence *atomic.Uint32) rootfsGuestTreeDeps {
	return rootfsGuestTreeDeps{
		openAt2: func(_ context.Context, fd int, path string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(fd, path, how)
		},
		mkdirAt: func(_ context.Context, fd int, path string, mode uint32) error {
			return unix.Mkdirat(fd, path, mode)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		fstatAt: func(_ context.Context, fd int, path string, stat *unix.Stat_t, flags int) error {
			return unix.Fstatat(fd, path, stat, flags)
		},
		pread: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			count, err := unix.Pread(fd, data, offset)
			if count == 0 && err == nil {
				return 0, io.EOF
			}
			return count, err
		},
		pwrite: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			return unix.Pwrite(fd, data, offset)
		},
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		fsync:  func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		renameAt: func(_ context.Context, oldFD int, oldName string, newFD int, newName string) error {
			return unix.Renameat(oldFD, oldName, newFD, newName)
		},
		symlinkAt: func(_ context.Context, target string, fd int, name string) error {
			return unix.Symlinkat(target, fd, name)
		},
		readlinkAt: func(_ context.Context, fd int, path string, buffer []byte) (int, error) {
			return unix.Readlinkat(fd, path, buffer)
		},
		unlinkAt: func(_ context.Context, fd int, name string, flags int) error {
			return unix.Unlinkat(fd, name, flags)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		tempName: func(context.Context) (string, error) {
			suffix := rootfsTestDecimal(int(sequence.Add(1)))
			return ".mvmctl-provision-" + strings.Repeat("0", 32-len(suffix)) + suffix, nil
		},
	}
}
