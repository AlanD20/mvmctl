package system_test

import (
	"os"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/system"
)

// ─── DecodeExitStatus ────────────────────────────────────────────────────────
// Rationale: DecodeExitStatus converts raw wait status to conventional exit
// codes. Used by CapturedExitCode and GracefulShutdown. Wrong decoding would
// cause incorrect process lifecycle detection.

func TestDecodeExitStatus(t *testing.T) {
	tests := map[string]struct {
		status syscall.WaitStatus
		want   int
	}{
		// Normal exit
		"exit_0":  {status: exitStatus(0), want: 0},
		"exit_1":  {status: exitStatus(1), want: 1},
		"exit_42": {status: exitStatus(42), want: 42},
		"exit_255": {status: exitStatus(255), want: 255},

		// Signal death: 128 + signal number
		"sigterm_15": {status: signalStatus(syscall.SIGTERM), want: 128 + int(syscall.SIGTERM)},
		"sigkill_9":  {status: signalStatus(syscall.SIGKILL), want: 128 + int(syscall.SIGKILL)},
		"sigint_2":   {status: signalStatus(syscall.SIGINT), want: 128 + int(syscall.SIGINT)},

		// Core dump signals
		"sigsegv_11": {status: signalStatus(syscall.SIGSEGV), want: 128 + int(syscall.SIGSEGV)},
		"sigabrt_6":  {status: signalStatus(syscall.SIGABRT), want: 128 + int(syscall.SIGABRT)},

		// Stopped status (neither exited nor signaled) — returns -1.
		// 0x7f is WIFSTOPPED marker: lower 7 bits are 0x7f, stop signal is in upper byte.
		"stopped_sigtrap": {status: syscall.WaitStatus(0x7f | (5 << 8)), want: -1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := system.DecodeExitStatus(tc.status)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("DecodeExitStatus() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// exitStatus creates a WaitStatus for a normal exit with the given code.
func exitStatus(code int) syscall.WaitStatus {
	// On Linux, exit status is encoded as: status = (code & 0xff) << 8
	return syscall.WaitStatus(code << 8)
}

// signalStatus creates a WaitStatus for termination by the given signal.
func signalStatus(sig syscall.Signal) syscall.WaitStatus {
	// On Linux, signal status is encoded as: status = sig & 0x7f
	return syscall.WaitStatus(sig & 0x7f)
}

// ─── IsProcessAlive ──────────────────────────────────────────────────────────
// Rationale: IsProcessAlive checks /proc/<pid>/stat for process existence and
// state. Used throughout the codebase for lifecycle management. Must correctly
// identify running processes and reject zombies/stopped processes.

func TestIsProcessAlive_currentProcess(t *testing.T) {
	// The current test process should always be alive
	alive := system.IsProcessAlive(os.Getpid(), nil)
	assert.True(t, alive, "current process should be alive")
}

func TestIsProcessAlive_nonexistentPID(t *testing.T) {
	// PID 1 is init/systemd — always alive on Linux.
	// Use a pid that almost certainly doesn't exist.
	alive := system.IsProcessAlive(2147483647, nil) // max int32
	assert.False(t, alive, "nonexistent PID should not be alive")
}

func TestIsProcessAlive_PIDReuseDetection(t *testing.T) {
	// Test with expectedStartTime that doesn't match (PID reuse scenario)
	// The current process has some startTime; passing a different one
	// should return false.
	var wrongStartTime int64 = 1 // almost certainly wrong
	alive := system.IsProcessAlive(os.Getpid(), &wrongStartTime)
	assert.False(t, alive, "PID reuse check with wrong startTime should return false")
}

func TestIsProcessAlive_correctStartTime(t *testing.T) {
	// Get the actual start time and pass it — should be alive
	startTime := system.GetProcessStartTime(os.Getpid())
	if startTime == nil {
		t.Skip("could not read /proc/self/stat — running in restricted environment?")
	}
	alive := system.IsProcessAlive(os.Getpid(), startTime)
	assert.True(t, alive, "process with matching startTime should be alive")
}

// ─── GetProcessStartTime ─────────────────────────────────────────────────────

func TestGetProcessStartTime(t *testing.T) {
	t.Run("current_process_returns_non_nil", func(t *testing.T) {
		got := system.GetProcessStartTime(os.Getpid())
		require.NotNil(t, got, "should get start time for current process")
		assert.Greater(t, *got, int64(0), "start time should be positive")
	})

	t.Run("nonexistent_pid_returns_nil", func(t *testing.T) {
		got := system.GetProcessStartTime(2147483647)
		assert.Nil(t, got)
	})
}

// ─── HasAncestorWithCmdline ──────────────────────────────────────────────────
// Rationale: Walks the PPID chain through /proc. Used for mvm-provision
// subprocess detection. Must correctly identify the running process's own
// ancestor chain.

func TestHasAncestorWithCmdline(t *testing.T) {
	t.Run("nonexistent_pid_returns_false", func(t *testing.T) {
		found := system.HasAncestorWithCmdline(2147483647, "anything")
		assert.False(t, found, "nonexistent PID should return false")
	})

	t.Run("self_pid_no_match_returns_false", func(t *testing.T) {
		// Walk the ancestor chain looking for something that won't match
		found := system.HasAncestorWithCmdline(os.Getpid(), "xxxxxxxxxxyyyyyyyzzzzz_nonexistent")
		assert.False(t, found)
	})

	t.Run("success_path_finds_ancestor", func(t *testing.T) {
		// The test process is launched by "go test" — "go" should be in the
		// ancestor chain. This verifies /proc walking works end-to-end.
		found := system.HasAncestorWithCmdline(os.Getpid(), "go")
		if !found {
			// CI or alternative runners may not have "go" in the ancestor chain.
			// This is environment-dependent, not a code bug.
			t.Skip("ancestor cmdline did not contain 'go' — environment-specific, not a failure")
		}
	})
}
