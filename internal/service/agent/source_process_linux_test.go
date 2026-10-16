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

func TestSourceCommandPIDFDAdmissionRetainsIdentityRequirements(t *testing.T) {
	// Rationale: Completion is eligible only after the atomic child pidfd has passed identity admission.
	for name, tc := range map[string]struct {
		flags int
		info  []byte
	}{
		"missing CLOEXEC": {info: []byte("Pid:\t42\n")},
		"different PID":   {flags: unix.FD_CLOEXEC, info: []byte("Pid:\t41\n")},
		"malformed PID":   {flags: unix.FD_CLOEXEC, info: []byte("Pid:\t-1\n")},
	} {
		t.Run(name, func(t *testing.T) {
			closed := 0
			effects := linuxSourceProcessEffects{
				fdFlags: func(int) (int, error) { return tc.flags, nil },
				fdInfo:  func(context.Context, int) ([]byte, error) { return tc.info, nil },
				pollFD:  func(int) (bool, error) { return true, nil },
				closeFD: func(int) error { closed++; return nil },
			}
			_, err := effects.AdoptSourceCommand(t.Context(), 42, 9)
			assert.Error(t, err)
			assert.Equal(t, 1, closed)
		})
	}
}

func TestSourcePIDFDExitObservationRejectsInvalidEvents(t *testing.T) {
	// Rationale: An invalid poll observation must never preserve a command result as proven completion.
	for name, tc := range map[string]struct {
		events int16
		exited bool
		err    bool
	}{
		"bad descriptor":       {events: unix.POLLNVAL, err: true},
		"poll error":           {events: unix.POLLERR, err: true},
		"readiness with error": {events: unix.POLLIN | unix.POLLERR, err: true},
		"hangup alone":         {events: unix.POLLHUP, err: true},
		"live":                 {},
		"exited":               {events: unix.POLLIN, exited: true},
		"exited and reaped":    {events: unix.POLLIN | unix.POLLHUP, exited: true},
	} {
		t.Run(name, func(t *testing.T) {
			exited, err := sourcePIDFDExited(tc.events)
			if tc.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.exited, exited)
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
