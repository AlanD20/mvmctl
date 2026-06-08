package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"

	"github.com/spf13/cobra"
)

// imageImportExtensionOrder defines the priority order for auto-detecting
// image format from filename extension. Must match Python's
// IMAGE_IMPORT_FORMAT_MAP iteration order for backwards compatibility.
var imageImportExtensionOrder = []string{
	".qcow2",
	".raw",
	".img",
	".ext4",
	".ext3",
	".ext2",
	".btrfs",
	".xfs",
	".vhd",
	".vhdx",
	".tar",
	".tar.gz",
	".tar.xz",
	".tgz",
}

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
func NewImageCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "image",
		Aliases: []string{"img"},
		Short:   "Image management",
		Long:    "Download, list, inspect, and manage VM images.",
	}

	cmd.AddCommand(newImageListCmd(op))
	cmd.AddCommand(newImagePullCmd(op))
	cmd.AddCommand(newImageRemoveCmd(op))
	cmd.AddCommand(newImageInspectCmd(op))
	cmd.AddCommand(newImageDefaultCmd(op))
	cmd.AddCommand(newImageImportCmd(op))
	cmd.AddCommand(newImageWarmCmd(op))

	return cmd
}

// ─── ls ──────────────────────────────────────────────────────────────────────

func newImageListCmd(op *api.Operation) *cobra.Command {
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
				_, versions, err := op.ImageListAll(cmd.Context(), true, typeFilter, noCache, nil)
				if err != nil {
					return err
				}
				printRemoteImages(versions, jsonOutput)
			} else {
				images, _, err := op.ImageListAll(cmd.Context(), false, "", false, nil)
				if err != nil {
					return err
				}
				printLocalImages(images, jsonOutput, longOutput, cmd.Context())
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
func printLocalImages(images []*model.ImageItem, jsonOutput bool, longOutput bool, ctx context.Context) {
	if jsonOutput {
		if images == nil {
			images = []*model.ImageItem{}
		}
		b, _ := json.MarshalIndent(images, "", "  ")
		fmt.Println(string(b))
		return
	}

	style := common.Cli.ResolveListingStyle(ctx, opRef, longOutput)
	common.RenderListing(images, imageColumns, style)
}

// ─── pull ────────────────────────────────────────────────────────────────────

func newImagePullCmd(op *api.Operation) *cobra.Command {
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

			// Match Python: if image_type is None (not explicitly set), parse selector for type:version
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
			img, err := op.ImagePull(cmd.Context(), input, func(e event.Progress) {
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
	cmd.Flags().BoolVar(&skipOptimization, "skip-optimization", false, "Skip shrink and compression, keep plain ext4")
	cmd.Flags().
		StringVar(&disableDetector, "disable-detector", "", "Comma-separated detectors to disable: type,label,size,filesystem,all")

	return cmd
}

// ─── rm ──────────────────────────────────────────────────────────────────────

func newImageRemoveCmd(op *api.Operation) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm [ids...]",
		Aliases: []string{"remove", "delete", "del"},
		Short:   "Remove cached images by ID prefix.",
		Long: `Remove cached images by ID prefix.

Examples:
  mvm image rm abc123
  mvm image rm abc123 def456`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result := op.ImageRemove(cmd.Context(), inputs.ImageInput{Identifiers: args}, force)
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

// ─── inspect ─────────────────────────────────────────────────────────────────

func newImageInspectCmd(op *api.Operation) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "inspect [id]",
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
			info, err := op.ImageInspect(cmd.Context(), input)
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

// ─── default ─────────────────────────────────────────────────────────────────

func newImageDefaultCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "default [id]",
		Short:             "Set the default image for VM creation.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeImageIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := args[0]
			imgInput := inputs.ImageInput{Identifiers: []string{prefix}}
			if err := op.ImageSetDefault(cmd.Context(), imgInput); err != nil {
				return err
			}
			common.Cli.Success(fmt.Sprintf("Default image set to: %s", prefix))
			return nil
		},
	}

	return cmd
}

// ─── import ──────────────────────────────────────────────────────────────────

