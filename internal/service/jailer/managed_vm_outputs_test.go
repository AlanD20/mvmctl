package jailer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type persistentOutputFixture struct {
	managed   managedCacheFixture
	cache     *managedCacheLease
	launch    *launchLease
	deps      managedPersistentOutputDeps
	directory string
}

// newPersistentOutputResources pins a real private cache independently of the launch record and output transaction.
func newPersistentOutputResources(t *testing.T) *persistentOutputFixture {
	t.Helper()
	managed := prepareManagedCacheFixture(t)
	managed.caller.gid = uint32(os.Getegid())
	if managed.caller.gid == 0 {
		managed.caller.gid = testAuthorityGID
	}
	cache, err := pinManagedCache(t.Context(), managed.deps, managed.policy, managed.caller, managed.locator)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Release(context.Background())) })
	directory := filepath.Join(managed.cacheDir, "vms", testVMID)
	require.NoError(t, os.MkdirAll(directory, 0700))
	if os.Geteuid() == 0 {
		for _, path := range []string{filepath.Dir(directory), directory} {
			require.NoError(t, os.Chown(path, int(managed.caller.uid), int(managed.caller.gid)))
		}
	}
	deps := realManagedPersistentOutputDeps()
	if os.Geteuid() != 0 {
		// L1 cannot acquire CAP_DAC_READ_SEARCH for AT_EMPTY_PATH. The test-only publication transport follows our
		// own anonymous FD through proc; production uses only root's AT_EMPTY_PATH and has no pathname fallback.
		deps.publish = func(_ context.Context, fd, parentFD int, name managedPersistentOutputName) error {
			return unix.Linkat(
				unix.AT_FDCWD,
				fmt.Sprintf("/proc/self/fd/%d", fd),
				parentFD,
				name.basename(),
				unix.AT_SYMLINK_FOLLOW,
			)
		}
	}
	return &persistentOutputFixture{managed: managed, cache: cache, deps: deps, directory: directory}
}

func (fixture *persistentOutputFixture) register(t *testing.T, authority *instanceAuthority) error {
	t.Helper()
	intent := testLaunchIntent(false)
	intent.cacheIdentity = fixture.cache.identity
	launch, err := registerTestLaunch(t, t.Context(), authority, fixture.managed.caller, intent)
	fixture.launch = launch
	return err
}

func newPersistentOutputFixture(t *testing.T) *persistentOutputFixture {
	t.Helper()
	fixture := newPersistentOutputResources(t)
	authority, _ := newTestInstanceAuthority(t)
	require.NoError(t, fixture.register(t, authority))
	t.Cleanup(func() { cleanupPersistentOutputLaunch(t, fixture.launch) })
	return fixture
}

func cleanupPersistentOutputLaunch(t *testing.T, launch *launchLease) {
	t.Helper()
	if launch == nil || launch.state.released {
		return
	}
	cleanup, err := launch.Abort(context.Background())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(context.Background()))
	require.NoError(t, cleanup.Release(context.Background()))
}

func (fixture *persistentOutputFixture) prepare(ctx context.Context, metrics managedPersistentMetricsPresence) error {
	return preparePersistentOutputsWithDeps(ctx, fixture.launch, fixture.cache, metrics, fixture.deps)
}

func (fixture *persistentOutputFixture) write(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(fixture.directory, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	if os.Geteuid() == 0 {
		require.NoError(t, os.Chown(path, int(fixture.managed.caller.uid), int(fixture.managed.caller.gid)))
	}
}

func readPersistentOutput(t *testing.T, directory, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, name))
	require.NoError(t, err)
	return string(content)
}

func assertPersistentOutputFiles(t *testing.T, fixture *persistentOutputFixture, names []string) {
	t.Helper()
	entries, err := os.ReadDir(fixture.directory)
	require.NoError(t, err)
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	if diff := cmp.Diff(names, actual); diff != "" {
		t.Errorf("persistent output names (-want +got):\n%s", diff)
	}
	for _, name := range names {
		assert.Empty(t, readPersistentOutput(t, fixture.directory, name))
		var stat unix.Stat_t
		require.NoError(t, unix.Lstat(filepath.Join(fixture.directory, name), &stat))
		want := struct {
			Mode     uint32
			UID, GID uint32
			Links    uint64
		}{
			Mode: unix.S_IFREG | 0600, UID: fixture.managed.caller.uid, GID: fixture.managed.caller.gid, Links: 1,
		}
		got := struct {
			Mode     uint32
			UID, GID uint32
			Links    uint64
		}{
			Mode: stat.Mode, UID: stat.Uid, GID: stat.Gid, Links: stat.Nlink,
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("persistent output metadata (-want +got):\n%s", diff)
		}
	}
}

