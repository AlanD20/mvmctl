package jailer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

func realBlockedLaunchProcessDeps() blockedLaunchProcessDeps {
	return blockedLaunchProcessDeps{
		pidfdAlive:      processPIDFDAlive,
		descriptorFlags: processDescriptorFlags,
		deriveLocator:   deriveProcessLocatorFromPIDFD,
		pinnedExecutableReinspect: func(
			ctx context.Context,
			executable *system.PinnedExecutable,
		) (system.PinnedExecutableIdentity, error) {
			return executable.Reinspect(ctx)
		},
		releasePinnedExecutable: func(ctx context.Context, executable *system.PinnedExecutable) error {
			return executable.Release(ctx)
		},
		terminatePIDFD: terminateProcessThroughPIDFD,
		waitReapPIDFD:  waitAndReapProcessThroughPIDFD,
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}

func terminateProcessThroughPIDFD(ctx context.Context, pidfd int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func waitAndReapProcessThroughPIDFD(ctx context.Context, pidfd int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		alive, err := processPIDFDAlive(ctx, pidfd)
		if err != nil {
			return err
		}
		if !alive {
			break
		}
		timer := time.NewTimer(processExecObservationInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	err := unix.Waitid(unix.P_PIDFD, pidfd, nil, unix.WEXITED|unix.WNOHANG, nil)
	if errors.Is(err, unix.ECHILD) {
		return nil
	}
	return err
}

func deriveProcessLocatorFromPIDFD(ctx context.Context, pidfd int) (_ instanceProcessLocator, returnErr error) {
	if err := ctx.Err(); err != nil {
		return instanceProcessLocator{}, err
	}
	pid, err := readPIDFDProcessID(ctx, pidfd)
	if err != nil {
		return instanceProcessLocator{}, err
	}
	processFD, err := openFixedProcessDirectory(ctx, pid)
	if err != nil {
		return instanceProcessLocator{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeProcessDescriptor(context.WithoutCancel(ctx), processFD))
	}()
	statRaw, err := readFixedProcessMetadata(ctx, processFD, processStatMetadata, maxProcessStatBytes)
	if err != nil {
		return instanceProcessLocator{}, err
	}
	startTicks, err := parseProcessStat(statRaw, pid)
	if err != nil {
		return instanceProcessLocator{}, err
	}
	finalPID, err := readPIDFDProcessID(ctx, pidfd)
	if err != nil {
		return instanceProcessLocator{}, err
	}
	if finalPID != pid {
		return instanceProcessLocator{}, fmt.Errorf("pidfd process identity changed during locator derivation")
	}
	alive, err := processPIDFDAlive(ctx, pidfd)
	if err != nil {
		return instanceProcessLocator{}, err
	}
	if !alive {
		return instanceProcessLocator{}, fmt.Errorf("pidfd process exited during locator derivation")
	}
	return instanceProcessLocator{pid: pid, startTimeTicks: startTicks}, nil
}

func readPIDFDProcessID(ctx context.Context, pidfd int) (_ int, returnErr error) {
	if pidfd < 0 {
		return 0, fmt.Errorf("pidfd is invalid")
	}
	path := "/proc/self/fdinfo/" + strconv.Itoa(pidfd)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	raw, err := readBoundedProcessDescriptor(ctx, fd, maxProcessPIDFDInfoBytes)
	if err != nil {
		return 0, err
	}
	return parsePIDFDProcessID(raw)
}

func parsePIDFDProcessID(raw []byte) (int, error) {
	if len(raw) == 0 || len(raw) > maxProcessPIDFDInfoBytes || raw[len(raw)-1] != '\n' {
		return 0, fmt.Errorf("pidfd info is not a bounded complete record")
	}
	pid := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if !strings.HasPrefix(line, "Pid:\t") {
			continue
		}
		if pid != 0 {
			return 0, fmt.Errorf("pidfd info contains duplicate process IDs")
		}
		value := strings.TrimPrefix(line, "Pid:\t")
		if value == "" || (len(value) > 1 && value[0] == '0') {
			return 0, fmt.Errorf("pidfd info process ID is not canonical")
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("pidfd info process ID is invalid")
		}
		pid = parsed
	}
	if pid == 0 {
		return 0, fmt.Errorf("pidfd info process ID is absent")
	}
	return pid, nil
}

func processPIDFDAlive(ctx context.Context, pidfd int) (bool, error) {
	return processPIDFDAliveWithPoll(ctx, pidfd, unix.Poll)
}

func processPIDFDAliveWithPoll(
	ctx context.Context,
	pidfd int,
	poll func([]unix.PollFd, int) (int, error),
) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("pidfd poll context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	pollFDs := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	for {
		count, err := poll(pollFDs, 0)
		if errors.Is(err, unix.EINTR) {
			if contextErr := ctx.Err(); contextErr != nil {
				return false, contextErr
			}
			pollFDs[0].Revents = 0
			continue
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			if pollFDs[0].Revents != 0 {
				return false, fmt.Errorf("pidfd poll returned flags without an event count")
			}
			return true, nil
		}
		break
	}
	if pollFDs[0].Revents&unix.POLLNVAL != 0 {
		return false, fmt.Errorf("pidfd is invalid")
	}
	if pollFDs[0].Revents&unix.POLLERR != 0 {
		return false, fmt.Errorf("pidfd poll error")
	}
	if pollFDs[0].Revents == 0 {
		return false, fmt.Errorf("pidfd poll returned an event count without flags")
	}
	if pollFDs[0].Revents != unix.POLLIN {
		return false, fmt.Errorf("pidfd poll returned unexpected events %#x", pollFDs[0].Revents)
	}
	return false, nil
}

func processDescriptorFlags(ctx context.Context, fd int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
}
