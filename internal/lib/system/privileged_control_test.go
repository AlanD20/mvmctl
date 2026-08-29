package system

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const privilegedControlHelperTest = "^TestPrivilegedControlHelper$"

func TestPrivilegedControlTransportUsesFixedCommandAndFDZeroDuplex(t *testing.T) {
	requestFrame := []byte("request-frame")
	request, err := NewPrivilegedControlRequest("test-duplex", requestFrame, 0, nil)
	require.NoError(t, err)
	requestFrame[0] = 'X'

	deps := realPrivilegedControlDeps()
	var capturedCommand *exec.Cmd
	var waitCount, closeCount atomic.Int32
	realWait := deps.wait
	realClose := deps.closeFile
	deps.command = func(path string, args ...string) *exec.Cmd {
		assert.Equal(t, "/usr/bin/sudo", path)
		assert.Equal(t, []string{
			"-n",
			"--",
			infra.SystemBinaryPath,
			"__mvm_privileged_v1",
			"test-duplex",
		}, args)
		capturedCommand = privilegedControlHelperCommand("test-duplex")
		return capturedCommand
	}
	deps.wait = func(ctx context.Context, command *exec.Cmd) error {
		waitCount.Add(1)
		return realWait(ctx, command)
	}
	deps.closeFile = func(ctx context.Context, file *os.File) error {
		closeCount.Add(1)
		return realClose(ctx, file)
	}
	policy := productionPrivilegedControlPolicy()
	policy.postResponseReapTimeout = 5 * time.Second
	policy.postExitDrainTimeout = 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	outcome := exchangePrivilegedControl(ctx, request, deps, policy)
	if outcome.Diagnostic() != nil {
		t.Logf("transport diagnostic: %#v / %v", outcome.Diagnostic(), outcome.Diagnostic().Err)
	}

	assert.True(t, outcome.ProcessStarted())
	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:request-frame"), outcome.Response())
	assert.Nil(t, outcome.Diagnostic())
	assert.Contains(t, string(outcome.DiagnosticStdout()), "stdout-poison")
	assert.Contains(t, string(outcome.DiagnosticStderr()), "stderr-poison")
	assert.NotContains(t, string(outcome.Response()), "poison")
	require.NotNil(t, capturedCommand)
	assert.Equal(t, "/", capturedCommand.Dir)
	assert.Equal(t, privilegedControlEnvironment(), capturedCommand.Env)
	assert.Empty(t, capturedCommand.ExtraFiles)
	require.NotNil(t, capturedCommand.SysProcAttr)
	assert.True(t, capturedCommand.SysProcAttr.Setpgid)
	assert.Equal(t, int32(1), waitCount.Load())
	assert.Equal(t, int32(2), closeCount.Load())
}

func TestOpenPrivilegedControlSocketPairSetsCloseOnExec(t *testing.T) {
	parent, child, err := openPrivilegedControlSocketPair(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, parent.Close()) })
	t.Cleanup(func() { require.NoError(t, child.Close()) })

	for name, endpoint := range map[string]*os.File{"parent": parent, "child": child} {
		t.Run(name, func(t *testing.T) {
			flags, err := unix.FcntlInt(endpoint.Fd(), unix.F_GETFD, 0)
			require.NoError(t, err)
			assert.NotZero(t, flags&unix.FD_CLOEXEC)
		})
	}
}

func TestPrivilegedControlTransportRejectsNilCommandBeforeStart(t *testing.T) {
	request, err := NewPrivilegedControlRequest("test-nil-command", []byte("request"), 0, nil)
	require.NoError(t, err)

	deps := realPrivilegedControlDeps()
	deps.command = func(_ string, _ ...string) *exec.Cmd { return nil }

	assert.NotPanics(t, func() {
		outcome := exchangePrivilegedControl(
			context.Background(),
			request,
			deps,
			productionPrivilegedControlPolicy(),
		)
		assert.False(t, outcome.ProcessStarted())
		assert.False(t, outcome.ResponseEOF())
		assert.NotNil(t, outcome.Diagnostic())
		assert.Equal(t, false, outcome.Diagnostic().Details["outcome_unknown"])
	})
}

