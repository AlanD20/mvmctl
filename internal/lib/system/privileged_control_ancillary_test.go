package system

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestPrivilegedControlVMExecConnectionSenderRejectsWithoutTakingOwnership(t *testing.T) {
	t.Run("nil connection", func(t *testing.T) {
		sender, err := newPrivilegedControlVMExecConnectionSender(nil)

		assert.Nil(t, sender)
		assert.Equal(t, errs.CodeValidationFailed, requireSystemVMExecDomainError(t, err).Code)
	})

	t.Run("closed connection", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "closed-connection")
		require.NoError(t, err)
		require.NoError(t, file.Close())

		sender, constructErr := newPrivilegedControlVMExecConnectionSender(file)

		assert.Nil(t, sender)
		assert.Equal(t, errs.CodeValidationFailed, requireSystemVMExecDomainError(t, constructErr).Code)
		assert.ErrorIs(t, file.Close(), os.ErrClosed)
	})
}

func TestPrivilegedControlVMExecConnectionSenderSendsOneCarrierAndOneDescriptor(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })

	sender, err := newPrivilegedControlVMExecConnectionSender(connection)
	require.NoError(t, err)
	frame := []byte{0x7b, 0x11, 0x22, 0x33}

	require.NoError(t, sender.send(context.Background(), controlParent, frame))
	_, statErr := connection.Stat()
	assert.ErrorIs(t, statErr, os.ErrClosed)

	carrier := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	count, controlCount, flags, from, err := unix.Recvmsg(
		int(controlPeer.Fd()),
		carrier,
		control,
		unix.MSG_CMSG_CLOEXEC,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	if diff := cmp.Diff(frame[:1], carrier); diff != "" {
		t.Errorf("ancillary carrier mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, unix.CmsgSpace(4), controlCount)
	assert.Zero(t, flags&^unix.MSG_CMSG_CLOEXEC)
	assert.Nil(t, from)

	messages, err := unix.ParseSocketControlMessage(control[:controlCount])
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, int64(unix.CmsgLen(4)), int64(messages[0].Header.Len))
	assert.Equal(t, int32(unix.SOL_SOCKET), messages[0].Header.Level)
	assert.Equal(t, int32(unix.SCM_RIGHTS), messages[0].Header.Type)
	assert.Len(t, messages[0].Data, 4)
	rights, err := unix.ParseUnixRights(&messages[0])
	require.NoError(t, err)
	require.Len(t, rights, 1)
	receivedFD := rights[0]
	t.Cleanup(func() { assert.NoError(t, unix.Close(receivedFD)) })
	descriptorFlags, err := unix.FcntlInt(uintptr(receivedFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, descriptorFlags&unix.FD_CLOEXEC)

	remainder := readSystemVMExecRemainder(t, int(controlPeer.Fd()), len(frame)-1)
	if diff := cmp.Diff(frame[1:], remainder); diff != "" {
		t.Errorf("ordinary frame remainder mismatch (-want +got):\n%s", diff)
	}

	payload := []byte("exact-received-descriptor")
	written, err := unix.Write(int(connectionPeer.Fd()), payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), written)
	received := make([]byte, len(payload))
	read, err := unix.Read(receivedFD, received)
	require.NoError(t, err)
	require.Equal(t, len(payload), read)
	assert.Equal(t, payload, received)
}

func TestPrivilegedControlVMExecConnectionSenderRejectsInvalidSendInputs(t *testing.T) {
	tests := map[string]struct {
		control func(*testing.T) *os.File
		frame   []byte
	}{
		"nil control": {
			frame: []byte{0x41},
		},
		"closed control": {
			control: func(t *testing.T) *os.File {
				file, err := os.CreateTemp(t.TempDir(), "closed-control")
				require.NoError(t, err)
				require.NoError(t, file.Close())
				return file
			},
			frame: []byte{0x41},
		},
		"empty frame": {
			control: requireSystemControlFile,
		},
		"NUL carrier": {
			control: requireSystemControlFile,
			frame:   []byte{0},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			connection, connectionPeer := requireSystemSocketPair(t)
			t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
			sender, err := newPrivilegedControlVMExecConnectionSender(connection)
			require.NoError(t, err)
			var control *os.File
			if test.control != nil {
				control = test.control(t)
			}

			err = sender.send(context.Background(), control, test.frame)

			assert.Equal(t, errs.CodeValidationFailed, requireSystemVMExecDomainError(t, err).Code)
			_, statErr := connection.Stat()
			assert.ErrorIs(t, statErr, os.ErrClosed)
		})
	}
}

