package cli

import (
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
	libversion "mvmctl/internal/lib/version"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"

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

			if remote {
				fmt.Fprintf(os.Stderr, "Fetching remote versions...\n")
				_, remoteVersions, err := binaryAPI.BinaryList(cmd.Context(), true, &limit, nil)
				if err != nil {
					return err
				}
				if common.RecordListResult(remoteVersions) {
					return nil
				}
				common.RenderVersionTree(remoteVersions)
				return nil
			}

			if common.RecordListResult(local) {
				return nil
			}

			// Local listing
			style := common.Cli.ResolveListingStyle(cmd.Context(), configAPI, longOutput)
			common.RenderListing(local, binaryColumns, style)

			return nil
		},
	}

	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Also show remote versions")
	cmd.Flags().IntVar(&limit, "limit", 5, "Max remote versions to show")

	return cmd
}

func newBinaryPullCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	var setDefault bool
	var force bool

	cmd := &cobra.Command{
		Use:               "pull TYPE[:VERSION]",
		Short:             "Install a Firecracker release through the trusted root authority",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBinaryPullSelectors,
		RunE: func(cmd *cobra.Command, args []string) error {
			typ, version := libversion.ParseSelector(args[0])
			if typ == "" && !strings.Contains(args[0], ":") {
				typ, version = args[0], "latest"
			}
			if typ != "firecracker" || version == "" {
				return errs.New(errs.CodeBinaryPullFailed, "expected firecracker or firecracker:version")
			}
			binaries, err := binaryAPI.BinaryPull(cmd.Context(), inputs.BinaryPullInput{
				Version:          version,
				Type:             typ,
				SetDefault:       setDefault,
				DownloadOverride: force,
			}, nil)
			common.RecordResult(binaries)
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

func completeBinaryPullSelectors(
	cmd *cobra.Command,
	args []string,
	prefix string,
) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	values, directive := completeInstalledBinaryReleaseVersions(cmd, args, prefix)
	if strings.HasPrefix("firecracker", prefix) {
		values = append([]string{"firecracker"}, values...)
	}
	return values, directive
}

func newBinaryRemoveCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm TYPE:EXACT_VERSION...",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more exact Firecracker release versions",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeInstalledBinaryReleaseVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			versions := make([]string, 0, len(args))
			for _, selector := range args {
				typ, version := libversion.ParseSelector(selector)
				if typ != "firecracker" || version == "" {
					return errs.New(errs.CodeValidationFailed, "binary removal requires firecracker:exact-version")
				}
				versions = append(versions, version)
			}
			batchResult := binaryAPI.BinaryRemove(
				cmd.Context(),
				inputs.BinaryRemoveInput{Versions: versions},
				force,
			)
			if common.StructuredOutput() {
				return common.RecordBatch(batchResult)
			}
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
			common.RecordResult(item)
			if err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Default binary set to %s v%s", item.Type, item.Version))
			return nil
		},
	}
}
