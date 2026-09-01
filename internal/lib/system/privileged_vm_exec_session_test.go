package system

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
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

func TestPrivilegedVMExecSessionBoundsReapAndUsesStableProcessKill(t *testing.T) {
	sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeSourcePeer)
	deps, _ := vmExecSessionHelperDeps("happy")
	realWait := deps.wait
	allowWait := make(chan struct{})
	waitFinished := make(chan struct{})
	var allowWaitOnce sync.Once
	var waitCount, stableKillCount, groupKillCount atomic.Int32
	deps.wait = func(ctx context.Context, selected *exec.Cmd) error {
		defer close(waitFinished)
		waitCount.Add(1)
		waitErr := realWait(ctx, selected)
		<-allowWait
		return waitErr
	}
	deps.killProcess = func(context.Context, *os.Process) error {
		stableKillCount.Add(1)
		allowWaitOnce.Do(func() { close(allowWait) })
		return nil
	}
	deps.killGroup = func(context.Context, int) error {
		groupKillCount.Add(1)
		return nil
	}
	policy := productionPrivilegedVMExecSessionPolicy()
	policy.reapTimeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	session, err := openPrivilegedVMExecSession(ctx, testVMExecOpenSource, sourceDuplicate, deps, policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = session.Cancel(cleanupCtx)
	})
	destinationDuplicate, closeDestinationPeer := requireVMExecSessionSocket(t)
	t.Cleanup(closeDestinationPeer)
	require.NoError(t, session.BindDestination(ctx, destinationDuplicate))

	type releaseResult struct {
		outcome vmexecsessionwire.Outcome
		err     error
	}
	releaseDone := make(chan releaseResult, 1)
	go func() {
		outcome, releaseErr := session.Release(ctx)
		releaseDone <- releaseResult{outcome: outcome, err: releaseErr}
	}()
	result := awaitVMExecSessionAsync(
		t,
		releaseDone,
		func() { allowWaitOnce.Do(func() { close(allowWait) }) },
		"Release did not finish after stable process kill",
	)

	assert.True(t, result.outcome.Released())
	assertVMExecSessionTransportError(t, result.err, true, false)
	assert.Equal(t, int32(1), waitCount.Load())
	assert.Equal(t, int32(1), stableKillCount.Load())
	assert.Equal(t, int32(0), groupKillCount.Load())
	requireVMExecSessionSignal(t, waitFinished, "sidecar waiter did not finish after Release")
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
	tests := map[string]struct {
		mode      string
		immediate bool
	}{
		"missing EOF":        {mode: "open-failure-open"},
		"trailing data":      {mode: "open-trailing", immediate: true},
		"wrong phase":        {mode: "open-wrong-phase", immediate: true},
		"malformed response": {mode: "open-malformed", immediate: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
			t.Cleanup(closeSourcePeer)
			deps, command := vmExecSessionHelperDeps(test.mode)
			deadline := 2 * time.Second
			if !test.immediate {
				deadline = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			t.Cleanup(cancel)
			type openResult struct {
				session *PrivilegedVMExecSession
				err     error
			}
			resultReady := make(chan openResult, 1)
			go func() {
				session, err := openPrivilegedVMExecSession(
					ctx,
					testVMExecOpenSource,
					sourceDuplicate,
					deps,
					productionPrivilegedVMExecSessionPolicy(),
				)
				resultReady <- openResult{session: session, err: err}
			}()
			result := awaitVMExecSessionAsync(
				t,
				resultReady,
				cancel,
				"OpenSource did not return after cancellation recovery",
			)

			assert.Nil(t, result.session)
			assertVMExecSessionTransportError(t, result.err, true, true)
			if test.immediate {
				assert.NotErrorIs(t, result.err, context.DeadlineExceeded)
				assert.NotErrorIs(t, result.err, context.Canceled)
			}
			assert.ErrorIs(t, sourceDuplicate.Close(), os.ErrClosed)
			require.NotNil(t, (*command).ProcessState)
		})
	}
}

