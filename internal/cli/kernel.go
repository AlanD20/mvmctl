// Package cli — kernel management commands
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"

	"github.com/spf13/cobra"
)

// kernelColumns defines the local listing columns for kernels.
var kernelColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.KernelItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.KernelItem).ID) }},
	{Header: "Name", Extract: func(v any) string {
		return common.Cli.FormatName(v.(*model.KernelItem).BaseName, !v.(*model.KernelItem).IsPresent)
	}},
	{Header: "Version", Extract: func(v any) string { return v.(*model.KernelItem).Version }},
	{Header: "Type", Extract: func(v any) string { return v.(*model.KernelItem).Type }},
	{Header: "Arch", Extract: func(v any) string { return v.(*model.KernelItem).Arch }, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.KernelItem).CreatedAt, "relative") },
	},
}

func NewKernelCmd(kernelAPI api.KernelAPI, configAPI api.ConfigAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Kernel management",
		Long:  "Manage kernels — list, pull, remove, inspect, set default, import.",
	}

	cmd.AddCommand(newKernelListCmd(kernelAPI, configAPI))
	cmd.AddCommand(newKernelPullCmd(kernelAPI))
	cmd.AddCommand(newKernelRemoveCmd(kernelAPI))
	cmd.AddCommand(newKernelInspectCmd(kernelAPI))
	cmd.AddCommand(newKernelDefaultCmd(kernelAPI))
	cmd.AddCommand(newKernelImportCmd(kernelAPI))

	return cmd
}