func TestPersistentOutputsRejectSelectionAndAuthorityBeforeEffects(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*persistentOutputFixture) managedPersistentMetricsPresence{
		"invalid metrics": func(*persistentOutputFixture) managedPersistentMetricsPresence { return 0 },
		"unconfirmed registration": func(f *persistentOutputFixture) managedPersistentMetricsPresence {
			f.launch.state.registrationComplete = false
			return managedPersistentMetricsEnabled
		},
		"cache identity mismatch": func(f *persistentOutputFixture) managedPersistentMetricsPresence {
			f.cache.identity.inode++
			return managedPersistentMetricsEnabled
		},
		"cache owner mismatch": func(f *persistentOutputFixture) managedPersistentMetricsPresence {
			f.cache.ownerUID++
			return managedPersistentMetricsEnabled
		},
		"incomplete effects": func(f *persistentOutputFixture) managedPersistentMetricsPresence {
			f.deps.publish = nil
			return managedPersistentMetricsEnabled
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputFixture(t)
			fixture.write(t, "firecracker.log", "prior launch evidence")
			metrics := mutate(fixture)
			err := fixture.prepare(t.Context(), metrics)
			require.Error(t, err)
			assert.Equal(t, "prior launch evidence", readPersistentOutput(t, fixture.directory, "firecracker.log"))
			assert.Nil(t, fixture.launch.state.persistentOutputs)
			assert.NotEmpty(t, fixture.cache.retained)
		})
	}
}

func TestPersistentOutputsPrepareExactlyFixedFiles(t *testing.T) {
	t.Parallel()
	for _, metrics := range []managedPersistentMetricsPresence{managedPersistentMetricsDisabled, managedPersistentMetricsEnabled} {
		t.Run(fmt.Sprint(metrics), func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputFixture(t)
			fixture.write(t, "firecracker.log", "previous launch")
			require.NoError(t, fixture.prepare(t.Context(), metrics))
			want := []string{"firecracker.console.log", "firecracker.log"}
			if metrics == managedPersistentMetricsEnabled {
				want = append(want, "firecracker.metrics")
			}
			assertPersistentOutputFiles(t, fixture, want)
			assert.Empty(t, fixture.cache.retained)
			require.NotNil(t, fixture.launch.state.persistentOutputs)
			assert.NoError(t, fixture.launch.state.persistentOutputs.requireReady())
		})
	}
}

