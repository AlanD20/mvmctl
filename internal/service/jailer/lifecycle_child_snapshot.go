package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// The authenticated root producer sends exactly two immutable capture FDs.
// 0444 grants the isolated VMM read access only through its private mount tree;
// the host capture directory remains root-only and no writable aliases exist.
func mountLaunchChildSnapshot(
	ctx context.Context,
	rootFD, configFD, stateFD, memoryFD, stateMountFD, memoryMountFD int,
) (returnErr error) {
	encoded, err := readSealedLaunchDeviceConfig(ctx, configFD)
	if err != nil {
		return err
	}
	config, err := decodeManagedFirecrackerConfig(encoded)
	if err != nil {
		return err
	}
	if config.MachineConfig.MemSizeMiB <= 0 || uint64(config.MachineConfig.MemSizeMiB) > ^uint64(0)>>20 {
		return instanceValidationError("snapshot captured RAM is invalid")
	}
	memoryBytes := uint64(config.MachineConfig.MemSizeMiB) << 20
	if err := unix.Mkdirat(rootFD, "snapshot", 0755); err != nil {
		return err
	}
	directory, err := unix.Openat2(
		rootFD,
		"snapshot",
		&unix.OpenHow{Flags: instanceDirectoryFlags, Resolve: managedVMResourceResolveFlags},
	)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	for _, source := range []struct {
		name    string
		fd      int
		mountFD int
	}{{"vmstate", stateFD, stateMountFD}, {"memory", memoryFD, memoryMountFD}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), source.fd)
		if err != nil {
			return err
		}
		if before.mode != unix.S_IFREG|0444 || before.uid != 0 || before.gid != 0 || before.nlink != 1 ||
			before.size == 0 {
			return instanceValidationError("snapshot artifact is not immutable root-owned data")
		}
		if source.name == "vmstate" && before.size > 10000000 {
			return instanceValidationError("snapshot vmstate exceeds the admitted complete-file bound")
		}
		if source.name == "memory" && before.size != memoryBytes {
			return instanceValidationError("snapshot memory length differs from captured RAM")
		}
		if err := mountLaunchChildResource(ctx, source.mountFD, directory, source.name, false, false); err != nil {
			return err
		}
		after, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), source.fd)
		if err != nil {
			return err
		}
		if before != after {
			return instanceValidationError("snapshot artifact changed during mount transfer")
		}
	}
	return nil
}
