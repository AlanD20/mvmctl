package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/pkg/errs"
)

// RunCmdOpts configures subprocess execution.
// Zero values use sensible defaults.
//
// Env is merged into the current process environment (never replaces).
// Interactive controls sudo mode:
//   - true  → plain "sudo" (allows password prompt via TTY forwarding)
//   - false → "sudo -n" (non-interactive, fails immediately if password required)
//
// Default is false (non-interactive), suitable for automated operations.
type RunCmdOpts struct {
	Check       bool              // return error on non-zero exit
	Capture     bool              // capture stdout/stderr
	Cwd         string            // working directory
	Timeout     time.Duration     // execution timeout
	Input       string            // stdin content
	Env         map[string]string // merged into process environment (append, never replace)
	Privileged  bool              // run via sudo if not root
	Interactive bool              // allow sudo password prompt
	StartOnly   bool              // spawn and forget (no wait)
}

// DefaultGracefulTimeout is the default timeout for graceful SIGTERM shutdown.
const DefaultGracefulTimeout = 4 * time.Second

// DefaultKillTimeout is the default timeout for SIGKILL shutdown.
const DefaultKillTimeout = 5 * time.Second

// signalExitCodeBase matches Python's CONST_SIGNAL_EXIT_CODE_BASE = 128.
// POSIX convention: exit code = 128 + signal number for signal death.
const signalExitCodeBase = 128

// ── Static helpers ──

// DecodeExitStatus decodes os.Waitpid() / syscall.Wait4 status into a
// conventional exit code, matching Python's _decode_exit_status():
//
//	Normal exit code (0-255) or 128+signal for signal death.
func DecodeExitStatus(wstatus syscall.WaitStatus) int {
	if wstatus.Exited() {
		return wstatus.ExitStatus()
	}
	if wstatus.Signaled() {
		return signalExitCodeBase + int(wstatus.Signal())
	}
	return -1
}

// procStat holds parsed fields from /proc/<pid>/stat.
type procStat struct {
	State     byte  // process state character: R, S, D, Z, X, etc.
	StartTime int64 // field 22, clock ticks since boot
}

// readProcStat reads and parses /proc/<pid>/stat in one shot.
// Returns an error if the process doesn't exist or the file is malformed.
func readProcStat(pid int) (*procStat, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil, err
	}
	content := string(data)
	closeParenIdx := strings.LastIndex(content, ")")
	if closeParenIdx == -1 {
		return nil, fmt.Errorf("malformed /proc/%d/stat: no closing paren in comm", pid)
	}
	rest := content[closeParenIdx+2:]
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return nil, fmt.Errorf("malformed /proc/%d/stat: only %d fields after comm", pid, len(fields))
	}
	state := fields[0][0]                                  // field 3 (0-indexed: 0 after comm removal)
	startTime, err := strconv.ParseInt(fields[19], 10, 64) // field 22
	if err != nil {
		return nil, fmt.Errorf("malformed /proc/%d/stat: bad start_time: %w", pid, err)
	}
	return &procStat{State: state, StartTime: startTime}, nil
}

// GetProcessStartTime reads /proc/<pid>/stat (field 22, clock ticks).
// Returns nil if the process doesn't exist or /proc is unreadable.
func GetProcessStartTime(pid int) *int64 {
	stat, err := readProcStat(pid)
	if err != nil {
		return nil
	}
	return &stat.StartTime
}

// ── Process lifecycle functions ──

// IsProcessAlive checks if a process with the given PID is genuinely running
// (not zombie, not dead, PID not reused).  Returns false for: dead, zombie,
// already reaped, PID reused.  Returns true for: running, sleeping, D-state.
func IsProcessAlive(pid int, expectedStartTime *int64) bool {
	stat, err := readProcStat(pid)
	if err != nil {
		return false
	}
	// PID reuse check (zero extra cost — same /proc read)
	if expectedStartTime != nil && stat.StartTime != *expectedStartTime {
		return false
	}
	// Zombie or exiting (dead but not yet reaped)
	if stat.State == 'Z' || stat.State == 'X' {
		return false
	}
	// Alive: running (R), sleeping (S), D-state, etc.
	return true
}