func TestPrivilegedControlVMExecConnectionSenderRejectsRepeatSend(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
	sender, err := newPrivilegedControlVMExecConnectionSender(connection)
	require.NoError(t, err)
	require.NoError(t, sender.send(context.Background(), controlParent, []byte{0x41}))

	err = sender.send(context.Background(), controlParent, []byte{0x42})

	assert.Equal(t, errs.CodeValidationFailed, requireSystemVMExecDomainError(t, err).Code)
}

// Rationale: Once sendmsg reports the carrier byte, an error cannot justify a
// retry because the first SCM_RIGHTS message may already have installed the FD.
func TestPrivilegedControlVMExecConnectionSenderDoesNotResendAfterPositiveCount(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })

	var calls atomic.Int32
	deps := realPrivilegedControlVMExecAncillaryDeps()
	deps.sendmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, error) {
		calls.Add(1)
		if diff := cmp.Diff([]byte{0x41}, payload); diff != "" {
			t.Errorf("sendmsg payload mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(unix.UnixRights(int(connection.Fd())), control); diff != "" {
			t.Errorf("sendmsg control mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL, flags)
		return 1, unix.EINTR
	}
	sender, err := newPrivilegedControlVMExecConnectionSenderWithDeps(connection, deps)
	require.NoError(t, err)

	err = sender.send(context.Background(), controlParent, []byte{0x41, 0x42})

	assert.Error(t, err)
	assert.Equal(t, int32(1), calls.Load())
	_, statErr := connection.Stat()
	assert.ErrorIs(t, statErr, os.ErrClosed)
}

func TestPrivilegedControlVMExecConnectionSenderClosesPointerSharedOwnerOnce(t *testing.T) {
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
	sender, err := newPrivilegedControlVMExecConnectionSender(connection)
	require.NoError(t, err)
	copyOfSender := *sender

	first := sender.close()
	second := copyOfSender.close()

	assert.NoError(t, first)
	assert.NoError(t, second)
	assert.ErrorIs(t, connection.Close(), os.ErrClosed)
}

func TestPrivilegedControlVMExecConnectionSenderCachesCloseFailure(t *testing.T) {
	connection := requireInvalidSystemFile(t)
	sender, err := newPrivilegedControlVMExecConnectionSender(connection)
	require.NoError(t, err)

	first := sender.close()
	second := sender.close()

	require.Error(t, first)
	assert.ErrorIs(t, first, unix.EBADF)
	assert.Same(t, first, second)
}

// Rationale: Closing the sender cannot release and permit reuse of the integer
// descriptor while the kernel is deciding the one ancillary sendmsg call.
func TestPrivilegedControlVMExecConnectionSenderPinsDescriptorDuringCarrierSend(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })

	entered := make(chan struct{})
	release := make(chan struct{})
	deps := realPrivilegedControlVMExecAncillaryDeps()
	realSendmsg := deps.sendmsg
	deps.sendmsg = func(
		ctx context.Context,
		fd int,
		payload []byte,
		control []byte,
		flags int,
	) (int, error) {
		close(entered)
		<-release
		return realSendmsg(ctx, fd, payload, control, flags)
	}
	sender, err := newPrivilegedControlVMExecConnectionSenderWithDeps(connection, deps)
	require.NoError(t, err)
	sendResult := make(chan error, 1)
	go func() {
		sendResult <- sender.send(context.Background(), controlParent, []byte{0x41})
	}()
	<-entered
	closeResult := make(chan error, 1)
	go func() { closeResult <- sender.close() }()

	select {
	case err := <-closeResult:
		t.Fatalf("close returned before carrier send decision: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-sendResult)
	require.NoError(t, <-closeResult)

	carrier := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	count, controlCount, _, _, err := unix.Recvmsg(
		int(controlPeer.Fd()),
		carrier,
		control,
		unix.MSG_CMSG_CLOEXEC,
	)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, unix.CmsgSpace(4), controlCount)
	messages, err := unix.ParseSocketControlMessage(control[:controlCount])
	require.NoError(t, err)
	require.Len(t, messages, 1)
	rights, err := unix.ParseUnixRights(&messages[0])
	require.NoError(t, err)
	require.Len(t, rights, 1)
	receivedFD := rights[0]
	t.Cleanup(func() { assert.NoError(t, unix.Close(receivedFD)) })
	payload := []byte("original-connection")
	_, err = unix.Write(int(connectionPeer.Fd()), payload)
	require.NoError(t, err)
	received := make([]byte, len(payload))
	_, err = unix.Read(receivedFD, received)
	require.NoError(t, err)
	assert.Equal(t, payload, received)
}

func TestPrivilegedControlVMExecConnectionSenderPreservesCloseFailureEvidence(t *testing.T) {
	connection := requireInvalidSystemFile(t)
	sender, err := newPrivilegedControlVMExecConnectionSender(connection)
	require.NoError(t, err)

	err = sender.send(context.Background(), nil, []byte{0x41})

	domainErr := requireSystemVMExecDomainError(t, err)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["cleanup_required"])
	assert.Equal(t, "close VM-exec connection duplicate", domainErr.Details["cleanup_diagnostic"])
	assert.Same(t, sender.owner.closeErr, sender.close())
}

