package system

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/pkg/errs"
)

const privilegedVMExecSessionHelperTest = "^TestPrivilegedVMExecSessionHelper$"

var testVMExecOpenSource = vmexecsessionwire.OpenSource{
	SourceVMID:      "0123456789abcdef0123456789abcdef",
	DestinationVMID: "fedcba9876543210fedcba9876543210",
}

func TestPrivilegedVMExecSessionUsesFixedProcessAndOrderedFDZeroProtocol(t *testing.T) {
	sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
	destinationDuplicate, closeDestinationPeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeSourcePeer)
	t.Cleanup(closeDestinationPeer)

	deps := realPrivilegedVMExecSessionDeps()
	var gotPath string
	var gotArgs []string
	var command *exec.Cmd
	deps.command = func(path string, args ...string) *exec.Cmd {
		gotPath = path
		gotArgs = append([]string(nil), args...)
		command = vmExecSessionHelperCommand("happy")
		return command
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	session, err := openPrivilegedVMExecSession(
		ctx,
		testVMExecOpenSource,
		sourceDuplicate,
		deps,
		productionPrivilegedVMExecSessionPolicy(),
	)
	require.NoError(t, err)
	require.NotNil(t, session)
	require.NoError(t, session.BindDestination(ctx, destinationDuplicate))
	outcome, err := session.Release(ctx)
	require.NoError(t, err)
	assert.True(t, outcome.Released())
	assert.Nil(t, outcome.OperationError())

	wantArgs := []string{
		"-n", "--", infra.SystemBinaryPath, infra.PrivilegedProtocolMarker, vmexecsessionwire.Action,
	}
	if diff := cmp.Diff("/usr/bin/sudo", gotPath); diff != "" {
		t.Errorf("fixed executable mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantArgs, gotArgs); diff != "" {
		t.Errorf("fixed argv mismatch (-want +got):\n%s", diff)
	}
	require.NotNil(t, command)
	assert.Equal(t, "/", command.Dir)
	if diff := cmp.Diff(privilegedControlEnvironment(), command.Env); diff != "" {
		t.Errorf("fixed environment mismatch (-want +got):\n%s", diff)
	}
	require.NotNil(t, command.SysProcAttr)
	assert.True(t, command.SysProcAttr.Setpgid)
	assert.Empty(t, command.ExtraFiles)
	assert.IsType(t, (*os.File)(nil), command.Stdin)
	assert.ErrorIs(t, sourceDuplicate.Close(), os.ErrClosed)
	assert.ErrorIs(t, destinationDuplicate.Close(), os.ErrClosed)
	require.NotNil(t, command.ProcessState)
	assert.True(t, command.ProcessState.Exited())
}

func TestPrivilegedVMExecSessionReturnsTrustedTerminalFailure(t *testing.T) {
	session, ctx, command := requireReadyVMExecSession(t, "terminal-failure")

	outcome, err := session.Release(ctx)

	require.NoError(t, err)
	assert.False(t, outcome.Released())
	require.NotNil(t, outcome.OperationError())
	assert.Equal(t, errs.CodeUnauthorized, outcome.OperationError().Code)
	require.NotNil(t, command.ProcessState)
}

func TestPrivilegedVMExecSessionTreatsNonzeroWaitAsSecondaryAfterTrustedRelease(t *testing.T) {
	sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeSourcePeer)
	deps, command := vmExecSessionHelperDeps("happy")
	realWait := deps.wait
	deps.wait = func(ctx context.Context, selected *exec.Cmd) error {
		if err := realWait(ctx, selected); err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	session, err := openPrivilegedVMExecSession(
		ctx,
		testVMExecOpenSource,
		sourceDuplicate,
		deps,
		productionPrivilegedVMExecSessionPolicy(),
	)
	require.NoError(t, err)
	destinationDuplicate, closeDestinationPeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeDestinationPeer)
	require.NoError(t, session.BindDestination(ctx, destinationDuplicate))

	outcome, err := session.Release(ctx)

	assert.True(t, outcome.Released())
	require.NoError(t, err)
	require.NotNil(t, (*command).ProcessState)
}

