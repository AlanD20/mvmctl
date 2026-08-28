package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	testManagedConfigMaxBytes    = int64(1 << 20)
	testManagedKernelMaxBytes    = int64(1 << 30)
	testManagedCloudInitMaxBytes = int64(256 << 20)
)

type managedVMRaceAttackerKind uint8

const (
	managedVMRaceVMStore managedVMRaceAttackerKind = iota + 1
	managedVMRaceVMDirectory
	managedVMRaceRootfs
	managedVMRaceConfig
	managedVMRaceCloudInit
	managedVMRaceKernelStore
	managedVMRaceKernel
)

func TestPinVMLaunchResourcesRejectsSymlinkAtEveryFixedLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		directory bool
		attacker  managedVMRaceAttackerKind
		path      func(managedVMLaunchResourceFixture) string
	}{
		{name: "VM store", directory: true, attacker: managedVMRaceVMStore, path: func(
			f managedVMLaunchResourceFixture,
		) string {
			return filepath.Join(f.cache.cacheDir, "vms")
		}},
		{name: "VM directory", directory: true, attacker: managedVMRaceVMDirectory, path: func(
			f managedVMLaunchResourceFixture,
		) string {
			return filepath.Dir(f.rootfsPath)
		}},
		{name: "VM rootfs", attacker: managedVMRaceRootfs, path: func(f managedVMLaunchResourceFixture) string {
			return f.rootfsPath
		}},
		{
			name:     "Firecracker configuration",
			attacker: managedVMRaceConfig,
			path:     func(f managedVMLaunchResourceFixture) string { return f.configPath },
		},
		{name: "cloud-init ISO", attacker: managedVMRaceCloudInit, path: func(
			f managedVMLaunchResourceFixture,
		) string {
			return f.cloudInitPath
		}},
		{name: "kernel store", directory: true, attacker: managedVMRaceKernelStore, path: func(
			f managedVMLaunchResourceFixture,
		) string {
			return filepath.Dir(f.kernelPath)
		}},
		{name: "kernel", attacker: managedVMRaceKernel, path: func(f managedVMLaunchResourceFixture) string {
			return f.kernelPath
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitPresent)
			attacker := filepath.Join(fixture.cache.cacheDir, "attacker")
			prepareManagedVMResourceRaceAttacker(t, attacker, tc.attacker)
			target := tc.path(fixture)
			if tc.directory {
				require.NoError(t, os.RemoveAll(target))
				require.NoError(t, os.Symlink(attacker, target))
			} else {
				require.NoError(t, os.Remove(target))
				require.NoError(t, os.Symlink(attacker, target))
			}

			lease, err := fixture.cacheLease.pinVMLaunchResources(
				context.Background(),
				mustManagedVMLaunchResourceSpec(t, cloudInitPresent, testManagedRootfsMinBytes),
			)
			registerManagedVMResourceLeaseCleanup(t, lease)
			assert.Nil(t, lease)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.NotEmpty(t, fixture.cacheLease.retained)
		})
	}
}

