package system

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestPrivilegedVMRootfsExportRefusalRetainsRequestOwnership(t *testing.T) {
	// Rationale: No other action or payload may acquire nonfatal interruption.
	for name, tc := range map[string]struct {
		action string
		length uint64
		reader bool
	}{
		"release_action": {action: "jailer-vm-rootfs-export-release"},
		"payload":        {action: PrivilegedVMRootfsExportAction, length: 1, reader: true},
		"empty_reader":   {action: PrivilegedVMRootfsExportAction, reader: true},
	} {
		t.Run(name, func(t *testing.T) {
			var payload *privilegedControlTrackingPayload
			var reader io.ReadCloser
			if tc.reader {
				payload = newPrivilegedControlTrackingPayload([]byte("x"))
				reader = payload
			}
			request, err := NewPrivilegedControlRequest(tc.action, []byte("request"), tc.length, reader)
			require.NoError(t, err)
			var effects atomic.Int32
			deps := realPrivilegedControlDeps()
			deps.socketPair = func(context.Context) (*os.File, *os.File, error) {
				effects.Add(1)
				return nil, nil, errors.New("unexpected socket creation")
			}
			outcome := exchangePrivilegedVMRootfsExport(t.Context(), request, deps, productionPrivilegedControlPolicy())
			assert.False(t, outcome.ProcessStarted())
			require.NotNil(t, outcome.Diagnostic())
			assert.Equal(t, false, outcome.Diagnostic().Details["outcome_unknown"])
			assert.Zero(t, effects.Load())
			if payload != nil {
				assert.Zero(t, payload.CloseCount())
			}
			_, _, _, owner, err := request.consume()
			assert.NoError(t, err, "refusal must not consume the request")
			require.NoError(t, owner.Close())
		})
	}
}

func TestPrivilegedVMRootfsExportPrecancelDoesNotStart(t *testing.T) {
	request, err := NewPrivilegedControlRequest(PrivilegedVMRootfsExportAction, []byte("request"), 0, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var effects atomic.Int32
	deps := realPrivilegedControlDeps()
	deps.socketPair = func(context.Context) (*os.File, *os.File, error) {
		effects.Add(1)
		return nil, nil, errors.New("unexpected socket creation")
	}
	outcome := exchangePrivilegedVMRootfsExport(ctx, request, deps, productionPrivilegedControlPolicy())
	assert.False(t, outcome.ProcessStarted())
	assert.ErrorIs(t, outcome.Diagnostic(), context.Canceled)
	assert.Zero(t, effects.Load())
}

func TestPrivilegedVMRootfsExportCancellationLeavesWorkerAliveAndReapsOnce(t *testing.T) {
	// Rationale: Root restoration may outlive the caller's five-second bound.
	// A real independently sessioned worker must survive disconnect and finish later.
	gateRead, gateWrite, err := os.Pipe()
	require.NoError(t, err)
	readyRead, readyWrite, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = gateRead.Close() // Fixture ends may already have been closed after Start.
		_ = gateWrite.Close()
		_ = readyRead.Close()
		_ = readyWrite.Close()
	})
	marker := filepath.Join(t.TempDir(), "restored")
	request, err := NewPrivilegedControlRequest(PrivilegedVMRootfsExportAction, []byte("request"), 0, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	deps := realPrivilegedControlDeps()
	var command *exec.Cmd
	deps.command = func(string, ...string) *exec.Cmd {
		command = exec.Command(os.Args[0], "-test.run=^TestPrivilegedVMRootfsExportWorker$", "--", marker)
		command.ExtraFiles = []*os.File{gateRead, readyWrite}
		return command
	}
	realWait, realClose, realKill := deps.wait, deps.closeFile, deps.killGroup
	var waits, kills, closes atomic.Int32
	finished := make(chan error, 1)
	deps.wait = func(ctx context.Context, command *exec.Cmd) error {
		waits.Add(1)
		err := realWait(ctx, command)
		finished <- err
		return err
	}
	deps.killGroup = func(ctx context.Context, pid int) error {
		kills.Add(1)
		return realKill(ctx, pid)
	}
	deps.closeFile = func(ctx context.Context, file *os.File) error {
		closes.Add(1)
		return realClose(ctx, file)
	}
	policy := productionPrivilegedControlPolicy()
	policy.postInterruptReapTimeout = 25 * time.Millisecond
	returned := make(chan PrivilegedControlOutcome, 1)
	go func() { returned <- exchangePrivilegedVMRootfsExport(ctx, request, deps, policy) }()
	ready := make(chan error, 1)
	go func() { _, err := io.ReadFull(readyRead, make([]byte, 1)); ready <- err }()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { _ = gateWrite.Close() }) // EOF deliberately completes the owned helper.
	}
	t.Cleanup(func() {
		release()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("owned helper did not finish after release")
		}
	})
	select {
	case err := <-ready:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not reach restoration gate")
	}
	require.NoError(t, gateRead.Close())
	require.NoError(t, readyWrite.Close())
	cancel()
	var outcome PrivilegedControlOutcome
	select {
	case outcome = <-returned:
	case <-time.After(time.Second):
		t.Fatal("export transport remained blocked after interruption bound")
	}
	assert.True(t, outcome.ProcessStarted())
	assert.False(t, outcome.ResponseEOF())
	assert.ErrorIs(t, outcome.Diagnostic(), context.Canceled)
	require.NotNil(t, outcome.Diagnostic())
	assert.Equal(t, true, outcome.Diagnostic().Details["outcome_unknown"])
	assert.Zero(t, kills.Load())
	assert.Equal(t, int32(2), closes.Load())
	assert.True(t, command.SysProcAttr.Setsid)
	assert.False(t, command.SysProcAttr.Setpgid)
	pid := command.Process.Pid
	sid, err := unix.Getsid(pid)
	require.NoError(t, err)
	assert.Equal(t, pid, sid, "worker must belong to a separate session")
	assert.NoError(t, unix.Kill(pid, 0), "worker must survive caller disconnect")
	release()
	// Wait completion synchronizes ProcessState access; do not inspect it while Wait runs.
	select {
	case waitErr := <-finished:
		assert.NoError(t, waitErr)
		finished <- waitErr // Preserve completion evidence for cleanup.
	case <-time.After(5 * time.Second):
		t.Fatal("helper was not reaped after deliberate completion")
	}
	assert.Equal(t, int32(1), waits.Load())
	assert.True(t, command.ProcessState.Success())
	assert.ErrorIs(t, unix.Kill(pid, 0), unix.ESRCH)
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	if diff := cmp.Diff("restored-after-disconnect", string(data)); diff != "" {
		t.Errorf("restoration completion mismatch (-want +got):\n%s", diff)
	}
}

