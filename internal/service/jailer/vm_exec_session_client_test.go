package jailer

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const (
	testVMExecClientSourceID      = "0123456789abcdef0123456789abcdef"
	testVMExecClientDestinationID = "fedcba9876543210fedcba9876543210"
)

func TestVMExecSessionClientTransfersDuplicatesAndRetainsOriginalConnections(t *testing.T) {
	source, sourcePeer := requireVMExecClientUnixPair(t)
	destination, destinationPeer := requireVMExecClientUnixPair(t)
	request := validVMExecSessionOpenRequest()
	var sourceDuplicate, destinationDuplicate *os.File
	control := &fakeVMExecSessionControl{
		bind: func(ctx context.Context, duplicate *os.File) error {
			assert.Same(t, t.Context(), ctx)
			destinationDuplicate = duplicate
			assertVMExecClientDuplicate(t, duplicate)
			return nil
		},
		release: func(ctx context.Context) (vmexecsessionwire.Outcome, error) {
			assert.Same(t, t.Context(), ctx)
			assert.NoError(t, sourceDuplicate.Close())
			assert.NoError(t, destinationDuplicate.Close())
			return releasedVMExecClientOutcome(t), nil
		},
	}
	transport := &fakeVMExecSessionTransport{
		open: func(
			ctx context.Context,
			got vmexecsessionwire.OpenSource,
			duplicate *os.File,
		) (vmExecSessionControl, error) {
			assert.Same(t, t.Context(), ctx)
			want := vmexecsessionwire.OpenSource{
				SourceVMID: request.SourceVMID, DestinationVMID: request.DestinationVMID,
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("open request projection mismatch (-want +got):\n%s", diff)
			}
			sourceDuplicate = duplicate
			assertVMExecClientDuplicate(t, duplicate)
			return control, nil
		},
	}
	client := newVMExecSessionClient(transport, vmExecSessionUnixConnectionDuplicator{})

	session, err := client.Open(t.Context(), request, source)
	require.NoError(t, err)
	require.NotNil(t, session)
	assertVMExecClientConnectionUsable(t, source, sourcePeer, "source-original")

	copyOfSession := *session
	require.NoError(t, copyOfSession.BindDestination(t.Context(), destination))
	assertVMExecClientConnectionUsable(t, destination, destinationPeer, "destination-original")
	require.NoError(t, session.Release(t.Context()))
	require.NoError(t, copyOfSession.Cancel(t.Context()))
	assertVMExecClientConnectionUsable(t, source, sourcePeer, "source-after-release")
	assertVMExecClientConnectionUsable(t, destination, destinationPeer, "destination-after-release")

	assert.ErrorIs(t, sourceDuplicate.Close(), os.ErrClosed)
	assert.ErrorIs(t, destinationDuplicate.Close(), os.ErrClosed)
	assert.Equal(t, 1, transport.calls)
	assert.Equal(t, 1, control.bindCalls)
	assert.Equal(t, 1, control.releaseCalls)
	assert.Equal(t, 1, control.cancelCalls)
}

// Rationale: An EOF-qualified sidecar operation error is authoritative and must not be replaced by local setup or
// terminal transport diagnostics.
func TestVMExecSessionClientPreservesOperationErrors(t *testing.T) {
	operationErr := errs.New(
		errs.CodeUnauthorized,
		"VM execution is not authorized",
		errs.WithClass(errs.ClassValidation),
		errs.WithEntity(testVMExecClientSourceID),
	)

	t.Run("open", func(t *testing.T) {
		source, _ := requireVMExecClientUnixPair(t)
		transport := &fakeVMExecSessionTransport{open: func(
			_ context.Context,
			_ vmexecsessionwire.OpenSource,
			duplicate *os.File,
		) (vmExecSessionControl, error) {
			require.NoError(t, duplicate.Close())
			return nil, operationErr
		}}
		client := newVMExecSessionClient(transport, vmExecSessionUnixConnectionDuplicator{})

		session, err := client.Open(t.Context(), validVMExecSessionOpenRequest(), source)

		assert.Nil(t, session)
		assert.Same(t, operationErr, err)
	})

	t.Run("bind destination", func(t *testing.T) {
		destination, _ := requireVMExecClientUnixPair(t)
		control := &fakeVMExecSessionControl{bind: func(_ context.Context, duplicate *os.File) error {
			require.NoError(t, duplicate.Close())
			return operationErr
		}}
		session := &VMExecSession{
			control: control, duplicator: &fakeVMExecSessionDuplicator{file: requireVMExecClientFile(t)},
		}

		err := session.BindDestination(t.Context(), destination)

		assert.Same(t, operationErr, err)
	})

	t.Run("release", func(t *testing.T) {
		outcome := failedVMExecClientOutcome(t, operationErr)
		expected := outcome.OperationError()
		control := &fakeVMExecSessionControl{release: func(context.Context) (vmexecsessionwire.Outcome, error) {
			return outcome, errors.New("secondary cleanup diagnostic")
		}}
		session := &VMExecSession{control: control, duplicator: vmExecSessionUnixConnectionDuplicator{}}

		err := session.Release(t.Context())

		assert.Same(t, expected, err)
	})

	t.Run("released with transport error", func(t *testing.T) {
		transportErr := errors.New("terminal transport sentinel")
		control := &fakeVMExecSessionControl{release: func(context.Context) (vmexecsessionwire.Outcome, error) {
			return releasedVMExecClientOutcome(t), transportErr
		}}
		session := &VMExecSession{control: control, duplicator: vmExecSessionUnixConnectionDuplicator{}}

		err := session.Release(t.Context())

		assert.Same(t, transportErr, err)
	})
}

