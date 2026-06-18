// Package common provides CLI display helpers — table rendering, JSON output,
// error display, and the MVMCli singleton.
package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/dustin/go-humanize"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// --- Error handler wrapper ---

// HandleErrors wraps a command function with consistent error handling.
//
// Error handling rules:
// - BrokenPipeError    → silent exit (return nil)
// - context.Canceled   → propagate as-is (Ctrl+C)
// - DomainError        → display with Code/Message/Details, return error
// - Database errors    → show "Run 'mvm init' first" or specific message
// - Unexpected errors  → show generic error message
//
// Usage:
//
//	RunE: func(cmd *cobra.Command, args []string) error {
//	    return common.HandleErrors(func() error {
//	        // original command logic
//	        return nil
//	    })()
//	},
func HandleErrors(fn func() error) func() error {
	return func() (err error) {
		err = fn()
		if err == nil {
			return nil
		}

		// --- 1. BrokenPipeError → silent exit (return nil) ---
		if isBrokenPipe(err) {
			// Silently close stderr to avoid further broken pipe
			_ = os.Stderr.Close()
			return nil
		}

		// --- 2. context.Canceled (Ctrl+C) ---
		// signal.NotifyContext cancels the context on SIGINT/SIGTERM.
		if errors.Is(err, context.Canceled) {
			return err
		}

		// --- 3. DomainError ---
		var domainErr *errs.DomainError
		if errors.As(err, &domainErr) {
			return handleDomainError(domainErr)
		}

		// --- 4. Database errors ---
		// modernc.org/sqlite doesn't have a dedicated error type;
		// we check the message for known SQLite error patterns.
		if isDatabaseError(err) {
			msg := err.Error()
			if strings.Contains(msg, "no such table") {
				Cli.Error(
					"Database schema not initialized. " +
						"Run 'mvm init' first to create the database.",
				)
			} else {
				Cli.Error("Database error: " + msg)
			}
			return err
		}

		// --- 5. Unexpected error ---
		Cli.Error(formatUnexpected(err))
		return err
	}
}

// --- Internal helpers ---

// handleDomainError displays a DomainError and returns the error.
//
// PrivilegeError (CodePrivilegeRequired + ClassNeedsInteraction):
// - Shows error message, details, and suggestions
//
// General DomainError:
// - Shows error message
func handleDomainError(de *errs.DomainError) error {
	displayMsg := de.Message
	if displayMsg == "" {
		displayMsg = string(de.Code)
	}
	if displayMsg == "" {
		displayMsg = "An error occurred"
	}

	// --- PrivilegeError (CodePrivilegeRequired) ---
	if de.Code == errs.CodePrivilegeRequired && de.Class == errs.ClassNeedsInteraction {
		Cli.Error(displayMsg)

		if de.Details != nil {
			detailMsg, _ := de.Details["message"].(string)
			if detailMsg != "" {
				Cli.Warning("Details: " + detailMsg)
			}

			Cli.Info("Options:")
			if suggestions, ok := de.Details["suggestions"]; ok {
				if sugList, ok := suggestions.([]string); ok {
					for _, suggestion := range sugList {
						Cli.Info("  - " + suggestion)
					}
				} else if sugList, ok := suggestions.([]any); ok {
					for _, s := range sugList {
						Cli.Info(fmt.Sprintf("  - %v", s))
					}
				}
			}
		}

		return de
	}

	// --- General DomainError ---
	Cli.Error(displayMsg)
	return de
}

// isBrokenPipe checks if err is a broken pipe / closed pipe error.
func isBrokenPipe(err error) bool {
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	// Some wrapped errors may contain "broken pipe" in the message.
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "Broken pipe")
}

// isDatabaseError checks if err matches known SQLite error patterns.
// Uses message matching since modernc.org/sqlite lacks a dedicated error type.
func isDatabaseError(err error) bool {
	msg := err.Error()
	// Common SQLite operational error patterns
	sqlitePatterns := []string{
		"no such table",
		"no such column",
		"database is locked",
		"UNIQUE constraint",
		"FOREIGN KEY constraint",
		"NOT NULL constraint",
		"CHECK constraint",
		"syntax error",
		"table already exists",
		"database or disk is full",
		"database disk image is malformed",
		"file is not a database",
	}
	for _, pattern := range sqlitePatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

func formatUnexpected(err error) string {
	return err.Error()
}

// FormatSpeed formats bytes-per-second as a human-readable transfer speed
// using go-humanize (e.g. "5.2 MiB/s").
func FormatSpeed(bytesPerSec float64) string {
	if bytesPerSec < 1 {
		return "0 B/s"
	}
	return humanize.Bytes(uint64(bytesPerSec)) + "/s"
}

// SudoResult carries the outcome of a sudo subprocess.
type SudoResult struct {
	Success    bool
	ReturnCode int
}

// RunWithSudo runs args via "sudo env ..." forwarding all MVM_* env vars
// as well as HOME and PATH. extraEnv are additional KEY=VALUE pairs placed
// after scanned vars so they take precedence (env uses last-wins).
func RunWithSudo(ctx context.Context, args []string, extraEnv ...string) SudoResult {
	mvmBin, err := os.Executable()
	if err != nil {
		mvmBin, err = exec.LookPath(infra.CLIName)
		if err != nil {
			mvmBin = infra.CLIName
		}
	}

	// Build env var assignments — passed via the 'env' utility to sudo.
	// Scanned vars first, then extraEnv so explicit overrides win.
	envAssignments := []string{}
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "MVM_") {
			envAssignments = append(envAssignments, env)
		}
	}
	for _, key := range []string{"HOME", "PATH"} {
		if val, ok := os.LookupEnv(key); ok {
			envAssignments = append(envAssignments, key+"="+val)
		}
	}
	envAssignments = append(envAssignments, extraEnv...)

	Cli.Info("")
	Cli.Info("Running host init with sudo...")

	// Use system.RunCmdCompat with the env utility to properly pass environment
	// variables through sudo's env_reset.
	cmdArgs := append([]string{mvmBin}, args...)
	runArgs := append([]string{"env"}, append(envAssignments, cmdArgs...)...)
	result, err := system.DefaultRunner.Run(ctx, runArgs, system.RunCmdOpts{
		Capture:     false,
		Check:       false,
		Privileged:  true,
		Interactive: true,
	})
	if err != nil || !result.Success() {
		return SudoResult{Success: false, ReturnCode: result.ExitCode}
	}
	return SudoResult{Success: true, ReturnCode: 0}
}
