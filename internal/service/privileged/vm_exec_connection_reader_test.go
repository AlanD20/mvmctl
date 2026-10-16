package privileged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

type fakeVMExecReceive struct {
	payload []byte
	control []byte
	flags   int
	from    unix.Sockaddr
	err     error
}

type fakeVMExecCloseTracker struct {
	// mu guards calls and contexts because cleanup may race test observation.
	mu               sync.Mutex
	calls            map[int]int
	shutdownCalls    map[int]int
	contexts         []context.Context
	shutdownContexts []context.Context
	err              error
	shutdownErr      error
	shutdownStarted  chan<- struct{}
	shutdownRelease  <-chan struct{}
}

func TestVMExecConnectionReaderRealSocketRoundTrip(t *testing.T) {
	controlReaderFD, controlWriterFD := requireVMExecSocketPairFDs(t, unix.SOCK_STREAM)
	connectionFD, connectionPeerFD := requireVMExecSocketPairFDs(t, unix.SOCK_STREAM)
	deferRequireUnixClose(t, controlReaderFD)
	deferRequireUnixClose(t, connectionFD)
	deferRequireUnixClose(t, connectionPeerFD)
	controlWriterOpen := true
	t.Cleanup(func() {
		if controlWriterOpen {
			assert.NoError(t, unix.Close(controlWriterFD))
		}
	})
	if _, err := unix.GetsockoptInt(connectionFD, unix.SOL_SOCKET, unix.SO_DOMAIN); errors.Is(err, unix.EPERM) {
		t.Skip("test sandbox blocks Unix socket admission syscalls")
	} else {
		require.NoError(t, err)
	}
	require.NoError(t, unix.SetNonblock(connectionFD, true))
	beforeStatus, err := unix.FcntlInt(uintptr(connectionFD), unix.F_GETFL, 0)
	require.NoError(t, err)

	frame := []byte{0x4d, 0x56, 0x4d, 0x45, 0x58, 0x45, 0x43}
	count, err := unix.SendmsgN(
		controlWriterFD,
		frame[:1],
		unix.UnixRights(connectionFD),
		nil,
		unix.MSG_NOSIGNAL,
	)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	written, err := unix.Write(controlWriterFD, frame[1:])
	require.NoError(t, err)
	require.Equal(t, len(frame)-1, written)
	require.NoError(t, unix.Close(controlWriterFD))
	controlWriterOpen = false

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reader := newVMExecConnectionReader(ctx, controlReaderFD, time.Now().Add(2*time.Second))
	type readResult struct {
		frame []byte
		err   error
	}
	resultChannel := make(chan readResult, 1)
	go func() {
		got, readErr := io.ReadAll(reader)
		resultChannel <- readResult{frame: got, err: readErr}
	}()

	var result readResult
	select {
	case result = <-resultChannel:
	case <-time.After(3 * time.Second):
		cancel()
		_ = unix.Shutdown(controlReaderFD, unix.SHUT_RDWR)
		select {
		case result = <-resultChannel:
			_ = reader.Close()
			t.Fatalf("VM-exec connection reader did not complete before watchdog: %v", result.err)
		case <-time.After(time.Second):
			t.Fatal("VM-exec connection reader failed to join before recovery watchdog")
		}
	}
	require.NoError(t, result.err)
	if diff := cmp.Diff(frame, result.frame); diff != "" {
		t.Errorf("VM-exec ordinary frame mismatch (-want +got):\n%s", diff)
	}

	require.NotNil(t, reader.connection)
	receivedFD := reader.connection.fd
	descriptorFlags, err := unix.FcntlInt(uintptr(receivedFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, descriptorFlags&unix.FD_CLOEXEC)
	afterSourceStatus, err := unix.FcntlInt(uintptr(connectionFD), unix.F_GETFL, 0)
	require.NoError(t, err)
	afterReceivedStatus, err := unix.FcntlInt(uintptr(receivedFD), unix.F_GETFL, 0)
	require.NoError(t, err)
	assert.Equal(t, beforeStatus, afterSourceStatus)
	assert.Equal(t, beforeStatus, afterReceivedStatus)

	message := []byte("round-trip")
	written, err = unix.Write(connectionPeerFD, message)
	require.NoError(t, err)
	require.Equal(t, len(message), written)
	received := make([]byte, len(message))
	read, err := unix.Read(receivedFD, received)
	require.NoError(t, err)
	require.Equal(t, len(received), read)
	if diff := cmp.Diff(message, received); diff != "" {
		t.Errorf("retained connection payload mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, reader.Close())
	_, err = unix.FcntlInt(uintptr(receivedFD), unix.F_GETFD, 0)
	assert.ErrorIs(t, err, unix.EBADF)
	assert.NoError(t, reader.Close())
}

func TestVMExecConnectionReaderRejectsAncillaryGrammarAndCleansInstalledFDs(t *testing.T) {
	t.Run("missing descriptor", func(t *testing.T) {
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{payload: []byte{0x41}}}, nil)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	})

	t.Run("two descriptors", func(t *testing.T) {
		firstFD := requireRawTestFD(t)
		secondFD := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
			payload: []byte{0x41},
			control: unix.UnixRights(firstFD, secondFD),
		}}, tracker)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, firstFD)
		assertVMExecFDClosedOnce(t, tracker, secondFD)
	})

	t.Run("duplicate rights messages close each distinct descriptor", func(t *testing.T) {
		firstFD := requireRawTestFD(t)
		secondFD := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, nil, tracker)
		controlBytes := append(unix.UnixRights(firstFD), unix.UnixRights(secondFD)...)

		control, parseErr := reader.ownControl(controlBytes)
		require.NoError(t, parseErr)
		_, validationErr := reader.validateInitialControl(control)
		err := reader.fail(validationErr)

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, firstFD)
		assertVMExecFDClosedOnce(t, tracker, secondFD)
	})

	t.Run("duplicate descriptor number closes once", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, nil, tracker)
		controlBytes := append(unix.UnixRights(fd), unix.UnixRights(fd)...)

		control, parseErr := reader.ownControl(controlBytes)
		require.NoError(t, parseErr)
		_, validationErr := reader.validateInitialControl(control)
		reader.fail(validationErr)

		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("unknown message after rights", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, nil, tracker)
		unknown := unix.UnixCredentials(&unix.Ucred{Pid: 1, Uid: 1, Gid: 1})
		controlBytes := append(unix.UnixRights(fd), unknown...)

		control, parseErr := reader.ownControl(controlBytes)
		require.NoError(t, parseErr)
		_, validationErr := reader.validateInitialControl(control)
		err := reader.fail(validationErr)

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("unknown message before rights", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, nil, tracker)
		unknown := unix.UnixCredentials(&unix.Ucred{Pid: 1, Uid: 1, Gid: 1})
		controlBytes := append(unknown, unix.UnixRights(fd)...)

		control, parseErr := reader.ownControl(controlBytes)
		require.NoError(t, parseErr)
		_, validationErr := reader.validateInitialControl(control)
		err := reader.fail(validationErr)

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("positive payload and control with recv error", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
			payload: []byte{0x41},
			control: unix.UnixRights(fd),
			err:     unix.EIO,
		}}, tracker)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeProcessError, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("truncated control", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
			payload: []byte{0x41},
			control: unix.UnixRights(fd),
			flags:   unix.MSG_CTRUNC,
		}}, tracker)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("late control", func(t *testing.T) {
		initialFD := requireRawTestFD(t)
		lateFD := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{
			{payload: []byte{0x41}, control: unix.UnixRights(initialFD)},
			{payload: []byte{0x42}, control: unix.UnixRights(lateFD)},
		}, tracker)

		count, err := reader.Read(make([]byte, 8))
		require.NoError(t, err)
		require.Equal(t, 1, count)
		_, err = reader.Read(make([]byte, 8))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, initialFD)
		assertVMExecFDClosedOnce(t, tracker, lateFD)
	})

	t.Run("trailing ancillary bytes", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, nil, tracker)
		controlBytes := append(unix.UnixRights(fd), 0x7f)

		_, parseErr := reader.ownControl(controlBytes)
		err := reader.fail(parseErr)

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("NUL carrier", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
			payload: []byte{0},
			control: unix.UnixRights(fd),
		}}, tracker)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("sender address", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
			payload: []byte{0x41},
			control: unix.UnixRights(fd),
			from:    &unix.SockaddrUnix{Name: "unexpected"},
		}}, tracker)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})

	t.Run("unexpected receive flags", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
			payload: []byte{0x41},
			control: unix.UnixRights(fd),
			flags:   unix.MSG_OOB,
		}}, tracker)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})
}

