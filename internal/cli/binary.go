package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// binaryColumns defines the local listing columns for binaries.
var binaryColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.BinaryItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.BinaryItem).ID) }},
	{Header: "Type", Extract: func(v any) string { return v.(*model.BinaryItem).Type }},
	{Header: "Architecture", Extract: func(v any) string { return v.(*model.BinaryItem).Architecture }},
	{Header: "Version", Extract: func(v any) string { return v.(*model.BinaryItem).Version }},
	{Header: "Full Version", Extract: func(v any) string {
		fv := v.(*model.BinaryItem).FullVersion
		if fv == "" {
			return "-"
		}
		return fv
	}, LongOnly: true},
	{Header: "Status", Extract: func(v any) string {
		if v.(*model.BinaryItem).DeletedAt != nil {
			return "[x]"
		}
		return ""
	}, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.BinaryItem).CreatedAt, "relative") },
	},
}

// NewBinaryCmd creates the binary command and its subcommands.
func NewBinaryCmd(binaryAPI api.BinaryAPI, configAPI api.ConfigAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bin",
		Aliases: []string{"binary"},
		Short:   "Binary management",
	}

	cmd.AddCommand(newBinaryListCmd(binaryAPI, configAPI))
	cmd.AddCommand(newBinaryPullCmd(binaryAPI))
	cmd.AddCommand(newBinaryRemoveCmd(binaryAPI))
	cmd.AddCommand(newBinaryDefaultCmd(binaryAPI))

	return cmd
}

func newBinaryListCmd(binaryAPI api.BinaryAPI, configAPI api.ConfigAPI) *cobra.Command {
	var jsonOutput bool
	var longOutput bool
	var remote bool
	var limit int

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List local (and optionally remote) Firecracker versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			local, _, err := binaryAPI.BinaryList(cmd.Context(), false, nil, nil)
			if err != nil {
				return err
			}

			if jsonOutput {
				if local == nil {
					local = []*model.BinaryItem{}
				}
				b, _ := json.MarshalIndent(local, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			if remote {
				fmt.Fprintf(os.Stderr, "Fetching remote versions...\n")
				_, remoteVersions, err := binaryAPI.BinaryList(cmd.Context(), true, &limit, nil)
				if err != nil {
					return err
				}
				common.RenderVersionTree(remoteVersions)
				return nil
			}

			// Local listing
			style := common.Cli.ResolveListingStyle(cmd.Context(), configAPI, longOutput)
			common.RenderListing(local, binaryColumns, style)

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Also show remote versions")
	cmd.Flags().IntVar(&limit, "limit", 5, "Max remote versions to show")

	return cmd
}

func newBinaryPullCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	var setDefault bool
	var force bool

	cmd := &cobra.Command{
		Use:               "pull <version>",
		Short:             "Install a Firecracker release through the trusted root authority",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeInstalledBinaryReleaseVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			binaries, err := binaryAPI.BinaryPull(cmd.Context(), inputs.BinaryPullInput{
				Version:          args[0],
				Type:             "firecracker",
				SetDefault:       setDefault,
				DownloadOverride: force,
			}, nil)
			if err != nil {
				return err
			}
			for _, b := range binaries {
				shortID := common.Cli.FormatID(b.ID)
				common.Cli.Success(fmt.Sprintf("Trusted release ready: %s v%s: %s", b.Type, b.Version, b.Path))
				common.Cli.Info(fmt.Sprintf("  ID: %s", shortID))
			}

			if setDefault && len(binaries) > 0 {
				common.Cli.Success(fmt.Sprintf("Default binary set to: v%s", binaries[0].Version))
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set the installed release as default")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Allow replacement of a different trusted release")

	return cmd
}

func newBinaryRemoveCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm <exact-version>...",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more exact Firecracker release versions",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeInstalledBinaryReleaseVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			batchResult := binaryAPI.BinaryRemove(
				cmd.Context(),
				inputs.BinaryRemoveInput{Versions: args},
				force,
			)
			for _, r := range batchResult.Items {
				for _, warning := range r.Warnings {
					common.Cli.Warning(warning)
				}
				if r.Status == "success" {
					msg := r.Message
					if msg == "" {
						msg = "Removed"
					}
					common.Cli.Success(msg)
				} else if r.Status == "skipped" {
					common.Cli.Info(r.Message)
				} else {
					msg := r.Message
					if msg == "" {
						msg = "Remove failed"
					}
					common.Cli.Error(msg)
				}
			}
			if batchResult.HasErrors() {
				return fmt.Errorf("one or more removals failed")
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Suppress local reference advisories; root policy is unchanged")

	return cmd
}

func newBinaryDefaultCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	return &cobra.Command{
		Use:               "default [selector]",
		Short:             "Set a binary as the active default",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			item, err := binaryAPI.BinarySetDefault(
				cmd.Context(),
				inputs.BinaryInput{Identifiers: []string{identifier}},
			)
			if err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Default binary set to %s v%s", item.Type, item.Version))
			return nil
		},
	}
}
