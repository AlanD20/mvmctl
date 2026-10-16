package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// These typed leases stand in for already admitted root-owned backings. This unprivileged handoff test does not
// prove their acquisition, grants, or root-owned metadata; those checks require privileged runner coverage.
func testIsolatedLaunchBackings(
	t *testing.T,
	cache *managedCacheLease,
	cloudInit cloudInitPresence,
) *isolatedLaunchBackings {
	t.Helper()
	rootfs := testProtectedBackingLease(t, cache, protectedBackingRootfs, testManagedVMID, testManagedRootfsMinBytes)
	kernel := testProtectedBackingLease(t, cache, protectedBackingKernel, testManagedKernelID, 6)
	backings := &isolatedLaunchBackings{
		rootfs:  rootfs,
		kernel:  kernel,
		ordered: []*protectedBackingLease{kernel, rootfs},
	}
	if cloudInit == cloudInitPresent {
		backings.cloudInit = testProtectedBackingLease(t, cache, protectedBackingCloudInit, testManagedVMID, 3)
		backings.ordered = append([]*protectedBackingLease{backings.cloudInit}, backings.ordered...)
	}
	t.Cleanup(func() { require.NoError(t, backings.Release(context.Background())) })
	return backings
}

func testProtectedBackingLease(
	t *testing.T,
	cache *managedCacheLease,
	kind, logicalID string,
	size uint64,
) *protectedBackingLease {
	t.Helper()
	fd, err := unix.MemfdCreate("isolated-launch-backing", unix.MFD_CLOEXEC)
	require.NoError(t, err)
	require.NoError(t, unix.Ftruncate(fd, int64(size)))
	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &stat))
	return &protectedBackingLease{
		fd: fd, lockFD: fd, retained: []int{fd},
		ownerUID: cache.ownerUID, cacheIdentity: cache.identity,
		identity: managedVMConfigStat{
			deviceMajor: unix.Major(stat.Dev), deviceMinor: unix.Minor(stat.Dev), inode: stat.Ino,
			mode: uint16(stat.Mode), uid: stat.Uid, gid: stat.Gid, nlink: uint32(stat.Nlink), size: size,
		},
		record: protectedBackingRecord{
			Kind: kind, ID: logicalID, Phase: "ready",
			Backing: protectedBackingIdentity{Device: stat.Dev, Inode: stat.Ino, Size: int64(size)},
		},
	}
}

func TestPinIsolatedVMLaunchResourcesUsesProtectedDescriptorsNotOperatorBackings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cloudInit cloudInitPresence
	}{{"without cloud init", cloudInitAbsent}, {"with cloud init", cloudInitPresent}} {
		t.Run(tc.name, func(t *testing.T) {
			cloudInit := tc.cloudInit
			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInit)
			backings := testIsolatedLaunchBackings(t, fixture.cacheLease, cloudInit)
			for _, path := range []string{fixture.rootfsPath, fixture.kernelPath} {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(fixture.configPath, path))
			}
			if cloudInit == cloudInitPresent {
				require.NoError(t, os.Remove(fixture.cloudInitPath))
				require.NoError(t, os.Symlink(fixture.configPath, fixture.cloudInitPath))
			}
			var opened []string
			realOpen := fixture.cacheLease.deps.openAt2
			fixture.cacheLease.deps.openAt2 = func(ctx context.Context, parent int, name string, how *unix.OpenHow) (int, error) {
				opened = append(opened, name)
				return realOpen(ctx, parent, name, how)
			}
			lease, err := fixture.cacheLease.pinIsolatedVMLaunchResources(
				t.Context(), mustManagedVMLaunchResourceSpec(t, cloudInit, testManagedRootfsMinBytes), backings,
			)
			require.NoError(t, err)
			require.NotNil(t, lease)
			assert.Same(t, backings, lease.protectedBackings)
			assert.Equal(t, backings.rootfs.fd, lease.rootfsFD)
			assert.Equal(t, backings.kernel.fd, lease.kernelFD)
			if cloudInit == cloudInitPresent {
				assert.Equal(t, backings.cloudInit.fd, lease.cloudInitFD)
			} else {
				assert.Equal(t, -1, lease.cloudInitFD)
			}
			if diff := cmp.Diff([]string{"vms", testManagedVMID, "firecracker.json"}, opened); diff != "" {
				t.Errorf("operator-cache opens (-want +got):\n%s", diff)
			}
			require.NotNil(t, lease.preparedConfig)
			assert.Empty(t, fixture.cacheLease.retained)
			require.NoError(t, lease.Release(t.Context()))
			assert.True(t, backings.rootfs.released)
			assert.True(t, backings.kernel.released)
		})
	}
}

