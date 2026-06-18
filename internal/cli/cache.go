// Package cli — cache management commands
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/api"

	"github.com/spf13/cobra"
)

// NewCacheCmd creates the cache command and its subcommands.
func NewCacheCmd(cacheAPI api.CacheAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Cache management",
	}

	cmd.AddCommand(newCacheInitCmd(cacheAPI))
	cmd.AddCommand(newCachePruneCmd(cacheAPI))
	cmd.AddCommand(newCacheCleanCmd(cacheAPI))
	return cmd
}

func newCacheInitCmd(cacheAPI api.CacheAPI) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize all cache resources",
		RunE: func(cmd *cobra.Command, args []string) error {
		// Uses progress callback for status updates during cache init.
			prog := common.NewProgress()
			prog.Start("Initializing cache...")

			item, err := cacheAPI.CacheInitAll(cmd.Context(), func(e event.Progress) {
				if e.Message != "" {
					prog.UpdateText(e.Message)
				}
			})

			prog.Stop()

			if err != nil {
				return err
			}

			common.Cli.Success("Cache initialized successfully")
			for _, dir := range item.Directories {
				common.Cli.Info(fmt.Sprintf("  %s", dir))
			}
			if item.GuestfsAppliance != "" {
				common.Cli.Info(fmt.Sprintf("  guestfs: %s", item.GuestfsAppliance))
			}
			if item.GuestfsKernel != "" {
				common.Cli.Info(fmt.Sprintf("  kernel: %s", item.GuestfsKernel))
			}

			return nil
		},
	}

	return cmd
}

// resourceDisplayName returns the display name for a resource type in messages.
func resourceDisplayName(resource string) string {
	switch resource {
	case "vm":
		return "VM"
	case "network":
		return "network"
	case "image":
		return "image"
	case "kernel":
		return "kernel"
	case "binary":
		return "binary"
	default:
		return resource
	}
}

// resourceDisplayNamePlural returns the plural display name for a resource type.
func resourceDisplayNamePlural(resource string) string {
	switch resource {
	case "vm":
		return "VMs"
	case "network":
		return "networks"
	case "image":
		return "images"
	case "kernel":
		return "kernels"
	case "binary":
		return "binaries"
	default:
		return resource + "s"
	}
}

// pruneResource handles pruning a specific resource type.
func pruneResource(
	cacheAPI api.CacheAPI,
	cmd *cobra.Command,
	resource string,
	dryRun bool,
	allResources bool,
	force bool,
) error {
	if !force && !dryRun {
		common.Cli.Warning(fmt.Sprintf("This will remove cached data for all %s", resourceDisplayNamePlural(resource)))
		common.Cli.Info("")
		confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Continue?", true)
		if pErr != nil {
			return pErr
		}
		if !confirmed {
			return nil
		}
	}

	var removed []string

	switch resource {
	case "image":
		ids, err := cacheAPI.CachePruneImages(cmd.Context(), dryRun, allResources)
		if err != nil {
			return err
		}
		removed = ids
	case "binary":
		ids, err := cacheAPI.CachePruneBinaries(cmd.Context(), dryRun, allResources)
		if err != nil {
			return err
		}
		removed = ids
	default:
		switch resource {
		case "vm":
			opResult := cacheAPI.CachePruneVMs(cmd.Context(), dryRun, allResources)
			if opResult == nil {
				return nil
			}
			if opResult.IsError() {
				return opResult.ToError()
			}
			if item, ok := opResult.Item.([]string); ok {
				removed = item
			}
		case "network":
			ids, err := cacheAPI.CachePruneNetworks(cmd.Context(), dryRun, allResources)
			if err != nil {
				return err
			}
			removed = ids
		case "kernel":
			ids, err := cacheAPI.CachePruneKernels(cmd.Context(), dryRun, allResources)
			if err != nil {
				return err
			}
			removed = ids
		}
	}

	if len(removed) > 0 {
		if dryRun {
			if resource == "binary" {
				common.Cli.Info(
					fmt.Sprintf("[DRY RUN] Would prune %d binaries: %s", len(removed), strings.Join(removed, ", ")),
				)
			} else {
				displayName := resourceDisplayName(resource)
				common.Cli.Info(
					fmt.Sprintf(
						"[DRY RUN] Would prune %d %s(s): %s",
						len(removed),
						displayName,
						strings.Join(removed, ", "),
					),
				)
			}
		} else {
			common.Cli.Success(fmt.Sprintf("Pruned: %s", strings.Join(removed, ", ")))
		}
	} else {
		plural := resourceDisplayNamePlural(resource)
		common.Cli.Info(fmt.Sprintf("No %s to prune", plural))
	}

	return nil
}

