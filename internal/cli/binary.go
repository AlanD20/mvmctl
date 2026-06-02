package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// binaryColumns defines the local listing columns for binaries.
var binaryColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.BinaryItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.BinaryItem).ID) }},
	{Header: "Name", Extract: func(v any) string { return v.(*model.BinaryItem).Name }},
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
func NewBinaryCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bin",
		Aliases: []string{"binary"},
		Short:   "Binary management",
	}

	cmd.AddCommand(newBinaryListCmd(op))
	cmd.AddCommand(newBinaryPullCmd(op))
	cmd.AddCommand(newBinaryRemoveCmd(op))
	cmd.AddCommand(newBinaryDefaultCmd(op))

	return cmd
}

func newBinaryListCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool
	var remote bool
	var limit int

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List local (and optionally remote) Firecracker versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			local, _, err := op.BinaryList(cmd.Context(), false, nil, nil)
			if err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(local, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			if remote {
				fmt.Fprintf(os.Stderr, "Fetching remote versions...\n")
				_, remoteVersions, err := op.BinaryList(cmd.Context(), true, &limit, nil)
				if err != nil {
					return err
				}
				common.RenderVersionTree(remoteVersions)
				return nil
			}

			// Local listing
			style := common.Cli.ResolveListingStyle(cmd.Context(), op, longOutput)
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

func newBinaryPullCmd(op *api.Operation) *cobra.Command {
	var version string
	var gitRef string
	var setDefault bool
	var force bool

	cmd := &cobra.Command{
		Use:               "pull [name|selector]",
		Short:             "Download a Firecracker version or build from source",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			effectiveVersion := version

			// Support name:version selector (matching kernel pull pattern)
			if strings.Contains(name, ":") {
				idx := strings.LastIndex(name, ":")
				effectiveVersion = name[idx+1:]
				name = name[:idx]
				if cmd.Flags().Changed("version") {
					return fmt.Errorf("mutually exclusive options")
				}
			}

			if strings.ToLower(name) != "firecracker" {
				common.Cli.Error(
					fmt.Sprintf(
						"Unsupported binary: '%s'. Only 'firecracker' is supported for download or build.",
						name,
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
				result := op.BinaryPull(cmd.Context(), &inputs.BinaryPullInput{
					Version:          "",
					Name:             name,
					GitRef:           gitRefPtr,
					SetDefault:       setDefault,
					DownloadOverride: false,
				}, nil)
				if result.Status == "error" {
					return fmt.Errorf("%s", result.Message)
				}
				binaries, _ := result.Item.([]*model.BinaryItem)

				common.Cli.Info("")
				for _, b := range binaries {
					shortID := common.Cli.FormatID(b.ID)
					common.Cli.Success(fmt.Sprintf("Built: %s %s: %s", b.Name, b.Version, b.Path))
					common.Cli.Info(fmt.Sprintf("  ID: %s", shortID))
				}

				if setDefault && len(binaries) > 0 {
					common.Cli.Success(fmt.Sprintf("Default binary set to: %s", binaries[0].Version))
				}

				return nil
			}

			// Normal download path
			result := op.BinaryPull(cmd.Context(), &inputs.BinaryPullInput{
				Version:          effectiveVersion,
				Name:             name,
				SetDefault:       setDefault,
				DownloadOverride: force,
			}, nil)

			// If binary already exists and --force wasn't set, offer to re-download
			if result.Status == "error" && result.Code == string(errs.CodeBinaryAlreadyExists) && !force {
				common.Cli.Warning(result.Message)

				confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Re-download?", false)
				if pErr != nil {
					return pErr
				}
				if !confirmed {
					common.Cli.Info("Aborted")
					return nil
				}

				result = op.BinaryPull(cmd.Context(), &inputs.BinaryPullInput{
					Version:          effectiveVersion,
					Name:             name,
					SetDefault:       setDefault,
					DownloadOverride: true,
				}, nil)
			}

			if result.Status == "error" {
				return fmt.Errorf("%s", result.Message)
			}

			if result.Status == "skipped" {
				common.Cli.Info(result.Message)
				if binaries, ok := result.Item.([]*model.BinaryItem); ok {
					for _, b := range binaries {
						shortID := common.Cli.FormatID(b.ID)
						common.Cli.Info(fmt.Sprintf("  %s v%s: %s", b.Name, b.Version, shortID))
					}
				}
				return nil
			}

			binaries, _ := result.Item.([]*model.BinaryItem)
			for _, b := range binaries {
				shortID := common.Cli.FormatID(b.ID)
				common.Cli.Success(fmt.Sprintf("Downloaded: %s v%s: %s", b.Name, b.Version, b.Path))
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

func newBinaryRemoveCmd(op *api.Operation) *cobra.Command {
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
				result := op.BinaryRemoveByVersion(cmd.Context(), version, force)
				if result.IsError() {
					return fmt.Errorf("%s", result.Message)
				}
				common.Cli.Success(fmt.Sprintf("Removed: v%s", version))
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}

			batchResult := op.BinaryRemove(cmd.Context(), &inputs.BinaryInput{Identifiers: args}, force)
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

func newBinaryDefaultCmd(op *api.Operation) *cobra.Command {
	return &cobra.Command{
		Use:               "default [identifier]",
		Short:             "Set a binary as the active default",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBinaryVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			result := op.BinarySetDefault(cmd.Context(), &inputs.BinaryInput{Identifiers: []string{identifier}})
			if result.IsError() {
				return fmt.Errorf("%s", result.Message)
			}

			msg := result.Message
			if msg == "" {
				msg = fmt.Sprintf("Default binary set to: %s", identifier)
			}
			common.Cli.Success(msg)
			return nil
		},
	}
}