// KillProcess sends SIGKILL to a process. Returns true if signal was delivered.
func KillProcess(pid int) bool {
	return syscall.Kill(pid, syscall.SIGKILL) == nil
}

// CaptureExitCode attempts to reap a child process exit code (non-blocking).
// Returns the exit code if the process has exited, nil otherwise.
func CaptureExitCode(pid int) *int {
	var wstatus syscall.WaitStatus
	npid, err := syscall.Wait4(pid, &wstatus, syscall.WNOHANG, nil)
	if err != nil {
		return nil
	}
	if npid != 0 {
		code := DecodeExitStatus(wstatus)
		return &code
	}
	return nil
}

// ShutdownConfig configures GracefulShutdown.
// Zero values use sensible defaults for timeout fields.
type ShutdownConfig struct {
	Pid               int
	IsChild           bool
	PreSignalHook     func() bool   // optional: called before SIGTERM; if returns false, wait only
	GracefulTimeout   time.Duration // zero = DefaultGracefulTimeout
	KillTimeout       time.Duration // zero = DefaultKillTimeout
	ExpectedStartTime *int64        // optional: PID reuse detection
}

// GracefulShutdown performs a complete SIGTERM → wait → SIGKILL cycle.
// Fields in cfg use their provided values, or fall back to defaults.
// Returns exit code if captured, nil if the process survived SIGKILL.
func GracefulShutdown(cfg ShutdownConfig) *int {
	gracefulTimeout := cfg.GracefulTimeout
	if gracefulTimeout == 0 {
		gracefulTimeout = DefaultGracefulTimeout
	}
	killTimeout := cfg.KillTimeout
	if killTimeout == 0 {
		killTimeout = DefaultKillTimeout
	}

	if !IsProcessAlive(cfg.Pid, cfg.ExpectedStartTime) {
		if cfg.IsChild {
			if code := CaptureExitCode(cfg.Pid); code != nil {
				return code
			}
		}
		return nil
	}

	// Pre-signal hook: if it returns false, hook initiated shutdown — just wait
	if cfg.PreSignalHook != nil && !cfg.PreSignalHook() {
		return waitForExit(cfg.Pid, cfg.IsChild, gracefulTimeout)
	}

	// Phase 1: SIGTERM
	if exitCode := signalAndCheck(cfg.Pid, cfg.IsChild, syscall.SIGTERM); exitCode != nil {
		return exitCode
	}

	// Phase 2: Wait for graceful exit
	if exitCode := waitForExit(cfg.Pid, cfg.IsChild, gracefulTimeout); exitCode != nil {
		return exitCode
	}

	// Phase 3: SIGKILL
	if exitCode := signalAndCheck(cfg.Pid, cfg.IsChild, syscall.SIGKILL); exitCode != nil {
		return exitCode
	}

	// Phase 4: Wait for SIGKILL (should be near-instant)
	return waitForExit(cfg.Pid, cfg.IsChild, killTimeout)
}

// signalAndCheck sends a signal and checks if the process is gone.
// Returns exit code if reaped, nil to signal "proceed to next phase".
func signalAndCheck(pid int, isChild bool, sig syscall.Signal) *int {
	err := syscall.Kill(pid, sig)
	if err == nil {
		return nil
	}
	// ESRCH = process doesn't exist, EPERM = no permission to signal
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		if isChild {
			return CaptureExitCode(pid)
		}
		return nil
	}
	// Unexpected error — log and proceed; SIGKILL may still succeed.
	slog.Warn("unexpected error from kill", "pid", pid, "signal", sig, "error", err)
	return nil
}

// waitForExit polls for process exit with a monotonic deadline.
func waitForExit(pid int, isChild bool, timeout time.Duration) *int {
	const pollInterval = 100 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isChild {
			if code := CaptureExitCode(pid); code != nil {
				return code
			}
		} else {
			if !IsProcessAlive(pid, nil) {
				return nil
			}
		}
		time.Sleep(pollInterval)
	}
	return nil // timeout
}