func TestPinVMLaunchResourcesRejectsUnsafeBaseResourceMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rootfsBytes uint64
		mutate      func(*testing.T, managedVMLaunchResourceFixture)
		wantMessage string
	}{
		{
			name: "rootfs FIFO",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Remove(f.rootfsPath))
				require.NoError(t, unix.Mkfifo(f.rootfsPath, 0600))
			},
			wantMessage: "not a regular file",
		},
		{
			name: "configuration directory",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Remove(f.configPath))
				require.NoError(t, os.Mkdir(f.configPath, 0700))
			},
			wantMessage: "not a regular file",
		},
		{
			name: "rootfs hardlink",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Link(f.rootfsPath, f.rootfsPath+".alias"))
			},
			wantMessage: "exactly one link",
		},
		{
			name: "rootfs missing owner write",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.rootfsPath, 0400))
			},
			wantMessage: "required owner access",
		},
		{
			name: "rootfs executable",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.rootfsPath, 0700))
			},
			wantMessage: "unsupported execute access",
		},
		{
			name: "configuration missing owner read",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.configPath, 0200))
			},
			wantMessage: "required owner access",
		},
		{
			name: "configuration group writable",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.configPath, 0620))
			},
			wantMessage: "writable by another user",
		},
		{
			name: "configuration executable",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.configPath, 0700))
			},
			wantMessage: "unsupported execute access",
		},
		{
			name: "configuration has setuid bit",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.configPath, 0600|os.ModeSetuid))
			},
			wantMessage: "special mode bits",
		},
		{
			name: "kernel missing owner read",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.kernelPath, 0200))
			},
			wantMessage: "required owner access",
		},
		{
			name: "cloud-init ISO missing owner read",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.cloudInitPath, 0200))
			},
			wantMessage: "required owner access",
		},
		{
			name: "cloud-init ISO executable",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(f.cloudInitPath, 0700))
			},
			wantMessage: "unsupported execute access",
		},
		{
			name: "VM store missing owner write",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				path := filepath.Join(f.cache.cacheDir, "vms")
				require.NoError(t, os.Chmod(path, 0500))
				t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0700)) })
			},
			wantMessage: "owner read, write, and traversal",
		},
		{
			name: "VM directory has sticky bit",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Chmod(filepath.Dir(f.rootfsPath), 0700|os.ModeSticky))
			},
			wantMessage: "special mode bits",
		},
		{
			name: "empty configuration",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.configPath, 0))
			},
			wantMessage: "size is outside",
		},
		{
			name: "oversized configuration",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.configPath, testManagedConfigMaxBytes+1))
			},
			wantMessage: "size is outside",
		},
		{
			name: "empty kernel",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.kernelPath, 0))
			},
			wantMessage: "size is outside",
		},
		{
			name: "oversized kernel",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.kernelPath, testManagedKernelMaxBytes+1))
			},
			wantMessage: "size is outside",
		},
		{
			name: "empty cloud-init ISO",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.cloudInitPath, 0))
			},
			wantMessage: "size is outside",
		},
		{
			name: "oversized cloud-init ISO",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.cloudInitPath, testManagedCloudInitMaxBytes+1))
			},
			wantMessage: "size is outside",
		},
		{
			name:        "rootfs expected size mismatch",
			rootfsBytes: testManagedRootfsMinBytes + 4096,
			mutate:      func(*testing.T, managedVMLaunchResourceFixture) {},
			wantMessage: "does not match the expected size",
		},
		{
			name: "rootfs below hard minimum",
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.rootfsPath, int64(testManagedRootfsMinBytes-1)))
			},
			wantMessage: "size is outside",
		},
		{
			name:        "rootfs above hard maximum",
			rootfsBytes: testManagedRootfsMaxBytes,
			mutate: func(t *testing.T, f managedVMLaunchResourceFixture) {
				require.NoError(t, os.Truncate(f.rootfsPath, int64(testManagedRootfsMaxBytes+1)))
			},
			wantMessage: "size is outside",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitPresent)
			tc.mutate(t, fixture)
			rootfsBytes := tc.rootfsBytes
			if rootfsBytes == 0 {
				rootfsBytes = testManagedRootfsMinBytes
			}
			lease, err := fixture.cacheLease.pinVMLaunchResources(
				context.Background(),
				mustManagedVMLaunchResourceSpec(t, cloudInitPresent, rootfsBytes),
			)
			registerManagedVMResourceLeaseCleanup(t, lease)
			assert.Nil(t, lease)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.Contains(t, err.Error(), tc.wantMessage)
			assert.NotEmpty(t, fixture.cacheLease.retained)
		})
	}
}

func TestPinVMLaunchResourcesAcceptsIndependentSizeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rootfs    int64
		config    int64
		kernel    int64
		cloudInit int64
	}{
		{
			name:      "minimum sizes",
			rootfs:    int64(testManagedRootfsMinBytes),
			config:    1,
			kernel:    1,
			cloudInit: 1,
		},
		{
			name:      "maximum sizes",
			rootfs:    int64(testManagedRootfsMaxBytes),
			config:    testManagedConfigMaxBytes,
			kernel:    testManagedKernelMaxBytes,
			cloudInit: testManagedCloudInitMaxBytes,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitPresent)
			require.NoError(t, os.Truncate(fixture.rootfsPath, tc.rootfs))
			require.NoError(t, os.Truncate(fixture.configPath, tc.config))
			require.NoError(t, os.Truncate(fixture.kernelPath, tc.kernel))
			require.NoError(t, os.Truncate(fixture.cloudInitPath, tc.cloudInit))
			lease, err := fixture.cacheLease.pinVMLaunchResources(
				context.Background(),
				mustManagedVMLaunchResourceSpec(t, cloudInitPresent, uint64(tc.rootfs)),
			)
			registerManagedVMResourceLeaseCleanup(t, lease)
			require.NoError(t, err)
			require.NotNil(t, lease)
			require.NoError(t, lease.Release(context.Background()))
		})
	}
}

