package cli

import (
	"fmt"

	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

// NewSelfUpdateCmd creates the self-update subcommand.
func NewSelfUpdateCmd(op api.API) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "self-update [check|apply]",
		Short: "Update mvm to the latest version",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sub := ""
			if len(args) > 0 {
				sub = args[0]
			}

			switch sub {
			case "check":
				result, err := op.SelfUpdateCheck(cmd.Context())
				if err != nil {
					return err
				}
				if result.HasUpdate {
					fmt.Printf("Update available: v%s → v%s\n", result.CurrentVersion, result.LatestVersion)
				} else {
					fmt.Printf("Already up to date (v%s)\n", result.CurrentVersion)
				}

			case "apply", "":
				result, err := op.SelfUpdateCheck(cmd.Context())
				if err != nil {
					return err
				}
				if !result.HasUpdate && !force {
					fmt.Printf("Already up to date (v%s)\n", result.CurrentVersion)
					return nil
				}
				if err := op.SelfUpdateApply(cmd.Context(), force); err != nil {
					return err
				}
				fmt.Printf("Updated to v%s\n", result.LatestVersion)
				fmt.Println("Restart any running daemons (console relay, nocloud-net) to pick up the new version.")

			default:
				return fmt.Errorf("unknown subcommand: %s (use check or apply)", sub)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Apply update even if same version")
	return cmd
}
