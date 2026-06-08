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

// RunCmdOpts matches Python's run_cmd() parameter set exactly.
// Fields map to Python's: args, check, capture, cwd, timeout, input, env,
// privileged, text.  Plus a StartOnly flag for background/spawn-and-forget.
//
// Interactive controls sudo mode:
//   - true  → plain "sudo" (allows password prompt via TTY forwarding)
//   - false → "sudo -n" (non-interactive, fails immediately if password required)
//
// Default is false (non-interactive), suitable for automated operations.
type RunCmdOpts struct {
	Cmd         string
	Args        []string
	Check       bool
	Capture     bool
	Cwd         string
	Timeout     time.Duration
	Input       string
	Env         map[string]string
	AppendEnv   map[string]string // merged into current env, not replacing
	Privileged  bool
	Interactive bool
	Text        bool
	StartOnly   bool
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
	PreSignalHook     func() bool  // optional: called before SIGTERM; if returns false, wait only
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
// CommandRunner interface and RealRunner implementation
// ────────────────────────────────────────────────────────────────────

// CommandRunner is the interface for executing subprocesses.
// Enables mocking in tests via testutil.FakeRunner.
type CommandRunner interface {
	Run(ctx context.Context, args []string, opts ...RunOption) (*RunResult, error)
	Stream(ctx context.Context, args []string, opts ...RunOption) (<-chan StreamLine, error)
}

// RunOption configures subprocess execution.
type RunOption func(*runConfig)

// runConfig holds the internal configuration for a command execution.
type runConfig struct {
	timeout     time.Duration
	cwd         string
	env         map[string]string
	stdin       string
	capture     bool
	privileged  bool
	interactive bool
	check       bool
	startOnly   bool
}

// defaultRunConfig returns a runConfig with sensible defaults.
func defaultRunConfig() *runConfig {
	return &runConfig{
		capture: true,
	}
}

// RunResult holds command output.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// StreamLine carries either a line of output or a final error.
type StreamLine struct {
	Line string
	Err  error
}

// RealRunner executes commands using os/exec.
type RealRunner struct{}

// DefaultRunner is the global CommandRunner used by RunCmdCompat and StreamCmd.
// Defaults to RealRunner. Tests can replace it with FakeRunner.
var DefaultRunner CommandRunner = &RealRunner{}

// Run executes a command and returns the result.
// Delegates to the existing runCmdInternal function.
func (r *RealRunner) Run(ctx context.Context, args []string, opts ...RunOption) (*RunResult, error) {
	cfg := defaultRunConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	cmdOpts := RunCmdOpts{
		Cmd:         args[0],
		Capture:     cfg.capture,
		Cwd:         cfg.cwd,
		Timeout:     cfg.timeout,
		Input:       cfg.stdin,
		Env:         cfg.env,
		Privileged:  cfg.privileged,
		Interactive: cfg.interactive,
		Text:        true,
		Check:       cfg.check,
		StartOnly:   cfg.startOnly,
	}
	if len(args) > 1 {
		cmdOpts.Args = args[1:]
	}

	result := runCmdInternal(ctx, cmdOpts)
	if result.Err != nil {
		return &RunResult{
			Stdout:   result.Stdout,
			Stderr:   result.Stderr,
			ExitCode: result.ExitCode,
		}, result.Err
	}

	return &RunResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, nil
}

// Stream executes a command and streams stdout lines as they are produced.
// Implements the streaming logic directly rather than delegating to StreamCmd
// to avoid circular dependency (StreamCmd delegates to DefaultRunner.Stream).
func (r *RealRunner) Stream(ctx context.Context, args []string, opts ...RunOption) (<-chan StreamLine, error) {
	cfg := defaultRunConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	if len(args) == 0 {
		return nil, fmt.Errorf("no command specified")
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if cfg.cwd != "" {
		cmd.Dir = cfg.cwd
	}

	// Merge stderr into stdout, matching StreamCmd behavior.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	ch := make(chan StreamLine)

	go func() {
		defer close(ch)
		defer pw.Close()
		defer pr.Close()

		if err := cmd.Start(); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				ch <- StreamLine{Err: errs.New(errs.CodeProcessError,
					fmt.Sprintf("Command not found: %s", args[0]),
				)}
			} else {
				ch <- StreamLine{Err: errs.New(errs.CodeProcessError, err.Error())}
			}
			return
		}

		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case ch <- StreamLine{Line: line}:
			case <-ctx.Done():
				// Context cancelled; the command will be killed by CommandContext
				_ = cmd.Wait()
				return
			}
		}

		// Wait for command to finish
		waitErr := cmd.Wait()
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				ch <- StreamLine{Err: errs.New(errs.CodeProcessError,
					fmt.Sprintf("Command failed (exit %d): %s", exitErr.ExitCode(), args[0]),
				)}
			}
		}
	}()

	return ch, nil
}

// WithTimeout sets execution timeout.
func WithTimeout(d time.Duration) RunOption {
	return func(cfg *runConfig) { cfg.timeout = d }
}

// WithCWD sets working directory.
func WithCWD(dir string) RunOption {
	return func(cfg *runConfig) { cfg.cwd = dir }
}

// WithEnv sets environment variables.
func WithEnv(env map[string]string) RunOption {
	return func(cfg *runConfig) { cfg.env = env }
}

// WithStdin sets stdin input.
func WithStdin(input string) RunOption {
	return func(cfg *runConfig) { cfg.stdin = input }
}

// WithCapture controls whether stdout/stderr are captured.
func WithCapture(capture bool) RunOption {
	return func(cfg *runConfig) { cfg.capture = capture }
}

// WithPrivileged prepends sudo when not root.
func WithPrivileged(priv bool) RunOption {
	return func(cfg *runConfig) { cfg.privileged = priv }
}

// WithInteractive controls sudo mode: true uses plain "sudo" (allows password
// prompt via TTY forwarding), false uses "sudo -n" (non-interactive).
func WithInteractive(interactive bool) RunOption {
	return func(cfg *runConfig) { cfg.interactive = interactive }
}

// WithCheck causes non-zero exit codes to return as errors.
func WithCheck(check bool) RunOption {
	return func(cfg *runConfig) { cfg.check = check }
}

// WithStartOnly spawns the process without waiting for it to complete.
func WithStartOnly(startOnly bool) RunOption {
	return func(cfg *runConfig) { cfg.startOnly = startOnly }
}