func newKernelListCmd(kernelAPI api.KernelAPI, configAPI api.ConfigAPI) *cobra.Command {
	var jsonOutput bool
	var longOutput bool
	var remote bool
	var noCache bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List cached kernels (or available remote kernels with --remote)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote {
				fmt.Fprintln(os.Stderr, "Fetching remote kernel versions...")
				_, remoteVersions, err := kernelAPI.KernelList(cmd.Context(), true, noCache, nil)
				if err != nil {
					return err
				}

				if jsonOutput {
					if remoteVersions == nil {
						remoteVersions = []model.VersionInfo{}
					}
					b, _ := json.MarshalIndent(remoteVersions, "", "  ")
					fmt.Println(string(b))
					return nil
				}

				if len(remoteVersions) == 0 {
					fmt.Println("No remote kernels available.")
					return nil
				}

				common.RenderVersionTree(remoteVersions)
				return nil
			}

			// Local listing
			kernels, _, err := kernelAPI.KernelList(cmd.Context(), false, false, nil)
			if err != nil {
				return err
			}

			if jsonOutput {
				if kernels == nil {
					kernels = []*model.KernelItem{}
				}
				b, _ := json.MarshalIndent(kernels, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			// Local listing
			style := common.Cli.ResolveListingStyle(cmd.Context(), configAPI, longOutput)
			common.RenderListing(kernels, kernelColumns, style)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Show available remote kernel versions")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Skip cached version listing and fetch live from upstream")

	return cmd
}

func newKernelPullCmd(kernelAPI api.KernelAPI) *cobra.Command {
	var kernelType string
	var version string
	var setDefault bool
	var jobs int
	var keepBuildDir bool
	var cleanBuild bool
	var kernelConfig string
	var features string

	cmd := &cobra.Command{
		Use:               "pull [type:version]",
		Short:             "Pull or build a kernel",
		ValidArgsFunction: completeKernelIDs,
		Long: `Pull or build a kernel.

Examples:
  mvm kernel pull official:6.19.9
  mvm kernel pull official:6.19.9 --default
  mvm kernel pull --type official --version 6.19.9`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveType := kernelType
			effectiveVersion := version

			if len(args) > 0 {
				selector := args[0]
				if strings.Contains(selector, ":") {
					// Split from the right (last colon) so "a:b:c" → type="a:b", version="c"
					// so "a:b:c" → type="a:b", version="c"
					idx := strings.LastIndex(selector, ":")
					effectiveType = selector[:idx]
					effectiveVersion = selector[idx+1:]
				} else {
					effectiveType = selector
				}
			}

			if effectiveType == "" {
				return fmt.Errorf("kernel type is required")
			}

			// Parse features string into a slice
			var featureList []string
			if features != "" {
				for _, f := range strings.Split(features, ",") {
					f = strings.TrimSpace(f)
					if f != "" {
						featureList = append(featureList, f)
					}
				}
			}

			// jobs flag: only pass if explicitly set
			jobsArg := 0
			if cmd.Flags().Changed("jobs") {
				jobsArg = jobs
			}

			prog := common.NewProgress()
			prog.Start("Pulling kernel...")

			// Build KernelPullInput
			featureStr := strings.Join(featureList, ",")
			kernelInput := inputs.KernelPullInput{
				KernelType:   effectiveType,
				Version:      effectiveVersion,
				Jobs:         jobsArg,
				KeepBuildDir: keepBuildDir,
				CleanBuild:   cleanBuild,
				KernelConfig: kernelConfig,
				SetDefault:   setDefault,
				Features:     featureStr,
			}

			kernelItem, err := kernelAPI.KernelPull(cmd.Context(), kernelInput, func(e event.Progress) {
				if e.Message != "" {
					prog.UpdateText(e.Message)
				}
			})
			prog.Stop()
			if err != nil {
				return err
			}
			if kernelItem != nil {
				common.Cli.Success(
					fmt.Sprintf("Pulled: %s (ID: %s)", kernelItem.Name, common.Cli.FormatID(kernelItem.ID)),
				)
			} else {
				common.Cli.Success("Pull completed")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&kernelType, "type", "", "Kernel type: firecracker or official")
	cmd.Flags().StringVar(&version, "version", "", "Kernel version")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after fetch")
	cmd.Flags().IntVar(&jobs, "jobs", 0, "Parallel build jobs (official only)")
	cmd.Flags().BoolVar(&keepBuildDir, "keep-build-dir", false, "Keep build directory (official only)")
	cmd.Flags().BoolVar(&cleanBuild, "clean-build", false, "Skip cache (official only)")
	cmd.Flags().StringVar(&kernelConfig, "config", "", "Custom kernel config file to apply as a fragment")
	cmd.Flags().StringVar(&features, "features", "", "Comma-separated kernel features (kvm, nftables, tuntap, btrfs)")

	return cmd
}

func newKernelRemoveCmd(kernelAPI api.KernelAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "rm [selectors...]",
		Aliases:           []string{"remove", "delete", "del"},
		Short:             "Remove one or more kernels",
		ValidArgsFunction: completeKernelIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("usage error")
			}
			result := kernelAPI.KernelRemove(cmd.Context(), inputs.KernelInput{Identifiers: args, Force: force})
			for _, item := range result.Items {
				if item.IsOK() {
					msg := item.Message
					if msg == "" {
						msg = "Removed"
					}
					common.Cli.Success(msg)
				} else {
					msg := item.Message
					if msg == "" {
						msg = "Remove failed"
					}
					common.Cli.Error(msg)
				}
			}
			if result.HasErrors() {
				return fmt.Errorf("one or more removals failed")
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Remove even if referenced by VMs")

	return cmd
}

func newKernelInspectCmd(kernelAPI api.KernelAPI) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [selector]",
		Short:             "Show detailed information about a kernel",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKernelIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := args[0]

			info, err := kernelAPI.KernelInspect(cmd.Context(), prefix)
			if err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(info, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			name := info.Kernel.Name
			if name == "" {
				name = prefix
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("Kernel: %s", name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newKernelDefaultCmd(kernelAPI api.KernelAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "default [selector]",
		Short:             "Set a kernel as the default",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKernelIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kernelID, checkErr := common.Cli.CheckArg(cmd, args[0])
			if checkErr != nil {
				return checkErr
			}
			if err := kernelAPI.KernelSetDefault(cmd.Context(), kernelID); err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Default kernel set to: %s", kernelID))
			return nil
		},
	}

	return cmd
}

func newKernelImportCmd(kernelAPI api.KernelAPI) *cobra.Command {
	var version string
	var setDefault bool

	cmd := &cobra.Command{
		Use:   "import [name] [path]",
		Short: "Register a vmlinux file as a kernel in the database",
		Long: `Register a vmlinux file as a kernel in the database.

Examples:
  mvm kernel import my-kernel ./vmlinux-6.1-x86_64
  mvm kernel import my-kernel ./vmlinux-custom --version 6.1 --default`,
		Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp // arg0 "name" is new — no completion
			}
			return nil, cobra.ShellCompDirectiveDefault // arg1 "path" — file completion
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			path := args[1]

			// Check source file exists
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("source file not found: %s", path)
			}

			// Build KernelImportInput
			var versionPtr *string
			if version != "" {
				v := version
				versionPtr = &v
			}
			importInput := inputs.KernelImportInput{
				Name:       name,
				Path:       path,
				Version:    versionPtr,
				SetDefault: setDefault,
			}
			kernelItem, err := kernelAPI.KernelImport(cmd.Context(), importInput)
			if err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Imported: %s", kernelItem.Name))
			common.Cli.Info(fmt.Sprintf("  ID:   %s", common.Cli.FormatID(kernelItem.ID)))
			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default kernel set to: %s", name))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Override auto-detected kernel version")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after import")

	return cmd
}
