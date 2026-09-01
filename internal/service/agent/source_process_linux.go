//go:build linux

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	maxProcStatBytes  = 4 * 1024
	maxPIDFDInfoBytes = 512
)

type linuxSourceProcessEffects struct {
	socketType func(fd int) (int, error)
	socketName func(fd int) (unix.Sockaddr, error)
	socketPeer func(fd int) (*unix.Ucred, int, error)
	fdFlags    func(fd int) (int, error)
	fdInfo     func(ctx context.Context, fd int) ([]byte, error)
	pollFD     func(fd int) (bool, error)
	signalFD   func(fd int) error
	closeFD    func(fd int) error
}

type linuxPinnedProcess struct {
	mu       sync.Mutex
	fd       int
	pollFD   func(fd int) (bool, error)
	signalFD func(fd int) error
	closeFD  func(fd int) error
}

func newSourceProcessEffects() sourceProcessEffects {
	return linuxSourceProcessEffects{}
}

func (linuxSourceProcessEffects) ResolveAccount(
	ctx context.Context,
	username string,
) (sourceAccount, error) {
	if err := ctx.Err(); err != nil {
		return sourceAccount{}, err
	}
	account, err := user.Lookup(username)
	if err != nil {
		return sourceAccount{}, err
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return sourceAccount{}, fmt.Errorf("parse account UID: %w", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return sourceAccount{}, fmt.Errorf("parse account GID: %w", err)
	}
	return sourceAccount{UID: uint32(uid), GID: uint32(gid)}, nil
}

func (effects linuxSourceProcessEffects) PeerCredentials(
	ctx context.Context,
	conn net.Conn,
) (peerCredentials, pinnedProcess, error) {
	return effects.peerCredentials(ctx, conn)
}

func (effects linuxSourceProcessEffects) peerCredentials(
	ctx context.Context,
	conn net.Conn,
) (peerCredentials, pinnedProcess, error) {
	if err := ctx.Err(); err != nil {
		return peerCredentials{}, nil, err
	}
	syscallConn, ok := conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return peerCredentials{}, nil, fmt.Errorf("local source connection is not an AF_UNIX socket")
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		return peerCredentials{}, nil, err
	}
	var credentials *unix.Ucred
	pidFD := -1
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketType, err := effects.getSocketType(int(fd))
		if err != nil {
			socketErr = err
			return
		}
		if socketType != unix.SOCK_STREAM {
			socketErr = fmt.Errorf("local source connection is not SOCK_STREAM")
			return
		}
		localAddress, err := effects.getSocketName(int(fd))
		if err != nil {
			socketErr = err
			return
		}
		if _, ok := localAddress.(*unix.SockaddrUnix); !ok {
			socketErr = fmt.Errorf("local source connection is not AF_UNIX")
			return
		}
		credentials, pidFD, socketErr = effects.getSocketPeer(int(fd))
	}); err != nil {
		return peerCredentials{}, nil, effects.rejectPIDFD(pidFD, err)
	}
	if socketErr != nil {
		return peerCredentials{}, nil, effects.rejectPIDFD(pidFD, socketErr)
	}
	if credentials == nil || credentials.Pid <= 0 {
		return peerCredentials{}, nil, effects.rejectPIDFD(
			pidFD,
			fmt.Errorf("local source peer credentials are invalid"),
		)
	}
	pin, err := effects.AdoptProcess(ctx, int(credentials.Pid), pidFD)
	if err != nil {
		return peerCredentials{}, nil, fmt.Errorf("adopt local peer pidfd: %w", err)
	}
	return peerCredentials{
		PID: int(credentials.Pid), UID: credentials.Uid, GID: credentials.Gid,
	}, pin, nil
}

func (effects linuxSourceProcessEffects) AdoptProcess(
	ctx context.Context,
	pid int,
	pidFD int,
) (pinnedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, effects.rejectPIDFD(pidFD, err)
	}
	if pid <= 0 || pidFD < 0 {
		return nil, effects.rejectPIDFD(pidFD, fmt.Errorf("process pidfd is invalid"))
	}
	flags, err := effects.getFDFlags(pidFD)
	if err != nil {
		return nil, effects.rejectPIDFD(pidFD, fmt.Errorf("read process pidfd flags: %w", err))
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return nil, effects.rejectPIDFD(pidFD, fmt.Errorf("process pidfd is missing CLOEXEC"))
	}
	info, err := effects.readFDInfo(ctx, pidFD)
	if err != nil {
		return nil, effects.rejectPIDFD(pidFD, fmt.Errorf("read process pidfd identity: %w", err))
	}
	pinnedPID, err := parsePIDFDInfo(info)
	if err != nil {
		return nil, effects.rejectPIDFD(pidFD, err)
	}
	if pinnedPID != pid {
		return nil, effects.rejectPIDFD(
			pidFD,
			fmt.Errorf("process pidfd PID %d does not match expected PID %d", pinnedPID, pid),
		)
	}
	pin := &linuxPinnedProcess{
		fd:       pidFD,
		pollFD:   effects.pollPIDFD,
		signalFD: effects.signalPIDFD,
		closeFD:  effects.closePIDFD,
	}
	if err := pin.Alive(ctx); err != nil {
		return nil, errors.Join(err, pin.Close())
	}
	return pin, nil
}

