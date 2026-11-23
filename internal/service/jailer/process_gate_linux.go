package jailer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/sys/unix"
)

const blockedLaunchGateCancellationPollInterval = 100 * time.Millisecond

func realBlockedLaunchGateDeps() blockedLaunchGateDeps {
	return blockedLaunchGateDeps{
		random: func(ctx context.Context, target []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := io.ReadFull(rand.Reader, target)
			return err
		},
		descriptorFlags: processDescriptorFlags,
		readFull:        readBlockedLaunchGateFull,
		writeFull:       writeBlockedLaunchGateFull,
		shutdown: func(_ context.Context, fd int) error {
			err := unix.Shutdown(fd, unix.SHUT_RDWR)
			if errors.Is(err, unix.ENOTCONN) || errors.Is(err, unix.EINVAL) {
				return nil
			}
			return err
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
	}
}

func readBlockedLaunchGateFull(ctx context.Context, fd int, target []byte) error {
	for len(target) > 0 {
		if err := blockedLaunchGateContextError(ctx); err != nil {
			return err
		}
		count, _, _, _, err := unix.Recvmsg(fd, target, nil, unix.MSG_DONTWAIT)
		if count > 0 {
			target = target[count:]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err == nil {
			if count == 0 {
				return unix.EPIPE
			}
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if err := waitBlockedLaunchGate(ctx, fd, unix.POLLIN); err != nil {
			return err
		}
	}
	return nil
}

func writeBlockedLaunchGateFull(ctx context.Context, fd int, source []byte) error {
	for len(source) > 0 {
		if err := blockedLaunchGateContextError(ctx); err != nil {
			return err
		}
		count, err := unix.SendmsgN(fd, source, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if count > 0 {
			source = source[count:]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err == nil {
			if count == 0 {
				return unix.EPIPE
			}
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if err := waitBlockedLaunchGate(ctx, fd, unix.POLLOUT); err != nil {
			return err
		}
	}
	return nil
}

func waitBlockedLaunchGate(ctx context.Context, fd int, event int16) error {
	for {
		if err := blockedLaunchGateContextError(ctx); err != nil {
			return err
		}
		timeout, err := blockedLaunchGatePollTimeout(ctx)
		if err != nil {
			return err
		}
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: event}}
		count, err := unix.Poll(pollFDs, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		revents := pollFDs[0].Revents
		if revents&unix.POLLNVAL != 0 {
			return unix.EBADF
		}
		if revents&unix.POLLERR != 0 {
			return unix.EIO
		}
		if revents&(unix.POLLHUP|unix.POLLRDHUP) != 0 {
			return unix.EPIPE
		}
		if revents&event != 0 {
			return nil
		}
		return unix.EIO
	}
}

func blockedLaunchGateContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("blocked launch gate context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func blockedLaunchGatePollTimeout(ctx context.Context) (int, error) {
	wait := blockedLaunchGateCancellationPollInterval
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, context.DeadlineExceeded
		}
		if remaining < wait {
			wait = remaining
		}
	}
	return int((wait + time.Millisecond - 1) / time.Millisecond), nil
}
