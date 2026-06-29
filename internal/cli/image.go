package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"

	"github.com/spf13/cobra"
)

// imageColumns defines the local listing columns for images.
var imageColumns = []common.ListingColumn{
	{Header: "", Extract: func(v any) string { return common.Cli.FormatMarker(v.(*model.ImageItem).IsDefault) }},
	{Header: "ID", Extract: func(v any) string { return common.Cli.FormatID(v.(*model.ImageItem).ID) }},
	{Header: "Name", Extract: func(v any) string {
		return common.Cli.FormatName(v.(*model.ImageItem).Name, !v.(*model.ImageItem).IsPresent)
	}},
	{Header: "Type", Extract: func(v any) string { return v.(*model.ImageItem).Type }},
	{Header: "Arch", Extract: func(v any) string { return v.(*model.ImageItem).Arch }, LongOnly: true},
	{Header: "FS Type", Extract: func(v any) string { return v.(*model.ImageItem).FSType }, LongOnly: true},
	{Header: "Size", Extract: func(v any) string {
		cs := v.(*model.ImageItem).CompressedSize
		if cs != nil {
			return common.Cli.FormatSize(*cs)
		}
		return "-"
	}, LongOnly: true},
	{
		Header:  "Created",
		Extract: func(v any) string { return common.Cli.FormatTimestamp(v.(*model.ImageItem).CreatedAt, "relative") },
	},
}

// NewImageCmd creates the image management command tree.
func NewImageCmd(imageAPI api.ImageAPI, configAPI api.ConfigAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "image",
		Aliases: []string{"img"},
		Short:   "Image management",
		Long:    "Download, list, inspect, and manage VM images.",
	}

	cmd.AddCommand(newImageListCmd(imageAPI, configAPI))
	cmd.AddCommand(newImagePullCmd(imageAPI))
	cmd.AddCommand(newImageRemoveCmd(imageAPI))
	cmd.AddCommand(newImageInspectCmd(imageAPI))
	cmd.AddCommand(newImageDefaultCmd(imageAPI))
	cmd.AddCommand(newImageImportCmd(imageAPI))
	cmd.AddCommand(newImageWarmCmd(imageAPI))

	return cmd
}

// --- ls ---

func newImageListCmd(imageAPI api.ImageAPI, configAPI api.ConfigAPI) *cobra.Command {
	var (
		jsonOutput bool
		remote     bool
		noCache    bool
		typeFilter string
		longOutput bool
	)

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List cached images (or available remote images with --remote).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote {
				fmt.Fprintln(os.Stderr, "Fetching remote images")
				_, versions, err := imageAPI.ImageListAll(cmd.Context(), true, typeFilter, noCache, nil)
				if err != nil {
					return err
				}
				printRemoteImages(versions, jsonOutput)
			} else {
				images, _, err := imageAPI.ImageListAll(cmd.Context(), false, "", false, nil)
				if err != nil {
					return err
				}
				printLocalImages(images, jsonOutput, longOutput, cmd.Context(), configAPI)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVarP(&remote, "remote", "r", false, "Show available remote images")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Skip cached version listing and fetch live from upstream")
	cmd.Flags().StringVar(&typeFilter, "type", "", "Filter by image type (e.g. ubuntu, alpine)")
	cmd.Flags().BoolVar(&longOutput, "long", false, "Show full listing with all columns")

	return cmd
}

func printRemoteImages(versions []model.VersionInfo, jsonOutput bool) {
	if jsonOutput {
		if versions == nil {
			versions = []model.VersionInfo{}
		}
		b, _ := json.MarshalIndent(versions, "", "  ")
		fmt.Println(string(b))
		return
	}

	if len(versions) == 0 {
		common.Cli.Info("No remote images available.")
		return
	}

	common.RenderVersionTree(versions)
}

// printLocalImages prints the local image listing table.
func printLocalImages(
	images []*model.ImageItem,
	jsonOutput bool,
	longOutput bool,
	ctx context.Context,
	configAPI api.ConfigAPI,
) {
	if jsonOutput {
		if images == nil {
			images = []*model.ImageItem{}
		}
		b, _ := json.MarshalIndent(images, "", "  ")
		fmt.Println(string(b))
		return
	}

	style := common.Cli.ResolveListingStyle(ctx, configAPI, longOutput)
	common.RenderListing(images, imageColumns, style)
}

// --- pull ---

func newImagePullCmd(imageAPI api.ImageAPI) *cobra.Command {
	var (
		imageType        string
		version          string
		force            bool
		noCache          bool
		setDefault       bool
		skipOptimization bool
		disableDetector  string
	)

	cmd := &cobra.Command{
		Use:   "pull [selector]",
		Short: "Download an image by its ID. Run 'mvm image ls -r' to list available image IDs.",
		Long: `Download an image by its ID. Run 'mvm image ls -r' to list available image IDs.

The selector can be a type (e.g. "ubuntu") or type:version (e.g. "ubuntu:24.04").`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRemoteImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			selector := args[0]

			var disabledDetectors []string
			if disableDetector != "" {
				for _, s := range strings.Split(disableDetector, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						disabledDetectors = append(disabledDetectors, s)
					}
				}
			}

			// If --type is not explicitly set, parse selector for type:version
			typeFlag := cmd.Flags().Lookup("type")
			typeExplicitlySet := typeFlag != nil && typeFlag.Changed

			effectiveType := imageType
			effectiveVersion := version

			if !typeExplicitlySet && strings.Contains(selector, ":") {
				parts := strings.SplitN(selector, ":", 2)
				effectiveType = parts[0]
				if len(parts) > 1 {
					effectiveVersion = parts[1]
				}
			} else if !typeExplicitlySet {
				effectiveType = selector
			}

			input := inputs.ImagePullInput{
				Type:              effectiveType,
				Version:           effectiveVersion,
				Force:             force,
				NoCache:           noCache,
				SetDefault:        setDefault,
				SkipOptimization:  skipOptimization,
				DisabledDetectors: disabledDetectors,
			}

			prog := common.NewProgress()
			prog.Start("Downloading image...")
			img, err := imageAPI.ImagePull(cmd.Context(), input, func(e event.Progress) {
				if e.Message != "" {
					prog.UpdateText(e.Message)
				}
			})
			prog.Stop()
			if err != nil {
				var ni *errs.NeedsInteraction
				if errors.As(err, &ni) {
					common.Cli.Info(ni.Message)
					return nil
				}
				return err
			}

			common.Cli.Success(fmt.Sprintf("Pulled: %s (ID: %s)", img.Name, common.Cli.FormatID(img.ID)))
			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default image set to: %s", selector))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&imageType, "type", "", "Image type from images.yaml (e.g. ubuntu, debian, firecracker)")
	cmd.Flags().
		StringVar(&version, "version", "", "Image spec version from images.yaml (required if multiple images share the same type)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Re-download even if exists")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Skip cached version listing and fetch live from upstream")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default image after download")
	cmd.Flags().BoolVar(&skipOptimization, "skip-optimization", false, "Skip OS cache cleanup (deblob)")
	cmd.Flags().
		StringVar(&disableDetector, "disable-detector", "", "Comma-separated detectors to disable: type,label,size,filesystem,all")

	return cmd
}