func pruneMisc(cacheAPI api.CacheAPI, cmd *cobra.Command, dryRun bool, force bool) error {
	if !force && !dryRun {
		common.Cli.Warning("This will remove cached data (appliance folder, warm images)")
		common.Cli.Info("")
		confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Continue?", true)
		if pErr != nil {
			return pErr
		}
		if !confirmed {
			return nil
		}
	}

	miscMap, err := cacheAPI.CachePruneMisc(cmd.Context(), dryRun)
	if err != nil {
		return err
	}

	if miscMap == nil {
		common.Cli.Info("No misc cache to prune")
		return nil
	}

	applianceRemoved, _ := miscMap["appliance"].(bool)
	warmRemoved, _ := miscMap["warm_images"].(bool)

	if applianceRemoved {
		if dryRun {
			common.Cli.Info("[DRY RUN] Would remove appliance folder")
		} else {
			common.Cli.Success("Removed: appliance folder")
		}
	}

	if warmRemoved {
		if dryRun {
			common.Cli.Info("[DRY RUN] Would remove warm images (ready pool)")
		} else {
			common.Cli.Success("Removed: warm images (ready pool)")
		}
	}

	if !applianceRemoved && !warmRemoved {
		common.Cli.Info("No misc cache to prune")
	}

	return nil
}

func pruneAll(cacheAPI api.CacheAPI, cmd *cobra.Command, dryRun bool, force bool) error {
	if dryRun {
		common.Cli.Info("[DRY RUN] The following would be removed:")
		common.Cli.Info("  - ALL VMs (including RUNNING and STARTING)")
		common.Cli.Info("  - ALL networks (including default)")
		common.Cli.Info("  - ALL images (including default)")
		common.Cli.Info("  - ALL kernels (including default)")
		common.Cli.Info("  - ALL binaries (including default)")
		common.Cli.Info("  - Appliance folder (libguestfs cache)")
		common.Cli.Info("  - Warm images (tmpfs ready pool)")
	} else if !force {
		common.Cli.Warning("This will remove ALL cache resources INCLUDING protected items:")
		common.Cli.Info("  - ALL VMs (including RUNNING and STARTING)")
		common.Cli.Info("  - ALL networks (including default)")
		common.Cli.Info("  - ALL images (including default)")
		common.Cli.Info("  - ALL kernels (including default)")
		common.Cli.Info("  - ALL binaries (including default)")
		common.Cli.Info("  - Appliance folder (libguestfs cache)")
		common.Cli.Info("  - Warm images (tmpfs ready pool)")
		common.Cli.Info("")
		confirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Continue?", true)
		if pErr != nil {
			return pErr
		}
		if !confirmed {
			common.Cli.Info("Aborted")
			return nil
		}
	}

	pruneItem, err := cacheAPI.CachePruneAll(cmd.Context(), dryRun, true)
	if err != nil {
		return err
	}
	if pruneItem != nil {
		if len(pruneItem.PrunedIDs) > 0 {
			if dryRun {
				common.Cli.Info(fmt.Sprintf("[DRY RUN] Would prune %d item(s)", len(pruneItem.PrunedIDs)))
			} else {
				common.Cli.Success("Pruned")
			}
		}

		if len(pruneItem.FailedIDs) > 0 {
			common.Cli.Warning(
				fmt.Sprintf(
					"Failed to prune %d item(s): %s",
					len(pruneItem.FailedIDs),
					strings.Join(pruneItem.FailedIDs, ", "),
				),
			)
		}

		if pruneItem.HadRunningVMs {
			common.Cli.Info("Note: running or starting VMs were present during prune")
		}
	}

	return nil
}

