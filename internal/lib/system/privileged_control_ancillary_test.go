package system

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"

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
		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	})

	t.Run("closed connection", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "closed-connection")
		require.NoError(t, err)
		require.NoError(t, file.Close())

		sender, constructErr := newPrivilegedControlVMExecConnectionSender(file)

		assert.Nil(t, sender)
		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(constructErr).Code)
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

	remainder := make([]byte, len(frame)-1)
	_, err = io.ReadFull(controlPeer, remainder)
	require.NoError(t, err)
	if diff := cmp.Diff(frame[1:], remainder); diff != "" {
		t.Errorf("ordinary frame remainder mismatch (-want +got):\n%s", diff)
	}
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

// requireSystemSocketPair creates a close-on-exec AF_UNIX stream pair for
// exercising the real ancillary transport without network or host privileges.
func requireSystemSocketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	return os.NewFile(uintptr(fds[0]), "vmexec-system-parent"),
		os.NewFile(uintptr(fds[1]), "vmexec-system-peer")
}
