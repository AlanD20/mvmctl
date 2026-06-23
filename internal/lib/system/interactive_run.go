package system

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// terminalReset performs a soft terminal reset (DECSTR) to restore DEC private
// modes to their defaults. Sent to os.Stdout after an interactive subprocess
// exits to clean up any modes left enabled (mouse tracking, bracketed paste,
// cursor hidden, keypad application mode, alternate screen, etc.).
// DECSTR (\x1b[!p) is a standard VT-level reset that does NOT clear the screen.
const terminalReset = "\x1b[!p\x1b[?25h\x1b[0m"

// RunInteractive runs an interactive subprocess with full terminal management.
// It puts the local terminal in raw mode, runs the subprocess with
// stdin/stdout/stderr connected directly to the terminal, and restores the
// terminal state (termios + DEC private modes) when the subprocess exits.
//
// Signals (SIGINT, SIGTERM, SIGQUIT) are passed through to the subprocess
// natively — Go's signal handlers discard them so the child can handle them.
//
// Returns the error from cmd.Run(), including *exec.ExitError on non-zero
// exit. The caller should check for ExitError if they care about exit codes.
func RunInteractive(path string, args []string, env []string) error {
	// Save terminal state for restoration.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}

	// Terminal cleanup defers (LIFO order):
	//   1. term.Restore — restores termios (runs first in program order)
	//   2. Write escape sequences — restores DEC modes (runs second)
	//   3. Signal cleanup — stops signal delivery, goroutine exits
	defer term.Restore(int(os.Stdin.Fd()), oldState) //nolint:errcheck
	defer func() {
		_, _ = os.Stdout.WriteString(terminalReset)
	}()

	// Signal handling: Go must NOT handle SIGINT/SIGTERM/SIGQUIT for
	// interactive subprocesses — the child in the same process group handles
	// them natively. signal.Notify prevents Go's default action; a goroutine
	// discards them.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(sigCh)

	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			select {
			case <-sigCh:
				// Discard — let the child handle these natively.
			case <-done:
				return
			}
		}
	}()

	// Run the subprocess with the terminal connected directly.
	cmd := exec.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), env...)

	return cmd.Run()
}
