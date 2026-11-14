//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// completedSourceEffects delays publication until the actual atomically pinned child has exited.
type completedSourceEffects struct {
	linuxSourceProcessEffects
}

func (effects completedSourceEffects) AdoptSourceCommand(
	ctx context.Context,
	pid int,
	pidFD int,
) (pinnedProcess, error) {
	fds := []unix.PollFd{{Fd: int32(pidFD), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, 2000)
	if err != nil {
		return nil, effects.rejectPIDFD(pidFD, err)
	}
	if fds[0].Revents&unix.POLLIN == 0 {
		return nil, effects.rejectPIDFD(pidFD, errors.New("child did not exit before publication"))
	}
	return effects.linuxSourceProcessEffects.AdoptSourceCommand(ctx, pid, pidFD)
}

func TestSourceExecPreservesCompletedUnusedSessionResult(t *testing.T) {
	// Rationale: The scheduler may let an opt-in command finish before its source binding is published.
	for name, tc := range map[string]struct {
		command   string
		status    int
		stdout    string
		stderr    string
		closeErr  error
		wantError string
	}{
		"close failure remains an agent error": {
			command: "exit 0", status: -1, closeErr: unix.EIO, wantError: "source session unavailable",
		},
		"exit zero": {command: "exit 0"},
		"nonzero with buffered output": {
			command: "printf 'out'; printf 'err' >&2; exit 7", status: 7, stdout: "out", stderr: "err",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			closes := 0
			effects := completedSourceEffects{linuxSourceProcessEffects{
				closeFD: func(fd int) error { closes++; return errors.Join(unix.Close(fd), tc.closeErr) },
			}}
			agent := testSourceAgent(effects)
			const sessionID = "00112233445566778899aabbccddeeff"
			sourceHost, sourceGuest := net.Pipe()
			defer sourceHost.Close()
			defer sourceGuest.Close()
			require.NoError(t, sourceHost.SetReadDeadline(time.Now().Add(4*time.Second)))
			agent.pendingSources[sessionID] = newSourceTransport(sourceGuest)
			host, guest := net.Pipe()
			defer host.Close()
			defer guest.Close()
			require.NoError(t, host.SetReadDeadline(time.Now().Add(4*time.Second)))
			done := make(chan struct{})
			go func() {
				agent.handleExec(ctx, &execRequest{
					ID: "completed", Type: requestTypeExec, Command: tc.command,
					NoSync: true, SourceSessionID: sessionID,
				}, guest, &sync.Mutex{})
				_ = guest.Close()
				close(done)
			}()

			decoder := json.NewDecoder(host)
			var stdout, stderr string
			terminals := 0
			for {
				var frame execResponse
				err := decoder.Decode(&frame)
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
				switch frame.Type {
				case responseTypeStdout:
					stdout += frame.Data
				case responseTypeStderr:
					stderr += frame.Data
				case responseTypeResult:
					terminals++
					assert.Equal(t, tc.status, frame.Status)
					assert.Equal(t, tc.wantError, frame.Error)
				default:
					t.Errorf("unexpected response type %q", frame.Type)
				}
			}
			<-done
			assert.Equal(t, tc.stdout, stdout)
			assert.Equal(t, tc.stderr, stderr)
			assert.Equal(t, 1, terminals)
			assert.Equal(t, 1, closes)
			assert.Empty(t, agent.pendingSources)
			assert.Empty(t, agent.sourceBindings)
			var payload [1]byte
			n, err := sourceHost.Read(payload[:])
			assert.Zero(t, n)
			assert.ErrorIs(t, err, io.EOF)
		})
	}
}

type sourcePublicationConn struct {
	discardConn
	closeErr error
	onClose  func()
}

func (conn *sourcePublicationConn) Close() error {
	if conn.onClose != nil {
		conn.onClose()
	}
	return conn.closeErr
}

