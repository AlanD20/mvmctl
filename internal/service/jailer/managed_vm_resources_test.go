package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	testManagedVMID           = "0123456789abcdef0123456789abcdef"
	testManagedKernelID       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testManagedRootfsMinBytes = uint64(128 << 20)
	testManagedRootfsMaxBytes = uint64(16 << 40)
)

func TestNewVMLaunchResourceSpecAcceptsClosedBaseSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rootfs    uint64
		cloudInit cloudInitPresence
	}{
		{name: "minimum rootfs without cloud init", rootfs: testManagedRootfsMinBytes, cloudInit: cloudInitAbsent},
		{name: "maximum rootfs with cloud init", rootfs: testManagedRootfsMaxBytes, cloudInit: cloudInitPresent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := newVMLaunchResourceSpec(
				testManagedVMID,
				testManagedKernelID,
				tc.rootfs,
				tc.cloudInit,
			)
			require.NoError(t, err)

			want := vmLaunchResourceSpec{
				vmID:                vmResourceID(testManagedVMID),
				kernelID:            kernelResourceID(testManagedKernelID),
				expectedRootfsBytes: tc.rootfs,
				cloudInit:           tc.cloudInit,
			}
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(vmLaunchResourceSpec{})); diff != "" {
				t.Errorf("newVMLaunchResourceSpec() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewVMLaunchResourceSpecRejectsInvalidSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		vmID        string
		kernelID    string
		rootfs      uint64
		cloudInit   cloudInitPresence
		wantMessage string
	}{
		{
			name:        "empty VM ID",
			vmID:        "",
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed VM ID must be 32 lowercase hexadecimal characters",
		},
		{
			name:        "uppercase VM ID",
			vmID:        strings.ToUpper(testManagedVMID),
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed VM ID must be 32 lowercase hexadecimal characters",
		},
		{
			name:        "non-hexadecimal VM ID",
			vmID:        strings.Repeat("g", 32),
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed VM ID must be 32 lowercase hexadecimal characters",
		},
		{
			name:        "short kernel ID",
			vmID:        testManagedVMID,
			kernelID:    testManagedKernelID[:63],
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed kernel ID must be 64 lowercase hexadecimal characters",
		},
		{
			name:        "uppercase kernel ID",
			vmID:        testManagedVMID,
			kernelID:    strings.ToUpper(testManagedKernelID),
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed kernel ID must be 64 lowercase hexadecimal characters",
		},
		{
			name:        "rootfs below minimum",
			vmID:        testManagedVMID,
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMinBytes - 1,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed VM rootfs size is outside the supported range",
		},
		{
			name:        "rootfs above maximum",
			vmID:        testManagedVMID,
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMaxBytes + 1,
			cloudInit:   cloudInitAbsent,
			wantMessage: "managed VM rootfs size is outside the supported range",
		},
		{
			name:        "zero cloud-init presence",
			vmID:        testManagedVMID,
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   0,
			wantMessage: "managed cloud-init presence is invalid",
		},
		{
			name:        "unknown cloud-init presence",
			vmID:        testManagedVMID,
			kernelID:    testManagedKernelID,
			rootfs:      testManagedRootfsMinBytes,
			cloudInit:   cloudInitPresence(3),
			wantMessage: "managed cloud-init presence is invalid",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := newVMLaunchResourceSpec(tc.vmID, tc.kernelID, tc.rootfs, tc.cloudInit)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
			assert.Equal(t, tc.wantMessage, domainErr.Message)
		})
	}
}

func TestValidateVMLaunchResourceSpecRejectsForgedValues(t *testing.T) {
	t.Parallel()

	tests := map[string]vmLaunchResourceSpec{
		"zero value": {},
		"forged VM ID": {
			vmID:                vmResourceID(strings.Repeat("g", 32)),
			kernelID:            kernelResourceID(testManagedKernelID),
			expectedRootfsBytes: testManagedRootfsMinBytes,
			cloudInit:           cloudInitAbsent,
		},
		"forged kernel ID": {
			vmID:                vmResourceID(testManagedVMID),
			kernelID:            kernelResourceID(strings.Repeat("g", 64)),
			expectedRootfsBytes: testManagedRootfsMinBytes,
			cloudInit:           cloudInitAbsent,
		},
	}

	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validateVMLaunchResourceSpec(spec)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
		})
	}
}