func TestVMExecConnectionReaderPreservesPhysicalControlEOFBeforeCarrier(t *testing.T) {
	reader := newVMExecConnectionReaderWithDeps(
		context.Background(),
		23,
		time.Now().Add(time.Second),
		successfulVMExecReaderDeps(nil),
	)

	count, err := reader.Read(make([]byte, 1))

	assert.Zero(t, count)
	assert.ErrorIs(t, err, io.EOF)
	assert.NoError(t, reader.Close())
}

func TestVMExecConnectionReaderUsesExactRecvmsgShape(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
	deps := successfulVMExecReaderDeps(tracker)
	var calls int
	deps.recvmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		calls++
		assert.Equal(t, 1, len(payload))
		assert.Equal(t, unix.CmsgSpace(4), len(control))
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		payload[0] = 0x41
		return 1, copy(control, unix.UnixRights(fd)), 0, nil, nil
	}
	reader := newVMExecConnectionReaderWithDeps(
		context.Background(),
		23,
		time.Now().Add(time.Second),
		deps,
	)

	count, err := reader.Read(make([]byte, 64))
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, 1, calls)
	assert.NoError(t, reader.Close())
	assertVMExecFDClosedOnce(t, tracker, fd)
}

func TestVMExecConnectionReaderClosesInstalledFDOnStructuralRejection(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
	deps := successfulVMExecReaderDeps(tracker)
	deps.socketType = func(context.Context, int) (int, error) { return unix.SOCK_DGRAM, nil }
	deps.recvmsg = oneVMExecReceive(t, fakeVMExecReceive{
		payload: []byte{0x41},
		control: unix.UnixRights(fd),
	})
	reader := newVMExecConnectionReaderWithDeps(
		context.Background(),
		23,
		time.Now().Add(time.Second),
		deps,
	)

	_, err := reader.Read(make([]byte, 1))

	assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	assertVMExecFDClosedOnce(t, tracker, fd)
	assert.Zero(t, tracker.shutdownCount(fd))
}