func TestPinVMLaunchResourcesRejectsUnstableMetadataAndMountCrossing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		openErr  error
		mutate   func(*unix.Statx_t)
		wantCode errs.Code
	}{
		{name: "descendant mount crossing", openErr: unix.EXDEV, wantCode: errs.CodeValidationFailed},
		{name: "unexpected open failure", openErr: unix.EIO, wantCode: errs.CodeVMAtomicFailed},
		{name: "foreign owner", mutate: func(stat *unix.Statx_t) { stat.Uid++ }, wantCode: errs.CodeValidationFailed},
		{name: "incomplete size metadata", mutate: func(stat *unix.Statx_t) {
			stat.Mask &^= unix.STATX_SIZE
		}, wantCode: errs.CodeValidationFailed},
		{name: "automount resource", mutate: func(stat *unix.Statx_t) {
			stat.Attributes_mask |= unix.STATX_ATTR_AUTOMOUNT
			stat.Attributes |= unix.STATX_ATTR_AUTOMOUNT
		}, wantCode: errs.CodeValidationFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
			rootfsInode := inodeForManagedCacheTestPath(t, fixture.rootfsPath)
			if tc.openErr != nil {
				realOpenAt2 := fixture.cacheLease.deps.openAt2
				fixture.cacheLease.deps.openAt2 = func(
					ctx context.Context,
					parentFD int,
					name string,
					how *unix.OpenHow,
				) (int, error) {
					if name == infra.VMRootfsFilename {
						return -1, tc.openErr
					}
					return realOpenAt2(ctx, parentFD, name, how)
				}
			} else {
				realStatx := fixture.cacheLease.deps.statx
				fixture.cacheLease.deps.statx = func(
					ctx context.Context,
					fd int,
					flags int,
					mask int,
					stat *unix.Statx_t,
				) error {
					if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
						return err
					}
					if stat.Ino == rootfsInode {
						tc.mutate(stat)
					}
					return nil
				}
			}

			lease, err := fixture.cacheLease.pinVMLaunchResources(
				context.Background(),
				mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes),
			)
			registerManagedVMResourceLeaseCleanup(t, lease)
			assert.Nil(t, lease)
			requireManagedVMResourceDomainErrorCode(t, err, tc.wantCode)
			assert.NotEmpty(t, fixture.cacheLease.retained)
		})
	}
}

func TestPinVMLaunchResourcesRejectsUnsafeDirectoryMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*unix.Statx_t)
	}{
		{name: "foreign owner", mutate: func(stat *unix.Statx_t) { stat.Uid++ }},
		{name: "incomplete type metadata", mutate: func(stat *unix.Statx_t) { stat.Mask &^= unix.STATX_TYPE }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
			vmsInode := inodeForManagedCacheTestPath(t, filepath.Join(fixture.cache.cacheDir, "vms"))
			realStatx := fixture.cacheLease.deps.statx
			fixture.cacheLease.deps.statx = func(
				ctx context.Context,
				fd int,
				flags int,
				mask int,
				stat *unix.Statx_t,
			) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == vmsInode {
					tc.mutate(stat)
				}
				return nil
			}

			lease, err := fixture.cacheLease.pinVMLaunchResources(
				context.Background(),
				mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes),
			)
			registerManagedVMResourceLeaseCleanup(t, lease)
			assert.Nil(t, lease)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.NotEmpty(t, fixture.cacheLease.retained)
		})
	}
}

func TestPinVMLaunchResourcesLeavesCacheLeaseReusableAfterRejection(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	require.NoError(t, os.Remove(fixture.configPath))
	spec := mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes)

	lease, err := fixture.cacheLease.pinVMLaunchResources(context.Background(), spec)
	registerManagedVMResourceLeaseCleanup(t, lease)
	assert.Nil(t, lease)
	requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
	assert.NotEmpty(t, fixture.cacheLease.retained)

	require.NoError(t, os.WriteFile(fixture.configPath, []byte("{}"), 0600))
	lease, err = fixture.cacheLease.pinVMLaunchResources(context.Background(), spec)
	registerManagedVMResourceLeaseCleanup(t, lease)
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Release(context.Background()))
}