func newCachePruneCmd(cacheAPI api.CacheAPI) *cobra.Command {
	var allResources bool
	var dryRun bool
	var force bool

	cmd := &cobra.Command{
		Use:               "prune [resource]",
		Short:             "Prune cache resources",
		ValidArgsFunction: completeCacheResources,
		Long: `Prune cache resources.

Default behavior prunes all items EXCEPT:
- RUNNING or STARTING VMs
- Default network and networks referenced by VMs
- Default image and images used by VMs
- Default kernel and kernels used by VMs
- Default Firecracker binary version

Use --all to remove everything including protected items.

Examples:
  mvm cache prune vm                     # Prune non-running VMs
  mvm cache prune vm --all               # Remove ALL VMs including running
  mvm cache prune image --all            # Remove ALL images including protected
  mvm cache prune network                # Prune unused networks only
  mvm cache prune misc                   # Remove appliance + warm images
  mvm cache prune --all                  # Prune ALL resources including protected
  mvm cache prune --all --force          # Prune all without confirmation`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var resource string
			if len(args) > 0 {
				resource = args[0]
			}

			switch resource {
			case "vm":
				return pruneResource(cacheAPI, cmd, "vm", dryRun, allResources, force)
			case "network":
				return pruneResource(cacheAPI, cmd, "network", dryRun, allResources, force)
			case "image":
				return pruneResource(cacheAPI, cmd, "image", dryRun, allResources, force)
			case "kernel":
				return pruneResource(cacheAPI, cmd, "kernel", dryRun, allResources, force)
			case "binary":
				return pruneResource(cacheAPI, cmd, "binary", dryRun, allResources, force)
			case "misc":
				return pruneMisc(cacheAPI, cmd, dryRun, force)
			case "":
				if allResources {
					return pruneAll(cacheAPI, cmd, dryRun, force)
				}
				common.Cli.Error("No resource specified. Use --all to prune all resource types.")
				common.Cli.Info("Valid resources: vm, network, image, kernel, binary, misc")
				common.Cli.Info("Or use: mvm cache prune --all  # Prune all types")
				return fmt.Errorf("no resource specified")
			default:
				// Unknown resource with --all should prune all resources.
				if allResources {
					return pruneAll(cacheAPI, cmd, dryRun, force)
				}
				common.Cli.Error(fmt.Sprintf("Unknown resource: %s", resource))
				common.Cli.Info("Valid resources: vm, network, image, kernel, binary, misc")
				common.Cli.Info("Or use: mvm cache prune --all  # Prune all types including protected")
				return fmt.Errorf("unknown resource: %s", resource)
			}
		},
	}

	cmd.Flags().BoolVarP(&allResources, "all", "a", false,
		"Remove ALL items including running VMs, default network, protected assets.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Show what would be removed without actually removing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompts")
	return cmd
}

// runCacheCleanWithSudo re-invokes "mvm cache clean" with sudo.
func runCacheCleanWithSudo(ctx context.Context) error {
	mvmBin, err := exec.LookPath(infra.CLIName)
	if err != nil {
		mvmBin, err = os.Executable()
		if err != nil {
			mvmBin = infra.CLIName
		}
	}

	common.Cli.Info("")
	common.Cli.Info("Running cache clean with sudo...")
	result, err := system.DefaultRunner.Run(ctx, []string{"sudo", mvmBin, "cache", "clean"}, system.RunCmdOpts{
		Capture: false,
		Check:   false,
	})
	if err != nil || !result.Success() {
		return fmt.Errorf("cache clean with sudo failed (exit %d)", result.ExitCode)
	}
	return nil
}

