package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestOpenInstanceAuthorityRootsCreatesFixedManagedTree(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots, err := openInstanceAuthorityRoots(
		context.Background(),
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, roots.Release(context.Background())) })

	assertExactMode(t, filepath.Join(root, "var/lib/mvmctl/instances"), 0700)
	assertExactMode(t, filepath.Join(root, "run/mvmctl"), 0700)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/releases"), 0700)

	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	assertExactMode(t, filepath.Join(root, "var/lib/mvmctl/instances/1000"), 0700)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/1000"), 0700)
}

func TestOpenInstanceAuthorityRootsRejectsUnsafeTree(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"missing_var": func(t *testing.T, root string) {
			require.NoError(t, os.RemoveAll(filepath.Join(root, "var")))
		},
		"writable_var": func(t *testing.T, root string) {
			require.NoError(t, os.Chmod(filepath.Join(root, "var"), 0777))
		},
		"symlink_mvmctl": func(t *testing.T, root string) {
			target := filepath.Join(root, "attacker")
			require.NoError(t, os.Mkdir(target, 0700))
			require.NoError(t, os.Symlink(target, filepath.Join(root, "var/lib/mvmctl")))
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			mutate(t, root)
			roots, err := openInstanceAuthorityRoots(
				context.Background(),
				realInstanceAuthorityDeps(),
				testInstanceAuthorityPolicy(root),
			)
			if roots != nil {
				require.NoError(t, roots.Release(context.Background()))
			}
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenInstanceAuthorityRootsRejectsSymlinkAtEveryFixedLevel(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"var": func(t *testing.T, root string) {
			require.NoError(t, os.RemoveAll(filepath.Join(root, "var")))
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "var")))
		},
		"lib": func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "var/lib")))
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "var/lib")))
		},
		"state_base": func(t *testing.T, root string) {
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "var/lib/mvmctl")))
		},
		"instances": func(t *testing.T, root string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "var/lib/mvmctl"), 0700))
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "var/lib/mvmctl/instances")))
		},
		"run": func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "run")))
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "run")))
		},
		"runtime_base": func(t *testing.T, root string) {
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "run/mvmctl")))
		},
		"release_locks": func(t *testing.T, root string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "run/mvmctl"), 0700))
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "run/mvmctl/releases")))
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			mutate(t, root)
			roots, err := openInstanceAuthorityRoots(
				context.Background(),
				realInstanceAuthorityDeps(),
				testInstanceAuthorityPolicy(root),
			)
			if roots != nil {
				require.NoError(t, roots.Release(context.Background()))
			}
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenInstanceAuthorityRootsRejectsWritableModeAtEveryFixedLevel(t *testing.T) {
	t.Parallel()

	relativePaths := []string{
		".",
		"var",
		"var/lib",
		"var/lib/mvmctl",
		"var/lib/mvmctl/instances",
		"run",
		"run/mvmctl",
		"run/mvmctl/releases",
	}
	for _, relativePath := range relativePaths {
		relativePath := relativePath
		t.Run(relativePath, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib/mvmctl/instances"), 0700))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "run/mvmctl/releases"), 0700))
			require.NoError(t, os.Chmod(filepath.Join(root, relativePath), 0777))

			roots, err := openInstanceAuthorityRoots(
				context.Background(),
				realInstanceAuthorityDeps(),
				testInstanceAuthorityPolicy(root),
			)
			if roots != nil {
				require.NoError(t, roots.Release(context.Background()))
			}
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestOpenInstanceAuthorityRootsTreatsMkdirEEXISTAsConcurrentCreation(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	realMkdirAt := deps.mkdirAt
	var chownCalls atomic.Int32
	var chmodCalls atomic.Int32
	deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		if err := realMkdirAt(ctx, parentFD, name, mode); err != nil {
			return err
		}
		return unix.EEXIST
	}
	deps.fchown = func(context.Context, int, int, int) error {
		chownCalls.Add(1)
		return errors.New("must not chown concurrently created directory")
	}
	deps.fchmod = func(context.Context, int, uint32) error {
		chmodCalls.Add(1)
		return errors.New("must not chmod concurrently created directory")
	}

	roots, err := openInstanceAuthorityRoots(context.Background(), deps, testInstanceAuthorityPolicy(root))
	require.NoError(t, err)
	require.NoError(t, roots.Release(context.Background()))
	assert.Zero(t, chownCalls.Load())
	assert.Zero(t, chmodCalls.Load())
}

func TestOpenInstanceAuthorityRootsRejectsManagedModeAndType(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"managed_mode": func(t *testing.T, root string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "var/lib/mvmctl"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "var/lib/mvmctl/instances"), 0755))
		},
		"managed_type": func(t *testing.T, root string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "var/lib/mvmctl"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "var/lib/mvmctl/instances"), nil, 0600))
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			mutate(t, root)
			roots, err := openInstanceAuthorityRoots(
				context.Background(),
				realInstanceAuthorityDeps(),
				testInstanceAuthorityPolicy(root),
			)
			if roots != nil {
				require.NoError(t, roots.Release(context.Background()))
			}
			require.Error(t, err)
		})
	}
}

