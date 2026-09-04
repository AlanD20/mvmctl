package privileged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const (
	receiverHelperEnvironment        = "MVM_PRIVILEGED_RECEIVER_HELPER"
	receiverRawSendHelperEnvironment = "MVM_PRIVILEGED_RECEIVER_RAW_SEND_HELPER"
)

// Rationale: Invocation and channel structure are the only failures for which the receiver
// cannot yet authenticate a response path. They must never write caller-visible authority.
func TestRun_RejectsMalformedInvocationAndControlAdmissionWithoutResponse(t *testing.T) {
	sentinel := errors.New("receiver admission sentinel")
	tests := map[string]struct {
		args   []string
		mutate func(*receiverEndpoint, *controlDeps)
	}{
		"cancelled_context": {
			args: []string{Marker, "unknown"},
		},
		"malformed_invocation": {
			args: []string{Marker, "-unknown"},
		},
		"descriptor_is_not_fd_zero": {
			args: []string{Marker, "unknown"},
			mutate: func(endpoint *receiverEndpoint, _ *controlDeps) {
				endpoint.fd = 3
			},
		},
		"initial_getfd_fails": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.descriptorFlags = func(context.Context, uintptr) (int, error) { return 0, sentinel }
			},
		},
		"setfd_fails": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.setDescriptorFlags = func(context.Context, uintptr, int) error { return sentinel }
			},
		},
		"recheck_getfd_fails": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				calls := 0
				deps.descriptorFlags = func(context.Context, uintptr) (int, error) {
					calls++
					if calls == 2 {
						return 0, sentinel
					}
					return 0, nil
				}
			},
		},
		"cloexec_not_observed": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.descriptorFlags = func(context.Context, uintptr) (int, error) { return 0, nil }
			},
		},
		"descriptor_status_read_fails": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.descriptorStatusFlags = func(context.Context, uintptr) (int, error) {
					return 0, sentinel
				}
			},
		},
		"nonblocking_descriptor": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.descriptorStatusFlags = func(context.Context, uintptr) (int, error) {
					return unix.O_NONBLOCK, nil
				}
			},
		},
		"wrong_socket_domain": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.socketDomain = func(context.Context, int) (int, error) { return unix.AF_INET, nil }
			},
		},
		"socket_domain_read_fails": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.socketDomain = func(context.Context, int) (int, error) { return 0, sentinel }
			},
		},
		"wrong_socket_type": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.socketType = func(context.Context, int) (int, error) { return unix.SOCK_DGRAM, nil }
			},
		},
		"socket_type_read_fails": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.socketType = func(context.Context, int) (int, error) { return 0, sentinel }
			},
		},
		"peer_credentials_fail": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.peerCredentials = func(context.Context, int) (*unix.Ucred, error) { return nil, sentinel }
			},
		},
		"peer_credentials_are_nil": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.peerCredentials = func(context.Context, int) (*unix.Ucred, error) { return nil, nil }
			},
		},
		"peer_pid_is_not_positive": {
			args: []string{Marker, "unknown"},
			mutate: func(_ *receiverEndpoint, deps *controlDeps) {
				deps.peerCredentials = func(context.Context, int) (*unix.Ucred, error) {
					return &unix.Ucred{Uid: 1000, Gid: 1000}, nil
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			control := validControlDeps(endpoint, nil)
			if tc.mutate != nil {
				tc.mutate(endpoint, &control)
			}
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = control
			ctx := context.Background()
			if name == "cancelled_context" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			err := run(ctx, tc.args, endpoint, runtime)

			require.Error(t, err)
			assert.Empty(t, endpoint.output.Bytes())
			assert.Zero(t, endpoint.readCalls)
			assert.Zero(t, endpoint.shutdownCalls)
			assert.Zero(t, endpoint.closeCalls)
		})
	}
}

