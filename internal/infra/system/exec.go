package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"mvmctl/internal/infra/errs"
)

const stderrPreviewLimit = 100

// RunCmdResult holds the result of running a command.
// Matches Python's subprocess.CompletedProcess fields.
//
// When Capture=false, Stdout and Stderr are zero-value (empty string)
// and should not be read — the output was inherited from the parent
// process (os.Stdout / os.Stderr).  Python returns None in this case.
//
// When Text=false (bytes mode), Stdout and Stderr still contain strings
// decoded from the raw bytes (matching Python's behavior for the common
// case where subprocess output is valid UTF-8 text even with text=False).
type RunCmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Success  bool
	Err      error // ProcessError equivalent, nil on success
	// StdoutBytes and StderrBytes always contain the raw captured output,
	// regardless of the Text setting.  Populated only when Capture=true.
	StdoutBytes []byte
	StderrBytes []byte
}

// DefaultRunCmdOpts returns RunCmdOpts with the same defaults as
// Python's run_cmd(): check=True, capture=True, text=True.
func DefaultRunCmdOpts() RunCmdOpts {
	return RunCmdOpts{
		Check:   true,
		Capture: true,
		Text:    true,
	}
}

// ── std{err,out} helpers ──

// sanitizeStderr matches Python's _sanitize_stderr(): strip, then truncate to
// _STDERR_PREVIEW_LIMIT (100) chars with "..." suffix.
func sanitizeStderr(stderr string) string {
	cleaned := strings.TrimSpace(stderr)
	if len(cleaned) > stderrPreviewLimit {
		return cleaned[:stderrPreviewLimit] + "..."
	}
	return cleaned
}

// ── Standalone convenience functions ──

// RunCmd runs a command with context and args.
func RunCmd(ctx context.Context, cmd string, args ...string) (string, string, error) {
	opts := RunCmdOpts{
		Cmd:     cmd,
		Args:    args,
		Check:   true,
		Capture: true,
		Text:    true,
	}
	result := runCmdInternal(ctx, opts)
	if result.Err != nil {
		return result.Stdout, result.Stderr, result.Err
	}
	return result.Stdout, result.Stderr, nil
}

// RunCmdCompat runs a command with []string args and opts, returning RunCmdResult.
// This is the primary function used by host/service.go and host/probe.go.
// It matches Python's run_cmd() behavior, including error handling and messages.
//
// NOTE: Unlike the earlier version, this does NOT override opts.Capture.
// Callers who pass Capture=false get non-captured output matching Python.
//
// Delegates through DefaultRunner so that the CommandRunner interface is
// actually used for every subprocess call (fix V1).
func RunCmdCompat(ctx context.Context, args []string, opts RunCmdOpts) *RunCmdResult {
	if len(args) == 0 {
		return &RunCmdResult{ExitCode: 1, Success: false}
	}

	runOpts := []RunOption{
		WithCapture(opts.Capture),
		WithCWD(opts.Cwd),
		WithTimeout(opts.Timeout),
		WithStdin(opts.Input),
		WithEnv(opts.Env),
		WithPrivileged(opts.Privileged),
		WithInteractive(opts.Interactive),
		WithCheck(opts.Check),
		WithStartOnly(opts.StartOnly),
	}
	result, err := DefaultRunner.Run(ctx, args, runOpts...)
	if result == nil {
		return &RunCmdResult{Err: err, ExitCode: -1}
	}
	return &RunCmdResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
		Success:  result.ExitCode == 0 && err == nil,
		Err:      err,
	}
}

// ── Core implementation ──