// ────────────────────────────────────────────────────────────────────
// Process detection
// ────────────────────────────────────────────────────────────────────

// HasAncestorWithCmdline walks the PPID chain for pid upward through /proc.
// Returns true if any ancestor process has the given substrings in its
// command line (case-insensitive). Returns false if the parent chain reaches
// PID 1 without finding a match or if /proc is inaccessible.
func HasAncestorWithCmdline(pid int, substr ...string) bool {
	visited := make(map[int]struct{})
	current := pid

	for current > 1 {
		if _, ok := visited[current]; ok {
			return false
		}
		visited[current] = struct{}{}

		// Read /proc/<pid>/cmdline — null-byte separated arguments
		cmdlinePath := filepath.Join("/proc", strconv.Itoa(current), "cmdline")
		raw, err := os.ReadFile(cmdlinePath)
		if err != nil {
			return false
		}
		// cmdline uses null bytes as separators; decode with replace
		// and convert to lowercase for case-insensitive matching
		cmdline := strings.ToLower(string(raw))
		cmdline = strings.ReplaceAll(cmdline, "\x00", " ")
		for _, s := range substr {
			if strings.Contains(cmdline, s) {
				return true
			}
		}

		// Read PPid from /proc/<pid>/status
		statusPath := filepath.Join("/proc", strconv.Itoa(current), "status")
		data, err := os.ReadFile(statusPath)
		if err != nil {
			return false
		}
		ppid := ParseProcStatusField(string(data), "PPid:")
		if ppid < 0 {
			break
		}
		current = ppid
	}

	return false
}

// ────────────────────────────────────────────────────────────────────
// Subprocess execution
// ────────────────────────────────────────────────────────────────────

// RunResult holds command output.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Success returns true if the command exited with code 0.
// This is the correct check regardless of the Check option —
// non-zero exits and exceptional errors (not found, timeout)
// both produce a non-zero ExitCode.
func (r *RunResult) Success() bool { return r.ExitCode == 0 }

// StreamLine carries either a line of output or a final error.
type StreamLine struct {
	Line string
	Err  error
}

// CommandRunner is the interface for executing subprocesses.
// Enables mocking in tests via testutil.FakeRunner.
type CommandRunner interface {
	Run(ctx context.Context, args []string, opts RunCmdOpts) (*RunResult, error)
	Stream(ctx context.Context, args []string, opts RunCmdOpts) (<-chan StreamLine, error)
}

// RealRunner executes commands using os/exec.
type RealRunner struct{}

// DefaultRunner is the global CommandRunner.
// Defaults to RealRunner. Tests can replace it with FakeRunner.
var DefaultRunner CommandRunner = &RealRunner{}