func TestPinVMLaunchResourcesPinsFixedBaseLeaves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cloudInit cloudInitPresence
	}{
		{name: "without cloud init", cloudInit: cloudInitAbsent},
		{name: "with cloud init", cloudInit: cloudInitPresent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := prepareManagedVMLaunchResourceFixture(t, tc.cloudInit)
			originalCacheIdentity := fixture.cacheLease.identity
			originalRootfsInode := inodeForManagedCacheTestPath(t, fixture.rootfsPath)

			var pathOpenCalls int
			fixture.cacheLease.deps.open = func(context.Context, string, int, uint32) (int, error) {
				pathOpenCalls++
				return -1, errors.New("managed resources must not reopen a path")
			}
			type observedOpen struct {
				Name    string
				Flags   uint64
				Resolve uint64
			}
			observed := make([]observedOpen, 0, 7)
			realOpenAt2 := fixture.cacheLease.deps.openAt2
			fixture.cacheLease.deps.openAt2 = func(
				ctx context.Context,
				parentFD int,
				name string,
				how *unix.OpenHow,
			) (int, error) {
				observed = append(observed, observedOpen{Name: name, Flags: how.Flags, Resolve: how.Resolve})
				return realOpenAt2(ctx, parentFD, name, how)
			}
			type observedStatx struct {
				Flags int
				Mask  int
			}
			observedStats := make([]observedStatx, 0, 7)
			realStatx := fixture.cacheLease.deps.statx
			fixture.cacheLease.deps.statx = func(
				ctx context.Context,
				fd int,
				flags int,
				mask int,
				stat *unix.Statx_t,
			) error {
				observedStats = append(observedStats, observedStatx{Flags: flags, Mask: mask})
				return realStatx(ctx, fd, flags, mask, stat)
			}

			spec, err := newVMLaunchResourceSpec(
				testManagedVMID,
				testManagedKernelID,
				testManagedRootfsMinBytes,
				tc.cloudInit,
			)
			require.NoError(t, err)
			lease, err := fixture.cacheLease.pinVMLaunchResources(context.Background(), spec)
			require.NoError(t, err)
			require.NotNil(t, lease)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

			assert.Zero(t, pathOpenCalls)
			directoryFlags := uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW)
			fileFlags := uint64(unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW)
			resolveFlags := uint64(
				unix.RESOLVE_BENEATH |
					unix.RESOLVE_NO_SYMLINKS |
					unix.RESOLVE_NO_MAGICLINKS |
					unix.RESOLVE_NO_XDEV,
			)
			wantObserved := []observedOpen{
				{Name: "vms", Flags: directoryFlags, Resolve: resolveFlags},
				{Name: testManagedVMID, Flags: directoryFlags, Resolve: resolveFlags},
				{Name: infra.VMRootfsFilename, Flags: fileFlags, Resolve: resolveFlags},
				{Name: infra.VMFirecrackerConfigFilename, Flags: fileFlags, Resolve: resolveFlags},
			}
			if tc.cloudInit == cloudInitPresent {
				wantObserved = append(wantObserved, observedOpen{
					Name: infra.VMCloudInitISOFilename, Flags: fileFlags, Resolve: resolveFlags,
				})
			}
			wantObserved = append(wantObserved,
				observedOpen{Name: "kernels", Flags: directoryFlags, Resolve: resolveFlags},
				observedOpen{Name: testManagedKernelID, Flags: fileFlags, Resolve: resolveFlags},
			)
			if diff := cmp.Diff(wantObserved, observed); diff != "" {
				t.Errorf("managed VM resource opens mismatch (-want +got):\n%s", diff)
			}
			wantStatFlags := unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
				unix.AT_STATX_FORCE_SYNC
			for _, observedStat := range observedStats {
				assert.Equal(t, wantStatFlags, observedStat.Flags)
				assert.Equal(t, unix.STATX_BASIC_STATS, observedStat.Mask)
				assert.Zero(t, observedStat.Flags&unix.AT_STATX_DONT_SYNC)
			}
			assert.Len(t, observedStats, len(wantObserved))

			assert.Equal(t, fixture.cache.caller.uid, lease.ownerUID)
			if diff := cmp.Diff(
				originalCacheIdentity,
				lease.cacheIdentity,
				cmpopts.EquateComparable(managedCacheIdentity{}),
			); diff != "" {
				t.Errorf("managed cache identity transfer mismatch (-want +got):\n%s", diff)
			}
			assertManagedDescriptorMatchesPath(t, lease.rootfsFD, fixture.rootfsPath)
			assertManagedDescriptorMatchesPath(t, lease.configFD, fixture.configPath)
			assertManagedDescriptorMatchesPath(t, lease.kernelFD, fixture.kernelPath)
			if tc.cloudInit == cloudInitPresent {
				assertManagedDescriptorMatchesPath(t, lease.cloudInitFD, fixture.cloudInitPath)
			} else {
				assert.Equal(t, -1, lease.cloudInitFD)
			}

			movedRootfs := fixture.rootfsPath + ".moved"
			require.NoError(t, os.Rename(fixture.rootfsPath, movedRootfs))
			require.NoError(t, os.Symlink(fixture.configPath, fixture.rootfsPath))
			var pinnedStat unix.Stat_t
			require.NoError(t, unix.Fstat(lease.rootfsFD, &pinnedStat))
			assert.Equal(t, originalRootfsInode, pinnedStat.Ino)

			assert.Empty(t, fixture.cacheLease.retained)
			assert.Equal(t, -1, fixture.cacheLease.cacheFD)
			assert.Zero(t, fixture.cacheLease.ownerUID)
		})
	}
}

