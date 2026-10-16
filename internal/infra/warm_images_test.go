package infra

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsurePrivateWarmDirectoryRejectsUnsafeExistingLeaf(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	tests := map[string]func(t *testing.T, path string){
		"symlink": func(t *testing.T, path string) {
			require.NoError(t, os.Symlink(t.TempDir(), path))
		},
		"permissive_mode": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0777))
			require.NoError(t, os.Chmod(path, 0777))
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ready")
			setup(t, path)
			err := ensurePrivateWarmDirectory(context.Background(), path, uid, gid)
			assert.ErrorContains(t, err, "ownership or mode is invalid")
		})
	}
}

func TestEnsurePrivateWarmDirectoryCreatesAndReusesPrivateLeaf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready")
	uid, gid := os.Getuid(), os.Getgid()
	require.NoError(t, ensurePrivateWarmDirectory(context.Background(), path, uid, gid))
	require.NoError(t, ensurePrivateWarmDirectory(context.Background(), path, uid, gid))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	assert.Equal(t, uint32(uid), stat.Uid)
	assert.Equal(t, uint32(gid), stat.Gid)
}

func TestEnsurePrivateWarmDirectoryCancelledBeforeCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ensurePrivateWarmDirectory(ctx, path, os.Getuid(), os.Getgid())
	assert.ErrorIs(t, err, context.Canceled)
	_, err = os.Lstat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestAdmitWarmTempRootRejectsWorldWritableNonStickyDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0777))
	err := admitWarmTempRoot(root, os.Getuid())
	assert.ErrorContains(t, err, "not a trusted sticky directory")
}
