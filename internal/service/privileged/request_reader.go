package privileged

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const requestCancellationPollInterval = 100 * time.Millisecond

type requestReader struct {
	ctx      context.Context
	fd       int
	deadline time.Time
	deps     controlDeps
}

func newRequestReader(ctx context.Context, fd int, deadline time.Time, deps controlDeps) io.Reader {
	return &requestReader{ctx: ctx, fd: fd, deadline: deadline, deps: deps}
}

func (reader *requestReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	for {
		if err := reader.ctx.Err(); err != nil {
			return 0, err
		}
		if !time.Now().Before(reader.deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		count, err := reader.deps.recvmsg(reader.ctx, reader.fd, target, unix.MSG_DONTWAIT)
		if count > 0 {
			return count, nil
		}
		if err == nil {
			return 0, io.EOF
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return 0, err
		}
		if err := reader.waitReadable(); err != nil {
			return 0, err
		}
	}
}

func (reader *requestReader) waitReadable() error {
	for {
		if err := reader.ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(reader.deadline)
		if remaining <= 0 {
			return os.ErrDeadlineExceeded
		}
		wait := remaining
		if reader.ctx.Done() != nil && wait > requestCancellationPollInterval {
			wait = requestCancellationPollInterval
		}
		timeoutMillis := int((wait + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(reader.fd), Events: unix.POLLIN}}
		count, err := reader.deps.poll(reader.ctx, pollFDs, timeoutMillis)
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
		if revents&(unix.POLLIN|unix.POLLHUP|unix.POLLRDHUP) != 0 {
			return nil
		}
		return unix.EIO
	}
}