func TestPrivilegedControlVMExecConnectionSenderHonorsCancellationWhilePolling(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := realPrivilegedControlVMExecAncillaryDeps()
	deps.sendmsg = func(context.Context, int, []byte, []byte, int) (int, error) {
		return 0, unix.EAGAIN
	}
	deps.poll = func(context.Context, []unix.PollFd, int) (int, error) {
		cancel()
		return 0, nil
	}
	sender, err := newPrivilegedControlVMExecConnectionSenderWithDeps(connection, deps)
	require.NoError(t, err)

	err = sender.send(ctx, controlParent, []byte{0x41})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	_, statErr := connection.Stat()
	assert.ErrorIs(t, statErr, os.ErrClosed)
}

func TestPrivilegedControlVMExecConnectionSenderPollsExactDescriptorAndTimeout(t *testing.T) {
	tests := map[string]struct {
		newContext     func(*testing.T) context.Context
		maximumTimeout int
	}{
		"poll interval": {
			newContext:     func(*testing.T) context.Context { return context.Background() },
			maximumTimeout: int(privilegedControlVMExecPollInterval / time.Millisecond),
		},
		"near deadline": {
			newContext: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			maximumTimeout: 40,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			controlParent, controlPeer := requireSystemSocketPair(t)
			t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
			t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
			connection, connectionPeer := requireSystemSocketPair(t)
			t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
			deps := realPrivilegedControlVMExecAncillaryDeps()
			var sendCalls int
			deps.sendmsg = func(context.Context, int, []byte, []byte, int) (int, error) {
				sendCalls++
				if sendCalls == 1 {
					return 0, unix.EAGAIN
				}
				return 1, nil
			}
			var pollCalls int
			deps.poll = func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
				pollCalls++
				require.Equal(t, []unix.PollFd{{
					Fd:     int32(controlParent.Fd()),
					Events: unix.POLLOUT,
				}}, pollFDs)
				require.Greater(t, timeoutMillis, 0)
				require.LessOrEqual(t, timeoutMillis, test.maximumTimeout)
				pollFDs[0].Revents = unix.POLLOUT
				return 1, nil
			}
			sender, err := newPrivilegedControlVMExecConnectionSenderWithDeps(connection, deps)
			require.NoError(t, err)

			require.NoError(t, sender.send(test.newContext(t), controlParent, []byte{0x41}))
			assert.Equal(t, 1, pollCalls)
		})
	}
}

