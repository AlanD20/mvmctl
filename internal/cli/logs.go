// Package cli — VM log viewing commands, matching Python's cli/logs.py
package cli

import (
	"fmt"

	"mvmctl/internal/cli/common"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

func NewLogsCmd(op *api.Operation) *cobra.Command {
	var osLog bool
	var rawLines int
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [VM identifier]",
		Short: "VM log management",
		Long: `View VM logs.

Provide a VM identifier as a positional argument.

By default shows the boot log (serial console output).
Use --os to show the Firecracker process log.`,
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Python: no_args_is_help=True — show help when no args given, not an error
			if len(args) == 0 {
				return cmd.Help()
			}
			identifier := args[0]

			// inputs.LogInput uses *int for Lines (nil = use default from config)
			var lines *int
			if cmd.Flags().Changed("lines") {
				lines = &rawLines
			}

			// inputs.LogInput uses *bool for Follow (nil = use default from config)
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

			// Python: for line in LogOperation.stream(inputs): print(line)
			// Go:     LogOperation.Stream(ctx, input, func(line) { fmt.Println(line) })
			err := op.LogStream(cmd.Context(), input, func(line string) error {
				fmt.Println(line)
				return nil
			})
			if err != nil {
				// Python: any exception propagates to @handle_errors which calls
				// mvm_cli.error(str(e)) and exits 1. In Go, with SilenceErrors=true
				// on the root command, the error is silently swallowed. We must
				// print it before returning to match Python's behavior.
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