// Rationale: The descriptor must be made non-inheritable and authenticated before
// any caller identity, environment, executable, NSS, or action work can run.
func TestRun_AdmissionOrdering(t *testing.T) {
	var calls []string
	endpoint := newReceiverEndpoint()
	endpoint.calls = &calls
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = validControlDeps(endpoint, &calls)
	runtime.identity.effectiveUID = func() int {
		calls = append(calls, "identity.euid")
		return 0
	}
	runtime.identity.lookupEnv = func(key string) (string, bool) {
		calls = append(calls, "identity."+key)
		return "1000", true
	}
	environment := newEnvironmentStore(nil)
	environment.calls = &calls
	runtime.environment = environment.deps()
	executable := trustedExecutableDeps()
	originalPin := executable.pin
	executable.pin = func(ctx context.Context) (*system.PinnedExecutable, error) {
		calls = append(calls, "executable.pin")
		return originalPin(ctx)
	}
	runtime.executable = executable
	authorization := memberAuthorizationDeps()
	originalLookup := authorization.lookupUserID
	authorization.lookupUserID = func(uid string) (*user.User, error) {
		calls = append(calls, "authorization.lookup_user")
		return originalLookup(uid)
	}
	runtime.authorization = authorization

	err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtime)

	require.NoError(t, err)
	assertCallOrder(t, calls,
		"control.getfd",
		"control.setfd",
		"control.getfd",
		"control.getfl",
		"control.domain",
		"control.type",
		"control.peer",
		"identity.euid",
		"identity.SUDO_UID",
		"identity.SUDO_GID",
		"environment.clear",
		"environment.set.LC_ALL",
		"executable.pin",
		"authorization.lookup_user",
		"control.deadline",
		"control.write",
		"control.shutdown",
	)
}

func TestRun_PeerMismatchReturnsFramedErrorBeforeEnvironmentClear(t *testing.T) {
	tests := map[string]unix.Ucred{
		"uid_mismatch": {Pid: 44, Uid: 1001, Gid: 1000},
		"gid_mismatch": {Pid: 44, Uid: 1000, Gid: 1001},
	}
	for name, peer := range tests {
		t.Run(name, func(t *testing.T) {
			var calls []string
			endpoint := newReceiverEndpoint()
			endpoint.calls = &calls
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = validControlDeps(endpoint, &calls)
			runtime.control.peerCredentials = func(context.Context, int) (*unix.Ucred, error) {
				calls = append(calls, "control.peer")
				return &peer, nil
			}
			environment := newEnvironmentStore(nil)
			environment.calls = &calls
			runtime.environment = environment.deps()

			err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtime)

			require.NoError(t, err)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), "unknown")
			assert.Equal(t, errs.CodePrivilegeRequired, operationErr.Code)
			assert.ErrorContains(t, operationErr, "peer UID/GID does not match")
			assert.Equal(t, -1, callIndex(calls, "environment.clear"))
			assert.Zero(t, endpoint.readCalls)
		})
	}
}

func TestRun_PreservesExistingDescriptorFlagsWhenSettingCloseOnExec(t *testing.T) {
	endpoint := newReceiverEndpoint()
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = validControlDeps(endpoint, nil)
	const existingFlags = 1 << 8
	flagsCalls := 0
	runtime.control.descriptorFlags = func(context.Context, uintptr) (int, error) {
		flagsCalls++
		if flagsCalls == 1 {
			return existingFlags, nil
		}
		return existingFlags | unix.FD_CLOEXEC, nil
	}
	runtime.control.setDescriptorFlags = func(_ context.Context, _ uintptr, flags int) error {
		assert.Equal(t, existingFlags|unix.FD_CLOEXEC, flags)
		return nil
	}

	require.NoError(t, run(context.Background(), []string{Marker, "unknown"}, endpoint, runtime))
}