func TestVMExecConnectionReaderSecondReadCancellationClosesInstalledFD(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
	type cleanupContextKey struct{}
	baseCtx := context.WithValue(context.Background(), cleanupContextKey{}, "retained")
	ctx, cancel := context.WithCancel(baseCtx)
	deps := successfulVMExecReaderDeps(tracker)
	deps.recvmsg = oneVMExecReceive(t, fakeVMExecReceive{
		payload: []byte{0x41},
		control: unix.UnixRights(fd),
	})
	reader := newVMExecConnectionReaderWithDeps(ctx, 23, time.Now().Add(time.Second), deps)
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	cancel()

	_, err = reader.Read(make([]byte, 1))

	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, errs.CodeProcessError, requireVMExecDomainError(t, err).Code)
	assertVMExecFDClosedOnce(t, tracker, fd)
	contexts := tracker.cleanupContexts()
	require.Len(t, contexts, 1)
	assert.NoError(t, contexts[0].Err())
	assert.Equal(t, "retained", contexts[0].Value(cleanupContextKey{}))
}

func TestVMExecConnectionReaderPollsExactDescriptorAndTimeout(t *testing.T) {
	tests := map[string]struct {
		deadline       func() time.Time
		maximumTimeout int
	}{
		"poll interval": {
			deadline:       func() time.Time { return time.Now().Add(time.Second) },
			maximumTimeout: int(vmExecConnectionPollInterval / time.Millisecond),
		},
		"near deadline": {
			deadline:       func() time.Time { return time.Now().Add(40 * time.Millisecond) },
			maximumTimeout: 40,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fd := requireRawTestFD(t)
			tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
			deps := successfulVMExecReaderDeps(tracker)
			var recvCalls int
			deps.recvmsg = func(
				_ context.Context,
				actualFD int,
				payload []byte,
				control []byte,
				flags int,
			) (int, int, int, unix.Sockaddr, error) {
				recvCalls++
				assert.Equal(t, 23, actualFD)
				assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
				if recvCalls == 1 {
					// Linux recvmsg returns -1, not zero, for EAGAIN.
					return -1, 0, 0, nil, unix.EAGAIN
				}
				payload[0] = 0x41
				return 1, copy(control, unix.UnixRights(fd)), 0, nil, nil
			}
			var pollCalls int
			deps.poll = func(_ context.Context, pollFDs []unix.PollFd, timeoutMillis int) (int, error) {
				pollCalls++
				wantPollFDs := []unix.PollFd{{Fd: 23, Events: unix.POLLIN}}
				if diff := cmp.Diff(wantPollFDs, pollFDs); diff != "" {
					t.Errorf("reader poll descriptors mismatch (-want +got):\n%s", diff)
				}
				require.Greater(t, timeoutMillis, 0)
				require.LessOrEqual(t, timeoutMillis, test.maximumTimeout)
				pollFDs[0].Revents = unix.POLLIN
				return 1, nil
			}
			reader := newVMExecConnectionReaderWithDeps(
				context.Background(),
				23,
				test.deadline(),
				deps,
			)

			_, err := reader.Read(make([]byte, 1))
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			assert.Equal(t, 1, pollCalls)
			assertVMExecFDClosedOnce(t, tracker, fd)
		})
	}
}

