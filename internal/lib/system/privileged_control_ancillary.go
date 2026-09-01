package system

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const privilegedControlVMExecPollInterval = 100 * time.Millisecond

type privilegedControlVMExecConnectionSender struct {
	owner *privilegedControlVMExecConnectionOwner
	deps  privilegedControlVMExecAncillaryDeps
}

type privilegedControlVMExecConnectionOwner struct {
	// mu guards single-use transmission and pointer-shared close-once ownership.
	mu       sync.Mutex
	file     *os.File
	used     bool
	closed   bool
	closeErr error
}

type privilegedControlVMExecAncillaryDeps struct {
	sendmsg     func(context.Context, int, []byte, []byte, int) (int, error)
	poll        func(context.Context, []unix.PollFd, int) (int, error)
	socketError func(context.Context, int) (int, error)
}

func newPrivilegedControlVMExecConnectionSender(
	connection *os.File,
) (*privilegedControlVMExecConnectionSender, error) {
	return newPrivilegedControlVMExecConnectionSenderWithDeps(
		connection,
		realPrivilegedControlVMExecAncillaryDeps(),
	)
}

func newPrivilegedControlVMExecConnectionSenderWithDeps(
	connection *os.File,
	deps privilegedControlVMExecAncillaryDeps,
) (*privilegedControlVMExecConnectionSender, error) {
	if connection == nil {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection duplicate is required")
	}
	fd := connection.Fd()
	if fd == ^uintptr(0) || fd > uintptr(^uint(0)>>1) {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection duplicate is invalid")
	}
	return &privilegedControlVMExecConnectionSender{
		owner: &privilegedControlVMExecConnectionOwner{file: connection},
		deps:  deps,
	}, nil
}

// CRITICAL: A positive carrier-byte count means the kernel consumed the one
// SCM_RIGHTS message. Retrying it could install a second descriptor.
func (sender *privilegedControlVMExecConnectionSender) send(
	ctx context.Context,
	control *os.File,
	frame []byte,
) (returnErr error) {
	if sender == nil || sender.owner == nil {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection sender is required")
	}
	defer func() {
		returnErr = appendPrivilegedControlVMExecCloseError(returnErr, sender.owner.close())
	}()

	connectionFD, err := sender.owner.beginSend()
	if err != nil {
		return err
	}
	if ctx == nil {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection send context is required")
	}
	if err := ctx.Err(); err != nil {
		return privilegedControlVMExecSystemError("send VM-exec connection duplicate", err)
	}
	if control == nil || control.Fd() == ^uintptr(0) {
		return errs.New(errs.CodeValidationFailed, "VM-exec control connection is required")
	}
	if len(frame) == 0 || frame[0] == 0 {
		return errs.New(errs.CodeValidationFailed, "VM-exec ancillary frame requires a non-NUL carrier byte")
	}

	controlFD := int(control.Fd())
	rights := unix.UnixRights(connectionFD)
	for {
		count, sendErr := sender.deps.sendmsg(
			ctx,
			controlFD,
			frame[:1],
			rights,
			unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL,
		)
		if count > 0 {
			if count != 1 {
				return errs.New(
					errs.CodeProcessError,
					"VM-exec ancillary carrier write returned an invalid byte count",
					errs.WithClass(errs.ClassInternal),
				)
			}
			if sendErr != nil {
				return privilegedControlVMExecSystemError("send VM-exec ancillary carrier", sendErr)
			}
			break
		}
		if sendErr == nil {
			return privilegedControlVMExecSystemError("send VM-exec ancillary carrier", io.ErrShortWrite)
		}
		if errors.Is(sendErr, unix.EINTR) {
			continue
		}
		if !errors.Is(sendErr, unix.EAGAIN) && !errors.Is(sendErr, unix.EWOULDBLOCK) {
			return privilegedControlVMExecSystemError("send VM-exec ancillary carrier", sendErr)
		}
		if err := waitPrivilegedControlVMExecWritable(ctx, controlFD, sender.deps); err != nil {
			return privilegedControlVMExecSystemError("wait to send VM-exec ancillary carrier", err)
		}
	}

	if err := writePrivilegedControlExact(ctx, control, frame[1:]); err != nil {
		return privilegedControlVMExecSystemError("write VM-exec ancillary frame", err)
	}
	return nil
}

func (sender *privilegedControlVMExecConnectionSender) close() error {
	if sender == nil || sender.owner == nil {
		return nil
	}
	return sender.owner.close()
}

func (owner *privilegedControlVMExecConnectionOwner) beginSend() (int, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.file == nil {
		return -1, errs.New(errs.CodeValidationFailed, "VM-exec connection duplicate is closed")
	}
	if owner.used {
		return -1, errs.New(errs.CodeValidationFailed, "VM-exec connection duplicate was already used")
	}
	owner.used = true
	return int(owner.file.Fd()), nil
}

func (owner *privilegedControlVMExecConnectionOwner) close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return owner.closeErr
	}
	owner.closed = true
	owner.closeErr = owner.file.Close()
	owner.file = nil
	return owner.closeErr
}

func waitPrivilegedControlVMExecWritable(
	ctx context.Context,
	fd int,
	deps privilegedControlVMExecAncillaryDeps,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wait := privilegedControlVMExecPollInterval
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return os.ErrDeadlineExceeded
			}
			if remaining < wait {
				wait = remaining
			}
		}
		timeoutMillis := int((wait + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		count, err := deps.poll(ctx, pollFDs, timeoutMillis)
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
			socketErr, optionErr := deps.socketError(ctx, fd)
			if optionErr != nil {
				return optionErr
			}
			if socketErr != 0 {
				return syscall.Errno(socketErr)
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

func realPrivilegedControlVMExecAncillaryDeps() privilegedControlVMExecAncillaryDeps {
	return privilegedControlVMExecAncillaryDeps{
		sendmsg: func(_ context.Context, fd int, payload, control []byte, flags int) (int, error) {
			return unix.SendmsgN(fd, payload, control, nil, flags)
		},
		poll: func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
			return unix.Poll(pollFDs, timeoutMillis)
		},
		socketError: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		},
	}
}

func privilegedControlVMExecSystemError(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeProcessError, message, cause, errs.WithClass(errs.ClassInternal))
}

func appendPrivilegedControlVMExecCloseError(primary error, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	if primary == nil {
		return errs.WrapMsg(
			errs.CodeProcessError,
			"close VM-exec connection duplicate",
			closeErr,
			errs.WithClass(errs.ClassInternal),
			errs.WithDetails(map[string]any{
				"cleanup_required":   true,
				"cleanup_diagnostic": "close VM-exec connection duplicate",
			}),
		)
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		return errs.WrapMsg(
			errs.CodeProcessError,
			primary.Error(),
			errors.Join(primary, closeErr),
			errs.WithClass(errs.ClassInternal),
			errs.WithDetails(map[string]any{
				"cleanup_required":   true,
				"cleanup_diagnostic": "close VM-exec connection duplicate",
			}),
		)
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any, 2)
	}
	result.Details["cleanup_required"] = true
	if _, exists := result.Details["cleanup_diagnostic"]; !exists {
		result.Details["cleanup_diagnostic"] = "close VM-exec connection duplicate"
	}
	result.Err = errors.Join(primary, closeErr)
	return &result
}
