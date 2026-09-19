package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// JailerChildResourcesReady is sent by the authenticated child after receiving its fixed resource packet.
const JailerChildResourcesReady = "MVMJRD01"

// JailerChildNamespacePacket is the sole namespace-transfer packet accepted by the launch child.
const JailerChildNamespacePacket = "MVMJNS01"

// JailerChildNamespaceTarget admits exactly one namespace transfer to a pidfd-bound root child.
// THREAD-SAFE: copies share the single-use channel and its cached cleanup result.
type JailerChildNamespaceTarget struct {
	state *jailerChildNamespaceTargetState
}

type jailerChildNamespaceTargetState struct {
	// mu guards the channel, retained child pidfd, and complete delivery/release lifecycle.
	mu         sync.Mutex
	fd         int
	pidfd      int
	sent       bool
	released   bool
	releaseErr error
}

// NewJailerChildNamespaceTarget duplicates the fixed setup channel and admits its resource-ready
// phase using kernel-supplied sender credentials matched to the retained child pidfd.
// CRITICAL: SO_PASSCRED must have been enabled before spawning the child. A root UID alone is
// insufficient: the acknowledgement must come from the exact live pidfd-bound child.
func NewJailerChildNamespaceTarget(ctx context.Context, channel *os.File, childPIDFD int) (_ *JailerChildNamespaceTarget, returnErr error) {
	if ctx == nil || channel == nil || os.Geteuid() != 0 || os.Getegid() != 0 || childPIDFD < 0 {
		return nil, fmt.Errorf("invalid Jailer child namespace target")
	}
	fd, err := unix.FcntlInt(channel.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	pidfd, err := unix.FcntlInt(uintptr(childPIDFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(err, unix.Close(fd))
	}
	target := &JailerChildNamespaceTarget{state: &jailerChildNamespaceTargetState{fd: fd, pidfd: pidfd}}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, target.Release(context.WithoutCancel(ctx)))
		}
	}()
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET {
		return nil, fmt.Errorf("invalid Jailer setup socket type: %w", err)
	}
	local, err := unix.Getsockname(fd)
	if err != nil {
		return nil, err
	}
	if _, ok := local.(*unix.SockaddrUnix); !ok {
		return nil, fmt.Errorf("Jailer setup socket is not Unix")
	}
	passCred, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED)
	if err != nil || passCred != 1 {
		return nil, fmt.Errorf("Jailer setup credential reception is unavailable")
	}
	expectedPID, err := jailerChildPIDFDProcessID(ctx, pidfd)
	if err != nil {
		return nil, err
	}
	packet := make([]byte, len(JailerChildResourcesReady)+1)
	control := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, controlN, flags, _, err := unix.Recvmsg(fd, packet, control, unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitJailerChildChannel(ctx, fd, unix.POLLIN); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		messages, parseErr := unix.ParseSocketControlMessage(control[:controlN])
		// Own and close any unexpected descriptors even when the packet is rejected.
		for _, message := range messages {
			if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
				rights, rightsErr := unix.ParseUnixRights(&message)
				parseErr = errors.Join(parseErr, rightsErr)
				for _, received := range rights {
					parseErr = errors.Join(parseErr, unix.Close(received))
				}
			}
		}
		if parseErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || len(messages) != 1 ||
			n != len(JailerChildResourcesReady) || string(packet[:n]) != JailerChildResourcesReady {
			return nil, fmt.Errorf("invalid Jailer resource-ready acknowledgement")
		}
		credentials, err := unix.ParseUnixCredentials(&messages[0])
		if err != nil || credentials == nil || credentials.Uid != 0 || credentials.Gid != 0 || int(credentials.Pid) != expectedPID {
			return nil, fmt.Errorf("Jailer resource-ready sender does not match the retained child")
		}
		if err := unix.PidfdSendSignal(pidfd, 0, nil, 0); err != nil {
			return nil, err
		}
		return target, nil
	}
}

// SendNetworkNamespace duplicates one verified network namespace into the bound child.
// The lending capability must retain its descriptor until this synchronous call returns.
func (target *JailerChildNamespaceTarget) SendNetworkNamespace(ctx context.Context, namespaceFD int) error {
	if target == nil || target.state == nil || ctx == nil {
		return fmt.Errorf("Jailer namespace target is unavailable")
	}
	state := target.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.sent {
		return fmt.Errorf("Jailer namespace target is no longer awaiting a namespace")
	}
	typeValue, err := unix.IoctlRetInt(namespaceFD, unix.NS_GET_NSTYPE)
	if err != nil || typeValue != unix.CLONE_NEWNET {
		return fmt.Errorf("Jailer namespace source is not a network namespace")
	}
	if err := unix.PidfdSendSignal(state.pidfd, 0, nil, 0); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.SendmsgN(state.fd, []byte(JailerChildNamespacePacket), unix.UnixRights(namespaceFD), nil,
			unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			if err := waitJailerChildChannel(ctx, state.fd, unix.POLLOUT); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if n != len(JailerChildNamespacePacket) {
			return fmt.Errorf("incomplete Jailer namespace packet")
		}
		state.sent = true
		return nil
	}
}

// Release closes only this transfer's descriptors, once, without terminating the child.
func (target *JailerChildNamespaceTarget) Release(ctx context.Context) error {
	if target == nil || target.state == nil {
		return nil
	}
	state := target.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.releaseErr = errors.Join(unix.Close(state.fd), unix.Close(state.pidfd))
	state.fd, state.pidfd = -1, -1
	state.released = true
	return state.releaseErr
}

func jailerChildPIDFDProcessID(ctx context.Context, pidfd int) (_ int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	file, err := os.OpenFile("/proc/self/fdinfo/"+strconv.Itoa(pidfd), os.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	data := make([]byte, 4097)
	n, err := file.Read(data)
	if err != nil || n == 0 || n > 4096 {
		return 0, fmt.Errorf("invalid Jailer child pidfd metadata")
	}
	pid := 0
	for _, line := range strings.Split(string(data[:n]), "\n") {
		if !strings.HasPrefix(line, "Pid:\t") {
			continue
		}
		if pid != 0 {
			return 0, fmt.Errorf("duplicate Jailer child pidfd identity")
		}
		value := strings.TrimPrefix(line, "Pid:\t")
		pid, err = strconv.Atoi(value)
		if err != nil || pid <= 0 {
			return 0, fmt.Errorf("invalid Jailer child pidfd identity")
		}
	}
	if pid == 0 {
		return 0, fmt.Errorf("Jailer child pidfd has no live identity")
	}
	return pid, nil
}

func waitJailerChildChannel(ctx context.Context, fd int, event int16) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wait := 100 * time.Millisecond
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return context.DeadlineExceeded
			}
			if remaining < wait {
				wait = remaining
			}
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: event}}
		n, err := unix.Poll(fds, int((wait+time.Millisecond-1)/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return fmt.Errorf("Jailer child channel is unavailable")
		}
		if fds[0].Revents&event != 0 {
			return nil
		}
		return fmt.Errorf("Jailer child channel closed")
	}
}
