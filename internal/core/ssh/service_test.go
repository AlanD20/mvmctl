package ssh_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/ssh"
)

// ─── Service.Connect — waitForSSH context cancellation ──────────────────────
// Rationale: waitForSSH has a tight probe loop (100ms). If it doesn't check
// context cancellation, the loop blocks for the full timeout even when the
// caller has cancelled. This was a real bug — Ctrl+C during env apply hung
// for 150s because waitForSSH slept through cancellation.

func TestConnect_contextCancelledDuringWait(t *testing.T) {
	// Use a closed port so waitForSSH enters its probe loop.
	// Then cancel the context — it should return immediately, not block
	// for the full timeout.
	svc := ssh.NewService("127.0.0.1", "root", "/dev/null", 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so waitForSSH has entered its loop.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := svc.Connect(ctx, "echo hello", false)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled,
		"Connect must return context.Canceled when context is cancelled")
	assert.Less(t, elapsed, 2*time.Second,
		"Connect must return quickly on context cancellation, not block for full timeout")
}

func TestConnect_contextAlreadyCancelled(t *testing.T) {
	svc := ssh.NewService("127.0.0.1", "root", "/dev/null", 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	start := time.Now()
	_, err := svc.Connect(ctx, "echo hello", false)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 1*time.Second,
		"Pre-cancelled context must return immediately")
}

// ─── Service.Connect — waitForSSH timeout ───────────────────────────────────
// Rationale: If the VM never becomes reachable, waitForSSH must return a
// timeout error after the specified duration — not hang forever.

func TestConnect_timeoutWhenPortClosed(t *testing.T) {
	// Use a non-routable IP (RFC 5737) so the port is never reachable.
	// Short timeout to keep the test fast.
	svc := ssh.NewService("192.0.2.1", "root", "/dev/null", 2*time.Second)

	start := time.Now()
	_, err := svc.Connect(context.Background(), "echo hello", false)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out",
		"Connect must return a timeout error when VM is unreachable")
	assert.InDelta(t, elapsed.Seconds(), 2.0, 0.5,
		"Connect must return after approximately the specified timeout")
}

// ─── Service.StreamCommand — context cancellation ───────────────────────────
// Rationale: StreamCommand also calls waitForSSH. It must respect context
// cancellation the same way as Connect.

func TestStreamCommand_contextCancelledDuringWait(t *testing.T) {
	svc := ssh.NewService("127.0.0.1", "root", "/dev/null", 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	ch, err := svc.StreamCommand(ctx, "echo hello")
	elapsed := time.Since(start)

	// StreamCommand may return the error directly or on the channel,
	// depending on whether waitForSSH returned before or after Start().
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, ch)
	} else {
		// Error came on the channel — drain it.
		for sl := range ch {
			if sl.Err != nil {
				assert.ErrorIs(t, sl.Err, context.Canceled)
			}
		}
	}
	assert.Less(t, elapsed, 2*time.Second,
		"StreamCommand must return quickly on context cancellation")
}

func TestStreamCommand_contextAlreadyCancelled(t *testing.T) {
	svc := ssh.NewService("127.0.0.1", "root", "/dev/null", 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	ch, err := svc.StreamCommand(ctx, "echo hello")
	elapsed := time.Since(start)

	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, ch)
	} else {
		for sl := range ch {
			if sl.Err != nil {
				assert.ErrorIs(t, sl.Err, context.Canceled)
			}
		}
	}
	assert.Less(t, elapsed, 1*time.Second)
}

// ─── Service.StreamCommand — timeout ────────────────────────────────────────
// Rationale: StreamCommand must timeout when VM is unreachable, same as
// Connect.

func TestStreamCommand_timeoutWhenPortClosed(t *testing.T) {
	svc := ssh.NewService("192.0.2.1", "root", "/dev/null", 2*time.Second)

	start := time.Now()
	ch, err := svc.StreamCommand(context.Background(), "echo hello")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Nil(t, ch)
	assert.Contains(t, err.Error(), "timed out")
	assert.InDelta(t, elapsed.Seconds(), 2.0, 0.5)
}