func TestPrivilegedVMRootfsExportPhysicalEOFRemainsAuthoritativeOnCancellation(t *testing.T) {
	// Rationale: Cancellation after actual peer EOF must retain response authority.
	request, err := NewPrivilegedControlRequest(PrivilegedVMRootfsExportAction, []byte("request"), 0, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	deps := privilegedControlHelperDeps(t, "test-payload")
	realShutdown := deps.shutdown
	var writes atomic.Int32
	deps.shutdown = func(ctx context.Context, file *os.File, how int) error {
		if how == unix.SHUT_WR && writes.Add(1) == 2 {
			cancel() // The second half-close follows the accepted physical peer EOF.
		}
		return realShutdown(ctx, file, how)
	}
	outcome := exchangePrivilegedVMRootfsExport(ctx, request, deps, productionPrivilegedControlPolicy())
	assert.True(t, outcome.ResponseEOF())
	if diff := cmp.Diff([]byte("response:request"), outcome.Response()); diff != "" {
		t.Errorf("authoritative response mismatch (-want +got):\n%s", diff)
	}
}

// TestPrivilegedVMRootfsExportWorker is a real child blocked until explicit owned completion.
func TestPrivilegedVMRootfsExportWorker(t *testing.T) {
	marker, ok := privilegedControlHelperAction(os.Args)
	if !ok {
		return
	}
	gate := os.NewFile(3, "export-completion-gate")
	ready := os.NewFile(4, "export-ready")
	if _, err := io.ReadAll(os.Stdin); err != nil {
		os.Exit(91)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(92)
	}
	if _, err := io.ReadAll(gate); err != nil {
		os.Exit(93)
	}
	if err := os.WriteFile(marker, []byte("restored-after-disconnect"), 0600); err != nil {
		os.Exit(94)
	}
}
