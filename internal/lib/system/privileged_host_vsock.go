package system

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

// HostVsockOpenAction selects only an authenticated fixed Firecracker vsock socket.
const HostVsockOpenAction = "jailer-host-vsock-open"

// HostVsockOpenRequest identifies one VM without exposing an effect path, guest port, or token.
type HostVsockOpenRequest struct {
	VMID string `json:"vm_id"`
}

// Validate requires an exact canonical VM identity.
func (request HostVsockOpenRequest) Validate() error {
	if len(request.VMID) != 32 || strings.Trim(request.VMID, "0123456789abcdef") != "" {
		return errs.New(errs.CodeValidationFailed, "host vsock requires a canonical VM ID")
	}
	return nil
}

// HostVsockOpenResult binds the transferred connection to the requested VM identity.
type HostVsockOpenResult struct {
	VMID      string `json:"vm_id"`
	Connected bool   `json:"connected"`
}

// OpenPrivilegedHostVsock returns only a received connected AF_UNIX stream after the exact response and EOF.
// The fixed privileged subprocess has exited before ownership transfers to the caller.
func OpenPrivilegedHostVsock(ctx context.Context, vmID string) (_ *net.UnixConn, returnErr error) {
	if ctx == nil {
		return nil, errs.New(errs.CodeValidationFailed, "host vsock context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := HostVsockOpenRequest{VMID: vmID}
	if err := body.Validate(); err != nil {
		return nil, err
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, HostVsockOpenAction, body, 0); err != nil {
		return nil, err
	}
	request, err := NewPrivilegedControlRequest(HostVsockOpenAction, frame.Bytes(), 0, nil)
	if err != nil {
		return nil, err
	}
	owner := &privilegedHostVsockResponseOwner{fd: -1}
	defer func() {
		if owner.fd >= 0 {
			returnErr = errors.Join(returnErr, unix.Close(owner.fd))
			owner.fd = -1
		}
	}()
	outcome := exchangePrivilegedControlWithHostVsock(ctx, request, realPrivilegedControlDeps(),
		productionPrivilegedControlPolicy(), owner)
	if !outcome.ResponseEOF() {
		return nil, hostVsockOutcomeError(hostVsockTransportError("host vsock outcome is unknown", nil), outcome)
	}
	response, err := privilegedwire.DecodeResponse[HostVsockOpenResult](context.WithoutCancel(ctx),
		bytes.NewReader(outcome.Response()), HostVsockOpenAction)
	if err != nil {
		return nil, hostVsockOutcomeError(err, outcome)
	}
	if err := response.OperationError(); err != nil {
		return nil, hostVsockOutcomeError(err, outcome)
	}
	if outcome.Diagnostic() != nil {
		return nil, outcome.Diagnostic()
	}
	result := response.Result()
	if result.VMID != vmID || !result.Connected || owner.fd < 0 {
		return nil, hostVsockTransportError("host vsock connection handoff is incomplete", nil)
	}
	if err := validateHostVsockDescriptor(owner.fd); err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(owner.fd), "mvm-host-vsock")
	owner.fd = -1
	connection, connErr := net.FileConn(file)
	closeErr := file.Close()
	if err := errors.Join(connErr, closeErr); err != nil {
		if connection != nil {
			err = errors.Join(err, connection.Close())
		}
		return nil, hostVsockTransportError("adopt host vsock connection", err)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, errors.Join(hostVsockTransportError("host vsock connection is not AF_UNIX", nil), connection.Close())
	}
	return unixConnection, nil
}

type privilegedHostVsockResponseOwner struct{ fd int }

func (owner *privilegedHostVsockResponseOwner) read(
	ctx context.Context, control *os.File, maximum int,
) privilegedControlResponseResult {
	result := privilegedControlResponseResult{}
	buffer := make([]byte, 4096)
	ancillary := make([]byte, unix.CmsgSpace(16*4))
	for {
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		target := buffer
		if len(result.response) == 0 {
			// Reading the first carrier alone proves ancillary data did not arrive on a later byte.
			target = buffer[:1]
		}
		n, controlN, flags, _, receiveErr := unix.Recvmsg(int(control.Fd()), target, ancillary,
			unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		if receiveErr != nil {
			if errors.Is(receiveErr, unix.EINTR) {
				continue
			}
			if errors.Is(receiveErr, unix.EAGAIN) || errors.Is(receiveErr, unix.EWOULDBLOCK) {
				_, pollErr := unix.Poll([]unix.PollFd{{Fd: int32(control.Fd()), Events: unix.POLLIN}}, 100)
				if pollErr == nil || errors.Is(pollErr, unix.EINTR) {
					continue
				}
				receiveErr = pollErr
			}
			result.err = receiveErr
			return result
		}
		installed, messages, ancillaryErr := receiveHostVsockRights(ancillary[:controlN])
		valid := ancillaryErr == nil && flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) == 0
		if controlN > 0 {
			valid = valid && len(result.response) == 0 && n > 0 && buffer[0] != 0 &&
				owner.fd < 0 && messages == 1 && len(installed) == 1
		}
		if !valid {
			for _, fd := range installed {
				ancillaryErr = errors.Join(ancillaryErr, unix.Close(fd))
			}
			result.err = hostVsockTransportError("host vsock ancillary response is invalid", ancillaryErr)
			return result
		}
		if len(installed) == 1 {
			owner.fd = installed[0]
		}
		if n == 0 {
			result.eof = true
			return result
		}
		result.response = append(result.response, buffer[:n]...)
		if len(result.response) > maximum {
			result.err = hostVsockTransportError("host vsock response exceeds the transport bound", nil)
			return result
		}
	}
}

func receiveHostVsockRights(control []byte) (installed []int, count int, returnErr error) {
	for len(control) > 0 {
		header, data, remaining, err := unix.ParseOneSocketControlMessage(control)
		if err != nil {
			return installed, count, err
		}
		count++
		message := unix.SocketControlMessage{Header: header, Data: data}
		if header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS {
			fds, err := unix.ParseUnixRights(&message)
			installed = append(installed, fds...)
			returnErr = errors.Join(returnErr, err)
		} else {
			returnErr = errors.Join(returnErr, hostVsockTransportError("unknown host vsock ancillary message", nil))
		}
		control = remaining
	}
	return installed, count, returnErr
}

func validateHostVsockDescriptor(fd int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return hostVsockTransportError("host vsock descriptor is not close-on-exec", err)
	}
	for _, selection := range []struct{ option, expected int }{
		{unix.SO_DOMAIN, unix.AF_UNIX}, {unix.SO_TYPE, unix.SOCK_STREAM}, {unix.SO_ACCEPTCONN, 0},
	} {
		value, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, selection.option)
		if err != nil || value != selection.expected {
			return hostVsockTransportError("host vsock descriptor has invalid socket structure", err)
		}
	}
	if _, err := unix.Getpeername(fd); err != nil {
		return hostVsockTransportError("host vsock descriptor is not connected", err)
	}
	return nil
}

func hostVsockTransportError(message string, cause error) error {
	return errs.WrapMsg(errs.CodeProcessError, message, cause)
}

func hostVsockOutcomeError(primary error, outcome PrivilegedControlOutcome) error {
	if diagnostic := outcome.Diagnostic(); diagnostic != nil {
		return errors.Join(primary, diagnostic)
	}
	return primary
}
