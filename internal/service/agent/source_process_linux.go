//go:build linux

package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxProcStatBytes = 4 * 1024

type linuxSourceProcessEffects struct{}

type linuxPinnedProcess struct {
	mu sync.Mutex
	fd int
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

func (linuxSourceProcessEffects) PeerCredentials(
	ctx context.Context,
	conn net.Conn,
) (peerCredentials, error) {
	if err := ctx.Err(); err != nil {
		return peerCredentials{}, err
	}
	syscallConn, ok := conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return peerCredentials{}, fmt.Errorf("local source connection is not an AF_UNIX socket")
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		return peerCredentials{}, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketType, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil {
			socketErr = err
			return
		}
		if socketType != unix.SOCK_STREAM {
			socketErr = fmt.Errorf("local source connection is not SOCK_STREAM")
			return
		}
		localAddress, err := unix.Getsockname(int(fd))
		if err != nil {
			socketErr = err
			return
		}
		if _, ok := localAddress.(*unix.SockaddrUnix); !ok {
			socketErr = fmt.Errorf("local source connection is not AF_UNIX")
			return
		}
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return peerCredentials{}, err
	}
	if socketErr != nil {
		return peerCredentials{}, socketErr
	}
	if credentials == nil || credentials.Pid <= 0 {
		return peerCredentials{}, fmt.Errorf("local source peer credentials are invalid")
	}
	return peerCredentials{
		PID: int(credentials.Pid), UID: credentials.Uid, GID: credentials.Gid,
	}, nil
}

func (linuxSourceProcessEffects) Pin(ctx context.Context, pid int) (pinnedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &linuxPinnedProcess{fd: fd}, nil
}

func (linuxSourceProcessEffects) ReadIdentity(ctx context.Context, pid int) (processIdentity, error) {
	if err := ctx.Err(); err != nil {
		return processIdentity{}, err
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	if len(data) == 0 || len(data) > maxProcStatBytes {
		return processIdentity{}, fmt.Errorf("process stat size is invalid")
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
	return unix.PidfdSendSignal(pin.fd, 0, nil, 0)
}

func (pin *linuxPinnedProcess) Close() error {
	pin.mu.Lock()
	defer pin.mu.Unlock()
	if pin.fd < 0 {
		return nil
	}
	err := unix.Close(pin.fd)
	pin.fd = -1
	return err
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
