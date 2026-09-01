package privileged

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const vmExecConnectionPollInterval = 100 * time.Millisecond

type vmExecConnectionReader struct {
	ctx      context.Context
	fd       int
	deadline time.Time
	deps     vmExecConnectionReaderDeps

	carrierRead  bool
	closed       bool
	terminalErr  error
	closeErr     error
	connection   *vmExecReceivedConnectionOwner
	installed    map[int]*vmExecReceivedConnectionOwner
	installOrder []*vmExecReceivedConnectionOwner
}

type vmExecReceivedConnectionOwner struct {
	// mu guards pointer-shared close-once ownership and one-time transfer.
	mu       sync.Mutex
	fd       int
	closeFD  func(int) error
	closed   bool
	taken    bool
	closeErr error
}

type vmExecConnectionReaderDeps struct {
	recvmsg         func(context.Context, int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error)
	poll            func(context.Context, []unix.PollFd, int) (int, error)
	fstat           func(context.Context, int, *unix.Stat_t) error
	descriptorFlags func(context.Context, uintptr) (int, error)
	socketDomain    func(context.Context, int) (int, error)
	socketType      func(context.Context, int) (int, error)
	accepting       func(context.Context, int) (int, error)
	socketError     func(context.Context, int) (int, error)
	socketName      func(context.Context, int) (unix.Sockaddr, error)
	peerName        func(context.Context, int) (unix.Sockaddr, error)
	closeFD         func(int) error
}

type vmExecConnectionReceive struct {
	payload []byte
	control []byte
	flags   int
	from    unix.Sockaddr
	err     error
}

type vmExecConnectionControl struct {
	messages []unix.SocketControlMessage
	rights   []int
}

func newVMExecConnectionReader(
	ctx context.Context,
	fd int,
	deadline time.Time,
) *vmExecConnectionReader {
	return newVMExecConnectionReaderWithDeps(ctx, fd, deadline, realVMExecConnectionReaderDeps())
}

func newVMExecConnectionReaderWithDeps(
	ctx context.Context,
	fd int,
	deadline time.Time,
	deps vmExecConnectionReaderDeps,
) *vmExecConnectionReader {
	return &vmExecConnectionReader{
		ctx:       ctx,
		fd:        fd,
		deadline:  deadline,
		deps:      deps,
		installed: make(map[int]*vmExecReceivedConnectionOwner),
	}
}

// CRITICAL: Every read remains a recvmsg call so ancillary data cannot be
// hidden behind an already accepted carrier byte.
func (reader *vmExecConnectionReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	if reader == nil {
		return 0, errs.New(errs.CodeValidationFailed, "VM-exec connection reader is required")
	}
	if reader.terminalErr != nil {
		return 0, reader.terminalErr
	}
	if reader.closed {
		return 0, errs.New(errs.CodeValidationFailed, "VM-exec connection reader is closed")
	}

	payloadSize := len(target)
	if !reader.carrierRead {
		payloadSize = 1
	}
	received := reader.receive(payloadSize)
	control, controlErr := reader.ownControl(received.control)
	if received.err != nil {
		return 0, reader.fail(vmExecConnectionSystemError("receive VM-exec connection frame", received.err))
	}
	if received.from != nil {
		return 0, reader.fail(errs.New(
			errs.CodeValidationFailed,
			"VM-exec connection frame must not have a sender address",
		))
	}
	unexpectedFlags := received.flags &^ unix.MSG_CMSG_CLOEXEC
	if unexpectedFlags != 0 {
		message := "VM-exec connection frame has unexpected receive flags"
		if unexpectedFlags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			message = "VM-exec connection frame is truncated"
		}
		return 0, reader.fail(errs.New(errs.CodeValidationFailed, message))
	}
	if controlErr != nil {
		return 0, reader.fail(controlErr)
	}

	if !reader.carrierRead {
		if len(received.payload) != 1 || received.payload[0] == 0 {
			return 0, reader.fail(errs.New(
				errs.CodeValidationFailed,
				"VM-exec connection frame requires one non-NUL carrier byte",
			))
		}
		connection, err := reader.validateInitialControl(control)
		if err != nil {
			return 0, reader.fail(err)
		}
		if err := admitVMExecConnection(reader.ctx, connection.fd, reader.deps); err != nil {
			return 0, reader.fail(err)
		}
		reader.connection = connection
		reader.carrierRead = true
		target[0] = received.payload[0]
		return 1, nil
	}

	if len(received.control) != 0 || len(control.messages) != 0 {
		return 0, reader.fail(errs.New(
			errs.CodeValidationFailed,
			"VM-exec connection frame contains late ancillary data",
		))
	}
	if len(received.payload) == 0 {
		return 0, io.EOF
	}
	return copy(target, received.payload), nil
}

// TakeConnection transfers the admitted descriptor exactly once. The caller
// must finish reading the closed frame before taking it to preserve late-cmsg detection.
func (reader *vmExecConnectionReader) TakeConnection() (*os.File, error) {
	if reader == nil || reader.connection == nil {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection is not admitted")
	}
	if reader.terminalErr != nil {
		return nil, reader.terminalErr
	}
	return reader.connection.take()
}