type managedVMLaunchResourceFixture struct {
	cache         managedCacheFixture
	cacheLease    *managedCacheLease
	rootfsPath    string
	configPath    string
	kernelPath    string
	cloudInitPath string
}

func prepareManagedVMLaunchResourceFixture(
	t *testing.T,
	cloudInit cloudInitPresence,
) managedVMLaunchResourceFixture {
	t.Helper()

	cache := prepareManagedCacheFixture(t)
	fixture := createManagedVMLaunchResourceFiles(t, cache, cloudInit)

	cacheLease, err := pinManagedCache(
		context.Background(),
		cache.deps,
		cache.policy,
		cache.caller,
		cache.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, cacheLease)
	t.Cleanup(func() { require.NoError(t, cacheLease.Release(context.Background())) })
	prepareManagedVMResourceOwnershipForTest(cacheLease, cache.caller.uid)
	fixture.cacheLease = cacheLease
	return fixture
}

func createManagedVMLaunchResourceFiles(
	t *testing.T,
	cache managedCacheFixture,
	cloudInit cloudInitPresence,
) managedVMLaunchResourceFixture {
	t.Helper()

	vmDir := filepath.Join(cache.cacheDir, "vms", testManagedVMID)
	kernelDir := filepath.Join(cache.cacheDir, "kernels")
	require.NoError(t, os.MkdirAll(vmDir, 0700))
	require.NoError(t, os.Mkdir(kernelDir, 0700))

	rootfsPath := filepath.Join(vmDir, infra.VMRootfsFilename)
	require.NoError(t, os.WriteFile(rootfsPath, nil, 0600))
	require.NoError(t, os.Truncate(rootfsPath, int64(testManagedRootfsMinBytes)))
	configPath := filepath.Join(vmDir, infra.VMFirecrackerConfigFilename)
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))
	kernelPath := filepath.Join(kernelDir, testManagedKernelID)
	require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0700))
	cloudInitPath := filepath.Join(vmDir, infra.VMCloudInitISOFilename)
	if cloudInit == cloudInitPresent {
		require.NoError(t, os.WriteFile(cloudInitPath, []byte("iso"), 0600))
	}

	return managedVMLaunchResourceFixture{
		cache:         cache,
		rootfsPath:    rootfsPath,
		configPath:    configPath,
		kernelPath:    kernelPath,
		cloudInitPath: cloudInitPath,
	}
}

func prepareManagedVMResourceOwnershipForTest(cacheLease *managedCacheLease, ownerUID uint32) {
	if ownerUID == uint32(os.Geteuid()) {
		return
	}
	realStatx := cacheLease.deps.statx
	cacheLease.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
		if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
			return err
		}
		stat.Uid = ownerUID
		return nil
	}
}

func assertManagedDescriptorMatchesPath(t *testing.T, fd int, path string) {
	t.Helper()

	var descriptorStat unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &descriptorStat))
	var pathStat unix.Stat_t
	require.NoError(t, unix.Stat(path, &pathStat))
	assert.Equal(t, pathStat.Dev, descriptorStat.Dev)
	assert.Equal(t, pathStat.Ino, descriptorStat.Ino)
}

func requireManagedVMResourceDomainErrorCode(t *testing.T, err error, code errs.Code) {
	t.Helper()

	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, code, domainErr.Code)
}
