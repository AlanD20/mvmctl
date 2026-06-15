// Package cli — VM console access commands — connect, state, kill
package cli

import (
	"context"
	"fmt"
	"os"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

func NewConsoleCmd(consoleAPI api.ConsoleAPI) *cobra.Command {
	var state bool
	var kill bool

	cmd := &cobra.Command{
		Use:   "console [vm-name]",
		Short: "VM console access",
		Long: `Attach to a VM console.

Provide a VM identifier (name, ID prefix, IP, or MAC address) as the
positional argument.

Press Ctrl+X then D to detach from the console.

Use --state to show the console relay state without attaching.
Use --kill to stop the console relay.`,
		// Python uses no_args_is_help=True on the Typer group, so running
		// "mvm console" with no args prints help text instead of an error.
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Simulate no_args_is_help=True: show help when no arguments given.
			if len(args) == 0 {
				return cmd.Help()
			}
			identifier := args[0]

			if state {
				return showConsoleState(consoleAPI, cmd.Context(), identifier)
			}
			if kill {
				return killConsoleRelay(consoleAPI, cmd.Context(), identifier)
			}
			return attachToConsole(consoleAPI, cmd, identifier)
		},
	}

	cmd.Flags().BoolVar(&state, "state", false, "Show console state without attaching")
	cmd.Flags().BoolVar(&kill, "kill", false, "Kill the console relay")

	return cmd
}

func showConsoleState(consoleAPI api.ConsoleAPI, ctx context.Context, identifier string) error {
	state, err := consoleAPI.ConsoleGetState(ctx, identifier)
	if err != nil {
		// Python's _show_console_state does NOT catch exceptions from
		// ConsoleOperation.get_state() — they propagate to @handle_errors,
		// which calls mvm_cli.error(str(e)) then raises typer.Exit(1).
		// In Go, print the error and return it (SilenceErrors on the root
		// command prevents Cobra from double-printing).
		common.Cli.Error(err.Error())
		return err
	}

	status := "stopped"
	if state.Running {
		status = "running"
	}
	common.Cli.Info(fmt.Sprintf("Console for '%s': %s", identifier, status))

	if state.PID != nil && *state.PID != 0 {
		common.Cli.Info(fmt.Sprintf("  PID: %d", *state.PID))
	}

	if state.SocketPath != "" {
		common.Cli.Info(fmt.Sprintf("  Socket: %s", state.SocketPath))
	}
	common.Cli.Info(fmt.Sprintf("Console for '%s': %s", identifier, status))
	return nil
}

func killConsoleRelay(consoleAPI api.ConsoleAPI, ctx context.Context, identifier string) error {
	err := consoleAPI.ConsoleKill(ctx, identifier)
	if err != nil {
		common.Cli.Error(err.Error())
		return err
	}

	common.Cli.Success(fmt.Sprintf("Stopped: %s", identifier))
	return nil
}

func attachToConsole(consoleAPI api.ConsoleAPI, cmd *cobra.Command, identifier string) error {
	info, err := consoleAPI.ConsoleGetConnectionInfo(cmd.Context(), identifier)
	if err != nil {
		// Python: get_connection_info raises MVMError which propagates to
		// @handle_errors, which calls mvm_cli.error(str(e)) then typer.Exit(1).
		// In Go, print the error and return it.
		common.Cli.Error(err.Error())
		return err
	}

	common.Cli.Info(fmt.Sprintf("Attaching to console of '%s'...", info.VMName))
	common.Cli.Info("Press Ctrl+X then D to detach")

	err = consoleAPI.ConsoleAttachConsole(cmd.Context(), info.SocketPath, os.Stdin, os.Stdout)
	if err == nil {
		// Python's _attach_to_console: mvm_cli.info("\nDetached from console")
		common.Cli.Info("\nDetached from console")
	} else {
		// Python's socket connection failure is handled inside _connect_socket
		// (mvm_cli.error(f"Console relay connection failed: {e}")) and then
		// _attach_to_console checks for None and prints "Console relay connection
		// failed" before raising typer.Exit(1). MVMErrors during _interact are
		// caught and printed via mvm_cli.error(str(e)). In Go, InteractiveAttach
		// returns these errors so we print and return them here.
		common.Cli.Error(err.Error())
	}
	return err
}