func TestVMExecConnectionReaderDecodesTypedPhaseBeforeTransferring(t *testing.T) {
	request := vmexecsessionwire.OpenSource{
		SourceVMID:      "0123456789abcdef0123456789abcdef",
		DestinationVMID: "fedcba9876543210fedcba9876543210",
	}
	var openFrame bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeOpenSource(t.Context(), &openFrame, request))
	var bindFrame bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &bindFrame))

	t.Run("open source", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader := newFramedVMExecConnectionReader(t, openFrame.Bytes(), fd, tracker)

		gotRequest, connection, err := reader.decodeOpenSourceAndRetain(t.Context())

		require.NoError(t, err)
		if diff := cmp.Diff(request, gotRequest); diff != "" {
			t.Errorf("open-source request mismatch (-want +got):\n%s", diff)
		}
		require.NotNil(t, connection)
		require.NoError(t, reader.Close())
		_, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		require.NoError(t, err)
		require.NoError(t, connection.shutdownAndClose(t.Context()))
		assertVMExecFDClosedOnce(t, tracker, fd)
		assert.Equal(t, 1, tracker.shutdownCount(fd))
	})

	t.Run("bind destination", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader := newFramedVMExecConnectionReader(t, bindFrame.Bytes(), fd, tracker)

		connection, err := reader.decodeBindDestinationAndRetain(t.Context())

		require.NoError(t, err)
		require.NotNil(t, connection)
		require.NoError(t, reader.Close())
		require.NoError(t, connection.shutdownAndClose(t.Context()))
		assertVMExecFDClosedOnce(t, tracker, fd)
		assert.Equal(t, 1, tracker.shutdownCount(fd))
	})
}

func TestVMExecConnectionReaderKeepsOwnershipWhenTypedPhaseDecodeFails(t *testing.T) {
	request := vmexecsessionwire.OpenSource{
		SourceVMID:      "0123456789abcdef0123456789abcdef",
		DestinationVMID: "fedcba9876543210fedcba9876543210",
	}
	var valid bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeOpenSource(t.Context(), &valid, request))
	var wrongPhase bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &wrongPhase))

	tests := map[string]struct {
		frame  []byte
		decode func(context.Context, *vmExecConnectionReader) (*vmExecRetainedConnection, error)
	}{
		"incomplete prefix": {
			frame: valid.Bytes()[:5],
			decode: func(ctx context.Context, reader *vmExecConnectionReader) (*vmExecRetainedConnection, error) {
				_, connection, err := reader.decodeOpenSourceAndRetain(ctx)
				return connection, err
			},
		},
		"malformed phase": {
			frame: wrongPhase.Bytes(),
			decode: func(ctx context.Context, reader *vmExecConnectionReader) (*vmExecRetainedConnection, error) {
				_, connection, err := reader.decodeOpenSourceAndRetain(ctx)
				return connection, err
			},
		},
		"truncated body": {
			frame: valid.Bytes()[:valid.Len()-1],
			decode: func(ctx context.Context, reader *vmExecConnectionReader) (*vmExecRetainedConnection, error) {
				return reader.decodeBindDestinationAndRetain(ctx)
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fd := requireRawTestFD(t)
			tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
			reader := newFramedVMExecConnectionReader(t, test.frame, fd, tracker)

			connection, err := test.decode(t.Context(), reader)

			assert.Nil(t, connection)
			assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
			assertVMExecFDClosedOnce(t, tracker, fd)
			assert.Zero(t, tracker.shutdownCount(fd))
			assert.NoError(t, reader.Close())
		})
	}
}

func TestVMExecConnectionReaderKeepsOwnershipWhenTypedPhaseContextCancelsAfterInstall(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
	var frame bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &frame))
	ctx, cancel := context.WithCancel(context.Background())
	deps := successfulVMExecReaderDeps(tracker)
	offset := 0
	firstRead := true
	deps.recvmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		if offset == frame.Len() {
			return 0, 0, 0, nil, nil
		}
		payloadCount := copy(payload, frame.Bytes()[offset:])
		offset += payloadCount
		if !firstRead {
			return payloadCount, 0, 0, nil, nil
		}
		firstRead = false
		controlCount := copy(control, unix.UnixRights(fd))
		cancel()
		return payloadCount, controlCount, 0, nil, nil
	}
	reader := newVMExecConnectionReaderWithDeps(ctx, 23, time.Now().Add(time.Second), deps)

	connection, err := reader.decodeBindDestinationAndRetain(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, connection)
	assertVMExecFDClosedOnce(t, tracker, fd)
	assert.Zero(t, tracker.shutdownCount(fd))
	assert.NoError(t, reader.Close())
}