func TestPrivilegedControlTransportRejectsNilSocketEndpointBeforeCommand(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload(nil)
	request, err := NewPrivilegedControlRequest("test-nil-socket", []byte("request"), 0, payload)
	require.NoError(t, err)
	deps := realPrivilegedControlDeps()
	parent, peer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	deps.socketPair = func(_ context.Context) (*os.File, *os.File, error) { return parent, nil, nil }
	var commandCount atomic.Int32
	var closeCount atomic.Int32
	deps.command = func(_ string, _ ...string) *exec.Cmd {
		commandCount.Add(1)
		return nil
	}
	realClose := deps.closeFile
	deps.closeFile = func(ctx context.Context, file *os.File) error {
		closeCount.Add(1)
		return realClose(ctx, file)
	}

	outcome := exchangePrivilegedControl(
		context.Background(),
		request,
		deps,
		productionPrivilegedControlPolicy(),
	)

	assert.False(t, outcome.ProcessStarted())
	assert.NotNil(t, outcome.Diagnostic())
	assert.Equal(t, int32(0), commandCount.Load())
	assert.Equal(t, int32(1), payload.CloseCount())
	assert.Equal(t, int32(1), closeCount.Load())
}

func TestPrivilegedControlTransportSocketPairFailureClosesReturnedEndpoints(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload(nil)
	request, err := NewPrivilegedControlRequest("test-socket-error", []byte("request"), 0, payload)
	require.NoError(t, err)
	parent, child, err := os.Pipe()
	require.NoError(t, err)
	socketErr := errors.New("socket pair failed")
	deps := realPrivilegedControlDeps()
	deps.socketPair = func(_ context.Context) (*os.File, *os.File, error) {
		return parent, child, socketErr
	}
	var closeCount atomic.Int32
	realClose := deps.closeFile
	deps.closeFile = func(ctx context.Context, file *os.File) error {
		closeCount.Add(1)
		return realClose(ctx, file)
	}

	outcome := exchangePrivilegedControl(
		context.Background(),
		request,
		deps,
		productionPrivilegedControlPolicy(),
	)

	assert.False(t, outcome.ProcessStarted())
	require.NotNil(t, outcome.Diagnostic())
	assert.ErrorIs(t, outcome.Diagnostic(), socketErr)
	assert.Equal(t, int32(1), payload.CloseCount())
	assert.Equal(t, int32(2), closeCount.Load())
}

func TestNewPrivilegedControlRequestRejectsUnboundedOrInvalidInput(t *testing.T) {
	tests := map[string]struct {
		action        string
		frame         []byte
		payloadLength uint64
		payload       io.ReadCloser
	}{
		"empty_action":      {action: "", frame: []byte("frame")},
		"path_action":       {action: "../launch", frame: []byte("frame")},
		"oversized_action":  {action: strings.Repeat("a", 65), frame: []byte("frame")},
		"empty_frame":       {action: "launch"},
		"oversized_frame":   {action: "launch", frame: make([]byte, privilegedControlMaxRequestFrameBytes+1)},
		"oversized_payload": {action: "launch", frame: []byte("frame"), payloadLength: 128*1024*1024 + 1},
		"missing_payload":   {action: "launch", frame: []byte("frame"), payloadLength: 1},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request, err := NewPrivilegedControlRequest(test.action, test.frame, test.payloadLength, test.payload)
			assert.Nil(t, request)
			assert.Error(t, err)
		})
	}
}

func TestRejectedPrivilegedControlRequestRetainsCallerPayloadOwnership(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload(nil)

	request, err := NewPrivilegedControlRequest("../invalid", []byte("frame"), 0, payload)

	assert.Nil(t, request)
	assert.Error(t, err)
	assert.Zero(t, payload.CloseCount())
}

func TestWritePrivilegedControlExactHandlesPartialAndZeroWrites(t *testing.T) {
	t.Run("partial", func(t *testing.T) {
		writer := &privilegedControlPartialWriter{maximum: 1}

		err := writePrivilegedControlExact(context.Background(), writer, []byte("frame"))

		assert.NoError(t, err)
		assert.Equal(t, []byte("frame"), writer.value.Bytes())
	})

	t.Run("zero", func(t *testing.T) {
		err := writePrivilegedControlExact(context.Background(), privilegedControlZeroWriter{}, []byte("frame"))

		assert.ErrorIs(t, err, io.ErrShortWrite)
	})
}