// Rationale: Admission failures happen before duplication, so the caller's live relay connection remains owned and
// usable by the caller.
func TestVMExecSessionClientRejectsInvalidOpenWithoutDuplicating(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	valid := validVMExecSessionOpenRequest()
	tests := map[string]struct {
		ctx     context.Context
		client  *VMExecSessionClient
		request VMExecSessionOpenRequest
		conn    bool
		cause   error
	}{
		"nil context": {client: &VMExecSessionClient{}, request: valid, conn: true},
		"cancelled context": {
			ctx: cancelled, client: &VMExecSessionClient{}, request: valid, conn: true, cause: context.Canceled,
		},
		"short source ID": {
			ctx:    t.Context(),
			client: &VMExecSessionClient{},
			request: VMExecSessionOpenRequest{
				SourceVMID:      valid.SourceVMID[:31],
				DestinationVMID: valid.DestinationVMID,
			},
			conn: true,
		},
		"nonhex source ID": {
			ctx:    t.Context(),
			client: &VMExecSessionClient{},
			request: VMExecSessionOpenRequest{
				SourceVMID:      strings.Repeat("g", 32),
				DestinationVMID: valid.DestinationVMID,
			},
			conn: true,
		},
		"uppercase destination": {
			ctx:    t.Context(),
			client: &VMExecSessionClient{},
			request: VMExecSessionOpenRequest{
				SourceVMID:      valid.SourceVMID,
				DestinationVMID: strings.ToUpper(valid.DestinationVMID),
			},
			conn: true,
		},
		"same VM": {
			ctx:     t.Context(),
			client:  &VMExecSessionClient{},
			request: VMExecSessionOpenRequest{SourceVMID: valid.SourceVMID, DestinationVMID: valid.SourceVMID},
			conn:    true,
		},
		"nil client":      {ctx: t.Context(), request: valid, conn: true},
		"inactive client": {ctx: t.Context(), client: &VMExecSessionClient{}, request: valid, conn: true},
		"nil connection":  {ctx: t.Context(), client: &VMExecSessionClient{}, request: valid},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			connection, peer := requireVMExecClientUnixPair(t)
			if !test.conn {
				connection = nil
			}
			duplicator := &fakeVMExecSessionDuplicator{}
			transport := &fakeVMExecSessionTransport{}
			if test.client != nil {
				test.client.transport = transport
				test.client.duplicator = duplicator
				if name == "inactive client" {
					test.client.transport = nil
				}
			}

			session, err := test.client.Open(test.ctx, test.request, connection)

			assert.Nil(t, session)
			assert.Error(t, err)
			assert.Zero(t, duplicator.calls.Load())
			assert.Zero(t, transport.calls)
			if test.cause != nil {
				assertVMExecSessionClientCancellation(t, err, test.cause)
			}
			if connection != nil {
				assertVMExecClientConnectionUsable(t, connection, peer, "rejected-original")
			}
		})
	}
}

// Rationale: Cancellation can race the descriptor duplication boundary. A duplicate that has not yet transferred to
// the transport remains client-owned and must be closed.
func TestVMExecSessionClientClosesDuplicateWhenContextCancelsAfterDuplication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source, _ := requireVMExecClientUnixPair(t)
	duplicate := requireVMExecClientFile(t)
	duplicator := &fakeVMExecSessionDuplicator{file: duplicate, hook: cancel}
	transport := &fakeVMExecSessionTransport{}
	client := newVMExecSessionClient(transport, duplicator)

	session, err := client.Open(ctx, validVMExecSessionOpenRequest(), source)

	assert.Nil(t, session)
	assertVMExecSessionClientCancellation(t, err, context.Canceled)
	assert.ErrorIs(t, duplicate.Close(), os.ErrClosed)
	assert.Zero(t, transport.calls)
}

