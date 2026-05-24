// Package common provides CLI display helpers — table rendering, JSON output,
// error display, and the MVMCli singleton matching Python's ``utils/cli.py:MVMCli``.
package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"mvmctl/internal/infra/errs"
)

// ─── Error handler wrapper (matching Python's @handle_errors) ────────────────

// HandleErrors wraps a command function with the global error handling
// pattern from Python's ``@handle_errors`` decorator in ``utils/cli.py``.
//
// Python behavior replicated:
//   - typer.Exit         → re-raised (Cobra returns the error)
//   - click.Abort        → exit code 130 (handled via signal in Go)
//   - KeyboardInterrupt  → exit code 130 (handled via signal in Go)
//   - BrokenPipeError    → exit code 0   (silent exit on pipe close)
//   - PrivilegeError     → show details/suggestions via mvm_cli.error(), exit 1
//   - MVMError           → show message via mvm_cli.error(), exit 1
//   - sqlite3.OperationalError → show DB init hint or error, exit 1
//   - Exception          → show unexpected error, exit 1
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

		// ── 1. BrokenPipeError → silent exit (return nil) ────────
		// Python catches BrokenPipeError and raises typer.Exit(code=0).
		if isBrokenPipe(err) {
			// Silently close stderr to avoid further broken pipe
			_ = os.Stderr.Close()
			return nil
		}

		// ── 2. context.Canceled (KeyboardInterrupt) ─────────────
		// Python catches KeyboardInterrupt and raises typer.Exit(code=130).
		// Go's signal.NotifyContext cancels the context on SIGINT/SIGTERM.
		if errors.Is(err, context.Canceled) {
			return err
		}

		// ── 3. DomainError (MVMError / PrivilegeError) ──────────
		var domainErr *errs.DomainError
		if errors.As(err, &domainErr) {
			return handleDomainError(domainErr)
		}

		// ── 4. sqlite3.OperationalError (database errors) ───────
		// Python checks for "no such table" in the message string.
		// In Go with modernc.org/sqlite, database errors don't have a
		// dedicated type; we check the message for known patterns.
		if isDatabaseError(err) {
			msg := err.Error()
			if strings.Contains(msg, "no such table") {
				MVMCLI.Error(
					"Database schema not initialized. " +
						"Run 'mvm init' first to create the database.",
				)
			} else {
				MVMCLI.Error("Database error: " + msg)
			}
			return err
		}

		// ── 5. Unexpected error (Exception) ─────────────────────
		// Python: mvm_cli.error(f"{e.__class__.__name__}: {e}", is_unexpected=True)
		MVMCLI.Error(formatUnexpected(err), true) // is_unexpected = true
		return err
	}
}

// ─── internal helpers ────────────────────────────────────────────────────────

// handleDomainError displays a DomainError according to Python's PrivilegeError
// and MVMError handling rules, then returns the error.
//
// Python PrivilegeError handling:
//
//	mvm_cli.error(str(e))
//	if e.details:
//	    detail_msg = e.details.get("message", "")
//	    if detail_msg:
//	        mvm_cli.warning(f"Details: {detail_msg}")
//	    mvm_cli.info("Options:")
//	    for suggestion in e.details.get("suggestions", []):
//	        mvm_cli.info(f"  - {suggestion}")
//
// Python MVMError handling:
//
//	mvm_cli.error(str(e))
func handleDomainError(de *errs.DomainError) error {
	// Resolve display message matching Python's str(e) behavior.
	// Python MVMError.__str__() returns just the message
	// (e.g. "VM not found: my-vm"), not the full code/op/entity prefix.
	// Fall back to de.Error() if Message is empty.
	displayMsg := de.Message
	if displayMsg == "" {
		displayMsg = de.Error()
	}

	// ── PrivilegeError subclass ──────────────────────────────────
	// Matches Python's PrivilegeError(MVMError) handling.
	if de.Code == errs.CodePrivilegeRequired && de.Class == errs.ClassNeedsInteraction {
		MVMCLI.Error(displayMsg)

		if de.Details != nil {
			detailMsg, _ := de.Details["message"].(string)
			if detailMsg != "" {
				MVMCLI.Warning("Details: " + detailMsg)
			}

			MVMCLI.Info("Options:")
			if suggestions, ok := de.Details["suggestions"]; ok {
				if sugList, ok := suggestions.([]string); ok {
					for _, suggestion := range sugList {
						MVMCLI.Info("  - " + suggestion)
					}
				} else if sugList, ok := suggestions.([]interface{}); ok {
					for _, s := range sugList {
						MVMCLI.Info(fmt.Sprintf("  - %v", s))
					}
				}
			}
		}

		return de
	}

	// ── General MVMError (and all other DomainErrors) ────────────
	// Python: mvm_cli.error(str(e))
	MVMCLI.Error(displayMsg)
	return de
}

// isBrokenPipe checks if err is a broken pipe / closed pipe error, matching
// Python's ``BrokenPipeError``.
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

// isDatabaseError checks if err looks like a sqlite3.OperationalError.
// Python catches sqlite3.OperationalError and checks for "no such table".
// In Go with modernc.org/sqlite, there's no dedicated error type, so we
// check for characteristic SQLite error patterns.
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

// formatUnexpected formats an unexpected error matching Python's
// ``f"{e.__class__.__name__}: {e}"`` format.
func formatUnexpected(err error) string {
	typeName := fmt.Sprintf("%T", err)
	// Strip leading "*" for pointer types
	if strings.HasPrefix(typeName, "*") {
		typeName = typeName[1:]
	}
	// Strip package path: "errors.errorString" → "errorString"
	if dotIdx := strings.LastIndex(typeName, "."); dotIdx >= 0 {
		typeName = typeName[dotIdx+1:]
	}
	return fmt.Sprintf("%s: %s", typeName, err.Error())
}