// runCmdInternal is the shared core that both RunCmd and RunCmdCompat
// delegate to.  It contains all the logic matching Python's run_cmd().
func runCmdInternal(ctx context.Context, opts RunCmdOpts) *RunCmdResult {
	// ── Build argument list (handle privileged) ──
	cmdArgs := opts.buildCmdArgs()

	cmd := cmdArgs[0]
	var cmdRest []string
	if len(cmdArgs) > 1 {
		cmdRest = cmdArgs[1:]
	}

	// ── Create timeout context if needed ──
	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// ── Build exec.Cmd ──
	c := exec.CommandContext(runCtx, cmd, cmdRest...)
	if opts.Cwd != "" {
		c.Dir = opts.Cwd
	}
	// Environment: Python's subprocess.run with env={...} replaces the
	// environment, it does NOT append.  Same here.
	if opts.Env != nil {
		env := make([]string, 0, len(opts.Env))
		for k, v := range opts.Env {
			env = append(env, k+"="+v)
		}
		c.Env = env
	}

	// ── Stdin ──
	if opts.Input != "" {
		c.Stdin = strings.NewReader(opts.Input)
	} else if opts.Interactive && opts.Privileged && !IsRoot() {
		// Interactive mode with sudo: forward os.Stdin so the user can
		// respond to sudo's password prompt via TTY.
		c.Stdin = os.Stdin
	}

	// ── Stdout / Stderr ──
	var stdout, stderr bytes.Buffer
	if opts.Capture {
		c.Stdout = &stdout
		c.Stderr = &stderr
	} else {
		// Python: capture_output=False → stdout/stderr inherited from parent.
		// Go: assign os.Stdout / os.Stderr to match.
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	}

	// ── StartOnly (background / fire-and-forget) ──
	if opts.StartOnly {
		err := c.Start()
		if err != nil {
			return &RunCmdResult{
				ExitCode: -1,
				Success:  false,
				Err:      errs.ProcessErrorWrapped(fmt.Sprintf("failed to start: %s", cmdArgs[0]), err),
			}
		}
		return &RunCmdResult{
			ExitCode: 0,
			Success:  true,
		}
	}

	// ── Run and capture ──
	err := c.Run()

	// Process captured output based on Text/bytes mode
	result := &RunCmdResult{
		Stdout:   "",
		Stderr:   "",
		ExitCode: 0,
		Success:  true,
	}

	if opts.Capture {
		result.StdoutBytes = stdout.Bytes()
		result.StderrBytes = stderr.Bytes()
		// Handle text=False (bytes mode): Python returns CompletedProcess[bytes].
		// In Go, we always decode to string for convenience, but preserve the
		// raw bytes.  For text=True (default), decode is straightforward.
		// For text=False, Python keeps them as bytes objects; we keep string
		// representation since Go's exec.Cmd always returns []byte which we
		// convert to string for the Stdout/Stderr fields.
		if opts.Text {
			result.Stdout = stdout.String()
			result.Stderr = strings.TrimSpace(stderr.String())
		} else {
			// text=False: return raw output as string (decoded from bytes).
			// Go strings are byte sequences, so string(bytes) is the equivalent
			// of Python's bytes.decode('utf-8', errors='replace').
			result.Stdout = string(result.StdoutBytes)
			result.Stderr = strings.TrimSpace(string(result.StderrBytes))
		}
	}

	if err != nil {
		result.Success = false

		// 1. Check for timeout (matches Python's subprocess.TimeoutExpired).
		//    Must check BEFORE exit-error because exec.CommandContext kills the
		//    process on timeout, which also produces an *exec.ExitError.
		//    Python format: f"Command timed out after {timeout}s: {args[0]}"
		if opts.Timeout > 0 && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.ExitCode = -1
			timeoutSecs := opts.Timeout.Seconds()
			timeoutStr := strconv.FormatFloat(timeoutSecs, 'f', -1, 64)
			// Python: f"{timeout}s" always has ".0" for whole-number floats
			// like 60.0.  Go's FormatFloat(60.0, 'f', -1, 64) produces "60",
			// so we add ".0" to match.
			if !strings.ContainsRune(timeoutStr, '.') {
				timeoutStr += ".0"
			}
			result.Err = errs.ProcessError(
				fmt.Sprintf("Command timed out after %ss: %s", timeoutStr, cmdArgs[0]),
			)
			return result
		}

		// 2. Check for command not found (matches Python's FileNotFoundError).
		if errors.Is(err, exec.ErrNotFound) {
			result.ExitCode = -1
			result.Err = errs.ProcessError(
				fmt.Sprintf("Command not found: %s", cmdArgs[0]),
			)
			return result
		}

		// 3. Non-zero exit (matches Python's subprocess.CalledProcessError).
		//    Python only raises this when check=True.
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			if opts.Check {
				sanitized := sanitizeStderr(result.Stderr)
				msg := fmt.Sprintf("Command failed (exit %d): %s", result.ExitCode, cmdArgs[0])
				if sanitized != "" {
					msg += "\n" + sanitized
				}
				result.Err = errs.ProcessError(msg)
			}
			result.Success = result.ExitCode == 0
			return result
		}

		// 4. Other error (e.g., permissions, context cancelled) — wrap with CodeProcessError
		result.ExitCode = -1
		result.Err = errs.Wrap(errs.CodeProcessError, err)
	}

	result.Success = result.ExitCode == 0
	return result
}

// ────────────────────────────────────────────────────────────────────
// Subprocess streaming — StreamCmd
// ────────────────────────────────────────────────────────────────────

// StreamCmdLine carries either a line of output or a final error from StreamCmd.
type StreamCmdLine struct {
	Line string
	Err  error
}

// StreamCmd matches Python's stream_cmd(). It streams stdout lines from a
// subprocess as they are produced, merging stderr into stdout.
//
// Delegates through DefaultRunner so that the CommandRunner interface is
// actually used for every subprocess call (fix V1).
//
// Python signature:
//
//	def stream_cmd(
//	    args: list[str],
//	    *,
//	    cwd: str | None = None,
//	) -> Iterator[str]:
//
// Yields each output line with trailing newline stripped.
// Raises ProcessError if the command is not found or exits with non-zero.
//
// The returned channel yields lines until the command completes.  After the
// channel is closed, any final error is delivered as a StreamLine with
// Err set (the last item).  If no error occurred, the channel simply closes.
func StreamCmd(ctx context.Context, args []string, cwd string) (<-chan StreamLine, error) {
	var runOpts []RunOption
	if cwd != "" {
		runOpts = append(runOpts, WithCWD(cwd))
	}
	return DefaultRunner.Stream(ctx, args, runOpts...)
}

// buildCmdArgs builds the final argument list, handling privileged mode.
// Matching Python's run_cmd():
//
//	if privileged and os.getuid() != 0:
//	    require_mvm_group_membership()
//	    if interactive:
//	        args = ["sudo", *args]       # allows password prompt
//	    else:
//	        args = ["sudo", "-n", *args]  # non-interactive, fails if password required
func (opts RunCmdOpts) buildCmdArgs() []string {
	args := []string{opts.Cmd}
	args = append(args, opts.Args...)

	if !opts.Privileged {
		return args
	}
	// Python: os.getuid() != 0  →  Go: IsRoot()
	if !IsRoot() {
		_ = RequireMvmGroupMembership() // warn only, like Python
		if opts.Interactive {
			return append([]string{"sudo"}, args...)
		}
		return append([]string{"sudo", "-n"}, args...)
	}
	return args
}