func TestPrivilegedVMExecSessionReturnsTypedEarlyFailures(t *testing.T) {
	t.Run("open source", func(t *testing.T) {
		sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
		t.Cleanup(closeSourcePeer)
		deps, command := vmExecSessionHelperDeps("open-failure")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)

		session, err := openPrivilegedVMExecSession(
			ctx,
			testVMExecOpenSource,
			sourceDuplicate,
			deps,
			productionPrivilegedVMExecSessionPolicy(),
		)

		assert.Nil(t, session)
		assertVMExecSessionOperationError(t, err, errs.CodeUnauthorized)
		assert.ErrorIs(t, sourceDuplicate.Close(), os.ErrClosed)
		require.NotNil(t, (*command).ProcessState)
		assert.True(t, (*command).ProcessState.Exited())
	})

	t.Run("bind destination", func(t *testing.T) {
		session, ctx, command := requireReadyVMExecSessionSource(t, "bind-failure")
		destinationDuplicate, closeDestinationPeer := requireVMExecSessionSocket(t)
		t.Cleanup(closeDestinationPeer)

		err := session.BindDestination(ctx, destinationDuplicate)

		assertVMExecSessionOperationError(t, err, errs.CodeUnauthorized)
		assert.ErrorIs(t, destinationDuplicate.Close(), os.ErrClosed)
		require.NotNil(t, command.ProcessState)
		assert.True(t, command.ProcessState.Exited())
	})

	t.Run("cleanup failure replaces typed operation authority", func(t *testing.T) {
		sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
		t.Cleanup(closeSourcePeer)
		deps, command := vmExecSessionHelperDeps("open-failure")
		realSocketPair := deps.socketPair
		var parent *os.File
		deps.socketPair = func(ctx context.Context) (*os.File, *os.File, error) {
			var child *os.File
			var err error
			parent, child, err = realSocketPair(ctx)
			return parent, child, err
		}
		closeSentinel := errors.New("close control failed")
		realClose := deps.closeFile
		deps.closeFile = func(ctx context.Context, file *os.File) error {
			err := realClose(ctx, file)
			if file == parent {
				return errors.Join(err, closeSentinel)
			}
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)

		session, err := openPrivilegedVMExecSession(
			ctx,
			testVMExecOpenSource,
			sourceDuplicate,
			deps,
			productionPrivilegedVMExecSessionPolicy(),
		)

		assert.Nil(t, session)
		assertVMExecSessionTransportError(t, err, true, false)
		assert.ErrorIs(t, err, closeSentinel)
		require.NotNil(t, (*command).ProcessState)
	})
}

func TestPrivilegedVMExecSessionRejectsUntrustedOpenReplies(t *testing.T) {
	tests := map[string]string{
		"missing EOF":   "open-failure-open",
		"trailing data": "open-trailing",
		"wrong phase":   "open-wrong-phase",
	}
	for name, mode := range tests {
		t.Run(name, func(t *testing.T) {
			sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
			t.Cleanup(closeSourcePeer)
			deps, command := vmExecSessionHelperDeps(mode)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			t.Cleanup(cancel)

			session, err := openPrivilegedVMExecSession(
				ctx,
				testVMExecOpenSource,
				sourceDuplicate,
				deps,
				productionPrivilegedVMExecSessionPolicy(),
			)

			assert.Nil(t, session)
			assertVMExecSessionTransportError(t, err, true, true)
			assert.ErrorIs(t, sourceDuplicate.Close(), os.ErrClosed)
			require.NotNil(t, (*command).ProcessState)
		})
	}
}

