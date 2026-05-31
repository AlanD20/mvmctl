package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/infra/errs"
)

// RunCmdOptions matches Python's run_cmd() parameter set exactly.
// Fields map to Python's: args, check, capture, cwd, timeout, input, env,
// privileged, text.  Plus a StartOnly flag for background/spawn-and-forget.
//
// Interactive controls sudo mode:
//   - true  → plain "sudo" (allows password prompt via TTY forwarding)
//   - false → "sudo -n" (non-interactive, fails immediately if password required)
//
// Default is false (non-interactive), suitable for automated operations.
type RunCmdOptions struct {
	Cmd         string
	Args        []string
	Check       bool
	Capture     bool
	Cwd         string
	Timeout     time.Duration
	Input       string
	Env         map[string]string
	Privileged  bool
	Interactive bool
	Text        bool
	StartOnly   bool
}

// ────────────────────────────────────────────────────────────────────
// Signal handling — SigtermContext
// ────────────────────────────────────────────────────────────────────

// SigtermContext matches Python's SigtermContext class.
//
// It sets up a SIGTERM signal handler on Enter, restores original handler
// on Exit.  The signal handler calls the provided cleanup function.
//
// Python signature:
//
//	class SigtermContext:
//	    def __init__(self, cleanup_fn: Callable[[], None]) -> None
//	    def __enter__(self) -> SigtermContext
//	    def __exit__(self, ...) -> None
type SigtermContext struct {
	cleanupFn func()
	oldCh     chan os.Signal
}

// NewSigtermContext creates a SigtermContext with the given cleanup function,
// matching Python's SigtermContext(cleanup_fn).
func NewSigtermContext(cleanupFn func()) *SigtermContext {
	return &SigtermContext{cleanupFn: cleanupFn}
}

// Enter sets up the SIGTERM handler.  When SIGTERM is received, cleanupFn
// is called.  Returns self for chaining, matching Python's __enter__.
func (s *SigtermContext) Enter() *SigtermContext {
	s.oldCh = make(chan os.Signal, 1)
	signal.Notify(s.oldCh, syscall.SIGTERM)
	go func() {
		<-s.oldCh
		s.cleanupFn()
	}()
	return s
}

// Exit restores the original signal handling by stopping notification,
// matching Python's __exit__.
func (s *SigtermContext) Exit() {
	signal.Stop(s.oldCh)
	close(s.oldCh)
}