func (reader *vmExecConnectionReader) Close() error {
	if reader == nil {
		return nil
	}
	if reader.closed {
		return reader.closeErr
	}
	reader.closed = true
	reader.closeErr = reader.closeInstalled(nil)
	return reader.closeErr
}

func (reader *vmExecConnectionReader) receive(payloadSize int) vmExecConnectionReceive {
	if reader.ctx == nil {
		return vmExecConnectionReceive{err: errors.New("VM-exec connection reader context is required")}
	}
	for {
		if err := reader.ctx.Err(); err != nil {
			return vmExecConnectionReceive{err: err}
		}
		if !time.Now().Before(reader.deadline) {
			return vmExecConnectionReceive{err: os.ErrDeadlineExceeded}
		}
		payload := make([]byte, payloadSize)
		control := make([]byte, unix.CmsgSpace(4))
		count, controlCount, flags, from, err := reader.deps.recvmsg(
			reader.ctx,
			reader.fd,
			payload,
			control,
			unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC,
		)
		if count < 0 || count > len(payload) || controlCount < 0 || controlCount > len(control) {
			return vmExecConnectionReceive{err: errors.New("VM-exec recvmsg returned invalid lengths")}
		}
		if count > 0 || controlCount > 0 || flags != 0 || from != nil {
			return vmExecConnectionReceive{
				payload: payload[:count],
				control: control[:controlCount],
				flags:   flags,
				from:    from,
				err:     err,
			}
		}
		if err == nil {
			return vmExecConnectionReceive{}
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return vmExecConnectionReceive{err: err}
		}
		if err := reader.waitReadable(); err != nil {
			return vmExecConnectionReceive{err: err}
		}
	}
}

func (reader *vmExecConnectionReader) waitReadable() error {
	for {
		if err := reader.ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(reader.deadline)
		if remaining <= 0 {
			return os.ErrDeadlineExceeded
		}
		wait := remaining
		if wait > vmExecConnectionPollInterval {
			wait = vmExecConnectionPollInterval
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

func (reader *vmExecConnectionReader) ownControl(
	controlBytes []byte,
) (vmExecConnectionControl, error) {
	result := vmExecConnectionControl{}
	remaining := controlBytes
	for len(remaining) > 0 {
		if len(remaining) < unix.CmsgLen(0) {
			return result, errs.New(errs.CodeValidationFailed, "VM-exec ancillary data has trailing bytes")
		}
		header, data, rest, err := unix.ParseOneSocketControlMessage(remaining)
		if err != nil {
			return result, errs.WrapMsg(
				errs.CodeValidationFailed,
				"parse VM-exec ancillary data",
				err,
				errs.WithClass(errs.ClassValidation),
			)
		}
		message := unix.SocketControlMessage{Header: header, Data: data}
		result.messages = append(result.messages, message)
		if header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS {
			rights, parseErr := unix.ParseUnixRights(&message)
			if parseErr != nil {
				return result, errs.WrapMsg(
					errs.CodeValidationFailed,
					"parse VM-exec descriptor rights",
					parseErr,
					errs.WithClass(errs.ClassValidation),
				)
			}
			result.rights = append(result.rights, rights...)
			for _, fd := range rights {
				if fd >= 0 {
					reader.ownInstalledFD(fd)
				}
			}
		}
		if len(remaining) < unix.CmsgSpace(len(data)) {
			return result, errs.New(errs.CodeValidationFailed, "VM-exec ancillary data is truncated")
		}
		remaining = rest
	}
	return result, nil
}

func (reader *vmExecConnectionReader) ownInstalledFD(fd int) *vmExecReceivedConnectionOwner {
	if owner, exists := reader.installed[fd]; exists {
		return owner
	}
	owner := &vmExecReceivedConnectionOwner{fd: fd, closeFD: reader.deps.closeFD}
	reader.installed[fd] = owner
	reader.installOrder = append(reader.installOrder, owner)
	return owner
}

func (reader *vmExecConnectionReader) validateInitialControl(
	control vmExecConnectionControl,
) (*vmExecReceivedConnectionOwner, error) {
	if len(control.messages) == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor is missing")
	}
	if len(control.messages) != 1 {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection frame requires exactly one control message")
	}
	message := control.messages[0]
	if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection frame has an unknown control message")
	}
	if int(message.Header.Len) != unix.CmsgLen(4) || len(message.Data) != 4 {
		return nil, errs.New(
			errs.CodeValidationFailed,
			"VM-exec descriptor rights must contain exactly four data bytes",
		)
	}
	if len(control.rights) != 1 || control.rights[0] < 0 {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec descriptor rights must contain exactly one descriptor")
	}
	return reader.installed[control.rights[0]], nil
}

func (reader *vmExecConnectionReader) fail(primary error) error {
	if reader.terminalErr != nil {
		return reader.terminalErr
	}
	reader.terminalErr = reader.closeInstalled(primary)
	return reader.terminalErr
}

func (reader *vmExecConnectionReader) closeInstalled(primary error) error {
	result := primary
	for _, owner := range reader.installOrder {
		result = appendVMExecConnectionCleanupError(result, owner.close())
	}
	return result
}

func (owner *vmExecReceivedConnectionOwner) take() (*os.File, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor is closed")
	}
	if owner.taken {
		return nil, errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor was already taken")
	}
	owner.taken = true
	return os.NewFile(uintptr(owner.fd), "mvm-vmexec-connection"), nil
}

