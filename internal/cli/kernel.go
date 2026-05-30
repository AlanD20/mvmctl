// Package cli — kernel management commands
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/internal/infra/errs"
)

// kernelColumns defines the local listing columns for kernels.
var kernelColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.KernelItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.KernelItem).ID) }},
	{Header: "Name", Extract: func(v any) string { return common.Cli.FormatName(v.(*model.KernelItem).BaseName, !v.(*model.KernelItem).IsPresent) }},
	{Header: "Version", Extract: func(v any) string { return v.(*model.KernelItem).Version }},
	{Header: "Type", Extract: func(v any) string { return v.(*model.KernelItem).Type }},
	{Header: "Arch", Extract: func(v any) string { return v.(*model.KernelItem).Arch }, LongOnly: true},
	{Header: "Created", Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.KernelItem).CreatedAt, "relative") }},
}

func NewKernelCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Kernel management",
		Long:  "Manage kernels — list, pull, remove, inspect, set default, import.",
	}

	cmd.AddCommand(newKernelLsCmd(op))
	cmd.AddCommand(newKernelPullCmd(op))
	cmd.AddCommand(newKernelRmCmd(op))
	cmd.AddCommand(newKernelInspectCmd(op))
	cmd.AddCommand(newKernelDefaultCmd(op))
	cmd.AddCommand(newKernelImportCmd(op))

	// Hidden help subcommand matching Python's Typer "help" command
	helpCmd := &cobra.Command{
		Use:    "help",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Parent().Help()
		},
	}
	cmd.AddCommand(helpCmd)

	return cmd
}

func newKernelLsCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool
	var longOutput bool
	var remote bool
	var noCache bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List cached kernels (or available remote kernels with --remote)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote {
				fmt.Fprintln(os.Stderr, "Fetching remote kernel versions...")
				_, remoteVersions, err := op.KernelList(cmd.Context(), true, noCache)
				if err != nil {
					return err
				}

				if jsonOutput {
					data := make([]map[string]interface{}, 0, len(remoteVersions))
					for _, v := range remoteVersions {
						var sha256URL interface{}
						if v.SHA256URL != nil {
							sha256URL = *v.SHA256URL
						}
						data = append(data, map[string]interface{}{
							"version":      v.Version,
							"type":         v.Type,
							"display_name": v.DisplayName,
							"download_url": v.DownloadURL,
							"sha256_url":   sha256URL,
							"format":       v.Format,
						})
					}
					b, _ := json.MarshalIndent(data, "", "  ")
					fmt.Println(string(b))
					return nil
				}

				if len(remoteVersions) == 0 {
					fmt.Println("No remote kernels available.")
					return nil
				}

				// Group by type
				groups := make(map[string][]model.VersionInfo)
				for _, v := range remoteVersions {
					groups[v.Type] = append(groups[v.Type], v)
				}

				// Sort types alphabetically
				sortedTypes := make([]string, 0, len(groups))
				for t := range groups {
					sortedTypes = append(sortedTypes, t)
				}
				sort.Strings(sortedTypes)

				rows := make([][]string, 0)
				for _, typeKey := range sortedTypes {
					versionList := groups[typeKey]
					if len(versionList) == 0 {
						continue
					}

					// Build display name for type header
					parts := strings.SplitN(typeKey, "-", 2)
					var typeDisplay string
					if len(parts) > 1 {
						typeDisplay = toTitle(parts[0]) + " " + parts[1]
					} else {
						typeDisplay = toTitle(typeKey)
					}
					suffix := ""
					if strings.HasPrefix(typeKey, "official") {
						suffix = " (build required)"
					}
					rows = append(rows, []string{typeKey, typeDisplay + suffix})

					// Version rows with tree indent
					for j, v := range versionList {
						isLast := j == len(versionList)-1
						prefix := "  └─ "
						if !isLast {
							prefix = "  ├─ "
						}
						display := v.DisplayName
						if display == "" {
							display = v.Version
						}
						rows = append(rows, []string{prefix + v.Version, display})
					}
				}

				common.Cli.Table([]string{"Type / Version", "Description"}, rows)
				return nil
			}

			// Local listing
			kernels, _, err := op.KernelList(cmd.Context(), false, false)
			if err != nil {
				return err
			}

			if jsonOutput {
				if kernels == nil {
					kernels = []*model.KernelItem{}
				}
				dictList := make([]map[string]interface{}, 0, len(kernels))
				for _, k := range kernels {
					dictList = append(dictList, map[string]interface{}{"id": k.ID, "name": k.Name, "version": k.Version})
				}
				b, _ := json.MarshalIndent(dictList, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			// Local listing
			style := common.Cli.ResolveListingStyle(cmd.Context(), op, longOutput)
			items := make([]any, len(kernels))
			for i, k := range kernels {
				items[i] = k
			}
			common.Cli.RenderListing(items, kernelColumns, style)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Show available remote kernel versions")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Skip cached version listing and fetch live from upstream")

	return cmd
}

