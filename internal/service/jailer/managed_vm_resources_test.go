package jailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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
				testVMFirecrackerLaunchIntent(tc.cloudInit),
			)
			require.NoError(t, err)

			want := vmLaunchResourceSpec{
				vmID:                vmResourceID(testManagedVMID),
				kernelID:            kernelResourceID(testManagedKernelID),
				expectedRootfsBytes: tc.rootfs,
				firecracker:         testVMFirecrackerLaunchIntent(tc.cloudInit),
			}
			if diff := cmp.Diff(
				want,
				got,
				cmp.AllowUnexported(
					vmLaunchResourceSpec{},
					vmFirecrackerLaunchIntent{},
					vmFirecrackerTapIdentity{},
				),
			); diff != "" {
				t.Errorf("newVMLaunchResourceSpec() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewVMLaunchResourceSpecOwnsMutableIntentValues(t *testing.T) {
	t.Parallel()

	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	originalCID := *intent.vsockCID
	originalCapability := intent.cpuConfig.KvmCapabilities[0]
	spec, err := newVMLaunchResourceSpec(
		testManagedVMID,
		testManagedKernelID,
		testManagedRootfsMinBytes,
		intent,
	)
	require.NoError(t, err)

	*intent.vsockCID++
	intent.cpuConfig.KvmCapabilities[0] = "forged"
	assert.Equal(t, originalCID, *spec.firecracker.vsockCID)
	assert.Equal(t, originalCapability, spec.firecracker.cpuConfig.KvmCapabilities[0])
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

			_, err := newVMLaunchResourceSpec(
				tc.vmID,
				tc.kernelID,
				tc.rootfs,
				testVMFirecrackerLaunchIntent(tc.cloudInit),
			)
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
			firecracker:         testVMFirecrackerLaunchIntent(cloudInitAbsent),
		},
		"forged kernel ID": {
			vmID:                vmResourceID(testManagedVMID),
			kernelID:            kernelResourceID(strings.Repeat("g", 64)),
			expectedRootfsBytes: testManagedRootfsMinBytes,
			firecracker:         testVMFirecrackerLaunchIntent(cloudInitAbsent),
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
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&cacheLease.configDeps)
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
	config := buildManagedFirecrackerConfig(testVMFirecrackerLaunchIntent(cloudInit))
	require.NoError(t, os.WriteFile(configPath, mustMarshalManagedFirecrackerConfig(t, config), 0600))
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

func requireManagedVMResourceDomainErrorCode(t *testing.T, err error, code errs.Code) {
	t.Helper()

	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, code, domainErr.Code)
}
