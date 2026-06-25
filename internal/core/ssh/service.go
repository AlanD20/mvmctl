// Package ssh provides SSH connection management for VMs.
// Layer: Core domain — never imports other core/* packages.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"mvmctl/internal/lib/system"
)

// Probe defaults for waitForSSH and ProbeUntilReady.
const (
	probeInterval   = 100 * time.Millisecond
	probeSSHTimeout = 2 // seconds — ConnectTimeout for the SSH probe
)

// Service is a stateful SSH connection service.
type Service struct {
	ip      string
	user    string
	keyPath string
	timeout time.Duration // 0 = no ConnectTimeout flag
}

// NewService creates a new SSHService with the given connection parameters.
func NewService(ip, user, keyPath string, timeout time.Duration) *Service {
	slog.Info("SSH service initialized", "user", user, "ip", ip)

	return &Service{
		ip:      ip,
		user:    user,
		keyPath: keyPath,
		timeout: timeout,
	}
}

// BuildCommand builds SSH command arguments for this connection.
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

// InteractiveSession opens an interactive SSH session as a subprocess.
// It saves and restores the terminal state (termios and DEC private modes)
// so programs like htop that enable mouse tracking don't leave the terminal
// in a broken state after the session exits.
func (s *Service) InteractiveSession(ctx context.Context) error {
	path, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}

	// Build args and insert -o RequestTTY=yes before the destination
	// (last element). Cannot append after the destination — SSH's getopt
	// stops parsing options at the first non-option argument.
	sshArgs := s.BuildCommand("")
	dest := sshArgs[len(sshArgs)-1]
	sshArgs = append(sshArgs[:len(sshArgs)-1], "-o", "RequestTTY=yes", dest)

	err = system.RunInteractive(path, sshArgs[1:], []string{"MVM_SSH_CONNECTION=1"})
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil // SSH exit codes are expected for interactive sessions
		}
		return fmt.Errorf("interactive SSH session failed: %w", err)
	}
	return nil
}

// RunCommand runs SSH as a subprocess and returns the exit code.
// Timeout is detected by checking the error message before the exit code, because
// exec.CommandContext kills the process on timeout and returns an error satisfying
// both ExitError and DeadlineExceeded; timeout must be detected first.
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
		// Check timeout via error message.
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

// waitForSSH retries SSH probe commands until the VM responds to SSH or the
// total timeout expires. Returns the remaining timeout for the command.
// Unlike a TCP port check, an SSH probe confirms the VM is fully booted
// (cloud-init finished, no apt locks, network configured).
// The caller MUST pass a positive timeout — no fallback.
func (s *Service) waitForSSH(ctx context.Context, timeout time.Duration) (time.Duration, error) {
	return ProbeUntilReady(ctx, s.ip, s.user, s.keyPath, timeout)
}

// Connect connects to the host via SSH.
// execMode controls whether to exec or run as subprocess.
// Errors propagate via return.
func (s *Service) Connect(ctx context.Context, command string, execMode bool) (int, error) {
	// exec_mode=True and not command (None/"") → interactive session (subprocess)
	if execMode && command == "" {
		return 0, s.InteractiveSession(ctx)
	}

	// Phase 1: Wait for SSH to become ready (probe with actual SSH command).
	// s.timeout is the total budget for wait + command execution.
	remaining, err := s.waitForSSH(ctx, s.timeout)
	if err != nil {
		return -1, err
	}

	// Phase 2: SSH is ready — run the actual command with the
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
	// Phase 1: Wait for SSH to become ready (probe with actual SSH command).
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