// Rationale: The transferred capability must cache the complete shutdown-and-close result while preserving both
// independent cleanup failures and removing cancellation without dropping context values.
func TestVMExecRetainedConnectionAggregatesShutdownAndCloseOnce(t *testing.T) {
	fd := requireRawTestFD(t)
	shutdownErr := errors.New("shutdown sentinel")
	closeErr := errors.New("close sentinel")
	shutdownStarted := make(chan struct{}, 1)
	shutdownRelease := make(chan struct{})
	tracker := &fakeVMExecCloseTracker{
		calls: make(map[int]int), shutdownCalls: make(map[int]int), err: closeErr, shutdownErr: shutdownErr,
		shutdownStarted: shutdownStarted, shutdownRelease: shutdownRelease,
	}
	var frame bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &frame))
	reader := newFramedVMExecConnectionReader(t, frame.Bytes(), fd, tracker)
	connection, err := reader.decodeBindDestinationAndRetain(t.Context())
	require.NoError(t, err)
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "cleanup-value"))
	cancel()

	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	var releaseOnce sync.Once
	releaseCleanup := func() {
		releaseOnce.Do(func() { close(shutdownRelease) })
	}
	collectResults := func(resultErrors []error) ([]error, bool) {
		watchdog := time.NewTimer(time.Second)
		defer watchdog.Stop()
		for len(resultErrors) < 2 {
			select {
			case resultErr := <-results:
				resultErrors = append(resultErrors, resultErr)
			case <-watchdog.C:
				return resultErrors, false
			}
		}
		return resultErrors, true
	}
	recoverAndJoin := func(resultErrors []error, failure string) {
		releaseCleanup()
		if _, joined := collectResults(resultErrors); !joined {
			t.Fatalf("%s; cleanup goroutines did not join after recovery", failure)
		}
		t.Fatal(failure)
	}
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			results <- connection.shutdownAndClose(ctx)
		}()
	}
	<-ready
	<-ready
	close(start)
	select {
	case <-shutdownStarted:
	case <-time.After(time.Second):
		recoverAndJoin(nil, "retained connection shutdown did not reach the cleanup phase")
	}
	releaseCleanup()
	resultErrors, joined := collectResults(make([]error, 0, 2))
	if !joined {
		recoverAndJoin(resultErrors, "concurrent retained connection cleanup did not complete")
	}
	firstErr := resultErrors[0]
	secondErr := resultErrors[1]

	assert.ErrorIs(t, firstErr, shutdownErr)
	assert.ErrorIs(t, firstErr, closeErr)
	assert.Equal(t, firstErr, secondErr)
	wantCounts := map[string]int{"close": 1, "shutdown": 1}
	gotCounts := map[string]int{"close": tracker.count(fd), "shutdown": tracker.shutdownCount(fd)}
	if diff := cmp.Diff(wantCounts, gotCounts); diff != "" {
		t.Errorf("retained connection cleanup counts mismatch (-want +got):\n%s", diff)
	}
	contexts := tracker.shutdownCleanupContexts()
	require.Len(t, contexts, 1)
	assert.NoError(t, contexts[0].Err())
	assert.Equal(t, "cleanup-value", contexts[0].Value(contextKey{}))
	closeContexts := tracker.cleanupContexts()
	require.Len(t, closeContexts, 1)
	assert.NoError(t, closeContexts[0].Err())
	assert.Equal(t, "cleanup-value", closeContexts[0].Value(contextKey{}))
}

func TestVMExecRetainedConnectionIgnoresENOTCONNWhenCloseSucceeds(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{
		calls: make(map[int]int), shutdownCalls: make(map[int]int), shutdownErr: unix.ENOTCONN,
	}
	var frame bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeBindDestination(t.Context(), &frame))
	reader := newFramedVMExecConnectionReader(t, frame.Bytes(), fd, tracker)
	connection, err := reader.decodeBindDestinationAndRetain(t.Context())
	require.NoError(t, err)

	assert.NoError(t, connection.shutdownAndClose(t.Context()))
	assertVMExecFDClosedOnce(t, tracker, fd)
	assert.Equal(t, 1, tracker.shutdownCount(fd))
}

func TestVMExecConnectionReaderClosesAdmittedUntransferredConnectionWithoutShutdown(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
	reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
		payload: []byte{0x41}, control: unix.UnixRights(fd),
	}}, tracker)
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	connection, err := reader.retainDecodedConnection()
	assert.Nil(t, connection)
	assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)

	firstErr := reader.Close()
	secondErr := reader.Close()

	assert.NoError(t, firstErr)
	assert.Equal(t, firstErr, secondErr)
	assertVMExecFDClosedOnce(t, tracker, fd)
	assert.Zero(t, tracker.shutdownCount(fd))
}

// Rationale: Cleanup failure is secondary evidence. It must neither replace
// the validation code nor cause a second close against a potentially reused FD.
func TestVMExecConnectionReaderPreservesPrimaryAndBoundsCleanupEvidence(t *testing.T) {
	firstFD := requireRawTestFD(t)
	secondFD := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int), err: errors.New("injected close failure")}
	t.Cleanup(func() { assert.NoError(t, unix.Close(firstFD)) })
	t.Cleanup(func() { assert.NoError(t, unix.Close(secondFD)) })
	reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
		payload: []byte{0x41},
		control: unix.UnixRights(firstFD, secondFD),
	}}, tracker)

	_, firstErr := reader.Read(make([]byte, 1))
	_, secondErr := reader.Read(make([]byte, 1))
	closeErr := reader.Close()

	for _, err := range []error{firstErr, secondErr} {
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
		assert.Equal(t, true, domainErr.Details["cleanup_required"])
		assert.Equal(t, "close rejected VM-exec connection descriptor", domainErr.Details["cleanup_diagnostic"])
	}
	assert.Error(t, closeErr)
	assert.Equal(t, 1, tracker.count(firstFD))
	assert.Equal(t, 1, tracker.count(secondFD))
}