func TestPrivilegedControlTransportUploadsExactPayloadAndClosesItOnce(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload([]byte("payload-data"))
	request, err := NewPrivilegedControlRequest("test-payload", []byte("header:"), 12, payload)
	require.NoError(t, err)

	outcome := runPrivilegedControlHelper(t, request, "test-payload", nil)

	assert.True(t, outcome.ProcessStarted())
	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:header:payload-data"), outcome.Response())
	assert.Nil(t, outcome.Diagnostic())
	assert.Equal(t, int32(1), payload.CloseCount())
}

func TestPrivilegedControlTransportPreservesEarlyEOFResponseAndProcessDiagnostics(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload(repeatedPrivilegedControlBytes("p", 1024*1024))
	request, err := NewPrivilegedControlRequest("test-early-response", []byte("header:"), 1024*1024, payload)
	require.NoError(t, err)

	outcome := runPrivilegedControlHelper(t, request, "test-early-response", nil)

	assert.True(t, outcome.ProcessStarted())
	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:early"), outcome.Response())
	assert.NotNil(t, outcome.Diagnostic())
	assert.Equal(t, true, outcome.Diagnostic().Details["outcome_unknown"])
	assert.Equal(t, int32(1), payload.CloseCount())
}

func TestPrivilegedControlTransportFinalEOFClosesBlockedPayloadRead(t *testing.T) {
	payload := newPrivilegedControlBlockingPayload()
	request, err := NewPrivilegedControlRequest("test-final-without-read", []byte("request"), 64, payload)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	outcome := runPrivilegedControlHelperWithContext(t, ctx, request, "test-final-without-read", nil)

	assert.True(t, outcome.ProcessStarted())
	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:final"), outcome.Response())
	require.NotNil(t, outcome.Diagnostic())
	assert.NotErrorIs(t, outcome.Diagnostic(), context.DeadlineExceeded)
	assert.ErrorIs(t, outcome.Diagnostic(), io.ErrClosedPipe)
	assert.Equal(t, int32(1), payload.CloseCount())
}

func TestPrivilegedControlTransportFinalEOFStopsBlockedSocketUpload(t *testing.T) {
	payload := io.NopCloser(io.LimitReader(
		privilegedControlZeroReader{},
		int64(privilegedControlMaxPayloadBytes),
	))
	request, err := NewPrivilegedControlRequest(
		"test-final-stops-upload",
		[]byte("request"),
		privilegedControlMaxPayloadBytes,
		payload,
	)
	require.NoError(t, err)

	outcome := runPrivilegedControlHelper(t, request, "test-final-stops-upload", nil)

	assert.True(t, outcome.ProcessStarted())
	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:final"), outcome.Response())
	assert.NotContains(t, string(outcome.DiagnosticStderr()), "missing local write shutdown")
}

func TestPrivilegedControlTransportNeverFabricatesResponseEOFOnCancellation(t *testing.T) {
	request, err := NewPrivilegedControlRequest("test-non-eof", []byte("request"), 0, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)

	outcome := runPrivilegedControlHelperWithContext(t, ctx, request, "test-non-eof", nil)

	assert.True(t, outcome.ProcessStarted())
	assert.False(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:partial"), outcome.Response())
	assert.NotNil(t, outcome.Diagnostic())
	assert.Equal(t, true, outcome.Diagnostic().Details["outcome_unknown"])
}

func TestPrivilegedControlTransportBoundsResponseAndDiagnostics(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		request, err := NewPrivilegedControlRequest("test-overflow", []byte("request"), 0, nil)
		require.NoError(t, err)
		policy := productionPrivilegedControlPolicy()
		policy.maxResponseBytes = 32

		outcome := runPrivilegedControlHelper(t, request, "test-overflow", &policy)

		assert.False(t, outcome.ResponseEOF())
		assert.Len(t, outcome.Response(), 33)
		assert.NotNil(t, outcome.Diagnostic())
	})

	t.Run("stdout_stderr", func(t *testing.T) {
		request, err := NewPrivilegedControlRequest("test-diagnostic-bound", []byte("request"), 0, nil)
		require.NoError(t, err)
		policy := productionPrivilegedControlPolicy()
		policy.maxDiagnosticBytes = 16

		outcome := runPrivilegedControlHelper(t, request, "test-diagnostic-bound", &policy)

		assert.True(t, outcome.ResponseEOF())
		assert.Equal(t, []byte("response:bounded"), outcome.Response())
		assert.Len(t, outcome.DiagnosticStdout(), 16)
		assert.Len(t, outcome.DiagnosticStderr(), 16)
		assert.True(t, outcome.DiagnosticStdoutTruncated())
		assert.True(t, outcome.DiagnosticStderrTruncated())
	})

	t.Run("exact_response_with_eof", func(t *testing.T) {
		request, err := NewPrivilegedControlRequest("test-exact-response-eof", []byte("request"), 0, nil)
		require.NoError(t, err)
		policy := productionPrivilegedControlPolicy()
		policy.maxResponseBytes = 32

		outcome := runPrivilegedControlHelper(t, request, "test-exact-response-eof", &policy)

		assert.True(t, outcome.ResponseEOF())
		assert.Len(t, outcome.Response(), 32)
		assert.Nil(t, outcome.Diagnostic())
	})

	t.Run("exact_response_without_eof", func(t *testing.T) {
		request, err := NewPrivilegedControlRequest("test-exact-response-open", []byte("request"), 0, nil)
		require.NoError(t, err)
		policy := productionPrivilegedControlPolicy()
		policy.maxResponseBytes = 32
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		t.Cleanup(cancel)

		outcome := runPrivilegedControlHelperWithContext(
			t,
			ctx,
			request,
			"test-exact-response-open",
			&policy,
		)

		assert.False(t, outcome.ResponseEOF())
		assert.Len(t, outcome.Response(), 32)
		assert.NotNil(t, outcome.Diagnostic())
	})
}