// WithSigtermContext wraps SigtermContext usage in a convenient pattern,
// matching Python's sigterm_context() context manager:
//
//	with sigterm_context(my_cleanup):
//	    # do work
func WithSigtermContext(cleanupFn func(), fn func() error) error {
	ctx := NewSigtermContext(cleanupFn)
	ctx.Enter()
	defer ctx.Exit()
	return fn()
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
type ProcessSignalHandler struct {
	Pid               int
	IsChild           bool
	ExpectedStartTime *int64
	GracefulTimeout   time.Duration
	KillTimeout       time.Duration
	PollInterval      time.Duration
	exitCode          *int
	reaped            bool
	lastErr           error
}

// NewProcessSignalHandler creates a new ProcessSignalHandler, matching Python's
// ProcessSignalHandler(pid, is_child=..., expected_start_time=..., ...).
func NewProcessSignalHandler(pid int, opts ...ProcessSignalHandlerOption) *ProcessSignalHandler {
	h := &ProcessSignalHandler{
		Pid:             pid,
		IsChild:         true,
		GracefulTimeout: 30 * time.Second,
		KillTimeout:     5 * time.Second,
		PollInterval:    100 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ProcessSignalHandlerOption configures a ProcessSignalHandler.
type ProcessSignalHandlerOption func(*ProcessSignalHandler)

// WithIsChild sets whether this process was spawned by us (can waitpid).
// False for external/orphaned processes.
func WithIsChild(v bool) ProcessSignalHandlerOption {
	return func(h *ProcessSignalHandler) { h.IsChild = v }
}

// WithExpectedStartTime sets the expected process start time for PID reuse
// detection (clock ticks from /proc/<pid>/stat field 22).
func WithExpectedStartTime(t int64) ProcessSignalHandlerOption {
	return func(h *ProcessSignalHandler) {
		h.ExpectedStartTime = &t
	}
}

// WithGracefulTimeout sets the seconds to wait after SIGTERM before SIGKILL.
func WithGracefulTimeout(d time.Duration) ProcessSignalHandlerOption {
	return func(h *ProcessSignalHandler) { h.GracefulTimeout = d }
}

// WithKillTimeout sets the seconds to wait after SIGKILL before giving up.
func WithKillTimeout(d time.Duration) ProcessSignalHandlerOption {
	return func(h *ProcessSignalHandler) { h.KillTimeout = d }
}

// WithPollInterval sets the seconds between poll checks.
func WithPollInterval(d time.Duration) ProcessSignalHandlerOption {
	return func(h *ProcessSignalHandler) { h.PollInterval = d }
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

// GetProcessStartTime reads /proc/<pid>/stat (field 22, clock ticks) and
// returns the start time.  Returns nil if the process doesn't exist or is
// unreadable.  Python's _get_process_start_time() uses the same logic:
//
//	with open(f"/proc/{pid}/stat") as f:
//	    content = f.read()
//	fields = content[content.rfind(")") + 2 :].split()
//	return int(fields[19])
func GetProcessStartTime(pid int) *int64 {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil
	}
	content := string(data)
	closeParenIdx := strings.LastIndex(content, ")")
	if closeParenIdx == -1 {
		return nil
	}
	// content after last ")" + " " (2 chars)
	rest := content[closeParenIdx+2:]
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return nil
	}
	// Field 22 overall (1-indexed) = index 19 (0-indexed) after removing comm
	startTime, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return nil
	}
	return &startTime
}

// IsPidReused checks if PID has been reused by comparing start times.
// Returns true if the current process with this PID has a different
// start time than expected (meaning the original process is gone).
// Python's _is_pid_reused() uses the same logic.
func IsPidReused(pid int, expectedStartTime int64) bool {
	currentStartTime := GetProcessStartTime(pid)
	if currentStartTime == nil {
		return false // Process doesn't exist, so no reuse concern
	}
	return *currentStartTime != expectedStartTime
}

// ── Instance methods ──

// IsAlive checks if the process is genuinely running (not zombie, not dead,
// not reused).  Returns false for: dead, zombie, already reaped, PID reused.
// Returns true for: running, sleeping, D-state.
// Python's is_alive() uses the same logic.
func (h *ProcessSignalHandler) IsAlive() bool {
	if h.reaped {
		return false
	}

	// Check PID reuse first
	if h.ExpectedStartTime != nil {
		if IsPidReused(h.Pid, *h.ExpectedStartTime) {
			return false
		}
	}

	// Check for zombie state via /proc
	if h.isZombie() {
		if h.IsChild {
			h.tryReap()
		}
		return false
	}

	// os.Kill(pid, 0) check — signal 0 tests for process existence
	err := syscall.Kill(h.Pid, syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	if errors.Is(err, syscall.EPERM) {
		return true // Exists but no permission to signal
	}
	h.lastErr = err
	return false
}

// Kill sends SIGKILL. Returns true if signal was sent.
// Python's kill() uses the same logic.
func (h *ProcessSignalHandler) Kill() bool {
	return h.SendSignal(syscall.SIGKILL)
}

// KillAndWait sends SIGKILL and polls until the process is dead.
// Returns true if the process was confirmed dead within timeout.
// Python's kill_and_wait() uses the same logic.
func (h *ProcessSignalHandler) KillAndWait(killTimeout time.Duration) bool {
	// Already dead — nothing to do
	if !h.IsAlive() {
		return true
	}

	h.SendSignal(syscall.SIGKILL)

	deadline := time.Now().Add(killTimeout)
	for time.Now().Before(deadline) {
		if !h.IsAlive() {
			return true
		}
		time.Sleep(h.PollInterval)
	}

	return false
}

// TerminateBatch batch-terminates orphaned PIDs: SIGTERM all → wait →
// SIGKILL survivors.  Returns list of PIDs confirmed dead.
// Python's terminate_batch() class method uses the same logic.
func TerminateBatch(pids []int, gracefulTimeout time.Duration) []int {
	terminated := make([]int, 0)

	// Phase 1: SIGTERM all
	for _, pid := range pids {
		handler := NewProcessSignalHandler(pid, WithIsChild(false))
		if handler.SendSignal(syscall.SIGTERM) {
			terminated = append(terminated, pid)
		}
	}

	// Phase 2: Wait, then SIGKILL survivors
	if len(terminated) > 0 {
		time.Sleep(gracefulTimeout)
		for _, pid := range terminated {
			handler := NewProcessSignalHandler(pid, WithIsChild(false))
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
// Args:
//   - preSignalHook: called before SIGTERM. Return false to skip SIGTERM
//     and only wait for exit (e.g., for Firecracker: call SendCtrlAltDel
//     here, then return false to wait for guest OS shutdown).
//
// Returns:
//   - Exit code if captured, nil if process survived SIGKILL or is not a child.
//
// Python's graceful_shutdown() uses the same logic.
func (h *ProcessSignalHandler) GracefulShutdown(preSignalHook func() bool) *int {
	if !h.IsAlive() {
		return h.exitCode
	}

	// Optional pre-signal hook (e.g., Firecracker SendCtrlAltDel)
	if preSignalHook != nil {
		if !preSignalHook() {
			// Hook handled the shutdown, just wait for exit
			return h.waitForExit(h.GracefulTimeout)
		}
	}

	// Phase 1: SIGTERM
	err := syscall.Kill(h.Pid, syscall.SIGTERM)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
			h.tryReap()
			return h.exitCode
		}
		h.lastErr = err
		return nil
	}

	// Phase 2: Wait for graceful exit
	exitCode := h.waitForExit(h.GracefulTimeout)
	if exitCode != nil {
		return exitCode
	}

	// Phase 3: SIGKILL
	err = syscall.Kill(h.Pid, syscall.SIGKILL)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
			h.tryReap()
			return h.exitCode
		}
		h.lastErr = err
		return nil
	}

	// Phase 4: Wait for SIGKILL (should be near-instant)
	return h.waitForExit(h.KillTimeout)
}

// WaitAndCaptureExit reaps the child process and captures exit code.
// Safe to call multiple times.  Python's wait_and_capture_exit() uses the
// same logic.
func (h *ProcessSignalHandler) WaitAndCaptureExit() *int {
	if h.reaped {
		return h.exitCode
	}
	if !h.IsChild {
		return nil
	}
	var wstatus syscall.WaitStatus
	pid, err := syscall.Wait4(h.Pid, &wstatus, syscall.WNOHANG, nil)
	if err != nil {
		// ChildProcessError — already waited or not a child
		if errors.Is(err, syscall.ECHILD) {
			h.reaped = true
		}
		return h.exitCode
	}
	if pid != 0 {
		code := DecodeExitStatus(wstatus)
		h.exitCode = &code
		h.reaped = true
	}
	return h.exitCode
}

// LastErr returns any unexpected error encountered during signal operations.
func (h *ProcessSignalHandler) LastErr() error {
	return h.lastErr
}

// ── Private helpers ──

// isZombie checks /proc/<pid>/stat for Z state.  Handles comm names with parens.
// Python's _is_zombie() uses the same logic.
func (h *ProcessSignalHandler) isZombie() bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(h.Pid), "stat"))
	if err != nil {
		return false
	}
	content := string(data)
	stateIdx := strings.LastIndex(content, ")")
	if stateIdx == -1 || stateIdx+2 >= len(content) {
		return false
	}
	return content[stateIdx+2] == 'Z'
}

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

// IsProcessRunning checks if a process is still running by PID.
// ── Signal helpers ──

// SendSignal sends a signal to a process by PID.
// Returns true if the signal was delivered successfully.
func SendSignal(pid int, sig syscall.Signal) bool {
	err := syscall.Kill(pid, sig)
	if err != nil {
		return false
	}
	return true
}

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

	cmdOpts := RunCmdOptions{
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
				ch <- StreamLine{Err: errs.ProcessError(
					fmt.Sprintf("Command not found: %s", args[0]),
				)}
			} else {
				ch <- StreamLine{Err: errs.ProcessError(err.Error())}
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
				ch <- StreamLine{Err: errs.ProcessError(
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