func newImageImportCmd(op *api.Operation) *cobra.Command {
	var (
		rootPartition    int
		format           string
		force            bool
		setDefault       bool
		skipOptimization bool
		disableDetector  string
	)

	cmd := &cobra.Command{
		Use:   "import [name] [path]",
		Short: "Import a local image file (qcow2, raw, tar-rootfs). The first argument is a display name.",
		Long: `Import a local image file. The first argument is a display name, the second is the file path.

Examples:
  mvm image import my-image /path/to/image.qcow2
  mvm image import my-image /path/to/image.raw --format raw`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sourcePath := args[1]

			// Verify source exists
			if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
				return fmt.Errorf("source file not found: %s", sourcePath)
			}

			var disabledDetectors []string
			if disableDetector != "" {
				for _, s := range strings.Split(disableDetector, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						disabledDetectors = append(disabledDetectors, s)
					}
				}
			}

			// Auto-detect format from extension if not explicitly set.
			// Matches Python: if format is None or format == "auto":
			//   fname = source_path.name.lower()
			//   format = next((fmt for ext, fmt in IMAGE_IMPORT_FORMAT_MAP.items() if fname.endswith(ext)), None)
			formatFlag := cmd.Flags().Lookup("format")
			formatExplicitlySet := formatFlag != nil && formatFlag.Changed
			if !formatExplicitlySet || format == "auto" {
				// Use the centralized format map for values but iterate in Python-compatible order.
				fname := strings.ToLower(filepath.Base(sourcePath))
				found := false
				for _, ext := range imageImportExtensionOrder {
					if strings.HasSuffix(fname, ext) {
						if fmtVal, ok := infra.ImageImportFormatMap[ext]; ok {
							format = fmtVal
							found = true
							break
						}
					}
				}
				if !found {
					return fmt.Errorf(
						"Cannot auto-detect format from '%s'. Use --format qcow2|raw|tar-rootfs.",
						filepath.Base(sourcePath),
					)
				}
			}

			input := inputs.ImageImportInput{
				Name:              name,
				Format:            format,
				SourcePath:        sourcePath,
				Partition:         rootPartition,
				DisabledDetectors: disabledDetectors,
				SkipOptimization:  skipOptimization,
				SetDefault:        setDefault,
				Force:             force,
			}

			prog := common.NewProgress()
			prog.Start("Importing image...")
			img, err := op.ImageImport(cmd.Context(), input, func(e event.Progress) {
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
	cmd.Flags().BoolVar(&skipOptimization, "skip-optimization", false, "Skip shrink and compression, keep plain ext4")
	cmd.Flags().
		StringVar(&disableDetector, "disable-detector", "", "Comma-separated detectors to disable: type,label,size,filesystem,all")

	return cmd
}

// ─── warm ────────────────────────────────────────────────────────────────────

func newImageWarmCmd(op *api.Operation) *cobra.Command {
	var (
		warmAll bool
	)

	cmd := &cobra.Command{
		Use:   "warm [id]",
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

			// Match Python behavior:
			//   if image_id is not None: warm specific image
			//   else: warm all (defaults to all=True when no argument given)
			prog := common.NewProgress()
			prog.Start("Warming images...")
			var paths []string
			var warmErr error
			if imageID != "" {
				warmInput := inputs.ImageInput{Identifiers: []string{imageID}}
				paths, warmErr = op.ImageWarm(cmd.Context(), warmInput, false, func(e event.Progress) {
					if e.Message != "" {
						prog.UpdateText(e.Message)
					}
				})
			} else {
				paths, warmErr = op.ImageWarm(cmd.Context(), inputs.ImageInput{}, true, func(e event.Progress) {
					if e.Message != "" {
						prog.UpdateText(e.Message)
					}
				})
			}
			prog.Stop()
			if warmErr != nil {
				return warmErr
			}

			// Match Python:  display_name = image_id or "all images"
			displayName := imageID
			if displayName == "" {
				displayName = "all images"
			}

			for _, p := range paths {
				// Match Python: size_str = mvm_cli.format_size(path.stat().st_size)
				// Python would raise an exception if path.stat() fails — let error propagate.
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