func newKernelPullCmd(op *api.Operation) *cobra.Command {
	var kernelType string
	var version string
	var arch string
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
  mvm kernel pull --type official --version 6.19.9
  mvm kernel pull firecracker --arch arm64`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveType := kernelType
			effectiveVersion := version

			if len(args) > 0 {
				selector := args[0]
				if strings.Contains(selector, ":") {
					// Python uses rsplit(":", maxsplit=1) — split from the RIGHT
					// so "a:b:c" → type="a:b", version="c"
					idx := strings.LastIndex(selector, ":")
					effectiveType = selector[:idx]
					effectiveVersion = selector[idx+1:]
				} else {
					effectiveType = selector
				}
			}

			if effectiveType == "" {
				common.Cli.Error("Kernel type is required. Use 'mvm kernel pull --type official' or 'mvm kernel pull official:6.19.9'")
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

			// jobs flag: only pass if explicitly set (matches Python's None default)
			jobsArg := 0
			if cmd.Flags().Changed("jobs") {
				jobsArg = jobs
			}

			spinner := common.NewSpinner("")
			spinner.Start()

			// Build KernelPullInput matching Python's KernelPullInput dataclass
			var versionPtr *string
			if effectiveVersion != "" {
				v := effectiveVersion
				versionPtr = &v
			}
			var archPtr *string
			if arch != "" {
				a := arch
				archPtr = &a
			}
			var jobsPtr *int
			if jobsArg > 0 {
				j := jobsArg
				jobsPtr = &j
			}
			var kernelCfgPtr *string
			if kernelConfig != "" {
				kc := kernelConfig
				kernelCfgPtr = &kc
			}
			featureStr := strings.Join(featureList, ",")

			kernelInput := &inputs.KernelPullInput{
				KernelType:   effectiveType,
				Version:      versionPtr,
				Arch:         archPtr,
				Jobs:         jobsPtr,
				KeepBuildDir: keepBuildDir,
				CleanBuild:   cleanBuild,
				KernelConfig: kernelCfgPtr,
				SetDefault:   setDefault,
				Features:     featureStr,
			}

			result := op.KernelPull(cmd.Context(), kernelInput, func(event errs.ProgressEvent) {
				if event.Message != "" {
					spinner.UpdateText(event.Message)
				}
			})
			spinner.Stop()
			if result == nil {
				return nil
			}
			if result.IsError() {
				common.Cli.Error(result.Message)
				return fmt.Errorf("%s", result.Message)
			}
			if result.Status == "skipped" {
				common.Cli.Info(result.Message)
				if result.Item != nil {
					if ki, ok := result.Item.(*model.KernelItem); ok && ki != nil {
						common.Cli.Info(fmt.Sprintf("  ID: %s", common.Cli.FormatID(ki.ID)))
					}
				}
				return nil
			}
			if result.Item != nil {
				if ki, ok := result.Item.(*model.KernelItem); ok && ki != nil {
					common.Cli.Success(fmt.Sprintf("Pulled: %s (ID: %s)", ki.Name, common.Cli.FormatID(ki.ID)))
					resolvedFeatures, _ := result.Metadata["features"].([]string)
					if len(resolvedFeatures) > 0 {
						common.Cli.Info("Enabled features: " + strings.Join(resolvedFeatures, ", "))
					}
				}
			} else {
				// Fallback for unexpected non-OperationResult returns
				common.Cli.Success("Pull completed")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&kernelType, "type", "", "Kernel type: firecracker or official")
	cmd.Flags().StringVar(&version, "version", "", "Kernel version")
	cmd.Flags().StringVar(&arch, "arch", "", "Architecture (x86_64, arm64)")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after fetch")
	cmd.Flags().IntVar(&jobs, "jobs", 0, "Parallel build jobs (official only)")
	cmd.Flags().BoolVar(&keepBuildDir, "keep-build-dir", false, "Keep build directory (official only)")
	cmd.Flags().BoolVar(&cleanBuild, "clean-build", false, "Skip cache (official only)")
	cmd.Flags().StringVar(&kernelConfig, "config", "", "Custom kernel config file to apply as a fragment")
	cmd.Flags().StringVar(&features, "features", "", "Comma-separated kernel features (kvm, nftables, tuntap)")

	return cmd
}

func newKernelRmCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:                "rm [identifiers...]",
		Aliases:            []string{"remove"},
		Short:              "Remove one or more kernels",
		ValidArgsFunction:  completeKernelIDs,
		DisableSuggestions: true,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				common.Cli.Error("Provide at least one kernel ID or name")
				return fmt.Errorf("usage error")
			}
			result := op.KernelRemove(cmd.Context(), args, force)
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

func newKernelInspectCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "inspect [prefix]",
		Short:             "Show detailed information about a kernel",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKernelIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := args[0]

			info, err := op.KernelInspect(cmd.Context(), prefix)
			if err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(info, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			name := prefix
			if n, ok := info["kernel"].(map[string]interface{}); ok {
				if kn, ok := n["name"].(string); ok {
					name = kn
				}
			}

			common.Cli.PrintDictTree(info, fmt.Sprintf("Kernel: %s", name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newKernelDefaultCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "default [kernel-id]",
		Short:              "Set a kernel as the default",
		Args:               cobra.ExactArgs(1),
		ValidArgsFunction:  completeKernelIDs,
		DisableSuggestions: true,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			kernelID, err := common.Cli.CheckNameArg(cmd, args[0])
			if err != nil {
				return err
			}
			result := op.KernelSetDefault(cmd.Context(), kernelID)
			if result.Status == "error" {
				return fmt.Errorf("set default failed: %s", result.Message)
			}
			msg := result.Message
			if msg == "" {
				msg = fmt.Sprintf("Default kernel set to: %s", kernelID)
			}
			common.Cli.Success(msg)
			return nil
		},
	}

	return cmd
}

func newKernelImportCmd(op *api.Operation) *cobra.Command {
	var version string
	var arch string
	var setDefault bool

	cmd := &cobra.Command{
		Use:   "import [name] [path]",
		Short: "Register a vmlinux file as a kernel in the database",
		Long: `Register a vmlinux file as a kernel in the database.

Examples:
  mvm kernel import my-kernel ./vmlinux-6.1-x86_64
  mvm kernel import my-kernel ./vmlinux-custom --version 6.1 --arch x86_64 --default`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			path := args[1]

			// Check source file exists
			if _, err := os.Stat(path); os.IsNotExist(err) {
				common.Cli.Error(fmt.Sprintf("Source file not found: %s", path))
				return fmt.Errorf("source file not found: %s", path)
			}

			// Build KernelImportInput matching Python's KernelImportInput dataclass
			var versionPtr *string
			if version != "" {
				v := version
				versionPtr = &v
			}
			var archPtr *string
			if arch != "" {
				a := arch
				archPtr = &a
			}
			importInput := &inputs.KernelImportInput{
				Name:       name,
				Path:       path,
				Version:    versionPtr,
				Arch:       archPtr,
				SetDefault: setDefault,
			}
			result := op.KernelImport(cmd.Context(), importInput)
			if result.Status == "error" {
				msg := result.Message
				if msg == "" {
					msg = fmt.Sprintf("Import failed: %s", name)
				}
				common.Cli.Error(msg)
				return fmt.Errorf("%s", msg)
			}
			if kernelItem, ok := result.Item.(*model.KernelItem); ok && kernelItem != nil {
				common.Cli.Success(fmt.Sprintf("Imported: %s", kernelItem.Name))
				common.Cli.Info(fmt.Sprintf("  ID:   %s", common.Cli.FormatID(kernelItem.ID)))
			}
			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default kernel set to: %s", name))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Override auto-detected kernel version")
	cmd.Flags().StringVar(&arch, "arch", "", "Kernel architecture (default: auto-detected)")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after import")

	return cmd
}

// sortedStringKeys returns alphabetically sorted keys of a string→[]string map.
func sortedStringKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// toTitle returns a title-cased copy of s (first character uppercase, rest lowercase).
func toTitle(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}