func TestVMExecConnectionReaderCancellationAndDeadline(t *testing.T) {
	t.Run("context cancellation interrupts poll", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		deps := successfulVMExecReaderDeps(nil)
		deps.recvmsg = func(
			context.Context,
			int,
			[]byte,
			[]byte,
			int,
		) (int, int, int, unix.Sockaddr, error) {
			return 0, 0, 0, nil, unix.EAGAIN
		}
		deps.poll = func(context.Context, []unix.PollFd, int) (int, error) {
			cancel()
			return 0, nil
		}
		reader := newVMExecConnectionReaderWithDeps(ctx, 23, time.Now().Add(time.Second), deps)

		_, err := reader.Read(make([]byte, 1))

		assert.True(t, errors.Is(err, context.Canceled))
		assert.Equal(t, errs.CodeProcessError, requireVMExecDomainError(t, err).Code)
	})

	t.Run("expired deadline fails before recvmsg", func(t *testing.T) {
		deps := successfulVMExecReaderDeps(nil)
		deps.recvmsg = func(
			context.Context,
			int,
			[]byte,
			[]byte,
			int,
		) (int, int, int, unix.Sockaddr, error) {
			t.Fatal("recvmsg called after deadline")
			return 0, 0, 0, nil, nil
		}
		reader := newVMExecConnectionReaderWithDeps(
			context.Background(),
			23,
			time.Now().Add(-time.Second),
			deps,
		)

		_, err := reader.Read(make([]byte, 1))

		assert.True(t, errors.Is(err, os.ErrDeadlineExceeded))
		assert.Equal(t, errs.CodeProcessError, requireVMExecDomainError(t, err).Code)
	})
}

func TestAdmitVMExecConnectionStructuralRejections(t *testing.T) {
	tests := map[string]func(*vmExecConnectionReaderDeps){
		"regular descriptor": func(deps *vmExecConnectionReaderDeps) {
			deps.fstat = func(_ context.Context, _ int, stat *unix.Stat_t) error {
				stat.Mode = unix.S_IFREG
				return nil
			}
		},
		"missing close-on-exec": func(deps *vmExecConnectionReaderDeps) {
			deps.descriptorFlags = func(context.Context, uintptr) (int, error) { return 0, nil }
		},
		"non-Unix domain": func(deps *vmExecConnectionReaderDeps) {
			deps.socketDomain = func(context.Context, int) (int, error) { return unix.AF_INET, nil }
		},
		"non-stream type": func(deps *vmExecConnectionReaderDeps) {
			deps.socketType = func(context.Context, int) (int, error) { return unix.SOCK_DGRAM, nil }
		},
		"listener": func(deps *vmExecConnectionReaderDeps) {
			deps.accepting = func(context.Context, int) (int, error) { return 1, nil }
		},
		"pending socket error": func(deps *vmExecConnectionReaderDeps) {
			deps.socketError = func(context.Context, int) (int, error) { return int(unix.ECONNREFUSED), nil }
		},
		"non-Unix local address": func(deps *vmExecConnectionReaderDeps) {
			deps.socketName = func(context.Context, int) (unix.Sockaddr, error) {
				return &unix.SockaddrInet4{}, nil
			}
		},
		"unconnected": func(deps *vmExecConnectionReaderDeps) {
			deps.peerName = func(context.Context, int) (unix.Sockaddr, error) { return nil, unix.ENOTCONN }
		},
		"non-Unix peer address": func(deps *vmExecConnectionReaderDeps) {
			deps.peerName = func(context.Context, int) (unix.Sockaddr, error) {
				return &unix.SockaddrInet4{}, nil
			}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			deps := successfulVMExecReaderDeps(nil)
			mutate(&deps)

			err := admitVMExecConnection(context.Background(), 23, deps)

			assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
		})
	}
}