// Run executes a command and returns the result.
func (r *RealRunner) Run(ctx context.Context, args []string, opts RunCmdOpts) (*RunResult, error) {
	if len(args) == 0 {
		return &RunResult{ExitCode: 1}, fmt.Errorf("no command specified")
	}

	// ── Build argument list, handling privileged mode ──
	cmdArgs := args
	if opts.Privileged && !IsRoot() {
		_ = requireMvmGroupMembership() // warn only
		if opts.Interactive {
			cmdArgs = append([]string{"sudo"}, args...)
		} else {
			cmdArgs = append([]string{"sudo", "-n"}, args...)
		}
	}

	// ── Create timeout context if needed ──
	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// ── Build exec.Cmd ──
	cmd := exec.CommandContext(runCtx, cmdArgs[0], cmdArgs[1:]...)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}

	// Environment: merge into current process env (always append)
	if opts.Env != nil {
		env := os.Environ()
		for k, v := range opts.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	// ── Stdin ──
	if opts.Input != "" {
		cmd.Stdin = strings.NewReader(opts.Input)
	} else if opts.Interactive && opts.Privileged && !IsRoot() {
		cmd.Stdin = os.Stdin
	}

	// ── Stdout / Stderr ──
	var stdout, stderr strings.Builder
	if opts.Capture {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	// ── StartOnly (background / fire-and-forget) ──
	if opts.StartOnly {
		if err := cmd.Start(); err != nil {
			return &RunResult{ExitCode: -1},
				errs.WrapMsg(errs.CodeProcessError, fmt.Sprintf("failed to start: %s", cmdArgs[0]), err)
		}
		return &RunResult{ExitCode: 0}, nil
	}

	// ── Run and capture ──
	err := cmd.Run()

	result := &RunResult{
		ExitCode: 0,
	}
	if opts.Capture {
		result.Stdout = stdout.String()
		result.Stderr = strings.TrimSpace(stderr.String())
	}

	if err != nil {
		// 1. Timeout — must check BEFORE exit-error because CommandContext
		//    kills the process on timeout, which also produces ExitError.
		if opts.Timeout > 0 && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.ExitCode = -1
			timeoutStr := strconv.FormatFloat(opts.Timeout.Seconds(), 'f', -1, 64)
			if !strings.ContainsRune(timeoutStr, '.') {
				timeoutStr += ".0"
			}
			return result, errs.New(errs.CodeProcessError,
				fmt.Sprintf("Command timed out after %ss: %s", timeoutStr, cmdArgs[0]))
		}

		// 2. Command not found
		if errors.Is(err, exec.ErrNotFound) {
			result.ExitCode = -1
			return result, errs.New(errs.CodeProcessError,
				fmt.Sprintf("Command not found: %s", cmdArgs[0]))
		}

		// 3. Non-zero exit — only error when Check is true
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			if opts.Check {
				sanitized := strings.TrimSpace(result.Stderr)
				if len(sanitized) > 100 {
					sanitized = sanitized[:100] + "..."
				}
				msg := fmt.Sprintf("Command failed (exit %d): %s", result.ExitCode, cmdArgs[0])
				if sanitized != "" {
					msg += "\n" + sanitized
				}
				return result, errs.New(errs.CodeProcessError, msg)
			}
			return result, nil
		}

		// 4. Other error — wrap with CodeProcessError
		result.ExitCode = -1
		return result, errs.Wrap(errs.CodeProcessError, err)
	}

	return result, nil
}

// Stream executes a command and streams stdout lines as they are produced.
func (r *RealRunner) Stream(ctx context.Context, args []string, opts RunCmdOpts) (<-chan StreamLine, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no command specified")
	}

	// Build command with privileged mode
	cmdArgs := args
	if opts.Privileged && !IsRoot() {
		if opts.Interactive {
			cmdArgs = append([]string{"sudo"}, args...)
		} else {
			cmdArgs = append([]string{"sudo", "-n"}, args...)
		}
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	// If context is cancelled, force-kill after 2s and force-close pipes
	// after 3s. This prevents deadlock when orphaned subprocesses hold
	// pipe descriptors open.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	// Merge stderr into stdout — must be set AFTER StdoutPipe(),
	// which sets cmd.Stdout to the write end of the pipe.
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		stdout.Close()
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errs.New(errs.CodeProcessError,
				fmt.Sprintf("Command not found: %s", cmdArgs[0]))
		}
		return nil, errs.New(errs.CodeProcessError, err.Error())
	}

	ch := make(chan StreamLine)
	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			ch <- StreamLine{Line: scanner.Text()}
		}

		// Pipe closed (process exited or WaitDelay expired) — reap.
		waitErr := cmd.Wait()
		if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
			if errors.Is(waitErr, exec.ErrWaitDelay) {
				ch <- StreamLine{Err: errs.New(errs.CodeProcessError,
					fmt.Sprintf("Command output truncated (orphaned subprocess?): %s", cmdArgs[0]))}
			} else if exitErr, ok := waitErr.(*exec.ExitError); ok {
				ch <- StreamLine{Err: errs.New(errs.CodeProcessError,
					fmt.Sprintf("Command failed (exit %d): %s", exitErr.ExitCode(), cmdArgs[0]))}
			}
		}
	}()

	return ch, nil
}