func TestSourcePublicationAcceptsOnlyProvenUnusedCompletion(t *testing.T) {
	// Rationale: Completion cannot turn identity, reservation, cancellation or cleanup failures into success.
	for name, tc := range map[string]struct {
		pin               *fakePinnedProcess
		mutate            func(*Agent, *sourceBinding, *fakeSourceProcessEffects)
		cancel            bool
		cancelDuringClose bool
		closeErr          error
		wantError         string
	}{
		"observation failure": {pin: &fakePinnedProcess{aliveErr: unix.EBADF}, wantError: "bad file descriptor"},
		"exit with observation failure": {
			pin: &fakePinnedProcess{exited: true, aliveErr: unix.EIO}, wantError: "input/output error",
		},
		"metadata failure": {
			mutate: func(_ *Agent, _ *sourceBinding, effects *fakeSourceProcessEffects) {
				effects.identities = nil
			}, wantError: "missing fake identity",
		},
		"identity drift": {
			mutate: func(_ *Agent, _ *sourceBinding, effects *fakeSourceProcessEffects) {
				effects.identities[42] = []processIdentity{
					{PID: 42, ParentPID: 1, StartTime: 10}, {PID: 42, ParentPID: 1, StartTime: 11},
				}
			}, wantError: "identity changed",
		},
		"lost reservation": {
			pin: &fakePinnedProcess{exited: true},
			mutate: func(agent *Agent, binding *sourceBinding, _ *fakeSourceProcessEffects) {
				delete(agent.sourceBindings, binding.sessionID)
			}, wantError: "no longer reserved",
		},
		"replaced reservation": {
			pin: &fakePinnedProcess{exited: true},
			mutate: func(agent *Agent, binding *sourceBinding, _ *fakeSourceProcessEffects) {
				agent.sourceBindings[binding.sessionID] = &sourceBinding{sessionID: binding.sessionID}
			}, wantError: "no longer reserved",
		},
		"claimed reservation": {
			pin:       &fakePinnedProcess{exited: true},
			mutate:    func(_ *Agent, binding *sourceBinding, _ *fakeSourceProcessEffects) { binding.claimed = true },
			wantError: "no longer reserved",
		},
		"published reservation": {
			pin:       &fakePinnedProcess{exited: true},
			mutate:    func(_ *Agent, binding *sourceBinding, _ *fakeSourceProcessEffects) { close(binding.published) },
			wantError: "already published",
		},
		"closed transport": {
			pin:       &fakePinnedProcess{exited: true},
			mutate:    func(_ *Agent, binding *sourceBinding, _ *fakeSourceProcessEffects) { _ = binding.transport.close() },
			wantError: "transport is closed",
		},
		"cancelled": {pin: &fakePinnedProcess{exited: true}, cancel: true, wantError: "context canceled"},
		"cancelled during cleanup": {
			pin: &fakePinnedProcess{exited: true}, cancelDuringClose: true, wantError: "context canceled",
		},
		"pin close failure": {
			pin: &fakePinnedProcess{exited: true, closeErr: unix.EIO}, wantError: "input/output error",
		},
		"transport close failure": {
			pin: &fakePinnedProcess{exited: true}, closeErr: unix.EIO, wantError: "input/output error",
		},
		"already exited":         {pin: &fakePinnedProcess{exited: true}},
		"exit after first read":  {pin: &fakePinnedProcess{exitedSeq: []bool{false, true}}},
		"exit after second read": {pin: &fakePinnedProcess{exitedSeq: []bool{false, false, true}}},
	} {
		t.Run(name, func(t *testing.T) {
			pin := tc.pin
			if pin == nil {
				pin = &fakePinnedProcess{}
			}
			effects := &fakeSourceProcessEffects{
				pins:       map[int]*fakePinnedProcess{42: pin},
				identities: map[int][]processIdentity{42: {{PID: 42, ParentPID: 1, StartTime: 10}}},
			}
			conn := &sourcePublicationConn{closeErr: tc.closeErr}
			binding := &sourceBinding{
				sessionID: "00112233445566778899aabbccddeeff", published: make(chan struct{}),
				transport: newSourceTransport(conn),
			}
			agent := testSourceAgent(effects, binding)
			defer agent.cleanupSourceBinding(binding)
			if tc.mutate != nil {
				tc.mutate(agent, binding, effects)
			}
			reserved := agent.sourceBindings[binding.sessionID]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelDuringClose {
				conn.onClose = cancel
			}
			if tc.cancel {
				cancel()
			}
			exited, err := agent.publishSourceBinding(ctx, binding, 42, 9)
			assert.True(t, pin.closed)
			assert.Equal(t, 1, pin.closes)
			if tc.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
				assert.False(t, exited)
				if reserved != nil && reserved != binding {
					assert.Same(t, reserved, agent.sourceBindings[binding.sessionID])
				}
				return
			}
			require.NoError(t, err)
			assert.True(t, exited)
			assert.Empty(t, agent.sourceBindings)
			assert.Nil(t, binding.rootPin)
			assert.False(t, binding.claimed)
			select {
			case <-binding.published:
				t.Error("completed source command published authority")
			default:
			}
			select {
			case <-binding.transport.done:
			default:
				t.Error("completed source transport remained open")
			}
		})
	}
}