// Rationale: A duplicator can allocate before reporting failure. The client must close that not-yet-transferred file
// and must not expose it to the privileged transport.
func TestVMExecSessionClientClosesDuplicateOnPreTransferFailure(t *testing.T) {
	source, _ := requireVMExecClientUnixPair(t)
	duplicate := requireVMExecClientFile(t)
	duplicateErr := errors.New("duplicate sentinel")
	duplicator := &fakeVMExecSessionDuplicator{file: duplicate, err: duplicateErr}
	transport := &fakeVMExecSessionTransport{}
	client := newVMExecSessionClient(transport, duplicator)

	session, err := client.Open(t.Context(), validVMExecSessionOpenRequest(), source)

	assert.Nil(t, session)
	assert.ErrorIs(t, err, duplicateErr)
	assert.ErrorIs(t, duplicate.Close(), os.ErrClosed)
	assert.Zero(t, transport.calls)
	assert.Equal(t, int32(1), duplicator.calls.Load())
}

// Rationale: An inactive session and rejected destination must not create or leak another descriptor, and context
// cancellation must reach each named control operation without being hidden.
func TestVMExecSessionRejectsInactiveNilAndCancelledOperations(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	destination, _ := requireVMExecClientUnixPair(t)
	duplicator := &fakeVMExecSessionDuplicator{}
	control := &fakeVMExecSessionControl{}
	active := &VMExecSession{control: control, duplicator: duplicator}
	var nilSession *VMExecSession

	tests := map[string]struct {
		call  func() error
		cause error
	}{
		"nil bind context": {call: func() error { return active.BindDestination(nil, destination) }},
		"cancelled bind context": {
			call: func() error { return active.BindDestination(cancelled, destination) }, cause: context.Canceled,
		},
		"nil destination": {call: func() error { return active.BindDestination(t.Context(), nil) }},
		"nil session bind": {
			call: func() error { return nilSession.BindDestination(t.Context(), destination) },
		},
		"nil release context": {call: func() error { return active.Release(nil) }},
		"cancelled release context": {
			call: func() error { return active.Release(cancelled) }, cause: context.Canceled,
		},
		"inactive release":    {call: func() error { return (&VMExecSession{}).Release(t.Context()) }},
		"nil session release": {call: func() error { return nilSession.Release(t.Context()) }},
		"nil cancel context":  {call: func() error { return active.Cancel(nil) }},
		"inactive cancel":     {call: func() error { return (&VMExecSession{}).Cancel(t.Context()) }},
		"nil session cancel":  {call: func() error { return nilSession.Cancel(t.Context()) }},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.call()
			assert.Error(t, err)
			if test.cause != nil {
				assertVMExecSessionClientCancellation(t, err, test.cause)
			}
		})
	}
	assert.Zero(t, duplicator.calls.Load())
	assert.Zero(t, control.bindCalls)
	assert.Zero(t, control.releaseCalls)
	assert.Zero(t, control.cancelCalls)
}

func TestVMExecSessionCancelUsesNonCancelledContextAndPreservesResult(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "caller-value"))
	cancel()
	cancelErr := errors.New("cancel sentinel")
	control := &fakeVMExecSessionControl{cancel: func(got context.Context) error {
		assert.NoError(t, got.Err())
		assert.Equal(t, "caller-value", got.Value(contextKey{}))
		return cancelErr
	}}
	session := &VMExecSession{control: control, duplicator: vmExecSessionUnixConnectionDuplicator{}}

	err := session.Cancel(ctx)

	assert.Same(t, cancelErr, err)
	assert.Equal(t, 1, control.cancelCalls)
}

func TestVMExecSessionReleaseRequiresExactReleasedOutcome(t *testing.T) {
	control := &fakeVMExecSessionControl{release: func(context.Context) (vmexecsessionwire.Outcome, error) {
		return vmexecsessionwire.Outcome{}, nil
	}}
	session := &VMExecSession{control: control, duplicator: vmExecSessionUnixConnectionDuplicator{}}

	err := session.Release(t.Context())

	assert.Error(t, err)
}

type fakeVMExecSessionTransport struct {
	open  func(context.Context, vmexecsessionwire.OpenSource, *os.File) (vmExecSessionControl, error)
	calls int
}

