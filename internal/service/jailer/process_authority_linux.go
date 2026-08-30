package jailer

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

func realBlockedLaunchProcessDeps() blockedLaunchProcessDeps {
	return blockedLaunchProcessDeps{
		pidfdAlive:      processPIDFDAlive,
		descriptorFlags: processDescriptorFlags,
		pinnedExecutableIdentity: func(
			ctx context.Context,
			executable *system.PinnedExecutable,
		) (system.PinnedExecutableIdentity, error) {
			return executable.Identity(ctx)
		},
		releasePinnedExecutable: func(ctx context.Context, executable *system.PinnedExecutable) error {
			return executable.Release(ctx)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}

func processPIDFDAlive(ctx context.Context, pidfd int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	pollFDs := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
	count, err := unix.Poll(pollFDs, 0)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return true, nil
	}
	if pollFDs[0].Revents&unix.POLLNVAL != 0 {
		return false, fmt.Errorf("pidfd is invalid")
	}
	if pollFDs[0].Revents == 0 {
		return false, fmt.Errorf("pidfd poll returned an event count without flags")
	}
	return false, nil
}

func processDescriptorFlags(ctx context.Context, fd int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
}