func TestPinVMLaunchResourcesValidatesForgedSpecBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	var openCalls int
	fixture.cacheLease.deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) {
		openCalls++
		return -1, errors.New("must not open")
	}

	lease, err := fixture.cacheLease.pinVMLaunchResources(context.Background(), vmLaunchResourceSpec{})
	registerManagedVMResourceLeaseCleanup(t, lease)
	assert.Nil(t, lease)
	requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
	assert.Zero(t, openCalls)
}

func TestVMLaunchResourceLeaseReleasesEveryDescriptorInReverseWithoutCancellation(t *testing.T) {
	t.Parallel()

	cache := prepareManagedCacheFixture(t)
	createManagedVMLaunchResourceFiles(t, cache, cloudInitPresent)
	deps := cache.deps
	opened := make([]int, 0, 12)
	realOpen := deps.open
	deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, path, flags, mode)
		if err == nil {
			opened = append(opened, fd)
		}
		return fd, err
	}
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil {
			opened = append(opened, fd)
		}
		return fd, err
	}

	cacheLease, err := pinManagedCache(context.Background(), deps, cache.policy, cache.caller, cache.locator)
	require.NoError(t, err)
	require.NotNil(t, cacheLease)
	t.Cleanup(func() { require.NoError(t, cacheLease.Release(context.Background())) })
	prepareManagedVMResourceOwnershipForTest(cacheLease, cache.caller.uid)
	lease, err := cacheLease.pinVMLaunchResources(
		context.Background(),
		mustManagedVMLaunchResourceSpec(t, cloudInitPresent, testManagedRootfsMinBytes),
	)
	registerManagedVMResourceLeaseCleanup(t, lease)
	require.NoError(t, err)
	require.NotNil(t, lease)

	closed := make([]int, 0, len(opened))
	realClose := lease.deps.close
	lease.deps.close = func(ctx context.Context, fd int) error {
		assert.NoError(t, ctx.Err())
		closed = append(closed, fd)
		return realClose(ctx, fd)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, lease.Release(cancelled))

	wantClosed := make([]int, len(opened))
	for index := range opened {
		wantClosed[index] = opened[len(opened)-1-index]
	}
	if diff := cmp.Diff(wantClosed, closed); diff != "" {
		t.Errorf("managed VM resource close order mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.Release(context.Background()))
	if diff := cmp.Diff(wantClosed, closed); diff != "" {
		t.Errorf("idempotent release changed close order (-want +got):\n%s", diff)
	}
}

func TestVMLaunchResourceLeaseReportsCloseFailureAfterClosingAllDescriptors(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	lease, err := fixture.cacheLease.pinVMLaunchResources(
		context.Background(),
		mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes),
	)
	registerManagedVMResourceLeaseCleanup(t, lease)
	require.NoError(t, err)
	require.NotNil(t, lease)

	failedFD := lease.configFD
	realClose := lease.deps.close
	var closeCalls int
	lease.deps.close = func(ctx context.Context, fd int) error {
		closeCalls++
		closeErr := realClose(ctx, fd)
		if fd == failedFD {
			return errors.Join(closeErr, errors.New("injected config close failure"))
		}
		return closeErr
	}
	wantCloseCalls := len(lease.retained)
	err = lease.Release(context.Background())
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, true, domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"])
	assert.Equal(t, wantCloseCalls, closeCalls)
	assert.Empty(t, lease.retained)
	require.NoError(t, lease.Release(context.Background()))
}

func TestAppendManagedVMResourceCleanupErrorPreservesPrimaryDomainMetadata(t *testing.T) {
	t.Parallel()

	primary := errs.New(
		errs.CodeValidationFailed,
		"primary failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(testManagedVMID),
		errs.WithDetails(map[string]any{"record_registered": true}),
	)
	joined := appendManagedVMResourceCleanupError(primary, "close resource descriptors", errors.New("close failed"))
	domainErr := errs.AsDomainError(joined)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, testManagedVMID, domainErr.Entity)
	assert.Equal(t, true, domainErr.Details["record_registered"])
	assert.Equal(t, true, domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"])
}