func TestPrivilegedVMExecSessionRejectsUntrustedTerminalReplies(t *testing.T) {
	tests := map[string]struct {
		mode      string
		immediate bool
	}{
		"missing EOF":        {mode: "terminal-open"},
		"trailing data":      {mode: "terminal-trailing", immediate: true},
		"wrong phase":        {mode: "terminal-wrong-phase", immediate: true},
		"malformed response": {mode: "terminal-malformed", immediate: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			session, _, command := requireReadyVMExecSession(t, test.mode)
			deadline := 2 * time.Second
			if !test.immediate {
				deadline = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			t.Cleanup(cancel)
			type releaseResult struct {
				outcome vmexecsessionwire.Outcome
				err     error
			}
			resultReady := make(chan releaseResult, 1)
			go func() {
				outcome, err := session.Release(ctx)
				resultReady <- releaseResult{outcome: outcome, err: err}
			}()
			result := awaitVMExecSessionAsync(
				t,
				resultReady,
				cancel,
				"Release did not return after cancellation recovery",
			)

			assert.False(t, result.outcome.Released())
			assert.Nil(t, result.outcome.OperationError())
			assertVMExecSessionTransportError(t, result.err, true, true)
			if test.immediate {
				assert.NotErrorIs(t, result.err, context.DeadlineExceeded)
				assert.NotErrorIs(t, result.err, context.Canceled)
			}
			require.NotNil(t, command.ProcessState)
		})
	}
}