func TestPinIsolatedVMLaunchResourcesRejectsIncompleteOrMismatchedAuthority(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*isolatedLaunchBackings, *vmLaunchResourceSpec)
		message string
	}{
		"missing rootfs": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.rootfs = nil },
			message: "protected rootfs authority is absent",
		},
		"missing kernel": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.kernel = nil },
			message: "protected kernel authority is absent",
		},
		"wrong rootfs kind": {mutate: func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) {
			b.rootfs.record.Kind = protectedBackingKernel
		}, message: "protected rootfs does not match launch selection"},
		"wrong rootfs identity": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.rootfs.record.ID = testManagedKernelID },
			message: "protected rootfs does not match launch selection",
		},
		"wrong rootfs owner": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.rootfs.ownerUID++ },
			message: "protected rootfs does not match launch selection",
		},
		"wrong rootfs cache": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.rootfs.cacheIdentity.inode++ },
			message: "protected rootfs does not match launch selection",
		},
		"wrong rootfs size": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.rootfs.record.Backing.Size++ },
			message: "protected rootfs does not match launch selection",
		},
		"inactive rootfs": {
			mutate:  func(b *isolatedLaunchBackings, _ *vmLaunchResourceSpec) { b.rootfs.record.Phase = "removed" },
			message: "protected backing lease is not active",
		},
		"forged selection": {
			mutate:  func(_ *isolatedLaunchBackings, spec *vmLaunchResourceSpec) { spec.vmID = "forged" },
			message: "managed VM ID must be 32 lowercase hexadecimal characters",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
			backings := testIsolatedLaunchBackings(t, fixture.cacheLease, cloudInitAbsent)
			spec := mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes)
			tc.mutate(backings, &spec)
			lease, err := fixture.cacheLease.pinIsolatedVMLaunchResources(t.Context(), spec, backings)
			assert.Nil(t, lease)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.Contains(t, err.Error(), tc.message)
			assert.NotEmpty(t, fixture.cacheLease.retained)
		})
	}
}

func TestPinIsolatedVMLaunchResourcesRejectsSymlinkedOperatorLeaves(t *testing.T) {
	tests := map[string]func(managedVMLaunchResourceFixture) string{
		"VM store": func(f managedVMLaunchResourceFixture) string {
			return filepath.Join(f.cache.cacheDir, "vms")
		},
		"VM directory":       func(f managedVMLaunchResourceFixture) string { return filepath.Dir(f.rootfsPath) },
		"Firecracker config": func(f managedVMLaunchResourceFixture) string { return f.configPath },
	}
	for name, targetPath := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
			backings := testIsolatedLaunchBackings(t, fixture.cacheLease, cloudInitAbsent)
			target := targetPath(fixture)
			require.NoError(t, os.Rename(target, target+".original"))
			require.NoError(t, os.Symlink(target+".original", target))
			lease, err := fixture.cacheLease.pinIsolatedVMLaunchResources(
				t.Context(), mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes), backings,
			)
			assert.Nil(t, lease)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.NotEmpty(t, fixture.cacheLease.retained)
			assert.False(t, backings.rootfs.released)
		})
	}
}

func TestPinIsolatedVMLaunchResourcesClosesPartialAcquisitionWithoutCallerCancellation(t *testing.T) {
	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	backings := testIsolatedLaunchBackings(t, fixture.cacheLease, cloudInitAbsent)
	ctx, cancel := context.WithCancel(t.Context())
	realOpen := fixture.cacheLease.deps.openAt2
	fixture.cacheLease.deps.openAt2 = func(ctx context.Context, parent int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpen(ctx, parent, name, how)
		if err == nil && name == "vms" {
			cancel()
		}
		return fd, err
	}
	realClose := fixture.cacheLease.deps.close
	var closeContexts []error
	fixture.cacheLease.deps.close = func(ctx context.Context, fd int) error {
		closeContexts = append(closeContexts, ctx.Err())
		return realClose(ctx, fd)
	}
	lease, err := fixture.cacheLease.pinIsolatedVMLaunchResources(
		ctx, mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes), backings,
	)
	assert.Nil(t, lease)
	requireManagedVMResourceDomainErrorCode(t, err, errs.CodeVMAtomicFailed)
	if diff := cmp.Diff([]error{nil}, closeContexts); diff != "" {
		t.Errorf("partial acquisition cleanup contexts (-want +got):\n%s", diff)
	}
	assert.NotEmpty(t, fixture.cacheLease.retained)
}

