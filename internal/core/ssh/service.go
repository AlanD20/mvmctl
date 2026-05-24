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

	"mvmctl/internal/infra/system"
)

// Service is a stateful SSH connection service.
// Matches Python's SSHService exactly.
type Service struct {
	ip      string
	user    string
	keyPath string
	timeout *int // nil = not set (no ConnectTimeout flag), matches Python's int | None
}

// NewService creates a new SSHService with the given connection parameters.
// Matches Python's SSHService.__init__() exactly.
// Python: key_path.chmod(CONST_FILE_PERMS_PRIVATE_KEY) raises OSError on failure.
// Go: os.Chmod returns error; we propagate it raw (no custom wrapping), matching
// Python's behavior of letting the exception propagate without modification.
func NewService(ip, user, keyPath string, timeout *int) (*Service, error) {
	if keyPath != "" {
		if err := os.Chmod(keyPath, 0600); err != nil {
			return nil, err
		}
	}

	// Matches Python's: logger.info("SSH service initialized for %s@%s", user, ip)
	slog.Info("SSH service initialized", "user", user, "ip", ip)

	return &Service{
		ip:      ip,
		user:    user,
		keyPath: keyPath,
		timeout: timeout,
	}, nil
}

// BuildCommand builds SSH command arguments for this connection.
// Matches Python's SSHService.build_command() exactly.
func (s *Service) BuildCommand(command string) []string {
	sshArgs := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
	}

	if s.timeout != nil {
		sshArgs = append(sshArgs, "-o", fmt.Sprintf("ConnectTimeout=%d", *s.timeout))
	}

	if s.keyPath != "" {
		if _, err := os.Stat(s.keyPath); err == nil {
			sshArgs = append(sshArgs, "-i", s.keyPath)
		}
	}

	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", s.user, s.ip))

	if command != "" {
		sshArgs = append(sshArgs, command)
	}

	return sshArgs
}

// ExecCommand executes SSH, replacing the current process.
// Matches Python's SSHService.exec_command() exactly.
//
// Python: os.execvp("ssh", ssh_args)
//   - Searches PATH for "ssh"
//   - Replaces current process with SSH
//   - Raises FileNotFoundError / OSError on failure (no stderr output, no os.Exit)
//
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
	if err := syscall.Exec(path, sshArgs, os.Environ()); err != nil {
		return err
	}
	return nil
}

// RunCommand runs SSH as a subprocess and returns the exit code.
// Matches Python's SSHService.run_command().
//
// Python: run_cmd(ssh_args, timeout=self._timeout, capture=False, check=False)
//   - capture=False → subprocess inherits stdout/stderr from parent
//   - check=False   → no exception on non-zero exit code
//   - timeout       → raises ProcessError with "timed out" message
//
// Python error catch scope:
//
//	try:
//	    result = run_cmd(ssh_args, timeout=self._timeout, capture=False, check=False)
//	except ProcessError as e:
//	    if "timed out" in str(e):
//	        raise ProcessError(f"SSH command timed out after {self._timeout}s") from None
//	    raise
//	return result.returncode
//
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

	var optsTimeout time.Duration
	if s.timeout != nil && *s.timeout > 0 {
		optsTimeout = time.Duration(*s.timeout) * time.Second
	}

	// capture=False → connect stdout/stderr directly to parent terminal
	result := system.RunCmdCompat(ctx, sshArgs, system.RunCmdOptions{
		Capture: false,
		Check:   false,
		Timeout: optsTimeout,
	})

	if result.Err != nil {
		// Check timeout via error message. RunCmdCompat formats timeout errors
		// as "Command timed out after Xs: ssh", matching Python's ProcessError.
		if strings.Contains(result.Err.Error(), "timed out") {
			timeoutVal := 0
			if s.timeout != nil {
				timeoutVal = *s.timeout
			}
			// Python: raise ProcessError(f"SSH command timed out after {self._timeout}s")
			return -1, fmt.Errorf("SSH command timed out after %ds", timeoutVal)
		}

		return -1, result.Err
	}

	if result.ExitCode != 0 {
		return result.ExitCode, nil
	}
	return 0, nil
}

// Connect connects to the host via SSH.
// Matches Python's SSHService.connect() exactly.
//
// Python:
//
//	def connect(self, command=None, *, exec_mode=True):
//	    if exec_mode and not command:
//	        self.exec_command(command)  # raises on failure
//	        return 0
//	    else:
//	        return self.run_command(command)
//
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
