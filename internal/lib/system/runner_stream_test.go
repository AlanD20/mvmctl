package system_test

import (
	"bufio"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/system"
)

// ─── RealRunner.Stream ───────────────────────────────────────────────────────

func TestStream_emptyArgs(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), nil, system.RunCmdOpts{})
	assert.Nil(t, ch)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no command specified")
}

func TestStream_commandNotFound(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"nonexistent_command_xyz_12345"}, system.RunCmdOpts{})
	assert.Nil(t, ch)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Command not found")
}

func TestStream_stdoutLines(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"printf", "line1\nline2\nline3\n"}, system.RunCmdOpts{})
	require.NoError(t, err)

	var lines []string
	for sl := range ch {
		require.NoError(t, sl.Err)
		lines = append(lines, sl.Line)
	}

	assert.Equal(t, []string{"line1", "line2", "line3"}, lines)
}

func TestStream_stderrMergedIntoStdout(t *testing.T) {
	// sh -c writes to both stdout and stderr — both should appear on the channel
	runner := &system.RealRunner{}
	ch, err := runner.Stream(
		context.Background(),
		[]string{"sh", "-c", "echo out1; echo err1 >&2; echo out2"},
		system.RunCmdOpts{},
	)
	require.NoError(t, err)

	var lines []string
	for sl := range ch {
		require.NoError(t, sl.Err)
		lines = append(lines, sl.Line)
	}

	// All three lines should appear (order may vary due to buffering, but
	// with a single process writing sequentially it should be deterministic)
	assert.Contains(t, lines, "out1")
	assert.Contains(t, lines, "err1")
	assert.Contains(t, lines, "out2")
}

func TestStream_nonZeroExit(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"sh", "-c", "echo fail; exit 42"}, system.RunCmdOpts{})
	require.NoError(t, err)

	var lines []string
	var streamErr error
	for sl := range ch {
		if sl.Err != nil {
			streamErr = sl.Err
		} else {
			lines = append(lines, sl.Line)
		}
	}

	assert.Equal(t, []string{"fail"}, lines)
	require.Error(t, streamErr)
	assert.Contains(t, streamErr.Error(), "exit 42")
}

func TestStream_zeroExitNoError(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"sh", "-c", "echo ok; exit 0"}, system.RunCmdOpts{})
	require.NoError(t, err)

	var lines []string
	var streamErr error
	for sl := range ch {
		if sl.Err != nil {
			streamErr = sl.Err
		} else {
			lines = append(lines, sl.Line)
		}
	}

	assert.Equal(t, []string{"ok"}, lines)
	assert.NoError(t, streamErr, "exit 0 should not produce an error")
}

func TestStream_contextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &system.RealRunner{}

	// A command that produces output slowly
	ch, err := runner.Stream(ctx, []string{"sh", "-c", "echo start; sleep 30; echo done"}, system.RunCmdOpts{})
	require.NoError(t, err)

	// Read the first line
	sl := <-ch
	require.NoError(t, sl.Err)
	assert.Equal(t, "start", sl.Line)

	// Cancel the context — this should kill the process and close the channel
	cancel()

	// Drain remaining lines/errors — channel should close eventually
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
		// Channel closed — good
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after context cancellation within 5s — deadlock?")
	}
}

func TestStream_contextCancelledBeforeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	runner := &system.RealRunner{}
	ch, err := runner.Stream(ctx, []string{"echo", "hello"}, system.RunCmdOpts{})
	// With a pre-cancelled context, cmd.Start() may fail or succeed briefly.
	// Either Stream returns an error or the channel closes cleanly.
	if err != nil {
		assert.Nil(t, ch)
		return
	}

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
		// Good — channel closed
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after pre-cancelled context — deadlock?")
	}
}

func TestStream_emptyOutput(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"true"}, system.RunCmdOpts{})
	require.NoError(t, err)

	var lines []string
	var streamErr error
	for sl := range ch {
		if sl.Err != nil {
			streamErr = sl.Err
		} else {
			lines = append(lines, sl.Line)
		}
	}

	assert.Empty(t, lines)
	assert.NoError(t, streamErr)
}

func TestStream_largeOutput(t *testing.T) {
	// Generate 1000 lines — scanner must handle this without deadlock
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"seq", "1", "1000"}, system.RunCmdOpts{})
	require.NoError(t, err)

	var count int
	for sl := range ch {
		require.NoError(t, sl.Err)
		count++
		if count == 1 {
			assert.Equal(t, "1", sl.Line)
		}
		if count == 1000 {
			assert.Equal(t, "1000", sl.Line)
		}
	}

	assert.Equal(t, 1000, count, "should receive exactly 1000 lines")
}

func TestStream_longLines(t *testing.T) {
	// Generate a line longer than scanner's default 64KB buffer
	// scanner.Scan() returns false with ErrTooLong if line exceeds buffer
	runner := &system.RealRunner{}
	// Generate a 100KB line
	ch, err := runner.Stream(context.Background(),
		[]string{"sh", "-c", "head -c 102400 /dev/zero | tr '\\0' 'A'; echo"},
		system.RunCmdOpts{})
	require.NoError(t, err)

	var lines []string
	var streamErr error
	for sl := range ch {
		if sl.Err != nil {
			streamErr = sl.Err
		} else {
			lines = append(lines, sl.Line)
		}
	}

	// This may fail with bufio.ErrTooLong if scanner buffer is too small
	if streamErr != nil && errors.Is(streamErr, bufio.ErrTooLong) {
		t.Fatal("scanner choked on a 100KB line — increase scanner buffer size")
	}
	// If we got here, the line was read successfully
	if len(lines) > 0 {
		assert.Len(t, lines[0], 102400, "should read the full 100KB line")
	}
}

// TestStream_noDeadlockOnProcessExit ensures that when a process exits
// cleanly, the channel closes without deadlock. This was a real bug:
// the old code deferred pw.Close() after cmd.Wait(), but cmd.Wait()
// blocks until all I/O completes, which requires EOF on the pipe,
// which requires pw.Close() — classic deadlock.
func TestStream_noDeadlockOnProcessExit(t *testing.T) {
	runner := &system.RealRunner{}
	ch, err := runner.Stream(context.Background(), []string{"echo", "hello"}, system.RunCmdOpts{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
		// Good — no deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: channel did not close within 3s after process exit")
	}
}