func TestOpenInstanceAuthorityRootsRejectsSimulatedAncestorOwnerMismatch(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	realFstat := deps.fstat
	var directoriesInspected atomic.Int32
	deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR && directoriesInspected.Add(1) == 2 {
			stat.Uid++
		}
		return nil
	}

	roots, err := openInstanceAuthorityRoots(context.Background(), deps, testInstanceAuthorityPolicy(root))
	assert.Nil(t, roots)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityVMLockIsCancellableAndPersistent(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	first, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	require.NoError(t, err)

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second, err := uidDirs.acquireVMLock(waitCtx, testVMID)
	assert.Nil(t, second)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, errs.ClassRetryable, errs.AsDomainError(err).Class)

	require.NoError(t, first.Release(context.Background()))
	assertExactMode(t, filepath.Join(root, "run/mvmctl/1000/"+testVMID+".lock"), 0600)

	reacquired, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(context.Background()))
}

func TestInstanceAuthorityLockRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	lockPath := filepath.Join(root, "run/mvmctl/1000/"+testVMID+".lock")
	require.NoError(t, os.Symlink("attacker", lockPath))

	lock, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	assert.Nil(t, lock)
	require.Error(t, err)
	assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityLockRejectsUnsafeModeAndType(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"mode": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, nil, 0644))
		},
		"type": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0700))
		},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			roots := openTestInstanceAuthorityRoots(t, root)
			uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
			path := filepath.Join(root, "run/mvmctl/1000/"+testVMID+".lock")
			create(t, path)

			lock, err := uidDirs.acquireVMLock(context.Background(), testVMID)
			assert.Nil(t, lock)
			require.Error(t, err)
		})
	}
}

func TestInstanceRecordAndLockRejectSimulatedOwnerMismatch(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	require.NoError(t, uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord()))
	lock, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	require.NoError(t, err)
	require.NoError(t, lock.Release(context.Background()))

	realFstat := uidDirs.deps.fstat
	uidDirs.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFREG {
			stat.Uid++
		}
		return nil
	}
	_, _, err = uidDirs.readRecord(context.Background(), testVMID)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	lock, err = uidDirs.acquireVMLock(context.Background(), testVMID)
	assert.Nil(t, lock)
	require.Error(t, err)
}

func TestInstanceRecordAtomicWriteAndRead(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	record := testRegisteredInstanceRecord()
	require.NoError(t, uidDirs.writeRecord(context.Background(), record))

	got, found, err := uidDirs.readRecord(context.Background(), testVMID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, record, got)
	assertExactMode(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"), 0600)
}

func TestInstanceRecordWriteHandlesPartialWrites(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	realWrite := uidDirs.deps.write
	uidDirs.deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
		if len(value) > 3 {
			value = value[:3]
		}
		return realWrite(ctx, fd, value)
	}
	require.NoError(t, uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord()))

	_, found, err := uidDirs.readRecord(context.Background(), testVMID)
	require.NoError(t, err)
	assert.True(t, found)
}

func TestInstanceRecordWriteVerifiesActualTemporarySize(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	uidDirs.deps.write = func(_ context.Context, _ int, value []byte) (int, error) { return len(value), nil }

	err = uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord())
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	_, statErr := os.Lstat(filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	assertNoInstanceTemps(t, filepath.Join(root, "var/lib/mvmctl/instances/1000"))
}

func TestInstanceRecordPreRenameFailurePreservesOldRecordAndCleansTemp(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	want := testRegisteredInstanceRecord()
	require.NoError(t, uidDirs.writeRecord(context.Background(), want))
	uidDirs.deps.renameAt = func(context.Context, int, string, int, string) error {
		return errors.New("rename failed")
	}
	updated := want
	updated.lifecycle = instanceLifecycleCleaning
	updated.cleanupGeneration = 1

	err = uidDirs.writeRecord(context.Background(), updated)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)

	got, found, readErr := uidDirs.readRecord(context.Background(), testVMID)
	require.NoError(t, readErr)
	assert.True(t, found)
	assert.Equal(t, want, got)
	assertNoInstanceTemps(t, filepath.Join(root, "var/lib/mvmctl/instances/1000"))
}