func TestPrivilegedControlTransportHalfClosesAfterShortDeclaredPayload(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload([]byte("tiny"))
	request, err := NewPrivilegedControlRequest("test-short-payload", []byte("header:"), 16, payload)
	require.NoError(t, err)

	outcome := runPrivilegedControlHelper(t, request, "test-short-payload", nil)

	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("observed:header:tiny"), outcome.Response())
	require.NotNil(t, outcome.Diagnostic())
	assert.ErrorIs(t, outcome.Diagnostic(), io.EOF)
	assert.Equal(t, int32(1), payload.CloseCount())
}

func TestPrivilegedControlTransportStartFailureClosesOwnedResourcesWithoutWait(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload(nil)
	request, err := NewPrivilegedControlRequest("test-start-failure", []byte("request"), 0, payload)
	require.NoError(t, err)

	deps := realPrivilegedControlDeps()
	deps.command = func(_ string, _ ...string) *exec.Cmd {
		return privilegedControlHelperCommand("test-start-failure")
	}
	startErr := errors.New("start failed")
	deps.start = func(_ context.Context, _ *exec.Cmd) error { return startErr }
	var waitCount atomic.Int32
	deps.wait = func(_ context.Context, _ *exec.Cmd) error {
		waitCount.Add(1)
		return nil
	}
	var closeCount atomic.Int32
	realClose := deps.closeFile
	deps.closeFile = func(ctx context.Context, file *os.File) error {
		closeCount.Add(1)
		return realClose(ctx, file)
	}

	outcome := exchangePrivilegedControl(
		context.Background(),
		request,
		deps,
		productionPrivilegedControlPolicy(),
	)

	assert.False(t, outcome.ProcessStarted())
	assert.False(t, outcome.ResponseEOF())
	require.NotNil(t, outcome.Diagnostic())
	assert.ErrorIs(t, outcome.Diagnostic(), startErr)
	assert.Equal(t, false, outcome.Diagnostic().Details["outcome_unknown"])
	assert.Equal(t, false, outcome.Diagnostic().Details["process_started"])
	assert.Equal(t, int32(1), payload.CloseCount())
	assert.Equal(t, int32(0), waitCount.Load())
	assert.Equal(t, int32(2), closeCount.Load())
}

func TestPrivilegedControlTransportCancellationClosesPayloadKillsAndWaitsOnce(t *testing.T) {
	payload := newPrivilegedControlBlockingPayload()
	request, err := NewPrivilegedControlRequest("test-cancel", []byte("request"), 64, payload)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)

	deps := privilegedControlHelperDeps(t, "test-cancel")
	var killCount atomic.Int32
	realKill := deps.killGroup
	deps.killGroup = func(ctx context.Context, pid int) error {
		killCount.Add(1)
		return realKill(ctx, pid)
	}
	var waitCount atomic.Int32
	realWait := deps.wait
	deps.wait = func(ctx context.Context, command *exec.Cmd) error {
		waitCount.Add(1)
		return realWait(ctx, command)
	}

	outcome := exchangePrivilegedControl(ctx, request, deps, productionPrivilegedControlPolicy())

	assert.True(t, outcome.ProcessStarted())
	assert.False(t, outcome.ResponseEOF())
	require.NotNil(t, outcome.Diagnostic())
	assert.Equal(t, true, outcome.Diagnostic().Details["outcome_unknown"])
	assert.Equal(t, true, outcome.Diagnostic().Details["process_started"])
	assert.Equal(t, int32(1), payload.CloseCount())
	assert.Equal(t, int32(1), killCount.Load())
	assert.Equal(t, int32(1), waitCount.Load())
}