func TestPrivilegedVMExecSessionReportsPreAndPostStartEvidence(t *testing.T) {
	t.Run("start failure closes every owned file once", func(t *testing.T) {
		sourceDuplicate, closeSourcePeer := requireVMExecSessionSocket(t)
		t.Cleanup(closeSourcePeer)
		deps := realPrivilegedVMExecSessionDeps()
		realSocketPair := deps.socketPair
		var parent, child *os.File
		deps.socketPair = func(ctx context.Context) (*os.File, *os.File, error) {
			var err error
			parent, child, err = realSocketPair(ctx)
			return parent, child, err
		}
		closeCounts := make(map[*os.File]int)
		var closeMu sync.Mutex
		realClose := deps.closeFile
		deps.closeFile = func(ctx context.Context, file *os.File) error {
			closeMu.Lock()
			closeCounts[file]++
			closeMu.Unlock()
			return realClose(ctx, file)
		}
		deps.command = func(string, ...string) *exec.Cmd { return exec.Command("/bin/true") }
		startSentinel := errors.New("start failed")
		deps.start = func(context.Context, *exec.Cmd) error { return startSentinel }

		session, err := openPrivilegedVMExecSession(
			t.Context(),
			testVMExecOpenSource,
			sourceDuplicate,
			deps,
			productionPrivilegedVMExecSessionPolicy(),
		)

		assert.Nil(t, session)
		assertVMExecSessionTransportError(t, err, false, false)
		assert.ErrorIs(t, err, startSentinel)
		closeMu.Lock()
		gotCloseCounts := map[string]int{
			"source": closeCounts[sourceDuplicate],
			"parent": closeCounts[parent],
			"child":  closeCounts[child],
		}
		closeMu.Unlock()
		wantCloseCounts := map[string]int{"source": 1, "parent": 1, "child": 1}
		if diff := cmp.Diff(wantCloseCounts, gotCloseCounts); diff != "" {
			t.Errorf("pre-start close counts mismatch (-want +got):\n%s", diff)
		}
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
		assert.NotContains(t, domainErr.Message, "diagnostic-secret")
		assert.NotContains(t, formatVMExecSessionDetails(domainErr.Details), "diagnostic-secret")
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
	var controlCloseCount atomic.Int32
	session.shared.lifecycleMu.Lock()
	control := session.shared.control
	session.shared.lifecycleMu.Unlock()
	realKill := session.shared.deps.killGroup
	realClose := session.shared.deps.closeFile
	session.shared.deps.killGroup = func(ctx context.Context, pid int) error {
		killCount.Add(1)
		return realKill(ctx, pid)
	}
	session.shared.deps.closeFile = func(ctx context.Context, file *os.File) error {
		if file == control {
			controlCloseCount.Add(1)
		}
		return realClose(ctx, file)
	}

	require.NoError(t, session.Cancel(t.Context()))

	assert.Equal(t, int32(1), killCount.Load())
	assert.Equal(t, int32(1), controlCloseCount.Load())
	require.NotNil(t, command.ProcessState)
	assert.NoError(t, session.Cancel(t.Context()))
	assert.Equal(t, int32(1), killCount.Load())
	assert.Equal(t, int32(1), controlCloseCount.Load())
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

func TestPrivilegedVMExecSessionPhasePinSurvivesCancelAndOriginalFDReuse(t *testing.T) {
	session, _, command := requireReadyVMExecSession(t, "terminal-open")
	session.shared.lifecycleMu.Lock()
	original := session.shared.control
	originalFD := int(original.Fd())
	session.shared.lifecycleMu.Unlock()
	replacementSource, err := os.Open("/dev/null")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replacementSource.Close()) })

	var pinned atomic.Pointer[os.File]
	var pinFD atomic.Int64
	pinReady := make(chan struct{})
	allowPin := make(chan struct{})
	var allowPinOnce sync.Once
	realDuplicate := session.shared.deps.duplicateControl
	session.shared.deps.duplicateControl = func(ctx context.Context, control *os.File) (*os.File, error) {
		pin, duplicateErr := realDuplicate(ctx, control)
		if duplicateErr != nil {
			return nil, duplicateErr
		}
		pinned.Store(pin)
		pinFD.Store(int64(pin.Fd()))
		close(pinReady)
		<-allowPin
		return pin, nil
	}
	var pinCloseCount, originalCloseCount atomic.Int32
	var replacement *os.File
	replacementReady := make(chan struct{})
	realClose := session.shared.deps.closeFile
	session.shared.deps.closeFile = func(ctx context.Context, file *os.File) error {
		if file == pinned.Load() {
			pinCloseCount.Add(1)
		}
		if file != original {
			return realClose(ctx, file)
		}
		originalCloseCount.Add(1)
		closeErr := realClose(ctx, file)
		replacementErr := unix.Dup3(int(replacementSource.Fd()), originalFD, unix.O_CLOEXEC)
		if replacementErr == nil {
			replacement = os.NewFile(uintptr(originalFD), "vm-exec-session-reused-control-fd")
		}
		close(replacementReady)
		return errors.Join(closeErr, replacementErr)
	}

	type releaseResult struct {
		outcome vmexecsessionwire.Outcome
		err     error
	}
	releaseDone := make(chan releaseResult, 1)
	go func() {
		outcome, releaseErr := session.Release(context.Background())
		releaseDone <- releaseResult{outcome: outcome, err: releaseErr}
	}()
	if !waitVMExecSessionSignal(pinReady) {
		allowPinOnce.Do(func() { close(allowPin) })
		_ = awaitVMExecSessionAsync(t, releaseDone, func() {}, "Release did not recover from pin barrier")
		t.Fatal("Release did not acquire a phase pin")
	}
	assert.NotEqual(t, int64(originalFD), pinFD.Load())
	session.shared.mu.Lock()
	phase := session.shared.activePhase
	session.shared.mu.Unlock()
	require.NotNil(t, phase)
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- session.Cancel(context.Background()) }()
	if !waitVMExecSessionCondition(func() bool {
		return phase.winner.Load() == privilegedVMExecSessionPhaseInterrupted
	}) {
		allowPinOnce.Do(func() { close(allowPin) })
		_ = awaitVMExecSessionAsync(t, cancelDone, func() {}, "Cancel did not recover from pin barrier")
		_ = awaitVMExecSessionAsync(t, releaseDone, func() {}, "Release did not recover from pin barrier")
		t.Fatal("Cancel did not win phase arbitration before shared-control shutdown")
	}
	allowPinOnce.Do(func() { close(allowPin) })
	cancelErr := awaitVMExecSessionAsync(t, cancelDone, func() {}, "Cancel did not finish")
	result := awaitVMExecSessionAsync(t, releaseDone, func() {}, "Release did not finish after Cancel")
	require.NoError(t, cancelErr)
	assert.False(t, result.outcome.Released())
	assertVMExecSessionTransportError(t, result.err, true, true)
	requireVMExecSessionSignal(t, replacementReady, "shared original was not closed")
	require.NotNil(t, replacement)
	_, err = unix.FcntlInt(replacement.Fd(), unix.F_GETFD, 0)
	require.NoError(t, err, "phase activity was misdirected to the reused original descriptor number")
	require.NoError(t, replacement.Close())
	assert.Equal(t, int32(1), originalCloseCount.Load())
	assert.Equal(t, int32(1), pinCloseCount.Load())
	require.NotNil(t, command.ProcessState)
}

