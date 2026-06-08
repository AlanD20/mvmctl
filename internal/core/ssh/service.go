package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/lib/system"
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

// Connect connects to the host via SSH.
// Matches Python's SSHService.connect() exactly.
// Go: execMode maps to Python's exec_mode parameter.
// Errors from ExecCommand propagate via return (matching Python's exception propagation).
func (s *Service) Connect(ctx context.Context, command string, execMode bool) (int, error) {
	// exec_mode=True and not command (None/"") → interactive session (exec)
	if execMode && command == "" {
		return 0, s.ExecCommand(command)
	}
	// exec_mode=False or command provided → subprocess mode
	return s.RunCommand(ctx, command)
}