func TestPrivilegedControlTransportKillsSameGroupSocketHolderAfterProcessExit(t *testing.T) {
	request, err := NewPrivilegedControlRequest("test-socket-holder", []byte("request"), 0, nil)
	require.NoError(t, err)
	policy := productionPrivilegedControlPolicy()
	policy.postExitDrainTimeout = 50 * time.Millisecond
	deps := privilegedControlHelperDeps(t, "test-socket-holder")
	var killCount atomic.Int32
	realKill := deps.killGroup
	deps.killGroup = func(ctx context.Context, pid int) error {
		killCount.Add(1)
		return realKill(ctx, pid)
	}

	outcome := exchangePrivilegedControl(context.Background(), request, deps, policy)
	require.True(t, outcome.ProcessStarted())
	require.False(t, outcome.ResponseEOF())
	require.True(t, strings.HasPrefix(string(outcome.Response()), "holder-pid:"))
	holderPID, err := strconv.Atoi(strings.TrimPrefix(string(outcome.Response()), "holder-pid:"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Kill(holderPID, unix.SIGKILL) })

	assert.Equal(t, int32(1), killCount.Load())
	assert.Eventually(t, func() bool {
		return errors.Is(unix.Kill(holderPID, 0), unix.ESRCH)
	}, time.Second, 10*time.Millisecond)
}

func TestPrivilegedControlTransportPreservesEOFWhenReapDeadlineKillsProcess(t *testing.T) {
	request, err := NewPrivilegedControlRequest("test-response-reap", []byte("request"), 0, nil)
	require.NoError(t, err)
	policy := productionPrivilegedControlPolicy()
	policy.postResponseReapTimeout = 50 * time.Millisecond
	deps := privilegedControlHelperDeps(t, "test-response-reap")
	var killCount, waitCount atomic.Int32
	realKill := deps.killGroup
	realWait := deps.wait
	killErr := errors.New("injected kill failure")
	deps.killGroup = func(ctx context.Context, pid int) error {
		killCount.Add(1)
		if err := realKill(ctx, pid); err != nil {
			return err
		}
		return killErr
	}
	deps.wait = func(ctx context.Context, command *exec.Cmd) error {
		waitCount.Add(1)
		return realWait(ctx, command)
	}

	outcome := exchangePrivilegedControl(context.Background(), request, deps, policy)

	assert.True(t, outcome.ProcessStarted())
	assert.True(t, outcome.ResponseEOF())
	assert.Equal(t, []byte("response:complete"), outcome.Response())
	assert.NotNil(t, outcome.Diagnostic())
	assert.ErrorIs(t, outcome.Diagnostic(), killErr)
	assert.Equal(t, int32(1), killCount.Load())
	assert.Equal(t, int32(1), waitCount.Load())
}