func TestAdmitVMExecConnectionRealStructuralRejections(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "regular")
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, file.Close()) })

		err = admitVMExecConnection(context.Background(), int(file.Fd()), realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	})

	t.Run("Unix listener", func(t *testing.T) {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		require.NoError(t, err)
		deferRequireUnixClose(t, fd)
		requireVMExecSocketAdmissionAvailable(t, fd)
		name := fmt.Sprintf("@mvm-vmexec-%d-%d", os.Getpid(), fd)
		require.NoError(t, unix.Bind(fd, &unix.SockaddrUnix{Name: name}))
		require.NoError(t, unix.Listen(fd, 1))

		err = admitVMExecConnection(context.Background(), fd, realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	})

	t.Run("unconnected Unix stream", func(t *testing.T) {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		require.NoError(t, err)
		deferRequireUnixClose(t, fd)
		requireVMExecSocketAdmissionAvailable(t, fd)

		err = admitVMExecConnection(context.Background(), fd, realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	})

	t.Run("non-Unix socket", func(t *testing.T) {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if errors.Is(err, unix.EPERM) {
			t.Skip("test sandbox blocks non-Unix socket creation")
		}
		require.NoError(t, err)
		deferRequireUnixClose(t, fd)
		requireVMExecSocketAdmissionAvailable(t, fd)

		err = admitVMExecConnection(context.Background(), fd, realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	})

	t.Run("Unix datagram", func(t *testing.T) {
		firstFD, secondFD := requireVMExecSocketPairFDs(t, unix.SOCK_DGRAM)
		deferRequireUnixClose(t, firstFD)
		deferRequireUnixClose(t, secondFD)
		requireVMExecSocketAdmissionAvailable(t, firstFD)

		err := admitVMExecConnection(context.Background(), firstFD, realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, requireVMExecDomainError(t, err).Code)
	})
}

func TestAdmitVMExecConnectionDoesNotInspectOrMutateNonblocking(t *testing.T) {
	for _, nonblocking := range []bool{false, true} {
		name := "blocking"
		if nonblocking {
			name = "nonblocking"
		}
		t.Run(name, func(t *testing.T) {
			firstFD, secondFD := requireVMExecSocketPairFDs(t, unix.SOCK_STREAM)
			deferRequireUnixClose(t, firstFD)
			deferRequireUnixClose(t, secondFD)
			requireVMExecSocketAdmissionAvailable(t, firstFD)
			require.NoError(t, unix.SetNonblock(firstFD, nonblocking))
			before, err := unix.FcntlInt(uintptr(firstFD), unix.F_GETFL, 0)
			require.NoError(t, err)

			require.NoError(t, admitVMExecConnection(context.Background(), firstFD, realVMExecConnectionReaderDeps()))
			after, err := unix.FcntlInt(uintptr(firstFD), unix.F_GETFL, 0)
			require.NoError(t, err)

			assert.Equal(t, before, after)
			assert.Equal(t, nonblocking, after&unix.O_NONBLOCK != 0)
		})
	}
}

// oneVMExecReceive copies one scripted recvmsg result into the caller-owned
// buffers so tests exercise the reader's exact payload and control sizing.
func oneVMExecReceive(
	t *testing.T,
	step fakeVMExecReceive,
) func(context.Context, int, []byte, []byte, int) (int, int, int, unix.Sockaddr, error) {
	t.Helper()
	return func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		return copy(payload, step.payload), copy(control, step.control), step.flags, step.from, step.err
	}
}

// newScriptedVMExecReader builds a deterministic recvmsg script with real
// descriptor cleanup and successful structural-admission defaults.
func newScriptedVMExecReader(
	t *testing.T,
	script []fakeVMExecReceive,
	tracker *fakeVMExecCloseTracker,
) (*vmExecConnectionReader, *int) {
	t.Helper()
	deps := successfulVMExecReaderDeps(tracker)
	index := 0
	deps.recvmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		if index >= len(script) {
			return 0, 0, 0, nil, nil
		}
		step := script[index]
		index++
		payloadCount := copy(payload, step.payload)
		controlCount := copy(control, step.control)
		if len(step.payload) > len(payload) || len(step.control) > len(control) {
			return payloadCount, controlCount, step.flags | unix.MSG_CTRUNC, step.from, step.err
		}
		return payloadCount, controlCount, step.flags, step.from, step.err
	}
	reader := newVMExecConnectionReaderWithDeps(
		context.Background(),
		23,
		time.Now().Add(time.Second),
		deps,
	)
	return reader, &index
}

func newFramedVMExecConnectionReader(
	t *testing.T,
	frame []byte,
	fd int,
	tracker *fakeVMExecCloseTracker,
) *vmExecConnectionReader {
	t.Helper()
	deps := successfulVMExecReaderDeps(tracker)
	offset := 0
	firstRead := true
	deps.recvmsg = func(
		_ context.Context,
		_ int,
		payload []byte,
		control []byte,
		flags int,
	) (int, int, int, unix.Sockaddr, error) {
		assert.Equal(t, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC, flags)
		if offset == len(frame) {
			return 0, 0, 0, nil, nil
		}
		payloadCount := copy(payload, frame[offset:])
		offset += payloadCount
		if !firstRead {
			return payloadCount, 0, 0, nil, nil
		}
		firstRead = false
		return payloadCount, copy(control, unix.UnixRights(fd)), 0, nil, nil
	}
	return newVMExecConnectionReaderWithDeps(
		context.Background(),
		23,
		time.Now().Add(time.Second),
		deps,
	)
}