func TestPinVMLaunchResourcesPreservesPrimaryErrorWhenRejectedFileCloseFails(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	configInode := inodeForManagedCacheTestPath(t, fixture.configPath)
	realStatx := fixture.cacheLease.deps.statx
	fixture.cacheLease.deps.statx = func(
		ctx context.Context,
		fd int,
		flags int,
		mask int,
		stat *unix.Statx_t,
	) error {
		if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
			return err
		}
		if stat.Ino == configInode {
			return errors.New("injected config stat failure")
		}
		return nil
	}
	realClose := fixture.cacheLease.deps.close
	fixture.cacheLease.deps.close = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		statErr := unix.Fstat(fd, &stat)
		closeErr := realClose(ctx, fd)
		if statErr == nil && stat.Ino == configInode {
			return errors.Join(closeErr, errors.New("injected rejected-file close failure"))
		}
		return closeErr
	}

	lease, err := fixture.cacheLease.pinVMLaunchResources(
		context.Background(),
		mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes),
	)
	registerManagedVMResourceLeaseCleanup(t, lease)
	assert.Nil(t, lease)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, true, domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"])
	assert.Contains(t, domainErr.Message, "inspect managed Firecracker configuration")
	assert.Contains(t, domainErr.Message, "close rejected managed VM file")
	assert.NotEmpty(t, fixture.cacheLease.retained)
}

func TestPinVMLaunchResourcesClosesPartialAcquisitionWithUncancelledContext(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	ctx, cancel := context.WithCancel(context.Background())
	realOpenAt2 := fixture.cacheLease.deps.openAt2
	fixture.cacheLease.deps.openAt2 = func(
		ctx context.Context,
		parentFD int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == "vms" {
			cancel()
		}
		return fd, err
	}
	realClose := fixture.cacheLease.deps.close
	closeContextErrors := make([]error, 0, 1)
	fixture.cacheLease.deps.close = func(ctx context.Context, fd int) error {
		closeContextErrors = append(closeContextErrors, ctx.Err())
		return realClose(ctx, fd)
	}

	lease, err := fixture.cacheLease.pinVMLaunchResources(
		ctx,
		mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes),
	)
	registerManagedVMResourceLeaseCleanup(t, lease)
	assert.Nil(t, lease)
	requireManagedVMResourceDomainErrorCode(t, err, errs.CodeVMAtomicFailed)
	if diff := cmp.Diff([]error{nil}, closeContextErrors); diff != "" {
		t.Errorf("partial acquisition cleanup contexts mismatch (-want +got):\n%s", diff)
	}
	assert.NotEmpty(t, fixture.cacheLease.retained)
}

func TestPinVMLaunchResourcesResistsContinuousSymlinkReplacementAtEveryFixedLevel(t *testing.T) {
	tests := []struct {
		name     string
		attacker managedVMRaceAttackerKind
		path     func(managedVMLaunchResourceFixture) string
	}{
		{name: "VM store", attacker: managedVMRaceVMStore, path: func(f managedVMLaunchResourceFixture) string {
			return filepath.Join(f.cache.cacheDir, "vms")
		}},
		{name: "VM directory", attacker: managedVMRaceVMDirectory, path: func(f managedVMLaunchResourceFixture) string {
			return filepath.Dir(f.rootfsPath)
		}},
		{name: "VM rootfs", attacker: managedVMRaceRootfs, path: func(f managedVMLaunchResourceFixture) string {
			return f.rootfsPath
		}},
		{
			name:     "Firecracker configuration",
			attacker: managedVMRaceConfig,
			path:     func(f managedVMLaunchResourceFixture) string { return f.configPath },
		},
		{name: "cloud-init ISO", attacker: managedVMRaceCloudInit, path: func(f managedVMLaunchResourceFixture) string {
			return f.cloudInitPath
		}},
		{name: "kernel store", attacker: managedVMRaceKernelStore, path: func(f managedVMLaunchResourceFixture) string {
			return filepath.Dir(f.kernelPath)
		}},
		{name: "kernel", attacker: managedVMRaceKernel, path: func(f managedVMLaunchResourceFixture) string {
			return f.kernelPath
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitPresent)
			rootfsInode := inodeForManagedCacheTestPath(t, fixture.rootfsPath)
			configInode := inodeForManagedCacheTestPath(t, fixture.configPath)
			kernelInode := inodeForManagedCacheTestPath(t, fixture.kernelPath)
			cloudInitInode := inodeForManagedCacheTestPath(t, fixture.cloudInitPath)

			target := tc.path(fixture)
			good := target + ".race-good"
			require.NoError(t, os.Rename(target, good))
			attacker := filepath.Join(fixture.cache.cacheDir, "race-attacker")
			prepareManagedVMResourceRaceAttacker(t, attacker, tc.attacker)

			stop := make(chan struct{})
			done := make(chan struct{})
			started := make(chan struct{})
			writerErr := make(chan error, 1)
			var startedOnce sync.Once
			reportWriterError := func(err error) {
				startedOnce.Do(func() { close(started) })
				writerErr <- err
			}
			go func() {
				defer close(done)
				for {
					select {
					case <-stop:
						return
					default:
					}
					if err := os.Symlink(attacker, target); err != nil {
						reportWriterError(err)
						return
					}
					startedOnce.Do(func() { close(started) })
					runtime.Gosched()
					if err := os.Remove(target); err != nil {
						reportWriterError(err)
						return
					}
					if err := os.Rename(good, target); err != nil {
						reportWriterError(err)
						return
					}
					runtime.Gosched()
					if err := os.Rename(target, good); err != nil {
						reportWriterError(err)
						return
					}
				}
			}()
			<-started
			var stopOnce sync.Once
			stopWriter := func() {
				stopOnce.Do(func() { close(stop) })
				<-done
			}
			t.Cleanup(stopWriter)

			for range 200 {
				lease, err := fixture.cacheLease.pinVMLaunchResources(
					context.Background(),
					mustManagedVMLaunchResourceSpec(t, cloudInitPresent, testManagedRootfsMinBytes),
				)
				registerManagedVMResourceLeaseCleanup(t, lease)
				if err != nil {
					requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
					runtime.Gosched()
					continue
				}
				require.NotNil(t, lease)
				assertManagedDescriptorInode(t, lease.rootfsFD, rootfsInode)
				assertManagedDescriptorInode(t, lease.configFD, configInode)
				assertManagedDescriptorInode(t, lease.kernelFD, kernelInode)
				assertManagedDescriptorInode(t, lease.cloudInitFD, cloudInitInode)
				require.NoError(t, lease.Release(context.Background()))
				break
			}
			stopWriter()
			select {
			case err := <-writerErr:
				require.NoError(t, err)
			default:
			}
		})
	}
}