func TestPrivilegedControlVMExecConnectionSenderRemainderUsesNonblockingSendmsg(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deps := realPrivilegedControlVMExecAncillaryDeps()
	var calls int
	deps.sendmsg = func(
		_ context.Context,
		fd int,
		payload []byte,
		control []byte,
		flags int,
	) (int, error) {
		calls++
		assert.Equal(t, int(controlParent.Fd()), fd)
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL, flags)
		if calls == 1 {
			assert.Equal(t, []byte{0x41}, payload)
			assert.NotEmpty(t, control)
			return 1, nil
		}
		assert.Equal(t, []byte{0x42, 0x43}, payload)
		assert.Nil(t, control)
		return 0, unix.EAGAIN
	}
	deps.poll = func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
		assert.Equal(t, []unix.PollFd{{Fd: int32(controlParent.Fd()), Events: unix.POLLOUT}}, pollFDs)
		assert.Greater(t, timeoutMillis, 0)
		cancel()
		return 0, nil
	}
	sender, err := newPrivilegedControlVMExecConnectionSenderWithDeps(connection, deps)
	require.NoError(t, err)

	err = sender.send(ctx, controlParent, []byte{0x41, 0x42, 0x43})

	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, 2, calls)
}

func TestPrivilegedControlVMExecConnectionSenderAdvancesPartialRemainder(t *testing.T) {
	controlParent, controlPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, controlParent.Close()) })
	t.Cleanup(func() { assert.NoError(t, controlPeer.Close()) })
	connection, connectionPeer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, connectionPeer.Close()) })
	deps := realPrivilegedControlVMExecAncillaryDeps()
	var calls int
	deps.sendmsg = func(_ context.Context, _ int, payload, control []byte, _ int) (int, error) {
		calls++
		switch calls {
		case 1:
			assert.Equal(t, []byte{0x41}, payload)
			assert.NotEmpty(t, control)
			return 1, nil
		case 2:
			assert.Equal(t, []byte{0x42, 0x43}, payload)
			assert.Nil(t, control)
			return 1, nil
		default:
			assert.Equal(t, []byte{0x43}, payload)
			assert.Nil(t, control)
			return 1, nil
		}
	}
	sender, err := newPrivilegedControlVMExecConnectionSenderWithDeps(connection, deps)
	require.NoError(t, err)

	require.NoError(t, sender.send(context.Background(), controlParent, []byte{0x41, 0x42, 0x43}))
	assert.Equal(t, 3, calls)
}

// readSystemVMExecRemainder reads ordinary bytes through recvmsg and proves
// that no control message follows the single ancillary carrier.
func readSystemVMExecRemainder(t *testing.T, fd int, size int) []byte {
	t.Helper()
	remainder := make([]byte, 0, size)
	for len(remainder) < size {
		payload := make([]byte, size-len(remainder))
		control := make([]byte, unix.CmsgSpace(4))
		count, controlCount, flags, from, err := unix.Recvmsg(fd, payload, control, unix.MSG_CMSG_CLOEXEC)
		require.NoError(t, err)
		require.Positive(t, count)
		assert.Zero(t, controlCount)
		assert.Zero(t, flags&^unix.MSG_CMSG_CLOEXEC)
		assert.Nil(t, from)
		remainder = append(remainder, payload[:count]...)
	}
	return remainder
}

func requireSystemControlFile(t *testing.T) *os.File {
	t.Helper()
	control, peer := requireSystemSocketPair(t)
	t.Cleanup(func() { assert.NoError(t, control.Close()) })
	t.Cleanup(func() { assert.NoError(t, peer.Close()) })
	return control
}

// requireInvalidSystemFile returns an os.File with an impossible descriptor so
// close-failure tests are deterministic and cannot race descriptor reuse.
func requireInvalidSystemFile(t *testing.T) *os.File {
	t.Helper()
	file := os.NewFile(uintptr(^uint(0)>>1), "invalid-vmexec-connection")
	require.NotNil(t, file)
	return file
}

func requireSystemVMExecDomainError(t *testing.T, err error) *errs.DomainError {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	return domainErr
}

// requireSystemSocketPair creates a close-on-exec AF_UNIX stream pair for
// exercising the real ancillary transport without network or host privileges.
func requireSystemSocketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	return os.NewFile(uintptr(fds[0]), "vmexec-system-parent"),
		os.NewFile(uintptr(fds[1]), "vmexec-system-peer")
}