// --- rm ---

func newImageRemoveCmd(imageAPI api.ImageAPI) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm [selectors...]",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Remove cached images by selector.",
		Long: `Remove cached images by selector.

Examples:
  mvm image rm abc123
  mvm image rm abc123 def456`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result := imageAPI.ImageRemove(cmd.Context(), inputs.ImageInput{Identifiers: args}, force)
			for _, r := range result.Items {
				itemID := "unknown"
				if r.Item != nil {
					if img, ok := r.Item.(*model.ImageItem); ok {
						itemID = common.Cli.FormatID(img.ID)
					}
				}
				if r.IsOK() {
					common.Cli.Success(fmt.Sprintf("Removed: %s", itemID))
				} else if r.Message != "" {
					common.Cli.Error(r.Message)
				} else {
					common.Cli.Error(fmt.Sprintf("Remove failed: %s", itemID))
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Remove even if referenced by VMs")

	return cmd
}

// --- inspect ---

func newImageInspectCmd(imageAPI api.ImageAPI) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "inspect [selector]",
		Short: "Show detailed information about an image.",
		Long: `Show detailed information about an image.

Examples:
  mvm image inspect abc123
  mvm image inspect abc123 --json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := args[0]

			input := inputs.ImageInput{Identifiers: []string{prefix}}
			info, err := imageAPI.ImageInspect(cmd.Context(), input)
			if err != nil {
				return err
			}
			if info == nil {
				return fmt.Errorf("image not found: %s", prefix)
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(info, "", "  ")
				fmt.Println(string(b))
				return nil
			}

			name := info.Image.Name
			if name == "" {
				name = prefix
			}
			common.Cli.PrintDictTree(common.Cli.ToMap(info), fmt.Sprintf("Image: %s", name))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

// --- default ---

func newImageDefaultCmd(imageAPI api.ImageAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "default [selector]",
		Short:             "Set the default image for VM creation.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := args[0]
			imgInput := inputs.ImageInput{Identifiers: []string{prefix}}
			if err := imageAPI.ImageSetDefault(cmd.Context(), imgInput); err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Default image set to: %s", prefix))
			return nil
		},
	}

	return cmd
}

// --- import ---

func newImageImportCmd(imageAPI api.ImageAPI) *cobra.Command {
	var (
		rootPartition    int
		format           string
		force            bool
		setDefault       bool
		skipOptimization bool
		disableDetector  string
		version          string
	)

	cmd := &cobra.Command{
		Use:   "import [name] [path|vm-selector]",
		Short: "Import a local image file (qcow2, raw, tar-rootfs). The first argument is a display name.",
		Long: `Import a local image file. The first argument is a display name, the second is the file path.

Examples:
  mvm image import my-image /path/to/image.qcow2
  mvm image import my-image /path/to/image.raw --format raw
  mvm image import my-base-img:v1.0 vmtest --version v2.0`,
		Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp // arg0 "name" is new — no completion
			}
			return nil, cobra.ShellCompDirectiveDefault // arg1 "path" — file completion
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sourcePath := args[1]

			// Support name:version selector (--version overrides)
			effectiveVersion := version
			if !cmd.Flags().Changed("version") && strings.Contains(name, ":") {
				idx := strings.LastIndex(name, ":")
				effectiveVersion = name[idx+1:]
				name = name[:idx]
			}

			var disabledDetectors []string
			if disableDetector != "" {
				for s := range strings.SplitSeq(disableDetector, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						disabledDetectors = append(disabledDetectors, s)
					}
				}
			}

			input := inputs.ImageImportInput{
				Name:              name,
				Format:            format,
				SourcePath:        sourcePath,
				Version:           effectiveVersion,
				Partition:         rootPartition,
				DisabledDetectors: disabledDetectors,
				SkipOptimization:  skipOptimization,
				SetDefault:        setDefault,
				Force:             force,
			}

			prog := common.NewProgress()
			prog.Start("Importing image...")
			img, err := imageAPI.ImageImport(cmd.Context(), input, func(e event.Progress) {
				if e.Message != "" {
					prog.UpdateText(e.Message)
				}
			})
			prog.Stop()
			if err != nil {
				return err
			}
			if img == nil {
				return fmt.Errorf("import failed: no image returned")
			}
			common.Cli.Success(fmt.Sprintf("Imported: %s", img.Path))
			common.Cli.Info(fmt.Sprintf("  Name: %s", name))
			common.Cli.Info(fmt.Sprintf("  ID:   %s", common.Cli.FormatID(img.ID)))

			if setDefault {
				common.Cli.Success(fmt.Sprintf("Default image set to: %s", name))
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&rootPartition, "root-partition", 0, "Root Partition: 1, 2, 3")
	cmd.Flags().StringVar(&format, "format", "", "Image format: qcow2, raw, tar-rootfs, or auto")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing")
	cmd.Flags().BoolVarP(&setDefault, "default", "d", false, "Set as default after import")
	cmd.Flags().BoolVar(&skipOptimization, "skip-optimization", true, "Skip OS cache cleanup (deblob)")
	cmd.Flags().
		StringVar(&disableDetector, "disable-detector", "", "Comma-separated detectors to disable: type,label,size,filesystem,all")
	cmd.Flags().StringVar(&version, "version", "", "Set image version (overrides name:ver format)")

	return cmd
}

// --- warm ---

func newImageWarmCmd(imageAPI api.ImageAPI) *cobra.Command {
	var (
		warmAll bool
	)

	cmd := &cobra.Command{
		Use:   "warm [selector]",
		Short: "Pre-decompress image to ready pool for fast VM creation.",
		Long: `Pre-decompress image to ready pool for fast VM creation.

This command decompresses the image to tmpfs/RAM ahead of time,
so subsequent VM creations can use fast copy instead of waiting
for decompression.

Examples:
  # Warm an image by OS slug:
  mvm image warm ubuntu-24.04

  # Warm by image ID prefix:
  mvm image warm abc123

  # Warm all cached images:
  mvm image warm --all`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			imageID := ""
			if len(args) > 0 {
				imageID = args[0]
			}

			// If image_id is provided, warm that specific image.
			// Otherwise warm all images (defaults to all when no argument given).
			prog := common.NewProgress()
			prog.Start("Warming images...")
			var paths []string
			var warmErr error
			if imageID != "" {
				warmInput := inputs.ImageInput{Identifiers: []string{imageID}}
				paths, warmErr = imageAPI.ImageWarm(cmd.Context(), warmInput, false, func(e event.Progress) {
					if e.Message != "" {
						prog.UpdateText(e.Message)
					}
				})
			} else {
				paths, warmErr = imageAPI.ImageWarm(cmd.Context(), inputs.ImageInput{}, true, func(e event.Progress) {
					if e.Message != "" {
						prog.UpdateText(e.Message)
					}
				})
			}
			prog.Stop()
			if warmErr != nil {
				return warmErr
			}

			displayName := imageID
			if displayName == "" {
				displayName = "all images"
			}

			for _, p := range paths {
				// Let error propagate if path.Stat() fails.
				info, err := os.Stat(p)
				if err != nil {
					prog.Stop()
					return fmt.Errorf("failed to stat warmed path %s: %w", p, err)
				}
				sizeStr := common.Cli.FormatSize(info.Size())

				common.Cli.Success(fmt.Sprintf("Warmed: %s", displayName))
				common.Cli.Info(fmt.Sprintf("  Path: %s", p))
				common.Cli.Info(fmt.Sprintf("  Size: %s", sizeStr))
			}
			common.Cli.Info("  Ready for fast VM creation")
			return nil
		},
	}

	cmd.Flags().BoolVarP(&warmAll, "all", "a", false, "Warm all cached images")

	return cmd
}