func TestPrivilegedVMExecSessionRejectsUntrustedTerminalReplies(t *testing.T) {
	tests := map[string]string{
		"missing EOF":   "terminal-open",
		"trailing data": "terminal-trailing",
		"wrong phase":   "terminal-wrong-phase",
	}
	for name, mode := range tests {
		t.Run(name, func(t *testing.T) {
			session, _, command := requireReadyVMExecSession(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			t.Cleanup(cancel)

			outcome, err := session.Release(ctx)

			assert.False(t, outcome.Released())
			assert.Nil(t, outcome.OperationError())
			assertVMExecSessionTransportError(t, err, true, true)
			require.NotNil(t, command.ProcessState)
		})
	}
}

func TestPrivilegedVMExecSessionReportsPreAndPostStartEvidence(t *testing.T) {
	t.Run("pre-start", func(t *testing.T) {
		sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
		t.Cleanup(closeSourcePeer)
		deps := realPrivilegedVMExecSessionDeps()
		var sourceCloseCount atomic.Int32
		realClose := deps.closeFile
		deps.closeFile = func(ctx context.Context, file *os.File) error {
			if file == sourceDuplicate {
				sourceCloseCount.Add(1)
			}
			return realClose(ctx, file)
		}
		deps.command = func(string, ...string) *exec.Cmd { return nil }

		session, err := openPrivilegedVMExecSession(
			t.Context(),
			testVMExecOpenSource,
			sourceDuplicate,
			deps,
			productionPrivilegedVMExecSessionPolicy(),
		)

		assert.Nil(t, session)
		assertVMExecSessionTransportError(t, err, false, false)
		assert.Equal(t, int32(1), sourceCloseCount.Load())
		assert.ErrorIs(t, sourceDuplicate.Close(), os.ErrClosed)
	})

	t.Run("post-start diagnostics stay non-authoritative", func(t *testing.T) {
		sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
		t.Cleanup(closeSourcePeer)
		deps, command := vmExecSessionHelperDeps("diagnostic-wrong-phase")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		t.Cleanup(cancel)

		session, err := openPrivilegedVMExecSession(
			ctx,
			testVMExecOpenSource,
			sourceDuplicate,
			deps,
			productionPrivilegedVMExecSessionPolicy(),
		)

		assert.Nil(t, session)
		assertVMExecSessionTransportError(t, err, true, true)
		assert.NotContains(t, err.Error(), "diagnostic-secret")
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.NotContains(t, domainErr.Details, "diagnostic-secret")
		require.NotNil(t, (*command).ProcessState)
	})
}

func TestPrivilegedVMExecSessionRejectsOutOfOrderAndRepeatedMethods(t *testing.T) {
	session, ctx, _ := requireReadyVMExecSessionSource(t, "happy")

	_, err := session.Release(ctx)
	assertVMExecSessionTransportError(t, err, true, false)

	destinationDuplicate, closeDestinationPeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeDestinationPeer)
	require.NoError(t, session.BindDestination(ctx, destinationDuplicate))

	repeatedDuplicate, closeRepeatedPeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeRepeatedPeer)
	err = session.BindDestination(ctx, repeatedDuplicate)
	assertVMExecSessionTransportError(t, err, true, false)
	assert.ErrorIs(t, repeatedDuplicate.Close(), os.ErrClosed)

	outcome, err := session.Release(ctx)
	require.NoError(t, err)
	assert.True(t, outcome.Released())
	_, err = session.Release(ctx)
	assertVMExecSessionTransportError(t, err, true, false)
}

func TestPrivilegedVMExecSessionCancelKillsReapsAndCloses(t *testing.T) {
	session, _, command := requireReadyVMExecSession(t, "wait-cancel")
	var killCount atomic.Int32
	realKill := session.shared.deps.killGroup
	session.shared.deps.killGroup = func(ctx context.Context, pid int) error {
		killCount.Add(1)
		return realKill(ctx, pid)
	}

	require.NoError(t, session.Cancel(t.Context()))

	assert.Equal(t, int32(1), killCount.Load())
	require.NotNil(t, command.ProcessState)
	assert.NoError(t, session.Cancel(t.Context()))
}

func TestPrivilegedVMExecSessionCancelUnblocksInFlightRelease(t *testing.T) {
	session, _, command := requireReadyVMExecSession(t, "terminal-open")
	releaseSent := make(chan struct{})
	var releaseSentOnce sync.Once
	realShutdown := session.shared.deps.shutdown
	session.shared.deps.shutdown = func(ctx context.Context, file *os.File, how int) error {
		if how == unix.SHUT_WR {
			releaseSentOnce.Do(func() { close(releaseSent) })
		}
		return realShutdown(ctx, file, how)
	}
	type result struct {
		outcome vmexecsessionwire.Outcome
		err     error
	}
	resultReady := make(chan result, 1)
	go func() {
		outcome, err := session.Release(context.Background())
		resultReady <- result{outcome: outcome, err: err}
	}()

	firstWatchdog := time.NewTimer(time.Second)
	defer firstWatchdog.Stop()
	select {
	case <-releaseSent:
	case <-firstWatchdog.C:
		t.Fatal("release did not reach the terminal read")
	}
	require.NoError(t, session.Cancel(t.Context()))
	secondWatchdog := time.NewTimer(time.Second)
	defer secondWatchdog.Stop()
	select {
	case got := <-resultReady:
		assert.False(t, got.outcome.Released())
		assertVMExecSessionTransportError(t, got.err, true, true)
	case <-secondWatchdog.C:
		t.Fatal("Cancel did not unblock the in-flight release")
	}
	require.NotNil(t, command.ProcessState)
}

