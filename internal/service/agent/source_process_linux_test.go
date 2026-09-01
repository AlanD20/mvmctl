//go:build linux

package agent

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestAdoptPIDFDRequiresCLOEXECAndMatchingPID(t *testing.T) {
	tests := map[string]struct {
		flags   int
		fdinfo  []byte
		wantErr string
	}{
		"missing CLOEXEC": {flags: 0, fdinfo: []byte("Pid:\t42\n"), wantErr: "CLOEXEC"},
		"PID mismatch": {
			flags: unix.FD_CLOEXEC, fdinfo: []byte("Pid:\t41\n"), wantErr: "does not match",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			closed := 0
			effects := linuxSourceProcessEffects{
				fdFlags: func(int) (int, error) { return tc.flags, nil },
				fdInfo:  func(context.Context, int) ([]byte, error) { return tc.fdinfo, nil },
				closeFD: func(int) error {
					closed++
					return nil
				},
			}
			_, err := effects.AdoptProcess(t.Context(), 42, 9)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, 1, closed)
		})
	}
}

func TestPeerCredentialsFailsClosedWhenSOPEERPIDFDIsUnavailable(t *testing.T) {
	raw := &fakeRawConn{}
	conn := &fakeSyscallConn{raw: raw}
	effects := linuxSourceProcessEffects{
		socketType: func(int) (int, error) { return unix.SOCK_STREAM, nil },
		socketName: func(int) (unix.Sockaddr, error) { return &unix.SockaddrUnix{Name: "source"}, nil },
		socketPeer: func(int) (*unix.Ucred, int, error) {
			return nil, -1, unix.ENOPROTOOPT
		},
	}

	_, _, err := effects.PeerCredentials(t.Context(), conn)
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.ENOPROTOOPT)
	assert.Equal(t, 1, raw.controlCalls)
}

func TestPeerCredentialsAndPIDFDAreCapturedInOneSocketControl(t *testing.T) {
	raw := &fakeRawConn{}
	conn := &fakeSyscallConn{raw: raw}
	closed := 0
	effects := linuxSourceProcessEffects{
		socketType: func(int) (int, error) { return unix.SOCK_STREAM, nil },
		socketName: func(int) (unix.Sockaddr, error) { return &unix.SockaddrUnix{Name: "source"}, nil },
		socketPeer: func(int) (*unix.Ucred, int, error) {
			return &unix.Ucred{Pid: 42, Uid: 1001, Gid: 1002}, 9, nil
		},
		fdFlags:  func(int) (int, error) { return unix.FD_CLOEXEC, nil },
		fdInfo:   func(context.Context, int) ([]byte, error) { return []byte("Pid:\t42\n"), nil },
		pollFD:   func(int) (bool, error) { return false, nil },
		signalFD: func(int) error { return nil },
		closeFD: func(int) error {
			closed++
			return nil
		},
	}

	credentials, pin, err := effects.PeerCredentials(t.Context(), conn)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(peerCredentials{PID: 42, UID: 1001, GID: 1002}, credentials))
	assert.Equal(t, 1, raw.controlCalls)
	require.NoError(t, pin.Close())
	assert.Equal(t, 1, closed)
}

func TestPinnedProcessPollReadinessRejectsExitedOrZombieProcess(t *testing.T) {
	closed := 0
	effects := linuxSourceProcessEffects{
		fdFlags: func(int) (int, error) { return unix.FD_CLOEXEC, nil },
		fdInfo:  func(context.Context, int) ([]byte, error) { return []byte("Pid:\t42\n"), nil },
		pollFD:  func(int) (bool, error) { return true, nil },
		closeFD: func(int) error {
			closed++
			return nil
		},
	}
	_, err := effects.AdoptProcess(t.Context(), 42, 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited")
	assert.Equal(t, 1, closed)
}

type fakeRawConn struct {
	controlCalls int
}

func (conn *fakeRawConn) Control(fn func(uintptr)) error {
	conn.controlCalls++
	fn(7)
	return nil
}

func (*fakeRawConn) Read(func(uintptr) bool) error  { return nil }
func (*fakeRawConn) Write(func(uintptr) bool) error { return nil }

type fakeSyscallConn struct {
	raw syscall.RawConn
}

func (*fakeSyscallConn) Read([]byte) (int, error)         { return 0, errors.New("unused") }
func (*fakeSyscallConn) Write(value []byte) (int, error)  { return len(value), nil }
func (*fakeSyscallConn) Close() error                     { return nil }
func (*fakeSyscallConn) LocalAddr() net.Addr              { return &net.UnixAddr{} }
func (*fakeSyscallConn) RemoteAddr() net.Addr             { return &net.UnixAddr{} }
func (*fakeSyscallConn) SetDeadline(time.Time) error      { return nil }
func (*fakeSyscallConn) SetReadDeadline(time.Time) error  { return nil }
func (*fakeSyscallConn) SetWriteDeadline(time.Time) error { return nil }
func (conn *fakeSyscallConn) SyscallConn() (syscall.RawConn, error) {
	return conn.raw, nil
}
