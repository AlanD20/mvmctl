package loopmount

import (
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	rootfsLoopControlPath    = "/dev/loop-control"
	rootfsLoopControlMajor   = 10
	rootfsLoopControlMinor   = 237
	rootfsLoopBlockMajor     = 7
	rootfsLoopDevicePrefix   = "/dev/loop"
	maxRootfsLoopDeviceIndex = (1 << 20) - 1
)

// newRootfsLoopConfig binds only the exact admitted backing descriptor and closed access mode.
// All offsets, size limits, names, encryption fields, block-size overrides, and legacy setup fields remain zero.
func newRootfsLoopConfig(rootfsFD int, access rootfsAccess) (unix.LoopConfig, error) {
	if rootfsFD < 0 {
		return unix.LoopConfig{}, rootfsAtomicError("managed rootfs descriptor is invalid", nil)
	}
	if uint64(rootfsFD) > uint64(^uint32(0)) {
		return unix.LoopConfig{}, rootfsAtomicError("managed rootfs descriptor exceeds the loop ABI", nil)
	}

	flags := uint32(unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN)
	switch access {
	case rootfsReadOnly:
		flags |= unix.LO_FLAGS_READ_ONLY
	case rootfsReadWrite:
	default:
		return unix.LoopConfig{}, rootfsAtomicError("managed rootfs access is invalid", nil)
	}
	return unix.LoopConfig{
		Fd: uint32(rootfsFD),
		Info: unix.LoopInfo64{
			Flags: flags,
		},
	}, nil
}

func rootfsLoopDevicePath(index int) (string, error) {
	if !validRootfsLoopDeviceIndex(index) {
		return "", rootfsAtomicError("loop device index is outside the supported range", nil)
	}
	return rootfsLoopDevicePrefix + strconv.Itoa(index), nil
}

func validateRootfsLoopControl(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR ||
		unix.Major(stat.Rdev) != rootfsLoopControlMajor ||
		unix.Minor(stat.Rdev) != rootfsLoopControlMinor {
		return rootfsAtomicError("opened device is not the loop-control device", nil)
	}
	return nil
}

func validateRootfsLoopDevice(index int, stat unix.Stat_t) (rootfsBlockIdentity, error) {
	if !validRootfsLoopDeviceIndex(index) {
		return rootfsBlockIdentity{}, rootfsAtomicError("loop device index is outside the supported range", nil)
	}
	major := unix.Major(stat.Rdev)
	minor := unix.Minor(stat.Rdev)
	if stat.Mode&unix.S_IFMT != unix.S_IFBLK || major != rootfsLoopBlockMajor || minor != uint32(index) {
		return rootfsBlockIdentity{}, rootfsAtomicError("opened device is not the requested loop block device", nil)
	}
	return rootfsBlockIdentity{major: major, minor: minor}, nil
}

func validRootfsLoopDeviceIndex(index int) bool {
	return index >= 0 && index <= maxRootfsLoopDeviceIndex
}