func TestInstanceRecordEveryPreRenameFailurePreservesOldRecord(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*instanceUIDDirectories) func(){
		"write": func(directories *instanceUIDDirectories) func() {
			directories.deps.write = func(context.Context, int, []byte) (int, error) {
				return 0, errors.New("write failed")
			}
			return func() {}
		},
		"fchown": func(directories *instanceUIDDirectories) func() {
			directories.deps.fchown = func(context.Context, int, int, int) error {
				return errors.New("chown failed")
			}
			return func() {}
		},
		"fchmod": func(directories *instanceUIDDirectories) func() {
			directories.deps.fchmod = func(context.Context, int, uint32) error {
				return errors.New("chmod failed")
			}
			return func() {}
		},
		"file_fsync": func(directories *instanceUIDDirectories) func() {
			directories.deps.fsync = func(context.Context, int) error {
				return errors.New("file sync failed")
			}
			return func() {}
		},
		"close": func(directories *instanceUIDDirectories) func() {
			realClose := directories.deps.close
			var calls atomic.Int32
			directories.deps.close = func(ctx context.Context, fd int) error {
				err := realClose(ctx, fd)
				if calls.Add(1) == 1 && err == nil {
					return errors.New("close failed")
				}
				return err
			}
			return func() { directories.deps.close = realClose }
		},
		"rename": func(directories *instanceUIDDirectories) func() {
			directories.deps.renameAt = func(context.Context, int, string, int, string) error {
				return errors.New("rename failed")
			}
			return func() {}
		},
	}

	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			roots := openTestInstanceAuthorityRoots(t, root)
			uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
			old := testRegisteredInstanceRecord()
			require.NoError(t, uidDirs.writeRecord(context.Background(), old))
			recordPath := filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json")
			oldRaw, err := os.ReadFile(recordPath)
			require.NoError(t, err)
			restore := inject(uidDirs)
			updated := old
			updated.lifecycle = instanceLifecycleCleaning
			updated.cleanupGeneration = 1

			err = uidDirs.writeRecord(context.Background(), updated)
			restore()
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			gotRaw, readErr := os.ReadFile(recordPath)
			require.NoError(t, readErr)
			assert.Equal(t, oldRaw, gotRaw)
			assertNoInstanceTemps(t, filepath.Dir(recordPath))
		})
	}
}

func TestInstanceRecordCleanupFailureIsReturned(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	uidDirs.deps.write = func(context.Context, int, []byte) (int, error) {
		return 0, errors.New("write failed")
	}
	realUnlink := uidDirs.deps.unlinkAt
	uidDirs.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		err := realUnlink(ctx, parentFD, name, flags)
		if err != nil {
			return err
		}
		return errors.New("unlink completion failed")
	}

	err = uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord())
	require.Error(t, err)
	assert.ErrorContains(t, err, "unlink completion failed")
	assertNoInstanceTemps(t, filepath.Join(root, "var/lib/mvmctl/instances/1000"))
}

func TestInstanceRecordWriteRejectsExistingUnsafeTarget(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, path string) {
			require.NoError(t, os.Symlink("attacker", path))
		},
		"directory": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0700))
		},
		"wrong_mode": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
		},
	}

	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			roots := openTestInstanceAuthorityRoots(t, root)
			uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
			recordPath := filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json")
			create(t, recordPath)

			err = uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord())
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			assertNoInstanceTemps(t, filepath.Dir(recordPath))
		})
	}
}

func TestInstanceRecordReadHandlesPartialReadsAndRejectsOversize(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	require.NoError(t, uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord()))

	realRead := uidDirs.deps.read
	uidDirs.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
		if len(value) > 3 {
			value = value[:3]
		}
		return realRead(ctx, fd, value)
	}
	_, found, err := uidDirs.readRecord(context.Background(), testVMID)
	require.NoError(t, err)
	assert.True(t, found)
	uidDirs.deps.read = realRead

	recordPath := filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json")
	require.NoError(t, os.WriteFile(recordPath, []byte(strings.Repeat("x", maxInstanceRecordBytes+1)), 0600))
	_, _, err = uidDirs.readRecord(context.Background(), testVMID)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceRecordPostRenameFailureReportsPartialSuccess(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	realFsync := uidDirs.deps.fsync
	var fsyncCalls atomic.Int32
	uidDirs.deps.fsync = func(ctx context.Context, fd int) error {
		if fsyncCalls.Add(1) == 2 {
			return errors.New("directory sync failed")
		}
		return realFsync(ctx, fd)
	}

	err = uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord())
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])

	_, found, readErr := uidDirs.readRecord(context.Background(), testVMID)
	require.NoError(t, readErr)
	assert.True(t, found)
}

