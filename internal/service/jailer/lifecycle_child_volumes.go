package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// mountLaunchChildVolumes consumes only the authenticated parent's ordered SCM_RIGHTS pins.
// CRITICAL: No caller path is opened. Each derived target is exclusive inside the fresh private jail.
func mountLaunchChildVolumes(
	ctx context.Context,
	plan launchChildPlan,
	rootFD int,
	descriptors []int,
) (returnErr error) {
	if len(plan.Volumes) == 0 {
		return nil
	}
	if len(descriptors) != len(plan.Volumes) {
		return instanceValidationError("launch child volume descriptor count differs from ordered intent")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Mkdirat(rootFD, "volumes", 0755); err != nil {
		return processAuthorityError(plan.VMID, "create exclusive jailed volume directory", err)
	}
	directory, err := unix.Openat(rootFD, "volumes", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return processAuthorityError(plan.VMID, "pin fresh jailed volume directory", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(directory)) }()
	deps := realManagedFirecrackerConfigDeps()
	for index, volume := range plan.Volumes {
		if err := volume.Validate(); err != nil {
			return err
		}
		fd := descriptors[index]
		before, err := stableManagedVMConfigStat(ctx, deps, fd)
		if err != nil {
			return err
		}
		mode := uint16(0600)
		if volume.ReadOnly {
			mode = 0400
		}
		if before.uid != plan.UID || before.size != volume.ExpectedBytes || before.nlink != 1 ||
			before.mode&unix.S_IFMT != unix.S_IFREG || before.mode&mode != mode || before.mode&07133 != 0 {
			return instanceValidationError("launch child pinned volume has unsafe or changed metadata")
		}
		if err := mountLaunchChildResource(
			ctx,
			fd,
			directory,
			volume.private().basename(),
			volume.ReadOnly,
			false,
		); err != nil {
			return processAuthorityError(plan.VMID, "mount admitted raw volume", err)
		}
		after, err := stableManagedVMConfigStat(ctx, deps, fd)
		if err != nil {
			return err
		}
		if before != after {
			return instanceValidationError("launch child pinned volume changed during mount handoff")
		}
	}
	return nil
}
