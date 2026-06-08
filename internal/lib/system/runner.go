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

	"mvmctl/internal/infra"
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

// ────────────────────────────────────────────────────────────────────
// Process lifecycle — ProcessSignalHandler
// ────────────────────────────────────────────────────────────────────

// signalExitCodeBase matches Python's CONST_SIGNAL_EXIT_CODE_BASE = 128.
// POSIX convention: exit code = 128 + signal number for signal death.
const signalExitCodeBase = 128

// ProcessSignalHandler matches Python's ProcessSignalHandler class.
//
// Robust Linux process lifecycle manager handling:
// zombie detection, graceful shutdown, exit code capture,
// PID reuse mitigation, D-state awareness.
//
// Python signature:
//
//	class ProcessSignalHandler:
//	    def __init__(self, pid, *, is_child=True, expected_start_time=None,
//	                 graceful_timeout=30.0, kill_timeout=5.0, poll_interval=0.1)
//
// ProcessSignalHandlerConfig holds all configurable fields for ProcessSignalHandler.
// Instead of functional options, the caller populates this struct and passes it
// to NewProcessSignalHandler. Zero values use sensible defaults.
type ProcessSignalHandlerConfig struct {
	PID               int
	IsChild           bool
	ExpectedStartTime *int64
	GracefulTimeout   time.Duration // defaults to 30s if zero
	KillTimeout       time.Duration // defaults to 5s if zero
	PollInterval      time.Duration // defaults to 100ms if zero
}

const (
	defaultGracefulTimeout = 4 * time.Second
	defaultKillTimeout     = 5 * time.Second
	defaultPollInterval    = 100 * time.Millisecond
)

// ProcessSignalHandler struct to match Python's class.
type ProcessSignalHandler struct {
	Pid               int
	IsChild           bool
	ExpectedStartTime *int64
	GracefulTimeout   time.Duration
	KillTimeout       time.Duration
	PollInterval      time.Duration
	exitCode          *int
	reaped            bool
}

// NewProcessSignalHandler creates a new ProcessSignalHandler.
// Fields in cfg use their provided values, or fall back to defaults.
func NewProcessSignalHandler(cfg ProcessSignalHandlerConfig) *ProcessSignalHandler {
	return &ProcessSignalHandler{
		Pid:               cfg.PID,
		IsChild:           cfg.IsChild,
		ExpectedStartTime: cfg.ExpectedStartTime,
		GracefulTimeout:   infra.NonZero(cfg.GracefulTimeout, defaultGracefulTimeout),
		KillTimeout:       infra.NonZero(cfg.KillTimeout, defaultKillTimeout),
		PollInterval:      infra.NonZero(cfg.PollInterval, defaultPollInterval),
	}
}

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

// ── Instance methods ──

// IsAlive checks if the process is genuinely running (not zombie, not dead,
// not reused).  Returns false for: dead, zombie, already reaped, PID reused.
// Returns true for: running, sleeping, D-state.
//
// Uses a single /proc/<pid>/stat read — no kill(0) needed since /proc is
// world-readable and provides both state and start time in one call.
func (h *ProcessSignalHandler) IsAlive() bool {
	if h.reaped {
		return false
	}

	stat, err := readProcStat(h.Pid)
	if err != nil {
		return false // Process doesn't exist
	}

	// PID reuse check (zero extra cost — same /proc read)
	if h.ExpectedStartTime != nil && stat.StartTime != *h.ExpectedStartTime {
		return false
	}

	// Zombie or exiting (dead but not yet reaped)
	if stat.State == 'Z' || stat.State == 'X' {
		if h.IsChild {
			h.tryReap()
		}
		return false
	}

	// Alive: running (R), sleeping (S), D-state, etc.
	return true
}

// Kill sends SIGKILL. Returns true if signal was sent.
// Python's kill() uses the same logic.
func (h *ProcessSignalHandler) Kill() bool {
	return h.SendSignal(syscall.SIGKILL)
}

// TerminateBatch batch-terminates orphaned PIDs: SIGTERM all → wait →
// SIGKILL survivors.  Returns list of PIDs confirmed dead.
// Python's terminate_batch() class method uses the same logic.
func TerminateBatch(pids []int, gracefulTimeout time.Duration) []int {
	terminated := make([]int, 0)

	// Phase 1: SIGTERM all
	for _, pid := range pids {
		handler := NewProcessSignalHandler(ProcessSignalHandlerConfig{PID: pid, IsChild: false})
		if handler.SendSignal(syscall.SIGTERM) {
			terminated = append(terminated, pid)
		}
	}

	// Phase 2: Wait, then SIGKILL survivors
	if len(terminated) > 0 {
		time.Sleep(gracefulTimeout)
		for _, pid := range terminated {
			handler := NewProcessSignalHandler(ProcessSignalHandlerConfig{PID: pid, IsChild: false})
			if handler.IsAlive() {
				handler.Kill()
				// Python logs: logger.debug("Sent SIGKILL to abandoned process %d", pid)
				// — we skip the log here as it's caller's responsibility
			}
		}
	}

	return terminated
}