func TestPrivilegedControlTransportReturnsWhenInterruptedProcessCannotBeReaped(t *testing.T) {
	// Rationale: The normal user may be unable to signal a sudo child after it
	// becomes root. A failed process-group kill must not block the caller forever.
	request, err := NewPrivilegedControlRequest("test-unreapable", []byte("request"), 0, nil)
	require.NoError(t, err)
	policy := productionPrivilegedControlPolicy()
	policy.postResponseReapTimeout = 10 * time.Millisecond
	policy.postInterruptReapTimeout = 10 * time.Millisecond

	wakeWait := make(chan struct{})
	var wakeWaitOnce sync.Once
	releaseWait := func() { wakeWaitOnce.Do(func() { close(wakeWait) }) }
	t.Cleanup(releaseWait)
	waitStarted := make(chan struct{})
	killErr := errors.New("injected kill permission failure")
	deps := realPrivilegedControlDeps()
	deps.command = func(_ string, _ ...string) *exec.Cmd { return exec.Command("/bin/true") }
	deps.start = func(_ context.Context, command *exec.Cmd) error {
		command.Process = &os.Process{Pid: 4242}
		return nil
	}
	deps.wait = func(_ context.Context, _ *exec.Cmd) error {
		close(waitStarted)
		<-wakeWait
		return nil
	}
	var killCount atomic.Int32
	deps.killGroup = func(_ context.Context, _ int) error {
		killCount.Add(1)
		return killErr
	}

	returned := make(chan PrivilegedControlOutcome, 1)
	go func() {
		returned <- exchangePrivilegedControl(context.Background(), request, deps, policy)
	}()
	select {
	case outcome := <-returned:
		releaseWait()
		assert.True(t, outcome.ProcessStarted())
		require.NotNil(t, outcome.Diagnostic())
		assert.ErrorIs(t, outcome.Diagnostic(), killErr)
		assert.ErrorContains(t, outcome.Diagnostic().Err, "did not exit after interruption")
		assert.Equal(t, true, outcome.Diagnostic().Details["outcome_unknown"])
		assert.Equal(t, int32(1), killCount.Load())
	case <-time.After(250 * time.Millisecond):
		releaseWait()
		t.Fatal("privileged control transport remained blocked after failed process-group kill")
	}
	<-waitStarted
}

func TestQueuedPrivilegedControlResponseWinsAtCancellationBoundary(t *testing.T) {
	responses := make(chan privilegedControlResponseResult, 1)
	responses <- privilegedControlResponseResult{response: []byte("response"), eof: true}

	response, ok := takeQueuedPrivilegedControlResponse(responses)

	require.True(t, ok)
	assert.True(t, response.eof)
	assert.Equal(t, []byte("response"), response.response)
}

func TestPrivilegedControlTransportSurfacesCleanupDependencyErrors(t *testing.T) {
	tests := map[string]func(
		deps *privilegedControlDeps,
		sentinel error,
	){
		"half_close": func(deps *privilegedControlDeps, sentinel error) {
			realShutdown := deps.shutdown
			deps.shutdown = func(ctx context.Context, file *os.File, how int) error {
				err := realShutdown(ctx, file, how)
				if how == unix.SHUT_WR && err == nil {
					return sentinel
				}
				return err
			}
		},
		"child_close": func(deps *privilegedControlDeps, sentinel error) {
			realClose := deps.closeFile
			deps.closeFile = func(ctx context.Context, file *os.File) error {
				err := realClose(ctx, file)
				if strings.Contains(file.Name(), "child") && err == nil {
					return sentinel
				}
				return err
			}
		},
		"parent_close": func(deps *privilegedControlDeps, sentinel error) {
			realClose := deps.closeFile
			deps.closeFile = func(ctx context.Context, file *os.File) error {
				err := realClose(ctx, file)
				if strings.Contains(file.Name(), "parent") && err == nil {
					return sentinel
				}
				return err
			}
		},
	}

	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			request, err := NewPrivilegedControlRequest("test-payload", []byte("header:"), 0, nil)
			require.NoError(t, err)
			deps := privilegedControlHelperDeps(t, "test-payload")
			sentinel := errors.New("injected " + name + " failure")
			inject(&deps, sentinel)

			outcome := exchangePrivilegedControl(
				context.Background(),
				request,
				deps,
				productionPrivilegedControlPolicy(),
			)

			assert.True(t, outcome.ResponseEOF())
			assert.Equal(t, []byte("response:header:"), outcome.Response())
			require.NotNil(t, outcome.Diagnostic())
			assert.ErrorIs(t, outcome.Diagnostic(), sentinel)
		})
	}
}

func TestPrivilegedControlTransportCancellationBeforeStartSkipsAllProcessEffects(t *testing.T) {
	payload := newPrivilegedControlTrackingPayload(nil)
	request, err := NewPrivilegedControlRequest("test-prestart-cancel", []byte("request"), 0, payload)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps := realPrivilegedControlDeps()
	var socketCount, commandCount, startCount, waitCount atomic.Int32
	deps.socketPair = func(_ context.Context) (*os.File, *os.File, error) {
		socketCount.Add(1)
		return nil, nil, errors.New("must not run")
	}
	deps.command = func(_ string, _ ...string) *exec.Cmd {
		commandCount.Add(1)
		return nil
	}
	deps.start = func(_ context.Context, _ *exec.Cmd) error {
		startCount.Add(1)
		return nil
	}
	deps.wait = func(_ context.Context, _ *exec.Cmd) error {
		waitCount.Add(1)
		return nil
	}

	outcome := exchangePrivilegedControl(ctx, request, deps, productionPrivilegedControlPolicy())

	assert.False(t, outcome.ProcessStarted())
	require.NotNil(t, outcome.Diagnostic())
	assert.ErrorIs(t, outcome.Diagnostic(), context.Canceled)
	assert.Equal(t, int32(1), payload.CloseCount())
	assert.Zero(t, socketCount.Load())
	assert.Zero(t, commandCount.Load())
	assert.Zero(t, startCount.Load())
	assert.Zero(t, waitCount.Load())
}