func prepareManagedVMResourceRaceAttacker(t *testing.T, path string, kind managedVMRaceAttackerKind) {
	t.Helper()

	switch kind {
	case managedVMRaceVMStore:
		prepareManagedVMResourceRaceAttackerVMDirectory(t, filepath.Join(path, testManagedVMID))
	case managedVMRaceVMDirectory:
		prepareManagedVMResourceRaceAttackerVMDirectory(t, path)
	case managedVMRaceRootfs:
		require.NoError(t, os.WriteFile(path, nil, 0600))
		require.NoError(t, os.Truncate(path, int64(testManagedRootfsMinBytes)))
	case managedVMRaceConfig:
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	case managedVMRaceCloudInit:
		require.NoError(t, os.WriteFile(path, []byte("iso"), 0600))
	case managedVMRaceKernelStore:
		require.NoError(t, os.Mkdir(path, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(path, testManagedKernelID), []byte("kernel"), 0700))
	case managedVMRaceKernel:
		require.NoError(t, os.WriteFile(path, []byte("kernel"), 0700))
	default:
		require.FailNow(t, "unknown managed VM race attacker kind")
	}
}

func prepareManagedVMResourceRaceAttackerVMDirectory(t *testing.T, path string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(path, 0700))
	rootfs := filepath.Join(path, infra.VMRootfsFilename)
	require.NoError(t, os.WriteFile(rootfs, nil, 0600))
	require.NoError(t, os.Truncate(rootfs, int64(testManagedRootfsMinBytes)))
	require.NoError(t, os.WriteFile(filepath.Join(path, infra.VMFirecrackerConfigFilename), []byte("{}"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(path, infra.VMCloudInitISOFilename), []byte("iso"), 0600))
}

func registerManagedVMResourceLeaseCleanup(t *testing.T, lease *vmLaunchResourceLease) {
	t.Helper()

	if lease == nil {
		return
	}
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
}

func assertManagedDescriptorInode(t *testing.T, fd int, wantInode uint64) {
	t.Helper()

	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &stat))
	assert.Equal(t, wantInode, stat.Ino)
}

func mustManagedVMLaunchResourceSpec(
	t *testing.T,
	cloudInit cloudInitPresence,
	rootfsBytes uint64,
) vmLaunchResourceSpec {
	t.Helper()

	spec, err := newVMLaunchResourceSpec(testManagedVMID, testManagedKernelID, rootfsBytes, cloudInit)
	require.NoError(t, err)
	return spec
}
