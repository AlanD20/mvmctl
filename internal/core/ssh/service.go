package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/lib/system"
)

// Probe defaults for waitForSSH.
const (
	probeInterval    = 100 * time.Millisecond
	dialTimeout      = 500 * time.Millisecond
)

// Service is a stateful SSH connection service.
// Matches Python's SSHService exactly.
type Service struct {
	ip      string
	user    string
	keyPath string
	timeout time.Duration // 0 = no ConnectTimeout flag
}

// NewService creates a new SSHService with the given connection parameters.
// Matches Python's SSHService.__init__() exactly.
func NewService(ip, user, keyPath string, timeout time.Duration) *Service {
	// Matches Python's: logger.info("SSH service initialized for %s@%s", user, ip)
	slog.Info("SSH service initialized", "user", user, "ip", ip)

	return &Service{
		ip:      ip,
		user:    user,
		keyPath: keyPath,
		timeout: timeout,
	}
}

// BuildCommand builds SSH command arguments for this connection.
// Matches Python's SSHService.build_command() exactly.
func (s *Service) BuildCommand(command string) []string {
	timeoutSec := 0
	if s.timeout > 0 {
		timeoutSec = int(s.timeout.Seconds())
	}
	opts := buildSSHOpts(s.ip, s.user, s.keyPath, timeoutSec)
	if command != "" {
		opts = append(opts, command)
	}
	return opts
}

// ExecCommand executes SSH, replacing the current process.
// Matches Python's SSHService.exec_command() exactly.
// Go equivalent:
//   - exec.LookPath("ssh") resolves the full path (PATH search)
//   - syscall.Exec replaces the current process
//   - Errors propagate via return (matching Python's exception propagation)
func (s *Service) ExecCommand(command string) error {
	sshArgs := s.BuildCommand(command)
	path, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	// syscall.Exec replaces the current process; if it returns, it failed
	env := append(os.Environ(), "MVM_SSH_CONNECTION=1")
	if err := syscall.Exec(path, sshArgs, env); err != nil {
		return err
	}
	return nil
}

// RunCommand runs SSH as a subprocess and returns the exit code.
// Matches Python's SSHService.run_command().
// Python catches ONLY ProcessError from run_cmd. With check=False, ProcessError
// is raised only on timeout or launch failure (NOT on non-zero exit). For timeout,
// Python re-raises with a modified message. For other ProcessError (launch failure),
// the original exception is re-raised. Non-Python-exceptions propagate as-is.
//
// Go:
//   - DeadlineExceeded is checked BEFORE ExitError because exec.CommandContext
//     kills the process on timeout and returns an error that satisfies BOTH
//     ExitError AND DeadlineExceeded. Checking DeadlineExceeded first ensures
//     we detect timeout correctly.
//   - ExitError → return exit code (matching check=False behavior).
//   - Other errors → propagate (matching Python's unhandled exception propagation).
func (s *Service) RunCommand(ctx context.Context, command string) (int, error) {
	sshArgs := s.BuildCommand(command)

	opts := system.RunCmdOpts{
		Capture: false,
		Check:   false,
		Env:     map[string]string{"MVM_SSH_CONNECTION": "1"},
	}
	if s.timeout > 0 {
		opts.Timeout = s.timeout
	}
	result, err := system.DefaultRunner.Run(ctx, sshArgs, opts)

	if err != nil {
		// Check timeout via error message. RunCmdCompat formatted timeout errors
		// as "Command timed out after Xs: ssh", matching Python's ProcessError.
		if strings.Contains(err.Error(), "timed out") {
			return -1, fmt.Errorf("SSH command timed out after %ds", int(s.timeout.Seconds()))
		}

		return -1, err
	}

	if !result.Success() {
		return result.ExitCode, nil
	}
	return 0, nil
}

// waitForSSH retries TCP port 22 checks until the VM is reachable or the
// total timeout expires. Returns the remaining timeout for the command.
// Uses aggressive 100ms probing with TCP dial (instant "connection refused"
// when VM is still booting) instead of SSH subprocess probes.
// The caller MUST pass a positive timeout — no fallback.
func (s *Service) waitForSSH(ctx context.Context, timeout time.Duration) (time.Duration, error) {
	deadline := time.Now().Add(timeout)

	dialer := net.Dialer{Timeout: dialTimeout}
	addr := net.JoinHostPort(s.ip, "22")
	attempt := 0

	for {
		attempt++
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("SSH connection timed out after waiting %ds for VM to become reachable",
				int(timeout.Seconds()))
		}

		// TCP dial to port 22 — fails instantly with "connection refused"
		// when VM is still booting, succeeds as soon as sshd listens.
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr == nil {
			conn.Close()
			if attempt > 1 {
				slog.Debug("VM reachable after probe", "attempts", attempt, "elapsed", time.Since(deadline.Add(-timeout)).String())
			}
			return remaining, nil
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(probeInterval):
		}
	}
}

// Connect connects to the host via SSH.
// Matches Python's SSHService.connect() exactly.
// Go: execMode maps to Python's exec_mode parameter.
// Errors from ExecCommand propagate via return (matching Python's exception propagation).
func (s *Service) Connect(ctx context.Context, command string, execMode bool) (int, error) {
	// exec_mode=True and not command (None/"") → interactive session (exec)
	if execMode && command == "" {
		return 0, s.ExecCommand(command)
	}

	// Phase 1: Wait for SSH port to become reachable.
	// s.timeout is the total budget for wait + command execution.
	remaining, err := s.waitForSSH(ctx, s.timeout)
	if err != nil {
		return -1, err
	}

	// Phase 2: SSH is reachable — run the actual command with the
	// remaining timeout. Use RunCommand which respects s.timeout.
	// Override opts.Timeout to remaining for the command itself.
	sshArgs := s.BuildCommand(command)
	opts := system.RunCmdOpts{
		Capture: false,
		Check:   false,
		Env:     map[string]string{"MVM_SSH_CONNECTION": "1"},
		Timeout: remaining,
	}
	result, runErr := system.DefaultRunner.Run(ctx, sshArgs, opts)

	if runErr != nil {
		return -1, runErr
	}
	if !result.Success() {
		return result.ExitCode, nil
	}
	return 0, nil
}

// StreamCommand runs an SSH command and streams output line by line.
// Returns a channel that yields StreamLine entries. The caller must
// consume the channel until it's closed. Returns an error if SSH
// is unreachable or the command fails to start.
func (s *Service) StreamCommand(ctx context.Context, command string) (<-chan system.StreamLine, error) {
	// Phase 1: Wait for SSH port to become reachable.
	remaining, err := s.waitForSSH(ctx, s.timeout)
	if err != nil {
		return nil, err
	}

	// Phase 2: Stream the command output.
	sshArgs := s.BuildCommand(command)
	opts := system.RunCmdOpts{
		Capture: false,
		Check:   false,
		Env:     map[string]string{"MVM_SSH_CONNECTION": "1"},
		Timeout: remaining,
	}
	return system.DefaultRunner.Stream(ctx, sshArgs, opts)
}