func TestRun_AuthenticatedFailuresReturnOneMatchingFramedError(t *testing.T) {
	tests := map[string]struct {
		mutate   func(*runtimeDeps)
		wantCode errs.Code
		wantErr  string
	}{
		"identity": {
			mutate: func(deps *runtimeDeps) {
				deps.identity.effectiveUID = func() int { return 1000 }
			},
			wantCode: errs.CodePrivilegeRequired,
			wantErr:  "requires effective UID 0",
		},
		"environment": {
			mutate: func(deps *runtimeDeps) {
				store := newEnvironmentStore(nil)
				store.setErrorKey = "HOME"
				deps.environment = store.deps()
			},
			wantCode: errs.CodeInternal,
			wantErr:  "set fixed privileged environment HOME",
		},
		"executable": {
			mutate: func(deps *runtimeDeps) {
				deps.executable = rejectedExecutableDeps("privileged protocol must run from /usr/local/bin/mvm")
			},
			wantCode: errs.CodePrivilegeRequired,
			wantErr:  "must run from /usr/local/bin/mvm",
		},
		"authorization": {
			mutate: func(deps *runtimeDeps) {
				authorization := memberAuthorizationDeps()
				authorization.groupIDs = func(*user.User) ([]string, error) { return []string{"1000"}, nil }
				deps.authorization = authorization
			},
			wantCode: errs.CodePrivilegeRequired,
			wantErr:  "is not a current member",
		},
		"unknown_action": {
			mutate:   func(*runtimeDeps) {},
			wantCode: errs.CodeValidationFailed,
			wantErr:  `unknown privileged action "unknown"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = validControlDeps(endpoint, nil)
			tc.mutate(&runtime)

			err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtime)

			require.NoError(t, err)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), "unknown")
			assert.Equal(t, tc.wantCode, operationErr.Code)
			assert.ErrorContains(t, operationErr, tc.wantErr)
			assert.Equal(t, 1, endpoint.shutdownCalls)
			assert.Zero(t, endpoint.readCalls)
			assert.Zero(t, endpoint.closeCalls)
		})
	}
}

// Rationale: Once the exact retained-session action is recognized, every authenticated pre-dispatch rejection must
// use its closed terminal frame. The VM-exec client does not decode the ordinary privileged response envelope.
func TestRun_VMExecAuthenticatedFailuresUseActionFrameWithoutReadingRequest(t *testing.T) {
	tests := map[string]struct {
		mutate func(*runtimeDeps)
	}{
		"identity": {
			mutate: func(deps *runtimeDeps) {
				deps.identity.effectiveUID = func() int { return 1000 }
			},
		},
		"peer authentication": {
			mutate: func(deps *runtimeDeps) {
				deps.control.peerCredentials = func(context.Context, int) (*unix.Ucred, error) {
					return &unix.Ucred{Pid: 44, Uid: 1001, Gid: 1000}, nil
				}
			},
		},
		"environment": {
			mutate: func(deps *runtimeDeps) {
				store := newEnvironmentStore(nil)
				store.setErrorKey = "HOME"
				deps.environment = store.deps()
			},
		},
		"executable": {
			mutate: func(deps *runtimeDeps) {
				deps.executable = rejectedExecutableDeps("privileged system executable rejected")
			},
		},
		"authorization": {
			mutate: func(deps *runtimeDeps) {
				authorization := memberAuthorizationDeps()
				authorization.groupIDs = func(*user.User) ([]string, error) { return []string{"1000"}, nil }
				deps.authorization = authorization
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requestBytes := []byte("must-not-be-read")
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(requestBytes)
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = validControlDeps(endpoint, nil)
			test.mutate(&runtime)

			err := run(t.Context(), []string{Marker, vmexecsessionwire.Action}, endpoint, runtime)

			require.NoError(t, err)
			responses := bytes.NewReader(endpoint.output.Bytes())
			outcome, decodeErr := vmexecsessionwire.DecodeOutcome(t.Context(), responses)
			require.NoError(t, decodeErr)
			operationErr := outcome.OperationError()
			require.NotNil(t, operationErr)
			assert.Equal(t, errs.CodeInternal, operationErr.Code)
			assert.Zero(t, responses.Len())
			assert.Equal(t, 1, bytes.Count(endpoint.output.Bytes(), []byte("MVMXSES1")))
			assert.NotContains(t, string(endpoint.output.Bytes()), "MVMRES01")
			assert.Zero(t, endpoint.readCalls)
			assert.Equal(t, int64(0), endpoint.input.Size()-int64(endpoint.input.Len()))
			assert.Equal(t, 1, endpoint.shutdownCalls)
		})
	}
}

// Rationale: A rejected closed action must not drain an untrusted advertised request and
// must finish the complete response frame before making EOF observable to the caller.
func TestRun_UnknownActionDoesNotReadAndHalfClosesAfterFullFrame(t *testing.T) {
	var calls []string
	endpoint := newReceiverEndpoint()
	endpoint.calls = &calls
	endpoint.input = *bytes.NewReader([]byte("must-not-be-read"))
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = validControlDeps(endpoint, &calls)

	err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtime)

	require.NoError(t, err)
	decodeReceiverError(t, endpoint.output.Bytes(), "unknown")
	assert.Zero(t, endpoint.readCalls)
	assert.Equal(t, 1, endpoint.shutdownCalls)
	assert.Less(t, callIndex(calls, "control.write"), callIndex(calls, "control.shutdown"))
	assert.Equal(t, 1, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")))
}

func TestRun_ResponseTransportFailuresAreProcessErrorsWithoutSecondResponse(t *testing.T) {
	sentinel := errors.New("response transport sentinel")
	tests := map[string]struct {
		mutate       func(*receiverEndpoint, *controlDeps)
		wantCause    error
		wantShutdown int
		wantWrites   int
	}{
		"deadline": {
			mutate: func(endpoint *receiverEndpoint, _ *controlDeps) {
				endpoint.writeDeadlineErr = sentinel
			},
			wantCause: sentinel,
		},
		"write": {
			mutate: func(endpoint *receiverEndpoint, _ *controlDeps) {
				endpoint.writeFunc = func([]byte) (int, error) { return 0, sentinel }
			},
			wantCause:  sentinel,
			wantWrites: 1,
		},
		"short_write": {
			mutate: func(endpoint *receiverEndpoint, _ *controlDeps) {
				endpoint.writeFunc = func([]byte) (int, error) { return 0, nil }
			},
			wantCause:  io.ErrShortWrite,
			wantWrites: 1,
		},
		"partial_write": {
			mutate: func(endpoint *receiverEndpoint, _ *controlDeps) {
				endpoint.writeFunc = func(value []byte) (int, error) {
					endpoint.output.Write(value[:1])
					return 1, sentinel
				}
			},
			wantCause:  sentinel,
			wantWrites: 1,
		},
		"shutdown": {
			mutate: func(endpoint *receiverEndpoint, deps *controlDeps) {
				deps.shutdownWrite = func(context.Context, int) error {
					endpoint.shutdownCalls++
					return sentinel
				}
			},
			wantCause:    sentinel,
			wantShutdown: 1,
			wantWrites:   2,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			control := validControlDeps(endpoint, nil)
			tc.mutate(endpoint, &control)
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = control

			err := run(context.Background(), []string{Marker, "unknown"}, endpoint, runtime)

			require.Error(t, err)
			assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
			assert.ErrorIs(t, err, tc.wantCause)
			assert.Equal(t, tc.wantShutdown, endpoint.shutdownCalls)
			assert.Equal(t, tc.wantWrites, endpoint.writeCalls)
			assert.LessOrEqual(t, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")), 1)
		})
	}
}

func TestRun_ResponseDeadlineUsesFixedBoundAndEarlierContext(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		ctx          func() (context.Context, context.CancelFunc)
		wantDeadline time.Time
	}{
		"fixed_five_seconds": {
			ctx: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			wantDeadline: now.Add(5 * time.Second),
		},
		"earlier_context_deadline": {
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), now.Add(time.Second))
			},
			wantDeadline: now.Add(time.Second),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = validControlDeps(endpoint, nil)
			runtime.control.now = func() time.Time { return now }
			ctx, cancel := tc.ctx()
			defer cancel()

			err := run(ctx, []string{Marker, "unknown"}, endpoint, runtime)

			require.NoError(t, err)
			assert.Equal(t, tc.wantDeadline, endpoint.writeDeadline)
		})
	}
}

// Rationale: Cancellation after admission must not create a fresh interval or suppress
// the final bounded error envelope that tells the caller the operation was rejected.
func TestRun_CancelledContextStillDeliversBoundedFinalError(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	endpoint := newReceiverEndpoint()
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = validControlDeps(endpoint, nil)
	runtime.control.now = func() time.Time { return now }
	environment := newEnvironmentStore(nil)
	environmentDeps := environment.deps()
	originalClear := environmentDeps.clear
	environmentDeps.clear = func() {
		originalClear()
		cancel()
	}
	runtime.environment = environmentDeps

	err := run(ctx, []string{Marker, "unknown"}, endpoint, runtime)

	require.NoError(t, err)
	decodeReceiverError(t, endpoint.output.Bytes(), "unknown")
	assert.Equal(t, now.Add(5*time.Second), endpoint.writeDeadline)
}

// Rationale: A real inherited socketpair proves the fd-0 receiver observes kernel peer
// credentials and makes response EOF observable independently of process exit.
func TestRun_RealSocketpairPeerCredentialsAndResponseEOF(t *testing.T) {
	if os.Getenv(receiverHelperEnvironment) == "1" {
		receiverHelper(t)
		return
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	statusFlags, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFL, 0)
	require.NoError(t, err)
	assert.Zero(t, statusFlags&unix.O_NONBLOCK)
	parent := os.NewFile(uintptr(fds[0]), "receiver-parent")
	child := os.NewFile(uintptr(fds[1]), "receiver-child")
	t.Cleanup(func() { _ = parent.Close() })
	t.Cleanup(func() { _ = child.Close() })
	if _, err := unix.GetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_DOMAIN); errors.Is(err, unix.EPERM) {
		t.Skip("test sandbox blocks Unix socket admission syscalls")
	} else {
		require.NoError(t, err)
	}
	syncFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	syncParent := os.NewFile(uintptr(syncFDs[0]), "receiver-sync-parent")
	syncChild := os.NewFile(uintptr(syncFDs[1]), "receiver-sync-child")
	t.Cleanup(func() { _ = syncParent.Close() })
	t.Cleanup(func() { _ = syncChild.Close() })

	command := exec.Command(os.Args[0], "-test.run=^TestRun_RealSocketpairPeerCredentialsAndResponseEOF$")
	command.Env = append(os.Environ(), receiverHelperEnvironment+"=1")
	command.Stdin = child
	command.ExtraFiles = []*os.File{syncChild}
	var diagnostic bytes.Buffer
	command.Stdout = &diagnostic
	command.Stderr = &diagnostic
	require.NoError(t, command.Start())
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		// Cleanup is best-effort after an earlier assertion already failed.
		_, _ = syncParent.Write([]byte{'X'})
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	require.NoError(t, child.Close())
	require.NoError(t, syncChild.Close())

	requireReceiverPoll(t, int(syncParent.Fd()), unix.POLLIN, 2*time.Second)
	var ready [1]byte
	_, err = io.ReadFull(syncParent, ready[:])
	require.NoError(t, err, diagnostic.String())
	assert.Equal(t, byte('R'), ready[0])
	requireReceiverPoll(t, int(parent.Fd()), unix.POLLRDHUP, 2*time.Second)
	require.NoError(t, unix.Kill(command.Process.Pid, 0), "helper must remain alive after response EOF")
	response, err := io.ReadAll(parent)
	require.NoError(t, err)
	operationErr := decodeReceiverError(t, response, "unknown")
	assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
	assert.ErrorContains(t, operationErr, `unknown privileged action "unknown"`)
	_, err = syncParent.Write([]byte{'X'})
	require.NoError(t, err)
	waitErr := command.Wait()
	waited = true
	require.NoError(t, waitErr, diagnostic.String())
}

// Rationale: Mutating O_NONBLOCK through a duplicate after admission must not bypass
// the absolute response deadline or turn a full peer buffer into an immediate EAGAIN.
func TestStdinControlEndpoint_RawSendHonorsDeadlineAfterStatusFlagMutation(t *testing.T) {
	if os.Getenv(receiverRawSendHelperEnvironment) == "1" {
		receiverRawSendHelper(t)
		return
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	if _, err := unix.GetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_DOMAIN); errors.Is(err, unix.EPERM) {
		unix.Close(fds[0])
		unix.Close(fds[1])
		t.Skip("test sandbox blocks Unix socket admission syscalls")
	} else {
		require.NoError(t, err)
	}
	require.NoError(t, unix.SetsockoptInt(fds[1], unix.SOL_SOCKET, unix.SO_SNDBUF, 1024))
	sharedFD, err := unix.Dup(fds[1])
	require.NoError(t, err)
	parent := os.NewFile(uintptr(fds[0]), "raw-send-parent")
	child := os.NewFile(uintptr(fds[1]), "raw-send-child")
	shared := os.NewFile(uintptr(sharedFD), "raw-send-shared")
	statusReader, statusWriter, err := os.Pipe()
	require.NoError(t, err)
	for _, file := range []*os.File{parent, child, shared, statusReader, statusWriter} {
		file := file
		t.Cleanup(func() { _ = file.Close() })
	}

	command := exec.Command(
		os.Args[0],
		"-test.run=^TestStdinControlEndpoint_RawSendHonorsDeadlineAfterStatusFlagMutation$",
	)
	command.Env = append(os.Environ(), receiverRawSendHelperEnvironment+"=1")
	command.Stdin = child
	command.ExtraFiles = []*os.File{shared, statusWriter}
	var diagnostic bytes.Buffer
	command.Stdout = &diagnostic
	command.Stderr = &diagnostic
	require.NoError(t, command.Start())
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		// Cleanup is best-effort after an earlier assertion already failed.
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	require.NoError(t, child.Close())
	require.NoError(t, shared.Close())
	require.NoError(t, statusWriter.Close())

	requireReceiverPoll(t, int(statusReader.Fd()), unix.POLLIN, 2*time.Second)
	status, err := io.ReadAll(statusReader)
	require.NoError(t, err)
	waitErr := command.Wait()
	waited = true
	require.NoError(t, waitErr, diagnostic.String())
	assert.Equal(t, "OK", string(status), diagnostic.String())
}

func receiverRawSendHelper(t *testing.T) {
	status := os.NewFile(4, "raw-send-status")
	require.NotNil(t, status)
	defer func() { require.NoError(t, status.Close()) }()

	result := runReceiverRawSendRegression()
	_, err := status.Write([]byte(result))
	require.NoError(t, err)
}

func runReceiverRawSendRegression() string {
	endpoint := newStdinControlEndpoint(os.Stdin)
	if _, err := admitControlEndpoint(context.Background(), endpoint, realControlDeps()); err != nil {
		return "ERR: admit: " + err.Error()
	}
	if err := unix.SetNonblock(3, true); err != nil {
		return "ERR: mutate status flags: " + err.Error()
	}
	flags, err := unix.FcntlInt(0, unix.F_GETFL, 0)
	if err != nil {
		return "ERR: verify status mutation: " + err.Error()
	}
	if flags&unix.O_NONBLOCK == 0 {
		return "ERR: status mutation did not reach fd 0"
	}
	deadline := time.Now().Add(150 * time.Millisecond)
	if err := endpoint.SetWriteDeadline(deadline); err != nil {
		return "ERR: set deadline: " + err.Error()
	}
	started := time.Now()
	written, err := endpoint.Write(make([]byte, 8*1024*1024))
	elapsed := time.Since(started)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Sprintf("ERR: write error=%v bytes=%d elapsed=%s", err, written, elapsed)
	}
	if elapsed < 100*time.Millisecond || elapsed > time.Second {
		return fmt.Sprintf("ERR: deadline elapsed=%s bytes=%d", elapsed, written)
	}
	if written <= 0 || written >= 8*1024*1024 {
		return fmt.Sprintf("ERR: unexpected written bytes=%d", written)
	}
	return "OK"
}

func receiverHelper(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	gid := strconv.Itoa(os.Getgid())
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": uid, "SUDO_GID": gid})
	runtime.control = realControlDeps()
	runtime.authorization = memberAuthorizationDeps()
	runtime.authorization.lookupUserID = func(string) (*user.User, error) {
		return &user.User{Uid: uid, Gid: gid, Username: "receiver-helper"}, nil
	}

	err := run(
		context.Background(),
		[]string{Marker, "unknown"},
		newStdinControlEndpoint(os.Stdin),
		runtime,
	)
	if err != nil {
		domainErr := errs.AsDomainError(err)
		t.Fatalf("receiver helper failed: %v (cause: %v)", err, domainErr.Err)
	}
	syncEndpoint := os.NewFile(3, "receiver-sync")
	require.NotNil(t, syncEndpoint)
	defer func() { require.NoError(t, syncEndpoint.Close()) }()
	_, err = syncEndpoint.Write([]byte{'R'})
	require.NoError(t, err)
	var release [1]byte
	_, err = io.ReadFull(syncEndpoint, release[:])
	require.NoError(t, err)
	assert.Equal(t, byte('X'), release[0])
}

type receiverEndpoint struct {
	input            bytes.Reader
	output           bytes.Buffer
	fd               uintptr
	readCalls        int
	writeCalls       int
	shutdownCalls    int
	closeCalls       int
	writeDeadline    time.Time
	writeDeadlineErr error
	writeFunc        func([]byte) (int, error)
	calls            *[]string
}

func newReceiverEndpoint() *receiverEndpoint {
	return &receiverEndpoint{}
}

func (endpoint *receiverEndpoint) Read(target []byte) (int, error) {
	endpoint.readCalls++
	return endpoint.input.Read(target)
}

func (endpoint *receiverEndpoint) Write(value []byte) (int, error) {
	endpoint.writeCalls++
	if endpoint.calls != nil {
		*endpoint.calls = append(*endpoint.calls, "control.write")
	}
	if endpoint.writeFunc != nil {
		return endpoint.writeFunc(value)
	}
	return endpoint.output.Write(value)
}

func (endpoint *receiverEndpoint) Fd() uintptr { return endpoint.fd }

func (endpoint *receiverEndpoint) SetReadDeadline(time.Time) error { return nil }

func (endpoint *receiverEndpoint) SetWriteDeadline(deadline time.Time) error {
	if endpoint.calls != nil {
		*endpoint.calls = append(*endpoint.calls, "control.deadline")
	}
	endpoint.writeDeadline = deadline
	return endpoint.writeDeadlineErr
}

func (endpoint *receiverEndpoint) Close() error {
	endpoint.closeCalls++
	return nil
}

func validControlDeps(endpoint *receiverEndpoint, calls *[]string) controlDeps {
	flagsCalls := 0
	return controlDeps{
		descriptorFlags: func(context.Context, uintptr) (int, error) {
			if calls != nil {
				*calls = append(*calls, "control.getfd")
			}
			flagsCalls++
			if flagsCalls == 1 {
				return 0, nil
			}
			return unix.FD_CLOEXEC, nil
		},
		setDescriptorFlags: func(_ context.Context, _ uintptr, flags int) error {
			if calls != nil {
				*calls = append(*calls, "control.setfd")
			}
			if flags&unix.FD_CLOEXEC == 0 {
				return errors.New("CLOEXEC flag missing")
			}
			return nil
		},
		descriptorStatusFlags: func(context.Context, uintptr) (int, error) {
			if calls != nil {
				*calls = append(*calls, "control.getfl")
			}
			return 0, nil
		},
		socketDomain: func(context.Context, int) (int, error) {
			if calls != nil {
				*calls = append(*calls, "control.domain")
			}
			return unix.AF_UNIX, nil
		},
		socketType: func(context.Context, int) (int, error) {
			if calls != nil {
				*calls = append(*calls, "control.type")
			}
			return unix.SOCK_STREAM, nil
		},
		peerCredentials: func(context.Context, int) (*unix.Ucred, error) {
			if calls != nil {
				*calls = append(*calls, "control.peer")
			}
			return &unix.Ucred{Pid: 44, Uid: 1000, Gid: 1000}, nil
		},
		shutdownWrite: func(context.Context, int) error {
			endpoint.shutdownCalls++
			if calls != nil {
				*calls = append(*calls, "control.shutdown")
			}
			return nil
		},
		now: time.Now,
	}
}

func decodeReceiverError(t *testing.T, frame []byte, action string) *errs.DomainError {
	t.Helper()
	outcome, protocolErr := privilegedwire.DecodeResponse[struct{}](
		context.Background(),
		bytes.NewReader(frame),
		action,
	)
	require.NoError(t, protocolErr)
	require.NotNil(t, outcome.OperationError())
	return outcome.OperationError()
}

func assertCallOrder(t *testing.T, calls []string, expected ...string) {
	t.Helper()
	previous := -1
	for _, call := range expected {
		index := -1
		for candidate := previous + 1; candidate < len(calls); candidate++ {
			if calls[candidate] == call {
				index = candidate
				break
			}
		}
		require.Greater(t, index, previous, "call %q was out of order in %v", call, calls)
		previous = index
	}
}

func requireReceiverPoll(t *testing.T, fd int, events int16, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("fd %d did not report poll events %#x within %s", fd, events, timeout)
		}
		timeoutMillis := int((remaining + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: events}}
		count, err := unix.Poll(pollFDs, timeoutMillis)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		require.NoError(t, err)
		if count == 0 {
			continue
		}
		if pollFDs[0].Revents&events != 0 {
			return
		}
		t.Fatalf("fd %d reported unexpected poll events %#x", fd, pollFDs[0].Revents)
	}
}