func newCacheCleanCmd(cacheAPI api.CacheAPI) *cobra.Command {
	var dryRun bool
	var force bool

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Completely clean all cache — prune everything, host clean, remove cache dir",
		Long: `Completely clean all cache — prune everything, host clean, remove cache dir.

This is the "nuclear option" for cache cleanup. It:
1. Prunes ALL resources (VMs, networks, images, kernels, binaries, misc)
2. Cleans host networking (TAPs, bridges, iptables chains)
3. Removes the entire cache directory at ~/.cache/mvmctl

Examples:
  mvm cache clean                # Clean all cache (with confirmation)
  mvm cache clean --dry-run      # Preview what would be removed
  mvm cache clean --force        # Clean without confirmation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check privileges early — before the destructive confirmation.
			if err := cacheAPI.CacheCheckPrivileges("/usr/sbin/ip", "clean cache"); err != nil {
				if cacheAPI.CacheSessionHasGroup() {
					// Group is active but something else is wrong (missing binary, etc.)
					// Return the original error — let Cobra's root error handler display it.
					return err
				}
				// Session doesn't have the group — offer sudo
				sudoConfirmed, pErr := common.Cli.PromptConfirm(
					cmd.Context(),
					"Elevated privileges required. Run with sudo instead?",
					true,
				)
				if pErr != nil {
					return pErr
				}
				if !sudoConfirmed {
					common.Cli.Info("Aborted")
					return nil
				}
				return runCacheCleanWithSudo(cmd.Context())
			}

			if dryRun {
				common.Cli.Info("[DRY RUN] The following would be removed:")
				common.Cli.Info("  - ALL VMs (including RUNNING and STARTING)")
				common.Cli.Info("  - ALL networks (including default)")
				common.Cli.Info("  - ALL images (including default)")
				common.Cli.Info("  - ALL kernels (including default)")
				common.Cli.Info("  - ALL binaries (including default)")
				common.Cli.Info("  - Appliance folder (libguestfs cache)")
				common.Cli.Info("  - Warm images (tmpfs ready pool)")
				common.Cli.Info("  - Host networking (TAPs, bridges, iptables chains)")
				common.Cli.Info("  - Entire cache directory (~/.cache/mvmctl)")
			} else if !force {
				common.Cli.Warning("This will COMPLETELY remove ALL cache data INCLUDING:")
				common.Cli.Info("  - ALL VMs (including RUNNING and STARTING)")
				common.Cli.Info("  - ALL networks (including default)")
				common.Cli.Info("  - ALL images (including default)")
				common.Cli.Info("  - ALL kernels (including default)")
				common.Cli.Info("  - ALL binaries (including default)")
				common.Cli.Info("  - Appliance folder (libguestfs cache)")
				common.Cli.Info("  - Warm images (tmpfs ready pool)")
				common.Cli.Info("  - Host networking (TAPs, bridges, iptables chains)")
				common.Cli.Info("  - Entire cache directory (~/.cache/mvmctl)")
				common.Cli.Info("")
				cleanConfirmed, pErr := common.Cli.PromptConfirm(cmd.Context(), "Continue?", true)
				if pErr != nil {
					return pErr
				}
				if !cleanConfirmed {
					return nil
				}
			}

			cleanResult, err := cacheAPI.CacheClean(cmd.Context(), dryRun)
			if err != nil {
				return err
			}
			if cleanResult != nil {
				prune := cleanResult.PruneResult

				if len(prune.PrunedIDs) > 0 {
					if dryRun {
						common.Cli.Info(fmt.Sprintf("[DRY RUN] Would prune %d item(s)", len(prune.PrunedIDs)))
					} else {
						common.Cli.Success("Pruned")
					}
				}

				if len(prune.FailedIDs) > 0 {
					common.Cli.Warning(
						fmt.Sprintf(
							"Failed to prune %d item(s): %s",
							len(prune.FailedIDs),
							strings.Join(prune.FailedIDs, ", "),
						),
					)
				}

				if prune.HadRunningVMs {
					common.Cli.Info("Note: running or starting VMs were present during clean")
				}

				if cleanResult.CacheDirRemoved {
					if dryRun {
						common.Cli.Info(fmt.Sprintf("[DRY RUN] Would remove cache directory: %s", cleanResult.CacheDir))
					} else {
						common.Cli.Success(fmt.Sprintf("Removed: %s", cleanResult.CacheDir))
					}
				} else {
					common.Cli.Info("Cache directory was already empty")
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without actually removing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompts")
	return cmd
}
