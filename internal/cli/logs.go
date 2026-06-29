// Package cli — VM log viewing commands
package cli

import (
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

func NewLogsCmd(logsAPI api.LogAPI) *cobra.Command {
	var osLog bool
	var rawLines int
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [vm-selector]",
		Short: "VM log management",
		Long: `View VM logs.

Provide a VM identifier as a positional argument.

By default shows the boot log (serial console output).
Use --os to show the Firecracker process log.`,
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when no args given, not an error
			if len(args) == 0 {
				return cmd.Help()
			}
			identifier := args[0]

			// LogInput uses *int for Lines (nil = use default from config)
			var lines *int
			if cmd.Flags().Changed("lines") {
				lines = &rawLines
			}

			// LogInput uses *bool for Follow (nil = use default from config)
			var followPtr *bool
			if cmd.Flags().Changed("follow") {
				followPtr = &follow
			}

			input := inputs.LogInput{
				Identifier: identifier,
				OsLog:      osLog,
				Lines:      lines,
				Follow:     followPtr,
			}

			// LogOperation.Stream(ctx, input, callback) — callback receives each line.
			err := logsAPI.LogStream(cmd.Context(), input, func(line string) error {
				fmt.Println(line)
				return nil
			})
			if err != nil {
				// With SilenceErrors=true on the root command, the error is silently
				// swallowed. Print it before returning.
				common.Cli.Error(err.Error())
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&osLog, "os", false, "Show Firecracker OS log instead of boot log")
	cmd.Flags().IntVarP(&rawLines, "lines", "n", 0, "Number of log lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output in real-time")

	return cmd
}
