package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/lib/model"
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
	{Header: "Version", Extract: func(v any) string { return v.(*model.BinaryItem).Version }},
	{Header: "Full Version", Extract: func(v any) string {
		fv := v.(*model.BinaryItem).FullVersion
		if fv == "" {
			return "-"
		}
		return fv
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
	var version string
	var gitRef string
	var setDefault bool
	var force bool

	cmd := &cobra.Command{
		Use:               "pull [selector]",
		Short:             "Download a Firecracker version or build from source",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			typ := args[0]
			effectiveVersion := version

			// Support type:version selector (matching kernel pull pattern)
			if strings.Contains(typ, ":") {
				idx := strings.LastIndex(typ, ":")
				effectiveVersion = typ[idx+1:]
				typ = typ[:idx]
				if cmd.Flags().Changed("version") {
					return fmt.Errorf("mutually exclusive options")
				}
			}

			if strings.ToLower(typ) != "firecracker" {
				common.Cli.Error(
					fmt.Sprintf(
						"Unsupported binary: '%s'. Only 'firecracker' is supported for download or build.",
						typ,
					),
				)
				return fmt.Errorf("unsupported binary")
			}

			if gitRef != "" && effectiveVersion != "" {
				return fmt.Errorf("mutually exclusive options")
			}

			// Git build path
			if gitRef != "" {
				common.Cli.Info(fmt.Sprintf("Building Firecracker from ref '%s' via Docker-based devtool...", gitRef))
				common.Cli.Info("  Phase 1: Cloning/updating Firecracker source (git)")
				common.Cli.Info("  Phase 2: Building release binary (5-15 min via Docker)")
				common.Cli.Info("  The build output will appear below once it starts:\n")

				gitRefPtr := &gitRef
				binaries, err := binaryAPI.BinaryPull(cmd.Context(), inputs.BinaryPullInput{
					Version:          "",
					Type:             typ,
					GitRef:           gitRefPtr,
					SetDefault:       setDefault,
					DownloadOverride: false,
				}, nil)
				if err != nil {
					return err
				}

				common.Cli.Info("")
				for _, b := range binaries {
					shortID := common.Cli.FormatID(b.ID)
					common.Cli.Success(fmt.Sprintf("Built: %s %s: %s", b.Type, b.Version, b.Path))
					common.Cli.Info(fmt.Sprintf("  ID: %s", shortID))
				}

				if setDefault && len(binaries) > 0 {
					common.Cli.Success(fmt.Sprintf("Default binary set to: %s", binaries[0].Version))
				}

				return nil
			}

			// Normal download path
			binaries, err := binaryAPI.BinaryPull(cmd.Context(), inputs.BinaryPullInput{
				Version:          effectiveVersion,
				Type:             typ,
				SetDefault:       setDefault,
				DownloadOverride: force,
			}, nil)

			// If binary already exists and --force wasn't set, offer to re-download
			if err != nil {
				var de *errs.DomainError
				if errors.As(err, &de) && de.Code == errs.CodeBinaryAlreadyExists && !force {
					common.Cli.Warning(de.Message)

					confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Re-download?", false)
					if pErr != nil {
						return pErr
					}
					if !confirmed {
						common.Cli.Info("Aborted")
						return nil
					}

					binaries, err = binaryAPI.BinaryPull(cmd.Context(), inputs.BinaryPullInput{
						Version:          effectiveVersion,
						Type:             typ,
						SetDefault:       setDefault,
						DownloadOverride: true,
					}, nil)
				}
				if err != nil {
					return err
				}
			}
			for _, b := range binaries {
				shortID := common.Cli.FormatID(b.ID)
				common.Cli.Success(fmt.Sprintf("Downloaded: %s v%s: %s", b.Type, b.Version, b.Path))
				common.Cli.Info(fmt.Sprintf("  ID: %s", shortID))
			}

			if setDefault && len(binaries) > 0 {
				common.Cli.Success(fmt.Sprintf("Default binary set to: v%s", binaries[0].Version))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Version to download (e.g. 1.15.0, latest)")
	cmd.Flags().
		StringVar(&gitRef, "git-ref", "", "Git ref (branch/tag/commit) to build from source. Mutually exclusive with --version.")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after download")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Re-download even if version already exists")

	return cmd
}

func newBinaryRemoveCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	var version string
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [identifiers...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more binaries. Use --version to remove by version pair.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if version != "" {
				if err := binaryAPI.BinaryRemoveByVersion(cmd.Context(), version, force); err != nil {
					return err
				}
				common.Cli.Success(fmt.Sprintf("Removed: v%s", version))
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}

			batchResult := binaryAPI.BinaryRemove(cmd.Context(), inputs.BinaryInput{Identifiers: args}, force)
			for _, r := range batchResult.Items {
				if r.Status == "success" {
					msg := r.Message
					if msg == "" {
						msg = "Removed"
					}
					common.Cli.Success(msg)
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

	cmd.Flags().StringVar(&version, "version", "", "Remove both firecracker and jailer for this version")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Remove even if referenced by VMs")

	return cmd
}

func newBinaryDefaultCmd(binaryAPI api.BinaryAPI) *cobra.Command {
	return &cobra.Command{
		Use:               "default [identifier]",
		Short:             "Set a binary as the active default",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			item, err := binaryAPI.BinarySetDefault(cmd.Context(), inputs.BinaryInput{Identifiers: []string{identifier}})
			if err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Default binary set to %s v%s", item.Type, item.Version))
			return nil
		},
	}
}
