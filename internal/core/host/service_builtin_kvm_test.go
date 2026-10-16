package host_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/core/host"
)

// Rationale: Built-in KVM must satisfy the real setup boundary even when the kernel
// has no /proc/modules. Qualify this case as root on the explicit no-MODULES runner;
// a skip on an ordinary CI kernel is not RED/GREEN evidence for this regression.
func TestEnsureKVMModulesWithBuiltInKVM(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires the approved root-owned no-MODULES KVM qualification lane")
	}
	_, moduleReadErr := os.ReadFile("/proc/modules")
	if moduleReadErr == nil {
		t.Skip("requires an actual kernel without loadable-module inventory")
	}
	require.ErrorIs(t, moduleReadErr, os.ErrNotExist)

	// Independent device admission prevents a synthetic/missing node from turning
	// the owning-boundary assertion into an environment skip or a false positive.
	fd, err := unix.Open("/dev/kvm", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unix.Close(fd)) })
	var device unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &device))
	require.Equal(t, uint32(unix.S_IFCHR), device.Mode&unix.S_IFMT)
	// Linux UAPI linux/kvm.h: KVM_GET_API_VERSION = _IO(0xAE, 0), version = 12.
	const kvmGetAPIVersion = 0xAE00
	apiVersion, err := unix.IoctlRetInt(fd, kvmGetAPIVersion)
	require.NoError(t, err)
	require.Equal(t, 12, apiVersion)
	t.Logf("admitted real moduleless KVM: modules_error=%v device=%d:%d api_version=%d euid=%d",
		moduleReadErr, unix.Major(uint64(device.Rdev)), unix.Minor(uint64(device.Rdev)), apiVersion, os.Geteuid())

	service := host.NewService(nil)
	const initialOrder = 19
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	changes, nextOrder, err := service.EnsureKVMModules(cancelled, "", initialOrder)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, changes)
	assert.Equal(t, initialOrder, nextOrder)

	changes, nextOrder, err = service.EnsureKVMModules(t.Context(), "", initialOrder)
	require.NoError(t, err)
	assert.Empty(t, changes)
	assert.Equal(t, initialOrder, nextOrder)
}
