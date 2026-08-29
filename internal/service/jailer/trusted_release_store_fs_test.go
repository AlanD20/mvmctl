package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: The trusted release path is an authorization boundary. Opening it must pin the exact root-owned directory
// objects once so later release operations cannot be redirected by replacing their pathnames.
func TestOpenTrustedReleaseStorePinsCanonicalSlotDirectories(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)

	var before unix.Stat_t
	require.NoError(t, unix.Fstat(directory.slotFD, &before))
	fdFlags, err := unix.FcntlInt(uintptr(directory.slotFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, fdFlags&unix.FD_CLOEXEC)
	movedPath := fixture.slotPath + ".moved"
	require.NoError(t, os.Rename(fixture.slotPath, movedPath))
	require.NoError(t, os.Mkdir(fixture.slotPath, 0700))
	var after unix.Stat_t
	require.NoError(t, unix.Fstat(directory.slotFD, &after))
	assert.Equal(t, before.Dev, after.Dev)
	assert.Equal(t, before.Ino, after.Ino)
	assert.NotEqual(t, fileInode(t, fixture.slotPath), after.Ino)

	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
}

func TestOpenTrustedReleaseStoreRejectsUnexpectedOwnerIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseStorePolicy){
		"wrong owner UID":   func(policy *trustedReleaseStorePolicy) { policy.expectedUID++ },
		"wrong managed GID": func(policy *trustedReleaseStorePolicy) { policy.expectedGID++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			mutate(&fixture.policy)
			_, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

// Rationale: O_NOFOLLOW must apply at every level, including the injected test root, or root could traverse an
// attacker-controlled link before reaching otherwise safe-looking managed directories.
func TestOpenTrustedReleaseStoreRejectsSymlinkAtEveryLevel(t *testing.T) {
	t.Parallel()

	levels := []string{"root", "var", "lib", "mvmctl", "binaries", "architecture", "version"}
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			fixture.replaceLevelWithSymlink(t, level)
			store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
			if level == "architecture" || level == "version" {
				require.NoError(t, err)
				_, err = store.openInstalledSlot(t.Context(), fixture.slot)
				require.NoError(t, store.Release(t.Context()))
			}
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenTrustedReleaseStoreRejectsUnsafeDirectoryMetadata(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		level string
		mode  os.FileMode
	}{
		"writable ancestor":       {level: "var", mode: 0775},
		"permissive managed root": {level: "mvmctl", mode: 0755},
		"wrong binaries mode":     {level: "binaries", mode: 0750},
		"wrong architecture mode": {level: "architecture", mode: 0711},
		"wrong version mode":      {level: "version", mode: 0755},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			require.NoError(t, os.Chmod(fixture.pathForLevel(tc.level), tc.mode))
			store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
			if tc.level == "architecture" || tc.level == "version" {
				require.NoError(t, err)
				_, err = store.openInstalledSlot(t.Context(), fixture.slot)
				require.NoError(t, store.Release(t.Context()))
			}
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenTrustedReleaseStoreRejectsNonDirectoryComponent(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.Remove(fixture.slotPath))
	require.NoError(t, os.WriteFile(fixture.slotPath, []byte("not a directory"), 0600))

	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	_, err = store.openInstalledSlot(t.Context(), fixture.slot)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	require.NoError(t, store.Release(t.Context()))
}

func TestOpenTrustedReleaseStoreReportsMissingRelease(t *testing.T) {
	t.Parallel()

	tests := []string{"architecture", "version"}
	for _, level := range tests {
		t.Run(level, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseStoreFixture(t)
			require.NoError(t, os.Remove(fixture.slotPath))
			if level == "architecture" {
				require.NoError(t, os.Remove(fixture.architecturePath))
			}
			store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
			require.NoError(t, err)
			_, err = store.openInstalledSlot(t.Context(), fixture.slot)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryNotFound, errs.AsDomainError(err).Code)
			require.NoError(t, store.Release(t.Context()))
		})
	}
}

func TestOpenTrustedReleaseStoreRejectsInvalidSlotBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	_, err = store.openInstalledSlot(t.Context(), releaseSlot{version: "../1.16.1", architecture: "x86_64"})
	require.Error(t, err)
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	require.NoError(t, store.Release(t.Context()))
}