func TestInstanceRecordCancellationBeforeRenamePreservesOldRecord(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	want := testRegisteredInstanceRecord()
	require.NoError(t, uidDirs.writeRecord(context.Background(), want))
	ctx, cancel := context.WithCancel(context.Background())
	realFsync := uidDirs.deps.fsync
	uidDirs.deps.fsync = func(ctx context.Context, fd int) error {
		err := realFsync(ctx, fd)
		cancel()
		return err
	}
	updated := want
	updated.lifecycle = instanceLifecycleCleaning
	updated.cleanupGeneration = 1

	err = uidDirs.writeRecord(ctx, updated)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	got, found, readErr := uidDirs.readRecord(context.Background(), testVMID)
	require.NoError(t, readErr)
	assert.True(t, found)
	assert.Equal(t, want, got)
	assertNoInstanceTemps(t, filepath.Join(root, "var/lib/mvmctl/instances/1000"))
}

func TestInstanceRecordReadRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })

	recordPath := filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json")
	require.NoError(t, os.Symlink("attacker", recordPath))

	_, _, err = uidDirs.readRecord(context.Background(), testVMID)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceDirectoryScansUseIndependentOffsets(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	require.NoError(t, uidDirs.Release(context.Background()))

	first, err := roots.deps.readDirNames(context.Background(), roots.stateFD)
	require.NoError(t, err)
	second, err := roots.deps.readDirNames(context.Background(), roots.stateFD)
	require.NoError(t, err)
	assert.Equal(t, []string{"1000"}, first)
	assert.Equal(t, first, second)
}

func TestInstanceRecordReadCloseFailureReturnsDomainError(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	require.NoError(t, uidDirs.writeRecord(context.Background(), testRegisteredInstanceRecord()))

	realClose := uidDirs.deps.close
	uidDirs.deps.close = func(ctx context.Context, fd int) error {
		closeErr := realClose(ctx, fd)
		if closeErr != nil {
			return closeErr
		}
		return errors.New("record close failed")
	}
	_, found, err := uidDirs.readRecord(context.Background(), testVMID)
	uidDirs.deps.close = realClose
	require.Error(t, err)
	assert.True(t, found)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestJoinInstanceAtomicErrorPreservesPartialSuccessDetails(t *testing.T) {
	t.Parallel()

	primary := annotateInstanceRecordReplacement(
		instanceAtomicError("sync record", errors.New("sync failed")),
		true,
	)
	joined := joinInstanceAtomicError(primary, "close descriptor", errors.New("close failed"))

	assert.Equal(t, true, joined.Details["record_replaced"])
	assert.Equal(t, true, joined.Details["durability_uncertain"])
}

func prepareInstanceAuthorityTestRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "run"), 0755))
	return root
}

func testInstanceAuthorityPolicy(root string) instanceAuthorityPolicy {
	return instanceAuthorityPolicy{
		rootPath:    root,
		expectedUID: uint32(os.Geteuid()),
		expectedGID: uint32(os.Getegid()),
	}
}

func openTestInstanceAuthorityRoots(t *testing.T, root string) *instanceAuthorityRoots {
	t.Helper()

	roots, err := openInstanceAuthorityRoots(
		context.Background(),
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, roots.Release(context.Background())) })
	return roots
}

func assertExactMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
}

func assertNoInstanceTemps(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".mvm-instance-"), entry.Name())
	}
}

func TestInstanceAuthorityReleaseReportsUnlockFailure(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	roots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := roots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uidDirs.Release(context.Background())) })
	lock, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	require.NoError(t, err)

	realFlock := lock.deps.flock
	lock.deps.flock = func(ctx context.Context, fd int, how int) error {
		if how == unix.LOCK_UN {
			return errors.New("unlock failed")
		}
		return realFlock(ctx, fd, how)
	}
	err = lock.Release(context.Background())
	require.Error(t, err)
	assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityReleasePropagatesCallerContextToCleanup(t *testing.T) {
	t.Parallel()

	type cleanupContextKey struct{}
	ctx := context.WithValue(context.Background(), cleanupContextKey{}, "release-context")
	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	realClose := deps.close
	contextObserved := false
	deps.close = func(closeCtx context.Context, fd int) error {
		if closeCtx.Value(cleanupContextKey{}) == "release-context" {
			contextObserved = true
		}
		return realClose(closeCtx, fd)
	}
	roots, err := openInstanceAuthorityRoots(context.Background(), deps, testInstanceAuthorityPolicy(root))
	require.NoError(t, err)

	require.NoError(t, roots.Release(ctx))
	assert.True(t, contextObserved)
}