// SendSignal sends a signal. Returns true if signal was delivered.
// Python's send_signal() uses the same logic.
func (h *ProcessSignalHandler) SendSignal(sig syscall.Signal) bool {
	err := syscall.Kill(h.Pid, sig)
	if err != nil {
		// Catch ESRCH (process not found) and EPERM (no permission)
		return false
	}
	return true
}

// GracefulShutdown performs a full graceful shutdown:
// optional hook → SIGTERM → wait → SIGKILL → wait.
//
// The IsAlive guard is essential: kill(2) returns 0 for zombies on Linux,
// so without it we'd waste a full timeout cycle waiting on a zombie process.
//
// Args:
//   - preSignalHook: called before SIGTERM. Return false to skip SIGTERM
//     and only wait for exit (e.g., for Firecracker: call SendCtrlAltDel
//     here, then return false to wait for guest OS shutdown).
//
// Returns:
//   - Exit code if captured, nil if process survived SIGKILL or is not a child.
func (h *ProcessSignalHandler) GracefulShutdown(preSignalHook func() bool) *int {
	if !h.IsAlive() {
		return h.exitCode
	}

	// Optional pre-signal hook (e.g., Firecracker SendCtrlAltDel).
	// If the hook returns false, it handled shutdown — just wait for exit.
	if preSignalHook != nil && !preSignalHook() {
		return h.waitForExit(h.GracefulTimeout)
	}

	// Phase 1: SIGTERM
	if exitCode := h.signalWithReap(syscall.SIGTERM); exitCode != nil {
		return exitCode
	}

	// Phase 2: Wait for graceful exit
	if exitCode := h.waitForExit(h.GracefulTimeout); exitCode != nil {
		return exitCode
	}

	// Phase 3: SIGKILL
	if exitCode := h.signalWithReap(syscall.SIGKILL); exitCode != nil {
		return exitCode
	}

	// Phase 4: Wait for SIGKILL (should be near-instant)
	return h.waitForExit(h.KillTimeout)
}

// signalWithReap sends sig and reaps exit code if the process is gone.
// Returns the exit code if captured, or nil to signal "proceed to next phase".
func (h *ProcessSignalHandler) signalWithReap(sig syscall.Signal) *int {
	err := syscall.Kill(h.Pid, sig)
	if err == nil {
		return nil
	}
	// ESRCH = process doesn't exist, EPERM = no permission to signal
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		h.tryReap()
		return h.exitCode
	}
	// Unexpected error — log and proceed; SIGKILL may still succeed.
	slog.Warn("unexpected error from kill", "pid", h.Pid, "signal", sig, "error", err)
	return nil
}

// TryCaptureExit reaps the child exit code if available (non-blocking, WNOHANG).
// Returns the exit code or nil if the process hasn't exited yet.
// Safe to call multiple times.
func (h *ProcessSignalHandler) TryCaptureExit() *int {
	h.tryReap()
	return h.exitCode
}

// ── Private helpers ──

// tryReap attempts to reap a zombie child.  Safe to call multiple times.
// Python's _try_reap() uses the same logic.
func (h *ProcessSignalHandler) tryReap() {
	if !h.IsChild || h.reaped {
		return
	}
	var wstatus syscall.WaitStatus
	pid, err := syscall.Wait4(h.Pid, &wstatus, syscall.WNOHANG, nil)
	if err != nil {
		// ChildProcessError — already waited
		if errors.Is(err, syscall.ECHILD) {
			h.reaped = true
		}
		return
	}
	if pid != 0 {
		code := DecodeExitStatus(wstatus)
		h.exitCode = &code
		h.reaped = true
	}
}

// waitForExit polls for process exit with a monotonic deadline.
// Python's _wait_for_exit() uses the same logic.
func (h *ProcessSignalHandler) waitForExit(timeout time.Duration) *int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.IsChild {
			var wstatus syscall.WaitStatus
			pid, err := syscall.Wait4(h.Pid, &wstatus, syscall.WNOHANG, nil)
			if err != nil {
				if errors.Is(err, syscall.ECHILD) {
					h.reaped = true
					return h.exitCode
				}
				return h.exitCode
			}
			if pid != 0 {
				code := DecodeExitStatus(wstatus)
				h.exitCode = &code
				h.reaped = true
				return h.exitCode
			}
		} else {
			if !h.IsAlive() {
				h.reaped = true
				return h.exitCode
			}
		}
		time.Sleep(h.PollInterval)
	}
	return nil
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