// successfulVMExecReaderDeps supplies the literal structural-admission shape;
// individual tests override exactly the dependency whose contract they probe.
func successfulVMExecReaderDeps(tracker *fakeVMExecCloseTracker) vmExecConnectionReaderDeps {
	closeFD := func(_ context.Context, fd int) error { return unix.Close(fd) }
	shutdownFD := func(_ context.Context, fd int, how int) error { return unix.Shutdown(fd, how) }
	if tracker != nil {
		closeFD = tracker.close
		shutdownFD = tracker.shutdown
	}
	return vmExecConnectionReaderDeps{
		recvmsg: func(
			context.Context,
			int,
			[]byte,
			[]byte,
			int,
		) (int, int, int, unix.Sockaddr, error) {
			return 0, 0, 0, nil, nil
		},
		poll: func(context.Context, []unix.PollFd, int) (int, error) { return 0, nil },
		fstat: func(_ context.Context, _ int, stat *unix.Stat_t) error {
			stat.Mode = unix.S_IFSOCK
			return nil
		},
		descriptorFlags: func(context.Context, uintptr) (int, error) { return unix.FD_CLOEXEC, nil },
		socketDomain:    func(context.Context, int) (int, error) { return unix.AF_UNIX, nil },
		socketType:      func(context.Context, int) (int, error) { return unix.SOCK_STREAM, nil },
		accepting:       func(context.Context, int) (int, error) { return 0, nil },
		socketError:     func(context.Context, int) (int, error) { return 0, nil },
		socketName: func(context.Context, int) (unix.Sockaddr, error) {
			return &unix.SockaddrUnix{Name: "local"}, nil
		},
		peerName: func(context.Context, int) (unix.Sockaddr, error) {
			return &unix.SockaddrUnix{Name: "peer"}, nil
		},
		closeFD: closeFD, shutdownFD: shutdownFD,
	}
}

func (tracker *fakeVMExecCloseTracker) close(ctx context.Context, fd int) error {
	tracker.mu.Lock()
	tracker.calls[fd]++
	tracker.contexts = append(tracker.contexts, ctx)
	err := tracker.err
	tracker.mu.Unlock()
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func (tracker *fakeVMExecCloseTracker) shutdown(ctx context.Context, fd int, how int) error {
	tracker.mu.Lock()
	if tracker.shutdownCalls == nil {
		tracker.shutdownCalls = make(map[int]int)
	}
	tracker.shutdownCalls[fd]++
	tracker.shutdownContexts = append(tracker.shutdownContexts, ctx)
	err := tracker.shutdownErr
	started := tracker.shutdownStarted
	release := tracker.shutdownRelease
	tracker.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if how != unix.SHUT_RDWR {
		return errors.Join(err, errors.New("unexpected shutdown direction"))
	}
	return err
}

func (tracker *fakeVMExecCloseTracker) cleanupContexts() []context.Context {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return append([]context.Context(nil), tracker.contexts...)
}

func (tracker *fakeVMExecCloseTracker) count(fd int) int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.calls[fd]
}

func (tracker *fakeVMExecCloseTracker) shutdownCount(fd int) int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.shutdownCalls[fd]
}

func (tracker *fakeVMExecCloseTracker) shutdownCleanupContexts() []context.Context {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return append([]context.Context(nil), tracker.shutdownContexts...)
}

func assertVMExecFDClosedOnce(t *testing.T, tracker *fakeVMExecCloseTracker, fd int) {
	t.Helper()
	assert.Equal(t, 1, tracker.count(fd))
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	assert.ErrorIs(t, err, unix.EBADF)
}

func requireRawTestFD(t *testing.T) int {
	t.Helper()
	fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := unix.Close(fd); closeErr != nil && !errors.Is(closeErr, unix.EBADF) {
			t.Errorf("close raw VM-exec test descriptor: %v", closeErr)
		}
	})
	return fd
}

func requireVMExecSocketPairFDs(t *testing.T, socketType int) (int, int) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, socketType|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	return fds[0], fds[1]
}

func deferRequireUnixClose(t *testing.T, fd int) {
	t.Helper()
	t.Cleanup(func() { assert.NoError(t, unix.Close(fd)) })
}

func requireVMExecSocketAdmissionAvailable(t *testing.T, fd int) {
	t.Helper()
	_, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if errors.Is(err, unix.EPERM) {
		t.Skip("test sandbox blocks Unix socket admission syscalls")
	}
	require.NoError(t, err)
}

func requireVMExecDomainError(t *testing.T, err error) *errs.DomainError {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	return domainErr
}
