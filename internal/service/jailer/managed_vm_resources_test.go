package jailer

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}