func TestPrivilegedVMExecSessionConcurrentReleaseIsPointerSharedAndCloseOnce(t *testing.T) {
	session, ctx, command := requireReadyVMExecSession(t, "happy")
	copyOfSession := *session
	var shutdownWriteCount atomic.Int32
	releaseAtShutdown := make(chan struct{})
	allowRelease := make(chan struct{})
	var allowReleaseOnce sync.Once
	realShutdown := session.shared.deps.shutdown
	session.shared.deps.shutdown = func(ctx context.Context, file *os.File, how int) error {
		if how == unix.SHUT_WR {
			shutdownWriteCount.Add(1)
			close(releaseAtShutdown)
			<-allowRelease
		}
		return realShutdown(ctx, file, how)
	}

	type result struct {
		outcome vmexecsessionwire.Outcome
		err     error
	}
	results := make(chan result, 2)
	go func() {
		outcome, err := session.Release(ctx)
		results <- result{outcome: outcome, err: err}
	}()
	if !waitVMExecSessionSignal(releaseAtShutdown) {
		allowReleaseOnce.Do(func() { close(allowRelease) })
		_ = awaitVMExecSessionAsync(t, results, func() {}, "first Release did not recover")
		t.Fatal("first Release did not reach the shutdown barrier")
	}
	secondObserved := make(chan struct{})
	secondCtx := &vmExecSessionObservedDoneContext{Context: ctx, observed: secondObserved}
	go func() {
		outcome, err := copyOfSession.Release(secondCtx)
		results <- result{outcome: outcome, err: err}
	}()
	if !waitVMExecSessionSignal(secondObserved) {
		allowReleaseOnce.Do(func() { close(allowRelease) })
		_ = awaitVMExecSessionAsync(t, results, func() {}, "first Release did not recover")
		_ = awaitVMExecSessionAsync(t, results, func() {}, "second Release did not recover")
		t.Fatal("second Release did not enter the shared in-flight wait")
	}
	allowReleaseOnce.Do(func() { close(allowRelease) })

	for range 2 {
		got := awaitVMExecSessionAsync(t, results, func() {}, "concurrent Release did not finish")
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
	case "open-malformed":
		_, _ = control.Write([]byte("malformed"))
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
	if mode == "terminal-wrong-phase" {
		_ = vmexecsessionwire.EncodeReady(context.Background(), control)
		_ = unix.Shutdown(int(control.Fd()), unix.SHUT_WR)
		return
	}
	if mode == "terminal-malformed" {
		_, _ = control.Write([]byte("malformed"))
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
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = session.Cancel(cleanupCtx)
	})
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
	type projection struct {
		Code    errs.Code
		Class   errs.Class
		Message string
		Details map[string]any
	}
	want := projection{
		Code:    errs.CodeProcessError,
		Class:   errs.ClassInternal,
		Message: "retained VM-exec session transport failed",
		Details: map[string]any{"process_started": started, "outcome_unknown": unknown},
	}
	got := projection{
		Code: domainErr.Code, Class: domainErr.Class, Message: domainErr.Message, Details: domainErr.Details,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("transport error projection mismatch (-want +got):\n%s", diff)
	}
}

func formatVMExecSessionDetails(details map[string]any) string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var value bytes.Buffer
	for _, key := range keys {
		_, _ = fmt.Fprintf(&value, "%s=%v;", key, details[key])
	}
	return value.String()
}

func awaitVMExecSessionAsync[T any](
	t *testing.T,
	result <-chan T,
	recoverOperation func(),
	description string,
) T {
	t.Helper()
	watchdog := time.NewTimer(2 * time.Second)
	defer watchdog.Stop()
	select {
	case value := <-result:
		return value
	case <-watchdog.C:
		recoverOperation()
	}
	recoveryWatchdog := time.NewTimer(2 * time.Second)
	defer recoveryWatchdog.Stop()
	select {
	case <-result:
		t.Fatalf("%s; recovery completed", description)
	case <-recoveryWatchdog.C:
		t.Fatalf("%s; recovery also failed", description)
	}
	var zero T
	return zero
}

func waitVMExecSessionSignal(signal <-chan struct{}) bool {
	watchdog := time.NewTimer(2 * time.Second)
	defer watchdog.Stop()
	select {
	case <-signal:
		return true
	case <-watchdog.C:
		return false
	}
}

func requireVMExecSessionSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	if !waitVMExecSessionSignal(signal) {
		t.Fatal(description)
	}
}

func waitVMExecSessionCondition(condition func() bool) bool {
	watchdog := time.NewTimer(2 * time.Second)
	defer watchdog.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		if condition() {
			return true
		}
		select {
		case <-poll.C:
		case <-watchdog.C:
			return false
		}
	}
}

type vmExecSessionObservedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *vmExecSessionObservedDoneContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
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