func TestPinIsolatedVMLaunchResourcesPreservesInspectionAndCloseFailures(t *testing.T) {
	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitAbsent)
	backings := testIsolatedLaunchBackings(t, fixture.cacheLease, cloudInitAbsent)
	configInode := inodeForManagedCacheTestPath(t, fixture.configPath)
	inspectionErr := errors.New("injected config inspection failure")
	closeErr := errors.New("injected rejected-file close failure")
	realStatx := fixture.cacheLease.deps.statx
	fixture.cacheLease.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
		if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
			return err
		}
		if stat.Ino == configInode {
			return inspectionErr
		}
		return nil
	}
	realClose := fixture.cacheLease.deps.close
	fixture.cacheLease.deps.close = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		statErr := unix.Fstat(fd, &stat)
		err := realClose(ctx, fd)
		if statErr == nil && stat.Ino == configInode {
			return errors.Join(err, closeErr)
		}
		return err
	}
	lease, err := fixture.cacheLease.pinIsolatedVMLaunchResources(
		t.Context(), mustManagedVMLaunchResourceSpec(t, cloudInitAbsent, testManagedRootfsMinBytes), backings,
	)
	assert.Nil(t, lease)
	require.ErrorIs(t, err, inspectionErr)
	require.ErrorIs(t, err, closeErr)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"])
	assert.NotEmpty(t, fixture.cacheLease.retained)
	assert.False(t, backings.rootfs.released)
}

func TestIsolatedVMLaunchResourceLeaseReleasesAllDescriptorsAfterCloseFailure(t *testing.T) {
	fixture := prepareManagedVMLaunchResourceFixture(t, cloudInitPresent)
	backings := testIsolatedLaunchBackings(t, fixture.cacheLease, cloudInitPresent)
	lease, err := fixture.cacheLease.pinIsolatedVMLaunchResources(
		t.Context(), mustManagedVMLaunchResourceSpec(t, cloudInitPresent, testManagedRootfsMinBytes), backings,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	failedFD := lease.preparedConfig.state.fd
	closeErr := errors.New("injected sealed config close failure")
	realClose := lease.deps.close
	var closed []int
	lease.deps.close = func(ctx context.Context, fd int) error {
		assert.NoError(t, ctx.Err())
		closed = append(closed, fd)
		err := realClose(ctx, fd)
		if fd == failedFD {
			return errors.Join(err, closeErr)
		}
		return err
	}
	lease.preparedConfig.state.close = lease.deps.close
	wantClosed := []int{failedFD}
	for index := len(lease.retained) - 1; index >= 0; index-- {
		wantClosed = append(wantClosed, lease.retained[index])
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	err = lease.Release(cancelled)
	require.ErrorIs(t, err, closeErr)
	if diff := cmp.Diff(wantClosed, closed); diff != "" {
		t.Errorf("resource close order (-want +got):\n%s", diff)
	}
	assert.True(t, backings.rootfs.released)
	assert.True(t, backings.kernel.released)
	assert.True(t, backings.cloudInit.released)
	require.NoError(t, lease.Release(t.Context()))
}

func TestAppendManagedVMResourceCleanupErrorPreservesPrimaryDomainMetadata(t *testing.T) {
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

func mustManagedVMLaunchResourceSpec(
	t *testing.T,
	cloudInit cloudInitPresence,
	rootfsBytes uint64,
) vmLaunchResourceSpec {
	t.Helper()
	spec, err := newVMLaunchResourceSpec(testManagedVMID, testManagedKernelID, rootfsBytes,
		testVMFirecrackerLaunchIntent(cloudInit))
	require.NoError(t, err)
	return spec
}