func (transport *fakeVMExecSessionTransport) OpenSource(
	ctx context.Context,
	request vmexecsessionwire.OpenSource,
	duplicate *os.File,
) (vmExecSessionControl, error) {
	transport.calls++
	if transport.open == nil {
		return nil, errors.New("unexpected VM-exec OpenSource call")
	}
	return transport.open(ctx, request, duplicate)
}

type fakeVMExecSessionControl struct {
	bind         func(context.Context, *os.File) error
	release      func(context.Context) (vmexecsessionwire.Outcome, error)
	cancel       func(context.Context) error
	bindCalls    int
	releaseCalls int
	cancelCalls  int
}

func (control *fakeVMExecSessionControl) BindDestination(ctx context.Context, duplicate *os.File) error {
	control.bindCalls++
	if control.bind == nil {
		return nil
	}
	return control.bind(ctx, duplicate)
}

func (control *fakeVMExecSessionControl) Release(ctx context.Context) (vmexecsessionwire.Outcome, error) {
	control.releaseCalls++
	if control.release == nil {
		return vmexecsessionwire.Outcome{}, errors.New("unexpected VM-exec Release call")
	}
	return control.release(ctx)
}

func (control *fakeVMExecSessionControl) Cancel(ctx context.Context) error {
	control.cancelCalls++
	if control.cancel == nil {
		return nil
	}
	return control.cancel(ctx)
}

type fakeVMExecSessionDuplicator struct {
	file  *os.File
	err   error
	hook  func()
	calls atomic.Int32
}

func (duplicator *fakeVMExecSessionDuplicator) Duplicate(
	context.Context,
	*net.UnixConn,
) (*os.File, error) {
	duplicator.calls.Add(1)
	if duplicator.hook != nil {
		duplicator.hook()
	}
	return duplicator.file, duplicator.err
}

func assertVMExecClientDuplicate(t *testing.T, duplicate *os.File) {
	t.Helper()
	t.Cleanup(func() {
		if err := duplicate.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close captured VM-exec duplicate: %v", err)
		}
	})
	flags, err := unix.FcntlInt(duplicate.Fd(), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, flags&unix.FD_CLOEXEC)
}

func assertVMExecSessionClientCancellation(t *testing.T, err error, cause error) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	type projection struct {
		Code  errs.Code
		Class errs.Class
	}
	want := projection{Code: errs.CodeProcessError, Class: errs.ClassInternal}
	got := projection{Code: domainErr.Code, Class: domainErr.Class}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("cancellation error projection mismatch (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, err, cause)
}

func validVMExecSessionOpenRequest() VMExecSessionOpenRequest {
	return VMExecSessionOpenRequest{
		SourceVMID: testVMExecClientSourceID, DestinationVMID: testVMExecClientDestinationID,
	}
}

func releasedVMExecClientOutcome(t *testing.T) vmexecsessionwire.Outcome {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeReleased(t.Context(), &encoded))
	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), bytes.NewReader(encoded.Bytes()))
	require.NoError(t, err)
	return outcome
}

func failedVMExecClientOutcome(t *testing.T, operationErr error) vmexecsessionwire.Outcome {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, vmexecsessionwire.EncodeFailure(t.Context(), &encoded, operationErr))
	outcome, err := vmexecsessionwire.DecodeOutcome(t.Context(), bytes.NewReader(encoded.Bytes()))
	require.NoError(t, err)
	return outcome
}

func requireVMExecClientUnixPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	files := []*os.File{
		os.NewFile(uintptr(descriptors[0]), "vm-exec-client-test-first"),
		os.NewFile(uintptr(descriptors[1]), "vm-exec-client-test-second"),
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, file := range files {
		connection, connectionErr := net.FileConn(file)
		require.NoError(t, connectionErr)
		require.NoError(t, file.Close())
		unixConnection, ok := connection.(*net.UnixConn)
		require.True(t, ok)
		connections = append(connections, unixConnection)
	}
	t.Cleanup(func() {
		for _, connection := range connections {
			assert.NoError(t, connection.Close())
		}
	})
	return connections[0], connections[1]
}

func assertVMExecClientConnectionUsable(
	t *testing.T,
	connection *net.UnixConn,
	peer *net.UnixConn,
	payload string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	require.NoError(t, connection.SetWriteDeadline(deadline))
	require.NoError(t, peer.SetReadDeadline(deadline))
	_, err := connection.Write([]byte(payload))
	require.NoError(t, err)
	got := make([]byte, len(payload))
	_, err = peer.Read(got)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte(payload), got); diff != "" {
		t.Errorf("original connection payload mismatch (-want +got):\n%s", diff)
	}
}

func requireVMExecClientFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "vm-exec-client-duplicate-*")
	require.NoError(t, err)
	return file
}