func TestPrivilegedControlTransportRejectsNilContextWithoutConsumingRequest(t *testing.T) {
	request, err := NewPrivilegedControlRequest("test-nil-context", []byte("request"), 0, nil)
	require.NoError(t, err)
	deps := realPrivilegedControlDeps()
	var socketCount atomic.Int32
	deps.socketPair = func(_ context.Context) (*os.File, *os.File, error) {
		socketCount.Add(1)
		return nil, nil, errors.New("must not run")
	}

	outcome := exchangePrivilegedControl(nil, request, deps, productionPrivilegedControlPolicy())

	assert.False(t, outcome.ProcessStarted())
	require.NotNil(t, outcome.Diagnostic())
	assert.Equal(t, errs.CodeValidationFailed, outcome.Diagnostic().Code)
	assert.Zero(t, socketCount.Load())
	_, _, _, _, err = request.consume()
	assert.NoError(t, err)
}

func TestPrivilegedControlRequestIsSingleUse(t *testing.T) {
	request, err := NewPrivilegedControlRequest("test-payload", []byte("header:"), 0, nil)
	require.NoError(t, err)
	first := runPrivilegedControlHelper(t, request, "test-payload", nil)
	require.True(t, first.ProcessStarted())

	second := exchangePrivilegedControl(
		context.Background(),
		request,
		realPrivilegedControlDeps(),
		productionPrivilegedControlPolicy(),
	)

	assert.False(t, second.ProcessStarted())
	assert.NotNil(t, second.Diagnostic())
}

func TestPrivilegedControlHelper(t *testing.T) {
	action, ok := privilegedControlHelperAction(os.Args)
	if !ok {
		return
	}

	// Helper cases ignore I/O errors unless the error itself is the exercised contract.
	switch action {
	case "test-duplex":
		request, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(91)
		}
		_, _ = os.Stdout.WriteString("stdout-poison")
		_, _ = os.Stderr.WriteString("stderr-poison")
		if _, err := os.Stdin.Write(append([]byte("response:"), request...)); err != nil {
			os.Exit(92)
		}
		if err := unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR); err != nil {
			os.Exit(93)
		}
	case "test-payload":
		request, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(95)
		}
		_, _ = os.Stdin.Write(append([]byte("response:"), request...))
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
	case "test-early-response":
		header := make([]byte, len("header:"))
		if _, err := io.ReadFull(os.Stdin, header); err != nil {
			os.Exit(96)
		}
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_RD)
		_, _ = os.Stdin.WriteString("response:early")
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
		os.Exit(17)
	case "test-final-without-read":
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_RD)
		_, _ = os.Stdin.WriteString("response:final")
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
	case "test-final-stops-upload":
		_, _ = os.Stdin.WriteString("response:final")
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
		poll := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLRDHUP}}
		count, err := unix.Poll(poll, 500)
		if err != nil || count == 0 || poll[0].Revents&unix.POLLRDHUP == 0 {
			_, _ = os.Stderr.WriteString("missing local write shutdown")
			os.Exit(98)
		}
	case "test-non-eof":
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdin.WriteString("response:partial")
		select {}
	case "test-overflow":
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdin.Write(repeatedPrivilegedControlBytes("r", 128))
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
	case "test-diagnostic-bound":
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdout.Write(repeatedPrivilegedControlBytes("o", 128))
		_, _ = os.Stderr.Write(repeatedPrivilegedControlBytes("e", 128))
		_, _ = os.Stdin.WriteString("response:bounded")
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
	case "test-exact-response-eof":
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdin.Write(repeatedPrivilegedControlBytes("r", 32))
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
	case "test-exact-response-open":
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdin.Write(repeatedPrivilegedControlBytes("r", 32))
		select {}
	case "test-short-payload":
		observed, _ := io.ReadAll(os.Stdin)
		_, _ = os.Stdin.Write(append([]byte("observed:"), observed...))
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
	case "test-cancel":
		_, _ = io.ReadAll(os.Stdin)
	case "test-socket-holder":
		_, _ = io.ReadAll(os.Stdin)
		holder := exec.Command("/bin/sleep", "30")
		holder.Stdin = os.Stdin
		if err := holder.Start(); err != nil {
			os.Exit(97)
		}
		_, _ = os.Stdin.WriteString("holder-pid:" + strconv.Itoa(holder.Process.Pid))
		os.Exit(0)
	case "test-response-reap":
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdin.WriteString("response:complete")
		_ = unix.Shutdown(int(os.Stdin.Fd()), unix.SHUT_WR)
		select {}
	default:
		os.Exit(94)
	}
}

