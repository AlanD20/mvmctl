// Package cli — "mvm exec" command — execute commands inside VMs via agent
package cli

import (
	"os"
	"strings"

	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

func NewExecCmd(execAPI api.ExecAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [vm-selector] [-- <command>...]",
		Short: "Execute a command inside a VM via agent",
		Long: `Execute a command inside a VM via the vsock guest agent.

If no command is provided, starts an interactive shell session.

The agent is injected at VM creation time.

Examples:
  mvm exec my-vm                  # interactive shell
  mvm exec my-vm -- ls -la /etc   # run command
  mvm exec my-vm --timeout 30 -- apt-get update
  mvm exec my-vm --port 1025 -- /bin/bash
  mvm exec my-vm --user ubuntu    # shell as ubuntu user`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(c *cobra.Command, args []string) error {
			port, _ := c.Flags().GetInt("port")
			timeout, _ := c.Flags().GetInt("timeout")
			user, _ := c.Flags().GetString("user")
			noSync, _ := c.Flags().GetBool("no-sync")

			command := ""
			if len(args) > 1 {
				command = strings.Join(args[1:], " ")
			}

			input := inputs.ExecInput{
				Identifier: args[0],
				Command:    command,
				Port:       port,
				Timeout:    timeout,
				User:       user,
				NoSync:     noSync,
			}
			result, err := execAPI.Exec(c.Context(), input)
			if err != nil {
				return err
			}
			// Non-nil result means captured execution (non-interactive).
			// Output is streamed directly by the vsock client during execution.
			if result != nil && result.ExitCode != 0 {
				os.Exit(result.ExitCode)
			}
			return nil
		},
	}

	cmd.Flags().IntP("port", "p", 1024, "Vsock port for the guest agent")
	cmd.Flags().IntP("timeout", "t", 0, "Vsock agent connect/probe timeout in seconds")
	cmd.Flags().StringP("user", "u", "", "User to run the command as (default: root)")
	cmd.Flags().Bool("no-sync", false, "Skip final sync() after command (faster but risks data loss on VM stop)")

	return cmd
}
