package privileged

import (
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
	mu    sync.Mutex
	calls map[int]int
	err   error
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

	reader := newVMExecConnectionReader(context.Background(), controlReaderFD, time.Now().Add(2*time.Second))
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
		t.Fatal("VM-exec connection reader did not complete before watchdog")
	}
	require.NoError(t, result.err)
	if diff := cmp.Diff(frame, result.frame); diff != "" {
		t.Errorf("VM-exec ordinary frame mismatch (-want +got):\n%s", diff)
	}

	connection, err := reader.TakeConnection()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	descriptorFlags, err := unix.FcntlInt(connection.Fd(), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, descriptorFlags&unix.FD_CLOEXEC)
	afterSourceStatus, err := unix.FcntlInt(uintptr(connectionFD), unix.F_GETFL, 0)
	require.NoError(t, err)
	afterReceivedStatus, err := unix.FcntlInt(connection.Fd(), unix.F_GETFL, 0)
	require.NoError(t, err)
	assert.Equal(t, beforeStatus, afterSourceStatus)
	assert.Equal(t, beforeStatus, afterReceivedStatus)

	message := []byte("round-trip")
	written, err = unix.Write(connectionPeerFD, message)
	require.NoError(t, err)
	require.Equal(t, len(message), written)
	received := make([]byte, len(message))
	_, err = io.ReadFull(connection, received)
	require.NoError(t, err)
	if diff := cmp.Diff(message, received); diff != "" {
		t.Errorf("transferred connection payload mismatch (-want +got):\n%s", diff)
	}
	assert.NoError(t, reader.Close())
	_, err = reader.TakeConnection()
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
}

func TestVMExecConnectionReaderRejectsAncillaryGrammarAndCleansInstalledFDs(t *testing.T) {
	t.Run("missing descriptor", func(t *testing.T) {
		reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{payload: []byte{0x41}}}, nil)

		_, err := reader.Read(make([]byte, 1))

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

	t.Run("unknown extra message", func(t *testing.T) {
		fd := requireRawTestFD(t)
		tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
		reader, _ := newScriptedVMExecReader(t, nil, tracker)
		unknown := unix.UnixCredentials(&unix.Ucred{Pid: 1, Uid: 1, Gid: 1})
		controlBytes := append(unix.UnixRights(fd), unknown...)

		control, parseErr := reader.ownControl(controlBytes)
		require.NoError(t, parseErr)
		_, validationErr := reader.validateInitialControl(control)
		err := reader.fail(validationErr)

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		assertVMExecFDClosedOnce(t, tracker, fd)
	})
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

func TestVMExecConnectionReaderTakeTransfersOnceAndCloseDoesNotRevoke(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int)}
	reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
		payload: []byte{0x41},
		control: unix.UnixRights(fd),
	}}, tracker)
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)

	connection, err := reader.TakeConnection()
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	_, secondErr := reader.TakeConnection()

	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(secondErr).Code)
	assert.Zero(t, tracker.count(fd))
	_, err = connection.Stat()
	assert.NoError(t, err)
	assert.NoError(t, connection.Close())
}

// Rationale: Cleanup failure is secondary evidence. It must neither replace
// the validation code nor cause a second close against a potentially reused FD.
func TestVMExecConnectionReaderPreservesPrimaryAndBoundsCleanupEvidence(t *testing.T) {
	fd := requireRawTestFD(t)
	tracker := &fakeVMExecCloseTracker{calls: make(map[int]int), err: errors.New("injected close failure")}
	t.Cleanup(func() { assert.NoError(t, unix.Close(fd)) })
	reader, _ := newScriptedVMExecReader(t, []fakeVMExecReceive{{
		payload: []byte{0x41},
		control: unix.UnixRights(fd, fd),
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
	assert.Equal(t, 1, tracker.count(fd))
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
		assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
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
		assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
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

			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestAdmitVMExecConnectionRealStructuralRejections(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "regular")
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, file.Close()) })

		err = admitVMExecConnection(context.Background(), int(file.Fd()), realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	})

	t.Run("unconnected Unix stream", func(t *testing.T) {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		require.NoError(t, err)
		deferRequireUnixClose(t, fd)
		requireVMExecSocketAdmissionAvailable(t, fd)

		err = admitVMExecConnection(context.Background(), fd, realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
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

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	})

	t.Run("Unix datagram", func(t *testing.T) {
		firstFD, secondFD := requireVMExecSocketPairFDs(t, unix.SOCK_DGRAM)
		deferRequireUnixClose(t, firstFD)
		deferRequireUnixClose(t, secondFD)
		requireVMExecSocketAdmissionAvailable(t, firstFD)

		err := admitVMExecConnection(context.Background(), firstFD, realVMExecConnectionReaderDeps())

		assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	})
}

func TestAdmitVMExecConnectionDoesNotInspectOrMutateNonblocking(t *testing.T) {
	firstFD, secondFD := requireVMExecSocketPairFDs(t, unix.SOCK_STREAM)
	deferRequireUnixClose(t, firstFD)
	deferRequireUnixClose(t, secondFD)
	requireVMExecSocketAdmissionAvailable(t, firstFD)
	require.NoError(t, unix.SetNonblock(firstFD, true))
	before, err := unix.FcntlInt(uintptr(firstFD), unix.F_GETFL, 0)
	require.NoError(t, err)

	require.NoError(t, admitVMExecConnection(context.Background(), firstFD, realVMExecConnectionReaderDeps()))
	after, err := unix.FcntlInt(uintptr(firstFD), unix.F_GETFL, 0)
	require.NoError(t, err)

	assert.Equal(t, before, after)
	assert.NotZero(t, after&unix.O_NONBLOCK)
}

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

func successfulVMExecReaderDeps(tracker *fakeVMExecCloseTracker) vmExecConnectionReaderDeps {
	closeFD := unix.Close
	if tracker != nil {
		closeFD = tracker.close
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
		closeFD: closeFD,
	}
}

func (tracker *fakeVMExecCloseTracker) close(fd int) error {
	tracker.mu.Lock()
	tracker.calls[fd]++
	err := tracker.err
	tracker.mu.Unlock()
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func (tracker *fakeVMExecCloseTracker) count(fd int) int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.calls[fd]
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
