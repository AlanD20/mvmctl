// Package cli — VM console access commands — connect, state, kill
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
)

func NewConsoleCmd(consoleAPI *api.ConsoleOperation) *cobra.Command {
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
		Args: cobra.MaximumNArgs(1),
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

func showConsoleState(consoleAPI *api.ConsoleOperation, ctx context.Context, identifier string) error {
	state, err := consoleAPI.GetState(ctx, identifier)
	if err != nil {
		// Python's _show_console_state does NOT catch exceptions from
		// ConsoleOperation.get_state() — they propagate to @handle_errors,
		// which calls mvm_cli.error(str(e)) then raises typer.Exit(1).
		// In Go, print the error and return it (SilenceErrors on the root
		// command prevents Cobra from double-printing).
		common.MVMCLI.Error(err.Error())
		return err
	}

	running, _ := state["running"].(bool)
	status := "stopped"
	if running {
		status = "running"
	}
	common.MVMCLI.Info(fmt.Sprintf("Console for '%s': %s", identifier, status))

	// Python: if state_dict["pid"]: — truthiness of int|None
	if pidPtr, ok := state["pid"].(*int); ok && pidPtr != nil && *pidPtr != 0 {
		common.MVMCLI.Info(fmt.Sprintf("  PID: %d", *pidPtr))
	}
	// Python: if state_dict["socket_path"]: — truthiness of str
	if socketPath, ok := state["socket_path"].(string); ok && socketPath != "" {
		common.MVMCLI.Info(fmt.Sprintf("  Socket: %s", socketPath))
	}

	return nil
}

func killConsoleRelay(consoleAPI *api.ConsoleOperation, ctx context.Context, identifier string) error {
	result, err := consoleAPI.Kill(ctx, identifier)
	if err != nil {
		// Python: resolution failure propagates as exception to @handle_errors
		common.MVMCLI.Error(err.Error())
		return err
	}

	if result.Status == "success" {
		common.MVMCLI.Success(fmt.Sprintf("Stopped: %s", identifier))
		return nil
	}

	// Python: mvm_cli.error(f"Console relay not running: {identifier}"); raise typer.Exit(1)
	// Python: raise typer.Exit(1) is caught by Typer which sets exit code 1
	// and allows deferred cleanup to run. Go's Cobra equivalent: return an error
	// (the root command has SilenceErrors=true, so it won't be printed again).
	if result.Status == "skipped" {
		common.MVMCLI.Error(fmt.Sprintf("Console relay not running: %s", identifier))
		return fmt.Errorf("console relay not running: %s", identifier)
	}

	// Python: mvm_cli.error(result.message or f"Stop failed: {identifier}")
	msg := result.Message
	if msg == "" {
		msg = fmt.Sprintf("Stop failed: %s", identifier)
	}
	common.MVMCLI.Error(msg)
	return fmt.Errorf("%s", msg)
}

func attachToConsole(consoleAPI *api.ConsoleOperation, cmd *cobra.Command, identifier string) error {
	info, err := consoleAPI.GetConnectionInfo(cmd.Context(), identifier)
	if err != nil {
		// Python: get_connection_info raises MVMError which propagates to
		// @handle_errors, which calls mvm_cli.error(str(e)) then typer.Exit(1).
		// In Go, print the error and return it.
		common.MVMCLI.Error(err.Error())
		return err
	}

	common.MVMCLI.Info(fmt.Sprintf("Attaching to console of '%s'...", info.VMName))
	common.MVMCLI.Info("Press Ctrl+X then D to detach")

	err = consoleAPI.AttachConsole(cmd.Context(), info.SocketPath, os.Stdin, os.Stdout)
	if err == nil {
		// Python's _attach_to_console: mvm_cli.info("\nDetached from console")
		common.MVMCLI.Info("\nDetached from console")
	} else {
		// Python's socket connection failure is handled inside _connect_socket
		// (mvm_cli.error(f"Console relay connection failed: {e}")) and then
		// _attach_to_console checks for None and prints "Console relay connection
		// failed" before raising typer.Exit(1). MVMErrors during _interact are
		// caught and printed via mvm_cli.error(str(e)). In Go, InteractiveAttach
		// returns these errors so we print and return them here.
		common.MVMCLI.Error(err.Error())
	}
	return err
}
