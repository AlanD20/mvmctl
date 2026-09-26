package inputs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/config"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

func networklessResolveFixture(t *testing.T, noNetwork bool) (*inputs.VMCreateRequest, *testutil.ConfigRepo) {
	t.Helper()
	dir := t.TempDir()
	kernelPath := filepath.Join(dir, "vmlinux")
	binaryPath := filepath.Join(dir, "firecracker")
	require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0600))
	require.NoError(t, os.WriteFile(binaryPath, []byte("binary"), 0700))
	imageRepo := testutil.NewImageRepo()
	kernelRepo := testutil.NewKernelRepo()
	binaryRepo := testutil.NewBinaryRepo()
	require.NoError(t, imageRepo.Upsert(t.Context(), &model.ImageItem{
		ID: "image", IsDefault: true, IsPresent: true, MinRootfsSizeMiB: 16, FSUUID: "image-fs",
	}))
	require.NoError(t, kernelRepo.Upsert(t.Context(), &model.KernelItem{
		ID: "kernel", Path: kernelPath, IsDefault: true, IsPresent: true,
	}))
	require.NoError(t, binaryRepo.Upsert(t.Context(), &model.BinaryItem{
		ID: "binary", Type: "firecracker", Architecture: system.RuntimeArch(),
		Path: binaryPath, IsDefault: true, IsPresent: true,
	}))
	configRepo := testutil.NewConfigRepo()
	request := inputs.NewVMCreateRequest(
		strings.Repeat("a", 32), filepath.Join(dir, "vms", strings.Repeat("a", 32)),
		inputs.VMCreateInput{Name: "offline-qa", NoNetwork: noNetwork},
		config.NewService(configRepo, config.NewConstraintRegistry()),
		testutil.NewVMRepo(), testutil.NewNetworkRepo(), imageRepo, kernelRepo, binaryRepo,
		testutil.NewKeyRepo(), testutil.NewVolumeRepo(), testutil.NewLeaseRepo(),
	)
	return request, configRepo
}

// Rationale: explicit absence bypasses default-network lookup while omission
// must retain the managed default requirement; neither may fabricate an endpoint.
func TestVMCreateRequestResolveNetworklessWithoutDefaultNetwork(t *testing.T) {
	request, _ := networklessResolveFixture(t, true)
	resolved, err := request.Resolve(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.VMNetworkPlacementNetworkless, resolved.NetworkPlacement)
	assert.Nil(t, resolved.Network)
	assert.Zero(t, resolved.NetworkPrefixLen)
	assert.Empty(t, resolved.NetworkNetmask)
	assert.True(t, resolved.SkipCINetworkConfig)

	managed, _ := networklessResolveFixture(t, false)
	_, err = managed.Resolve(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMNetworkNotFound, errs.AsDomainError(err).Code)
}

func TestVMCreateRequestRejectsNetworkBootArgumentFromDefault(t *testing.T) {
	request, configRepo := networklessResolveFixture(t, true)
	require.NoError(t, configRepo.Set(t.Context(), "defaults.vm", "boot_args", "console=ttyS0 ip=dhcp"))
	resolved, err := request.Resolve(t.Context())
	require.Error(t, err)
	assert.Nil(t, resolved)
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	assert.ErrorContains(t, err, "from input or defaults")
}
