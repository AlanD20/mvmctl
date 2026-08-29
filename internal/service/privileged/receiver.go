package privileged

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

const responseWriteTimeout = 5 * time.Second

type controlEndpoint interface {
	io.Reader
	io.Writer
	Fd() uintptr
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type stdinControlEndpoint struct {
	file          *os.File
	fd            uintptr
	readDeadline  time.Time
	writeDeadline time.Time
}

func newStdinControlEndpoint(file *os.File) controlEndpoint {
	return &stdinControlEndpoint{file: file, fd: file.Fd()}
}

func (endpoint *stdinControlEndpoint) Read(target []byte) (int, error) {
	if !endpoint.readDeadline.IsZero() {
		if err := setSocketDeadline(endpoint.fd, unix.SO_RCVTIMEO, endpoint.readDeadline); err != nil {
			return 0, err
		}
	}
	return endpoint.file.Read(target)
}

func (endpoint *stdinControlEndpoint) Write(value []byte) (int, error) {
	if endpoint.writeDeadline.IsZero() {
		return 0, os.ErrNoDeadline
	}
	return sendResponseUntil(endpoint.fd, value, endpoint.writeDeadline)
}

func (endpoint *stdinControlEndpoint) Fd() uintptr {
	return endpoint.fd
}

func (endpoint *stdinControlEndpoint) SetReadDeadline(deadline time.Time) error {
	if err := setSocketDeadline(endpoint.fd, unix.SO_RCVTIMEO, deadline); err != nil {
		return err
	}
	endpoint.readDeadline = deadline
	return nil
}

func (endpoint *stdinControlEndpoint) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() || !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	endpoint.writeDeadline = deadline
	return nil
}

type controlDeps struct {
	descriptorFlags       func(context.Context, uintptr) (int, error)
	setDescriptorFlags    func(context.Context, uintptr, int) error
	descriptorStatusFlags func(context.Context, uintptr) (int, error)
	socketDomain          func(context.Context, int) (int, error)
	socketType            func(context.Context, int) (int, error)
	peerCredentials       func(context.Context, int) (*unix.Ucred, error)
	shutdownWrite         func(context.Context, int) error
	now                   func() time.Time
}

func admitControlEndpoint(
	ctx context.Context,
	endpoint controlEndpoint,
	deps controlDeps,
) (unix.Ucred, error) {
	if endpoint == nil {
		return unix.Ucred{}, errs.New(errs.CodeValidationFailed, "privileged control endpoint is required")
	}
	fd := endpoint.Fd()
	if fd != 0 {
		return unix.Ucred{}, errs.New(
			errs.CodeValidationFailed,
			"privileged control endpoint must be file descriptor 0",
		)
	}
	flags, err := deps.descriptorFlags(ctx, fd)
	if err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("read privileged control descriptor flags", err)
	}
	if err := deps.setDescriptorFlags(ctx, fd, flags|unix.FD_CLOEXEC); err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("set privileged control descriptor close-on-exec", err)
	}
	verifiedFlags, err := deps.descriptorFlags(ctx, fd)
	if err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("verify privileged control descriptor flags", err)
	}
	if verifiedFlags&unix.FD_CLOEXEC == 0 {
		return unix.Ucred{}, errs.New(
			errs.CodeProcessError,
			"privileged control descriptor close-on-exec verification failed",
			errs.WithClass(errs.ClassInternal),
		)
	}
	statusFlags, err := deps.descriptorStatusFlags(ctx, fd)
	if err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("read privileged control descriptor status flags", err)
	}
	if statusFlags&unix.O_NONBLOCK != 0 {
		return unix.Ucred{}, errs.New(
			errs.CodeValidationFailed,
			"privileged control endpoint must use blocking socket semantics",
		)
	}
	domain, err := deps.socketDomain(ctx, int(fd))
	if err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("read privileged control socket domain", err)
	}
	if domain != unix.AF_UNIX {
		return unix.Ucred{}, errs.New(
			errs.CodeValidationFailed,
			"privileged control endpoint must be an AF_UNIX socket",
		)
	}
	socketType, err := deps.socketType(ctx, int(fd))
	if err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("read privileged control socket type", err)
	}
	if socketType != unix.SOCK_STREAM {
		return unix.Ucred{}, errs.New(
			errs.CodeValidationFailed,
			"privileged control endpoint must be a SOCK_STREAM socket",
		)
	}
	peer, err := deps.peerCredentials(ctx, int(fd))
	if err != nil {
		return unix.Ucred{}, controlAdmissionSystemError("read privileged control peer credentials", err)
	}
	if peer == nil {
		return unix.Ucred{}, errs.New(
			errs.CodeProcessError,
			"privileged control peer credentials are unavailable",
			errs.WithClass(errs.ClassInternal),
		)
	}
	if peer.Pid <= 0 {
		return unix.Ucred{}, errs.New(
			errs.CodeValidationFailed,
			"privileged control peer PID must be positive",
		)
	}
	return *peer, nil
}