func (linuxSourceProcessEffects) ReadIdentity(ctx context.Context, pid int) (processIdentity, error) {
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	data, err := readBoundedProcFile(fmt.Sprintf("/proc/%d/stat", pid), maxProcStatBytes)
	if err != nil {
		return processIdentity{}, err
	}
	return parseProcessStat(data)
}

func (pin *linuxPinnedProcess) Alive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pin.mu.Lock()
	defer pin.mu.Unlock()
	if pin.fd < 0 {
		return fmt.Errorf("process pin is closed")
	}
	exited, err := pin.pollFD(pin.fd)
	if err != nil {
		return err
	}
	if exited {
		return fmt.Errorf("process has exited or is a zombie")
	}
	return pin.signalFD(pin.fd)
}

func (pin *linuxPinnedProcess) Close() error {
	pin.mu.Lock()
	defer pin.mu.Unlock()
	if pin.fd < 0 {
		return nil
	}
	err := pin.closeFD(pin.fd)
	pin.fd = -1
	return err
}

func (effects linuxSourceProcessEffects) getSocketType(fd int) (int, error) {
	if effects.socketType != nil {
		return effects.socketType(fd)
	}
	return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
}

func (effects linuxSourceProcessEffects) getSocketName(fd int) (unix.Sockaddr, error) {
	if effects.socketName != nil {
		return effects.socketName(fd)
	}
	return unix.Getsockname(fd)
}

func (effects linuxSourceProcessEffects) getSocketPeer(fd int) (*unix.Ucred, int, error) {
	if effects.socketPeer != nil {
		return effects.socketPeer(fd)
	}
	credentials, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return nil, -1, err
	}
	pidFD, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return nil, -1, err
	}
	return credentials, pidFD, nil
}

func (effects linuxSourceProcessEffects) getFDFlags(fd int) (int, error) {
	if effects.fdFlags != nil {
		return effects.fdFlags(fd)
	}
	return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
}

func (effects linuxSourceProcessEffects) readFDInfo(ctx context.Context, fd int) ([]byte, error) {
	if effects.fdInfo != nil {
		return effects.fdInfo(ctx, fd)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return readBoundedProcFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd), maxPIDFDInfoBytes)
}

func (effects linuxSourceProcessEffects) pollPIDFD(fd int) (bool, error) {
	if effects.pollFD != nil {
		return effects.pollFD(fd)
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, 0)
	if err != nil {
		return false, err
	}
	return fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0, nil
}

func (effects linuxSourceProcessEffects) signalPIDFD(fd int) error {
	if effects.signalFD != nil {
		return effects.signalFD(fd)
	}
	return unix.PidfdSendSignal(fd, 0, nil, 0)
}

func (effects linuxSourceProcessEffects) closePIDFD(fd int) error {
	if effects.closeFD != nil {
		return effects.closeFD(fd)
	}
	return unix.Close(fd)
}

func (effects linuxSourceProcessEffects) rejectPIDFD(fd int, primary error) error {
	if fd < 0 {
		return primary
	}
	return errors.Join(primary, effects.closePIDFD(fd))
}

func parsePIDFDInfo(data []byte) (int, error) {
	if len(data) == 0 || len(data) > maxPIDFDInfoBytes {
		return 0, fmt.Errorf("process pidfd info size is invalid")
	}
	pid := 0
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		field, value, ok := bytes.Cut(line, []byte{':'})
		if !ok || string(field) != "Pid" {
			continue
		}
		if pid != 0 {
			return 0, fmt.Errorf("process pidfd info contains duplicate PID")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(string(value)))
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("process pidfd info PID is malformed")
		}
		pid = parsed
	}
	if pid == 0 {
		return 0, fmt.Errorf("process pidfd info is missing PID")
	}
	return pid, nil
}

func readBoundedProcFile(path string, limit int) (data []byte, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err = io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > limit {
		return nil, fmt.Errorf("process file size is invalid")
	}
	return data, nil
}

func parseProcessStat(data []byte) (processIdentity, error) {
	value := strings.TrimSpace(string(data))
	open := strings.IndexByte(value, '(')
	close := strings.LastIndexByte(value, ')')
	if open <= 0 || close <= open || close+2 > len(value) {
		return processIdentity{}, fmt.Errorf("process stat framing is malformed")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(value[:open]))
	if err != nil || pid <= 0 {
		return processIdentity{}, fmt.Errorf("process stat PID is malformed")
	}
	fields := strings.Fields(value[close+1:])
	// fields starts at state (field 3); ppid is index 1 and starttime is index 19.
	if len(fields) <= 19 || len(fields[0]) != 1 {
		return processIdentity{}, fmt.Errorf("process stat fields are malformed")
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil || parentPID < 0 {
		return processIdentity{}, fmt.Errorf("process stat parent PID is malformed")
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || startTime == 0 {
		return processIdentity{}, fmt.Errorf("process stat start time is malformed")
	}
	return processIdentity{PID: pid, ParentPID: parentPID, StartTime: startTime}, nil
}