func TestPrivilegedVMExecSessionConcurrentReleaseIsPointerSharedAndCloseOnce(t *testing.T) {
	session, ctx, command := requireReadyVMExecSession(t, "release-delay")
	copyOfSession := *session
	var shutdownWriteCount atomic.Int32
	realShutdown := session.shared.deps.shutdown
	session.shared.deps.shutdown = func(ctx context.Context, file *os.File, how int) error {
		if how == unix.SHUT_WR {
			shutdownWriteCount.Add(1)
		}
		return realShutdown(ctx, file, how)
	}

	type result struct {
		outcome vmexecsessionwire.Outcome
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for _, selected := range []*PrivilegedVMExecSession{session, &copyOfSession} {
		go func(current *PrivilegedVMExecSession) {
			defer callers.Done()
			<-start
			outcome, err := current.Release(ctx)
			results <- result{outcome: outcome, err: err}
		}(selected)
	}
	close(start)
	callers.Wait()
	close(results)

	for got := range results {
		require.NoError(t, got.err)
		assert.True(t, got.outcome.Released())
	}
	assert.Equal(t, int32(1), shutdownWriteCount.Load())
	require.NotNil(t, command.ProcessState)
	assert.True(t, command.ProcessState.Exited())
}

func TestPrivilegedVMExecSessionHelper(t *testing.T) {
	mode, ok := vmExecSessionHelperMode(os.Args)
	if !ok {
		return
	}
	control := os.NewFile(0, "vm-exec-session-helper-control")
	if control == nil {
		os.Exit(3)
	}

	sourceFrame, sourceFD := receiveVMExecSessionDuplicate(control)
	if _, err := vmexecsessionwire.DecodeOpenSource(context.Background(), bytes.NewReader(sourceFrame)); err != nil {
		os.Exit(4)
	}
	_ = unix.Close(sourceFD)
	switch mode {
	case "open-failure", "open-failure-open", "open-trailing":
		if err := vmexecsessionwire.EncodeFailure(
			context.Background(),
			control,
			errs.New(errs.CodeUnauthorized, "helper-private"),
		); err != nil {
			os.Exit(30)
		}
		if mode == "open-failure-open" {
			select {}
		}
		if mode == "open-trailing" {
			_, _ = control.Write([]byte("x"))
		}
		_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
		return
	case "open-wrong-phase", "diagnostic-wrong-phase":
		if mode == "diagnostic-wrong-phase" {
			_, _ = os.Stdout.WriteString("diagnostic-secret")
			_, _ = os.Stderr.WriteString("diagnostic-secret")
		}
		_ = vmexecsessionwire.EncodeReady(context.Background(), control)
		_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
		return
	}
	if err := vmexecsessionwire.EncodeSourceRetained(context.Background(), control); err != nil {
		os.Exit(5)
	}

	destinationFrame, destinationFD := receiveVMExecSessionDuplicate(control)
	if err := vmexecsessionwire.DecodeBindDestination(
		context.Background(),
		bytes.NewReader(destinationFrame),
	); err != nil {
		os.Exit(6)
	}
	_ = unix.Close(destinationFD)
	if mode == "bind-failure" {
		_ = vmexecsessionwire.EncodeFailure(
			context.Background(),
			control,
			errs.New(errs.CodeUnauthorized, "helper-private"),
		)
		_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
		return
	}
	if err := vmexecsessionwire.EncodeReady(context.Background(), control); err != nil {
		os.Exit(7)
	}
	if mode == "wait-cancel" {
		_, _ = io.ReadAll(control)
		return
	}
	if err := vmexecsessionwire.DecodeRelease(context.Background(), control); err != nil {
		os.Exit(8)
	}
	if mode == "release-delay" {
		time.Sleep(100 * time.Millisecond)
	}
	if mode == "terminal-wrong-phase" {
		_ = vmexecsessionwire.EncodeReady(context.Background(), control)
		_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
		return
	}
	if mode == "terminal-failure" {
		_ = vmexecsessionwire.EncodeFailure(
			context.Background(),
			control,
			errs.New(errs.CodeUnauthorized, "helper-private"),
		)
		_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
		os.Exit(17)
	}
	if err := vmexecsessionwire.EncodeReleased(context.Background(), control); err != nil {
		os.Exit(9)
	}
	if mode == "terminal-open" {
		select {}
	}
	if mode == "terminal-trailing" {
		_, _ = control.Write([]byte("x"))
	}
	_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
}

func vmExecSessionHelperMode(args []string) (string, bool) {
	for index := range args {
		if args[index] == "--" && index+1 < len(args) {
			return args[index+1], true
		}
	}
	return "", false
}

func vmExecSessionHelperCommand(mode string) *exec.Cmd {
	return exec.Command(os.Args[0], "-test.run="+privilegedVMExecSessionHelperTest, "--", mode)
}

func vmExecSessionHelperDeps(mode string) (privilegedVMExecSessionDeps, **exec.Cmd) {
	deps := realPrivilegedVMExecSessionDeps()
	var command *exec.Cmd
	deps.command = func(string, ...string) *exec.Cmd {
		command = vmExecSessionHelperCommand(mode)
		return command
	}
	return deps, &command
}

func requireReadyVMExecSessionSource(
	t *testing.T,
	mode string,
) (*PrivilegedVMExecSession, context.Context, *exec.Cmd) {
	t.Helper()
	sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeSourcePeer)
	deps, command := vmExecSessionHelperDeps(mode)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	session, err := openPrivilegedVMExecSession(
		ctx,
		testVMExecOpenSource,
		sourceDuplicate,
		deps,
		productionPrivilegedVMExecSessionPolicy(),
	)
	require.NoError(t, err)
	require.NotNil(t, session)
	require.NotNil(t, *command)
	return session, ctx, *command
}