func privilegedControlHelperAction(args []string) (string, bool) {
	separator := slices.Index(args, "--")
	if separator < 0 || separator+1 >= len(args) {
		return "", false
	}
	return args[separator+1], true
}

func privilegedControlHelperCommand(action string) *exec.Cmd {
	return exec.Command(os.Args[0], "-test.run="+privilegedControlHelperTest, "--", action)
}

func privilegedControlHelperDeps(t *testing.T, action string) privilegedControlDeps {
	t.Helper()
	deps := realPrivilegedControlDeps()
	deps.command = func(_ string, _ ...string) *exec.Cmd {
		return privilegedControlHelperCommand(action)
	}
	return deps
}

func runPrivilegedControlHelper(
	t *testing.T,
	request *PrivilegedControlRequest,
	action string,
	policy *privilegedControlPolicy,
) PrivilegedControlOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return runPrivilegedControlHelperWithContext(t, ctx, request, action, policy)
}

func runPrivilegedControlHelperWithContext(
	t *testing.T,
	ctx context.Context,
	request *PrivilegedControlRequest,
	action string,
	policy *privilegedControlPolicy,
) PrivilegedControlOutcome {
	t.Helper()
	selectedPolicy := productionPrivilegedControlPolicy()
	if policy != nil {
		selectedPolicy = *policy
	}
	return exchangePrivilegedControl(ctx, request, privilegedControlHelperDeps(t, action), selectedPolicy)
}

type privilegedControlTrackingPayload struct {
	reader     *bytes.Reader
	closeCount atomic.Int32
	closeErr   error
}

type privilegedControlZeroReader struct{}

func (privilegedControlZeroReader) Read(target []byte) (int, error) {
	clear(target)
	return len(target), nil
}

type privilegedControlPartialWriter struct {
	maximum int
	value   bytes.Buffer
}

func (writer *privilegedControlPartialWriter) Write(value []byte) (int, error) {
	if len(value) > writer.maximum {
		value = value[:writer.maximum]
	}
	return writer.value.Write(value)
}

type privilegedControlZeroWriter struct{}

func (privilegedControlZeroWriter) Write(_ []byte) (int, error) { return 0, nil }

func newPrivilegedControlTrackingPayload(value []byte) *privilegedControlTrackingPayload {
	return &privilegedControlTrackingPayload{reader: bytes.NewReader(value)}
}

func (payload *privilegedControlTrackingPayload) Read(target []byte) (int, error) {
	return payload.reader.Read(target)
}

func (payload *privilegedControlTrackingPayload) Close() error {
	payload.closeCount.Add(1)
	return payload.closeErr
}

func (payload *privilegedControlTrackingPayload) CloseCount() int32 { return payload.closeCount.Load() }

type privilegedControlBlockingPayload struct {
	closed     chan struct{}
	closeCount atomic.Int32
	closeOnce  sync.Once
}

func newPrivilegedControlBlockingPayload() *privilegedControlBlockingPayload {
	return &privilegedControlBlockingPayload{closed: make(chan struct{})}
}

func (payload *privilegedControlBlockingPayload) Read(_ []byte) (int, error) {
	<-payload.closed
	return 0, io.ErrClosedPipe
}

func (payload *privilegedControlBlockingPayload) Close() error {
	payload.closeCount.Add(1)
	payload.closeOnce.Do(func() { close(payload.closed) })
	return nil
}

func (payload *privilegedControlBlockingPayload) CloseCount() int32 { return payload.closeCount.Load() }

func repeatedPrivilegedControlBytes(value string, count int) []byte {
	return []byte(strings.Repeat(value, count))
}