func TestTrustedReleaseStoreReleaseClosesEachDescriptorOnce(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	var mutex sync.Mutex
	closed := make(map[int]int)
	realClose := fixture.deps.close
	fixture.deps.close = func(ctx context.Context, fd int) error {
		mutex.Lock()
		closed[fd]++
		mutex.Unlock()
		return realClose(ctx, fd)
	}

	store, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.NoError(t, err)
	directory, err := store.openInstalledSlot(t.Context(), fixture.slot)
	require.NoError(t, err)
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, directory.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))
	require.NoError(t, store.Release(t.Context()))

	mutex.Lock()
	defer mutex.Unlock()
	assert.Len(t, closed, 7)
	for fd, count := range closed {
		assert.Equal(t, 1, count, "descriptor %d close count", fd)
	}
}

func TestOpenTrustedReleaseStoreHonorsCancellation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store, err := openTrustedReleaseStoreForRead(ctx, fixture.deps, fixture.policy)
	assert.Nil(t, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
}

func TestOpenTrustedReleaseStorePreservesPrimaryErrorWhenCleanupFails(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.Chmod(fixture.varPath, 0775))
	realClose := fixture.deps.close
	fixture.deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), unix.EIO)
	}

	_, err := openTrustedReleaseStoreForRead(t.Context(), fixture.deps, fixture.policy)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.Contains(t, domainErr.Message, "close rejected trusted release store")
	assert.ErrorIs(t, err, unix.EIO)
}

type trustedReleaseStoreFixture struct {
	root             string
	varPath          string
	libPath          string
	mvmctlPath       string
	binariesPath     string
	architecturePath string
	slotPath         string
	slot             releaseSlot
	deps             trustedReleaseStoreDeps
	policy           trustedReleaseStorePolicy
}

func newTrustedReleaseStoreFixture(t *testing.T) *trustedReleaseStoreFixture {
	t.Helper()

	container := t.TempDir()
	root := filepath.Join(container, "root")
	varPath := filepath.Join(root, "var")
	libPath := filepath.Join(varPath, "lib")
	mvmctlPath := filepath.Join(libPath, "mvmctl")
	binariesPath := filepath.Join(mvmctlPath, "binaries")
	architecturePath := filepath.Join(binariesPath, "x86_64")
	slotPath := filepath.Join(architecturePath, "1.16.1")
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.Mkdir(varPath, 0755))
	require.NoError(t, os.Mkdir(libPath, 0755))
	require.NoError(t, os.Mkdir(mvmctlPath, 0700))
	require.NoError(t, os.Mkdir(binariesPath, 0700))
	require.NoError(t, os.Mkdir(architecturePath, 0700))
	require.NoError(t, os.Mkdir(slotPath, 0700))

	return &trustedReleaseStoreFixture{
		root:             root,
		varPath:          varPath,
		libPath:          libPath,
		mvmctlPath:       mvmctlPath,
		binariesPath:     binariesPath,
		architecturePath: architecturePath,
		slotPath:         slotPath,
		slot:             releaseSlot{version: "1.16.1", architecture: "x86_64"},
		deps:             realTrustedReleaseStoreDeps(),
		policy: trustedReleaseStorePolicy{
			rootPath:    root,
			expectedUID: uint32(os.Getuid()),
			expectedGID: uint32(os.Getgid()),
		},
	}
}

func (fixture *trustedReleaseStoreFixture) pathForLevel(level string) string {
	switch level {
	case "root":
		return fixture.root
	case "var":
		return fixture.varPath
	case "lib":
		return fixture.libPath
	case "mvmctl":
		return fixture.mvmctlPath
	case "binaries":
		return fixture.binariesPath
	case "architecture":
		return fixture.architecturePath
	case "version":
		return fixture.slotPath
	default:
		panic("unknown trusted release store fixture level: " + level)
	}
}

func (fixture *trustedReleaseStoreFixture) replaceLevelWithSymlink(t *testing.T, level string) {
	t.Helper()

	target := fixture.pathForLevel(level)
	moved := target + ".real"
	require.NoError(t, os.Rename(target, moved))
	require.NoError(t, os.Symlink(moved, target))
}

func fileInode(t *testing.T, path string) uint64 {
	t.Helper()

	var stat unix.Stat_t
	require.NoError(t, unix.Stat(path, &stat))
	return stat.Ino
}
