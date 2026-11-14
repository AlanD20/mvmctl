package loopmount

import (
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestNewRootfsLoopConfig(t *testing.T) {
	t.Parallel()

	type testCase struct {
		rootfsFD int
		access   rootfsAccess
		want     unix.LoopConfig
		wantErr  string
	}
	tests := map[string]testCase{
		"read only": {
			rootfsFD: 41,
			access:   rootfsReadOnly,
			want: unix.LoopConfig{
				Fd: 41,
				Info: unix.LoopInfo64{
					Flags: unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN | unix.LO_FLAGS_READ_ONLY,
				},
			},
		},
		"read write": {
			rootfsFD: 42,
			access:   rootfsReadWrite,
			want: unix.LoopConfig{
				Fd: 42,
				Info: unix.LoopInfo64{
					Flags: unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN,
				},
			},
		},
		"zero descriptor": {
			rootfsFD: 0,
			access:   rootfsReadOnly,
			want: unix.LoopConfig{
				Info: unix.LoopInfo64{
					Flags: unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN | unix.LO_FLAGS_READ_ONLY,
				},
			},
		},
		"negative descriptor": {
			rootfsFD: -1, access: rootfsReadOnly, wantErr: "descriptor is invalid",
		},
		"invalid access": {
			rootfsFD: 41, wantErr: "access is invalid",
		},
	}
	if strconv.IntSize > 32 {
		aboveLoopABI := uint64(^uint32(0)) + 1
		tests["descriptor exceeds loop ABI"] = testCase{
			rootfsFD: int(aboveLoopABI),
			access:   rootfsReadOnly,
			wantErr:  "descriptor exceeds",
		}
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := newRootfsLoopConfig(test.rootfsFD, test.access)
			if test.wantErr != "" {
				requireRootfsLoopContractError(t, err, test.wantErr)
				assert.Equal(t, unix.LoopConfig{}, got)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Errorf("newRootfsLoopConfig() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRootfsLoopDevicePath(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		index   int
		want    string
		wantErr string
	}{
		"zero":   {index: 0, want: "/dev/loop0"},
		"middle": {index: 512, want: "/dev/loop512"},
		"maximum": {
			index: maxRootfsLoopDeviceIndex, want: "/dev/loop1048575",
		},
		"negative":      {index: -1, wantErr: "index is outside"},
		"above maximum": {index: maxRootfsLoopDeviceIndex + 1, wantErr: "index is outside"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := rootfsLoopDevicePath(test.index)
			if test.wantErr != "" {
				requireRootfsLoopContractError(t, err, test.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestValidateRootfsLoopControl(t *testing.T) {
	t.Parallel()

	valid := unix.Stat_t{Mode: unix.S_IFCHR | 0o600, Rdev: unix.Mkdev(10, 237)}
	tests := map[string]struct {
		stat    unix.Stat_t
		wantErr string
	}{
		"exact control device": {stat: valid},
		"regular file": {
			stat: unix.Stat_t{Mode: unix.S_IFREG | 0o600, Rdev: valid.Rdev}, wantErr: "not the loop-control device",
		},
		"wrong character major": {
			stat:    unix.Stat_t{Mode: valid.Mode, Rdev: unix.Mkdev(11, 237)},
			wantErr: "not the loop-control device",
		},
		"wrong character minor": {
			stat:    unix.Stat_t{Mode: valid.Mode, Rdev: unix.Mkdev(10, 236)},
			wantErr: "not the loop-control device",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRootfsLoopControl(test.stat)
			if test.wantErr != "" {
				requireRootfsLoopContractError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateRootfsLoopDevice(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		index   int
		stat    unix.Stat_t
		want    rootfsBlockIdentity
		wantErr string
	}{
		"loop zero": {
			index: 0,
			stat:  unix.Stat_t{Mode: unix.S_IFBLK | 0o600, Rdev: unix.Mkdev(7, 0)},
			want:  rootfsBlockIdentity{major: 7, minor: 0},
		},
		"loop at maximum index": {
			index: maxRootfsLoopDeviceIndex,
			stat: unix.Stat_t{
				Mode: unix.S_IFBLK | 0o600,
				Rdev: unix.Mkdev(7, maxRootfsLoopDeviceIndex),
			},
			want: rootfsBlockIdentity{major: 7, minor: maxRootfsLoopDeviceIndex},
		},
		"negative index": {
			index: -1, wantErr: "index is outside",
		},
		"index above maximum": {
			index: maxRootfsLoopDeviceIndex + 1, wantErr: "index is outside",
		},
		"regular file": {
			index:   3,
			stat:    unix.Stat_t{Mode: unix.S_IFREG | 0o600, Rdev: unix.Mkdev(7, 3)},
			wantErr: "not the requested loop block device",
		},
		"wrong block major": {
			index:   3,
			stat:    unix.Stat_t{Mode: unix.S_IFBLK | 0o600, Rdev: unix.Mkdev(8, 3)},
			wantErr: "not the requested loop block device",
		},
		"wrong block minor": {
			index:   3,
			stat:    unix.Stat_t{Mode: unix.S_IFBLK | 0o600, Rdev: unix.Mkdev(7, 4)},
			wantErr: "not the requested loop block device",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := validateRootfsLoopDevice(test.index, test.stat)
			if test.wantErr != "" {
				requireRootfsLoopContractError(t, err, test.wantErr)
				assert.Equal(t, rootfsBlockIdentity{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func requireRootfsLoopContractError(t *testing.T, err error, message string) {
	t.Helper()

	require.Error(t, err)
	assert.ErrorContains(t, err, message)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
}