// Rationale: admission must reject every unsafe existing object before truncating any otherwise valid output.
func TestPersistentOutputsRejectUnsafeObjectsWithoutTruncation(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*testing.T, *persistentOutputFixture){
		"symlink": func(t *testing.T, f *persistentOutputFixture) {
			require.NoError(t, os.Symlink("firecracker.log", filepath.Join(f.directory, "firecracker.console.log")))
		},
		"hard link": func(t *testing.T, f *persistentOutputFixture) {
			require.NoError(
				t,
				os.Link(
					filepath.Join(f.directory, "firecracker.log"),
					filepath.Join(f.directory, "firecracker.console.log"),
				),
			)
		},
		"directory leaf": func(t *testing.T, f *persistentOutputFixture) {
			require.NoError(t, os.Mkdir(filepath.Join(f.directory, "firecracker.console.log"), 0700))
		},
		"fifo": func(t *testing.T, f *persistentOutputFixture) {
			require.NoError(t, unix.Mkfifo(filepath.Join(f.directory, "firecracker.console.log"), 0600))
		},
		"world readable file": func(t *testing.T, f *persistentOutputFixture) {
			f.write(t, "firecracker.console.log", "console evidence")
			require.NoError(t, os.Chmod(filepath.Join(f.directory, "firecracker.console.log"), 0644))
		},
		"unsafe directory": func(t *testing.T, f *persistentOutputFixture) {
			require.NoError(t, os.Chmod(f.directory, 0777))
		},
		"foreign owner": func(t *testing.T, f *persistentOutputFixture) {
			f.write(t, "firecracker.console.log", "console evidence")
			inode := inodeForManagedCacheTestPath(t, filepath.Join(f.directory, "firecracker.console.log"))
			realStat := f.deps.statx
			f.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				err := realStat(ctx, fd, flags, mask, stat)
				if stat.Ino == inode {
					stat.Uid++
				}
				return err
			}
		},
		"foreign group": func(t *testing.T, f *persistentOutputFixture) {
			f.write(t, "firecracker.console.log", "console evidence")
			inode := inodeForManagedCacheTestPath(t, filepath.Join(f.directory, "firecracker.console.log"))
			realStat := f.deps.statx
			f.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				err := realStat(ctx, fd, flags, mask, stat)
				if stat.Ino == inode {
					stat.Gid++
				}
				return err
			}
		},
		"unstable metadata": func(t *testing.T, f *persistentOutputFixture) {
			f.write(t, "firecracker.console.log", "console evidence")
			inode := inodeForManagedCacheTestPath(t, filepath.Join(f.directory, "firecracker.console.log"))
			realStat := f.deps.statx
			observations := int64(0)
			f.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				err := realStat(ctx, fd, flags, mask, stat)
				if stat.Ino == inode {
					observations++
					stat.Ctime.Sec += observations
				}
				return err
			}
		},
		"cross mount": func(_ *testing.T, f *persistentOutputFixture) {
			realOpen := f.deps.openAt2
			f.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				if name == testVMID {
					return -1, unix.EXDEV
				}
				return realOpen(ctx, fd, name, how)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputFixture(t)
			fixture.write(t, "firecracker.log", "retain this evidence")
			mutate(t, fixture)
			err := fixture.prepare(t.Context(), managedPersistentMetricsEnabled)
			require.Error(t, err)
			assert.Equal(t, "retain this evidence", readPersistentOutput(t, fixture.directory, "firecracker.log"))
			assert.Equal(t, false, errs.AsDomainError(err).Details["output_mutation_attempted"])
			assert.Equal(t, true, errs.AsDomainError(err).Details["output_cleanup_required"])
		})
	}
}

// Rationale: replacement after opening must never redirect truncation to a pathname-reopened substitute.
func TestPersistentOutputsRetainOriginalFileDuringNameReplacement(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	fixture.write(t, "firecracker.log", "original")
	realOpen := fixture.deps.openAt2
	fixture.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened, err := realOpen(ctx, fd, name, how)
		if err == nil && name == "firecracker.log" {
			require.NoError(
				t,
				os.Rename(
					filepath.Join(fixture.directory, name),
					filepath.Join(fixture.directory, "retained-original"),
				),
			)
			fixture.write(t, name, "replacement must survive")
		}
		return opened, err
	}
	require.NoError(t, fixture.prepare(t.Context(), managedPersistentMetricsDisabled))
	assert.Equal(t, "replacement must survive", readPersistentOutput(t, fixture.directory, "firecracker.log"))
	assert.Empty(t, readPersistentOutput(t, fixture.directory, "retained-original"))
}

func TestPersistentOutputsNeverTouchDisabledMetrics(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	fixture.write(t, "firecracker.metrics", "older metrics")
	require.NoError(t, fixture.prepare(t.Context(), managedPersistentMetricsDisabled))
	assert.Equal(t, "older metrics", readPersistentOutput(t, fixture.directory, "firecracker.metrics"))
}

func TestPersistentOutputsRejectCancelledOrMissingContextBeforeOwnershipTransfer(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputFixture(t)
			fixture.write(t, "firecracker.log", "preserved log")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if missing {
				ctx = nil
			}
			err := fixture.prepare(ctx, managedPersistentMetricsEnabled)
			require.Error(t, err)
			assert.Nil(t, fixture.launch.state.persistentOutputs)
			assert.NotEmpty(t, fixture.cache.retained)
			assert.Equal(t, "preserved log", readPersistentOutput(t, fixture.directory, "firecracker.log"))
		})
	}
}