func (owner *vmExecReceivedConnectionOwner) close() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.taken {
		return owner.closeErr
	}
	owner.closed = true
	owner.closeErr = owner.closeFD(owner.fd)
	return owner.closeErr
}

func admitVMExecConnection(ctx context.Context, fd int, deps vmExecConnectionReaderDeps) error {
	if ctx == nil {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection admission context is required")
	}
	if err := ctx.Err(); err != nil {
		return vmExecConnectionSystemError("admit VM-exec connection descriptor", err)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return vmExecConnectionSystemError("inspect VM-exec connection descriptor", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor must be a socket")
	}
	descriptorFlags, err := deps.descriptorFlags(ctx, uintptr(fd))
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection descriptor flags", err)
	}
	if descriptorFlags&unix.FD_CLOEXEC == 0 {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor must be close-on-exec")
	}
	domain, err := deps.socketDomain(ctx, fd)
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection socket domain", err)
	}
	if domain != unix.AF_UNIX {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor must be an AF_UNIX socket")
	}
	socketType, err := deps.socketType(ctx, fd)
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection socket type", err)
	}
	if socketType != unix.SOCK_STREAM {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor must be a SOCK_STREAM socket")
	}
	accepting, err := deps.accepting(ctx, fd)
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection listener state", err)
	}
	if accepting != 0 {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor must not be listening")
	}
	socketErr, err := deps.socketError(ctx, fd)
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection socket error", err)
	}
	if socketErr != 0 {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor has a pending socket error")
	}
	local, err := deps.socketName(ctx, fd)
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection local address", err)
	}
	if _, ok := local.(*unix.SockaddrUnix); !ok {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection local address must be AF_UNIX")
	}
	peer, err := deps.peerName(ctx, fd)
	if errors.Is(err, unix.ENOTCONN) {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection descriptor must be connected")
	}
	if err != nil {
		return vmExecConnectionSystemError("read VM-exec connection peer address", err)
	}
	if _, ok := peer.(*unix.SockaddrUnix); !ok {
		return errs.New(errs.CodeValidationFailed, "VM-exec connection peer address must be AF_UNIX")
	}
	return nil
}

func realVMExecConnectionReaderDeps() vmExecConnectionReaderDeps {
	return vmExecConnectionReaderDeps{
		recvmsg: func(
			_ context.Context,
			fd int,
			payload []byte,
			control []byte,
			flags int,
		) (int, int, int, unix.Sockaddr, error) {
			return unix.Recvmsg(fd, payload, control, flags)
		},
		poll: func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
			return unix.Poll(pollFDs, timeoutMillis)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
		descriptorFlags: func(_ context.Context, fd uintptr) (int, error) {
			return unix.FcntlInt(fd, unix.F_GETFD, 0)
		},
		socketDomain: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
		},
		socketType: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		},
		accepting: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
		},
		socketError: func(_ context.Context, fd int) (int, error) {
			return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		},
		socketName: func(_ context.Context, fd int) (unix.Sockaddr, error) {
			return unix.Getsockname(fd)
		},
		peerName: func(_ context.Context, fd int) (unix.Sockaddr, error) {
			return unix.Getpeername(fd)
		},
		closeFD: unix.Close,
	}
}

func vmExecConnectionSystemError(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeProcessError, message, cause, errs.WithClass(errs.ClassInternal))
}

func appendVMExecConnectionCleanupError(primary error, cleanupErr error) error {
	if cleanupErr == nil {
		return primary
	}
	if primary == nil {
		return errs.WrapMsg(
			errs.CodeProcessError,
			"close rejected VM-exec connection descriptor",
			cleanupErr,
			errs.WithClass(errs.ClassInternal),
			errs.WithDetails(map[string]any{
				"cleanup_required":   true,
				"cleanup_diagnostic": "close rejected VM-exec connection descriptor",
			}),
		)
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		return errs.WrapMsg(
			errs.CodeProcessError,
			primary.Error(),
			errors.Join(primary, cleanupErr),
			errs.WithClass(errs.ClassInternal),
			errs.WithDetails(map[string]any{
				"cleanup_required":   true,
				"cleanup_diagnostic": "close rejected VM-exec connection descriptor",
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
		result.Details["cleanup_diagnostic"] = "close rejected VM-exec connection descriptor"
	}
	result.Err = errors.Join(primary, cleanupErr)
	return &result
}