func authenticateControlPeer(caller callerIdentity, peer unix.Ucred) error {
	if peer.Uid == caller.uid && peer.Gid == caller.gid {
		return nil
	}
	return errs.New(
		errs.CodePrivilegeRequired,
		fmt.Sprintf(
			"privileged control peer UID/GID does not match authenticated sudo caller %d:%d",
			caller.uid,
			caller.gid,
		),
	)
}

func deliverOperationError(
	ctx context.Context,
	endpoint controlEndpoint,
	action string,
	operationErr error,
	deadline time.Time,
	deps controlDeps,
) error {
	finalContext, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	if err := endpoint.SetWriteDeadline(deadline); err != nil {
		return responseTransportError(operationErr, err, false)
	}
	writer := &responseStartWriter{writer: endpoint}
	if err := privilegedwire.EncodeError(finalContext, writer, action, operationErr); err != nil {
		return responseTransportError(operationErr, err, writer.started)
	}
	if err := deps.shutdownWrite(finalContext, int(endpoint.Fd())); err != nil {
		return responseTransportError(operationErr, err, true)
	}
	return nil
}

type responseStartWriter struct {
	writer  io.Writer
	started bool
}

func (writer *responseStartWriter) Write(value []byte) (int, error) {
	writer.started = true
	return writer.writer.Write(value)
}

func boundedResponseDeadline(ctx context.Context, now time.Time) time.Time {
	deadline := now.Add(responseWriteTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func responseTransportError(operationErr, transportErr error, responseStarted bool) error {
	return errs.WrapMsg(
		errs.CodeProcessError,
		"deliver privileged error response",
		errors.Join(operationErr, transportErr),
		errs.WithClass(errs.ClassInternal),
		errs.WithDetails(map[string]any{"response_started": responseStarted}),
	)
}

func controlAdmissionSystemError(message string, cause error) error {
	return errs.WrapMsg(
		errs.CodeProcessError,
		message,
		cause,
		errs.WithClass(errs.ClassInternal),
	)
}

func setSocketDeadline(fd uintptr, option int, deadline time.Time) error {
	// CRITICAL: Calling os.File.Fd disables the file's runtime-poller read deadlines.
	// SO_RCVTIMEO preserves the absolute read bound while borrowing fd 0 without closing it.
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return os.ErrDeadlineExceeded
	}
	timeout := unix.NsecToTimeval(remaining.Nanoseconds())
	if timeout.Sec == 0 && timeout.Usec == 0 {
		timeout.Usec = 1
	}
	return unix.SetsockoptTimeval(int(fd), unix.SOL_SOCKET, option, &timeout)
}

func sendResponseUntil(fd uintptr, value []byte, deadline time.Time) (int, error) {
	written := 0
	for written < len(value) {
		if !time.Now().Before(deadline) {
			return written, os.ErrDeadlineExceeded
		}
		count, err := unix.SendmsgN(
			int(fd),
			value[written:],
			nil,
			nil,
			unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL,
		)
		if count > 0 {
			written += count
		}
		if err == nil && count > 0 {
			continue
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return written, err
		}
		if err := waitResponseWritable(int(fd), deadline); err != nil {
			return written, err
		}
	}
	return written, nil
}

func waitResponseWritable(fd int, deadline time.Time) error {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return os.ErrDeadlineExceeded
		}
		timeoutMillis := int((remaining + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		count, err := unix.Poll(pollFDs, timeoutMillis)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 0 {
			if time.Now().Before(deadline) {
				continue
			}
			return os.ErrDeadlineExceeded
		}
		revents := pollFDs[0].Revents
		if revents&unix.POLLNVAL != 0 {
			return unix.EBADF
		}
		if revents&unix.POLLERR != 0 {
			socketError, socketErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
			if socketErr != nil {
				return socketErr
			}
			if socketError != 0 {
				return syscall.Errno(socketError)
			}
			return unix.EPIPE
		}
		if revents&(unix.POLLHUP|unix.POLLRDHUP) != 0 {
			return unix.EPIPE
		}
		if revents&unix.POLLOUT != 0 {
			return nil
		}
		return unix.EIO
	}
}

func realControlDeps() controlDeps {
	return controlDeps{
		descriptorFlags: func(_ context.Context, fd uintptr) (int, error) {
			return unix.FcntlInt(fd, unix.F_GETFD, 0)
		},
		setDescriptorFlags: func(_ context.Context, fd uintptr, flags int) error {
			_, err := unix.FcntlInt(fd, unix.F_SETFD, flags)
			return err
		},
		descriptorStatusFlags: func(_ context.Context, fd uintptr) (int, error) {
			return unix.FcntlInt(fd, unix.F_GETFL, 0)
		},
		socketDomain: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
		},
		socketType: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		},
		peerCredentials: func(_ context.Context, fd int) (*unix.Ucred, error) {
			return unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		},
		shutdownWrite: func(ctx context.Context, fd int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Shutdown(fd, unix.SHUT_WR)
		},
		now: time.Now,
	}
}