func requireReadyVMExecSession(
	t *testing.T,
	mode string,
) (*PrivilegedVMExecSession, context.Context, *exec.Cmd) {
	t.Helper()
	session, ctx, command := requireReadyVMExecSessionSource(t, mode)
	destinationDuplicate, closeDestinationPeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeDestinationPeer)
	require.NoError(t, session.BindDestination(ctx, destinationDuplicate))
	return session, ctx, command
}

func assertVMExecSessionOperationError(t *testing.T, err error, code errs.Code) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, code, domainErr.Code)
	assert.NotContains(t, domainErr.Message, "helper-private")
}

func assertVMExecSessionTransportError(t *testing.T, err error, started, unknown bool) {
	t.Helper()
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, "retained VM-exec session transport failed", domainErr.Message)
	assert.Equal(t, started, domainErr.Details["process_started"])
	assert.Equal(t, unknown, domainErr.Details["outcome_unknown"])
}

func receiveVMExecSessionDuplicate(control *os.File) ([]byte, int) {
	var carrier [1]byte
	oob := make([]byte, unix.CmsgSpace(4))
	count, oobCount, _, _, err := unix.Recvmsg(int(control.Fd()), carrier[:], oob, 0)
	if err != nil || count != 1 {
		os.Exit(20)
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobCount])
	if err != nil || len(messages) != 1 {
		os.Exit(21)
	}
	rights, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(rights) != 1 {
		os.Exit(22)
	}
	prefix := make([]byte, 14)
	prefix[0] = carrier[0]
	if _, err := io.ReadFull(control, prefix[1:]); err != nil {
		os.Exit(23)
	}
	bodyLength := binary.BigEndian.Uint32(prefix[10:14])
	body := make([]byte, bodyLength)
	if _, err := io.ReadFull(control, body); err != nil {
		os.Exit(24)
	}
	return append(prefix, body...), rights[0]
}

func requireVMExecSessionSocket(t *testing.T) (*os.File, func()) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	duplicate := os.NewFile(uintptr(fds[0]), "vm-exec-session-duplicate")
	peer := os.NewFile(uintptr(fds[1]), "vm-exec-session-peer")
	return duplicate, func() { require.NoError(t, peer.Close()) }
}
