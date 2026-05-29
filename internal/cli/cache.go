// Package cli — cache management commands, matching Python's cli/cache.py
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api"
)

// NewCacheCmd creates the cache command and its subcommands.
func NewCacheCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Cache management",
	}

	cmd.AddCommand(newCacheInitCmd(op))
	cmd.AddCommand(newCachePruneCmd(op))
	cmd.AddCommand(newCacheCleanCmd(op))
	cmd.AddCommand(newCacheHelpCmd())

	return cmd
}

func newCacheInitCmd(op *api.Operation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize all cache resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Match Python: uses Rich Console spinner with on_progress callback.
			// Python: console.status("", spinner="dots") with _on_progress(event) that
			// calls status.update(f"[dim]{event.message}[/dim]") to update in-place.
			spinner := common.NewSpinner("")
			spinner.Start()

			result := op.CacheInitAll(cmd.Context(), func(event errs.ProgressEvent) {
				if event.Message != "" {
					spinner.UpdateText(event.Message)
				}
			})

			spinner.Stop()

			if result.IsError() {
				common.Cli.Error(result.Message)
				return fmt.Errorf("cache init failed: %s", result.Message)
			}

			// Match Python: mvm_cli.success(operation_result.message)
			common.Cli.Success(result.Message)
			// Match Python: for resource, path in item.items(): if path: mvm_cli.info(f"  {resource}: {path}")
			if result.Item != nil {
				if item, ok := result.Item.(map[string]interface{}); ok {
					for resource, path := range item {
						if path != nil && path != "" {
							common.Cli.Info(fmt.Sprintf("  %s: %v", resource, path))
						}
					}
				}
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
// Matches Python's resource-specific blocks exactly.
func pruneResource(op *api.Operation, cmd *cobra.Command, resource string, dryRun bool, allResources bool, force bool) error {
	if !force && !dryRun {
		// Match Python: mvm_cli.warning("This will remove cached data for all VMs")
		common.Cli.Warning(fmt.Sprintf("This will remove cached data for all %s", resourceDisplayNamePlural(resource)))
		common.Cli.Info("")
		if !promptConfirm("Continue?", true) {
			return nil
		}
	}

	var opResult *errs.OperationResult

	switch resource {
	case "vm":
		opResult = op.CachePruneVMs(cmd.Context(), dryRun, allResources)
	case "network":
		opResult = op.CachePruneNetworks(cmd.Context(), dryRun, allResources)
	case "image":
		opResult = op.CachePruneImages(cmd.Context(), dryRun, allResources)
	case "kernel":
		opResult = op.CachePruneKernels(cmd.Context(), dryRun, allResources)
	case "binary":
		opResult = op.CachePruneBinaries(cmd.Context(), dryRun, allResources)
	}

	if opResult == nil {
		return nil
	}
	if opResult.IsError() {
		common.Cli.Error(opResult.Message)
		return fmt.Errorf("prune %s failed: %s", resource, opResult.Message)
	}

	var removed []string
	if item, ok := opResult.Item.([]string); ok {
		removed = item
	}

	if len(removed) > 0 {
		if dryRun {
			// Match Python: mvm_cli.info(f"[DRY RUN] Would prune {len(removed)} VM(s): {', '.join(removed)}")
			if resource == "binary" {
				// Python: f"[DRY RUN] Would prune {len(removed)} binaries: {', '.join(removed)}"
				common.Cli.Info(fmt.Sprintf("[DRY RUN] Would prune %d binaries: %s", len(removed), strings.Join(removed, ", ")))
			} else {
				displayName := resourceDisplayName(resource)
				common.Cli.Info(fmt.Sprintf("[DRY RUN] Would prune %d %s(s): %s", len(removed), displayName, strings.Join(removed, ", ")))
			}
		} else {
			// Match Python: mvm_cli.success(f"Pruned: {', '.join(removed)}")
			common.Cli.Success(fmt.Sprintf("Pruned: %s", strings.Join(removed, ", ")))
		}
	} else {
		// Match Python: lowercase, e.g. mvm_cli.info("No binaries to prune") — NOT title-case
		plural := resourceDisplayNamePlural(resource)
		common.Cli.Info(fmt.Sprintf("No %s to prune", plural))
	}

	return nil
}

func pruneMisc(op *api.Operation, cmd *cobra.Command, dryRun bool, force bool) error {
	if !force && !dryRun {
		// Match Python: mvm_cli.warning("This will remove cached data (appliance folder, warm images)")
		common.Cli.Warning("This will remove cached data (appliance folder, warm images)")
		common.Cli.Info("")
		if !promptConfirm("Continue?", true) {
			return nil
		}
	}

	miscResult := op.CachePruneMisc(cmd.Context(), dryRun)
	if miscResult.IsError() {
		common.Cli.Error(miscResult.Message)
		return fmt.Errorf("prune misc failed: %s", miscResult.Message)
	}

	if miscResult.Item == nil {
		common.Cli.Info("No misc cache to prune")
		return nil
	}

	miscMap, ok := miscResult.Item.(map[string]interface{})
	if !ok {
		common.Cli.Info("No misc cache to prune")
		return nil
	}

	// Match Python: if misc_result.get("appliance")
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

func pruneAll(op *api.Operation, cmd *cobra.Command, dryRun bool, force bool) error {
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
		if !promptConfirm("Continue?", true) {
			common.Cli.Info("Aborted")
			return nil
		}
	}

	pruneOpResult := op.CachePruneAll(cmd.Context(), dryRun, true)
	if pruneOpResult.IsError() {
		common.Cli.Error(pruneOpResult.Message)
		return fmt.Errorf("prune failed: %s", pruneOpResult.Message)
	}

	// Match Python: prune_item = prune_op_result.item (type-safe from *model.PruneAllResult)
	// Python uses a typed PruneAllResult dataclass with pruned_ids, failed_ids, had_running_vms.
	pruneItem, _ := pruneOpResult.Item.(*model.PruneAllResult)
	if pruneItem != nil {
		if len(pruneItem.PrunedIDs) > 0 {
			if dryRun {
				common.Cli.Info(fmt.Sprintf("[DRY RUN] Would prune %d item(s)", len(pruneItem.PrunedIDs)))
			} else {
				common.Cli.Success("Pruned")
			}
		}

		if len(pruneItem.FailedIDs) > 0 {
			common.Cli.Warning(fmt.Sprintf("Failed to prune %d item(s): %s", len(pruneItem.FailedIDs), strings.Join(pruneItem.FailedIDs, ", ")))
		}

		if pruneItem.HadRunningVMs {
			common.Cli.Info("Note: running or starting VMs were present during prune")
		}
	}

	return nil
}

// promptConfirm asks a yes/no question. Returns true for yes.
// Matches Python's typer.confirm(text, default=True).
// Python shows "[Y/n]: " for default=True and "[y/N]: " for default=False (with colon space).
func promptConfirm(prompt string, defaultYes bool) bool {
	suffix := " [Y/n]: "
	if !defaultYes {
		suffix = " [y/N]: "
	}
	fmt.Fprintf(os.Stderr, "%s%s", prompt, suffix)

	var response string
	_, err := fmt.Scanln(&response)
	if err != nil {
		return defaultYes
	}
	response = strings.TrimSpace(strings.ToLower(response))

	if response == "" {
		return defaultYes
	}
	return response == "y" || response == "yes"
}

func newCachePruneCmd(op *api.Operation) *cobra.Command {
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
				return pruneResource(op, cmd, "vm", dryRun, allResources, force)
			case "network":
				return pruneResource(op, cmd, "network", dryRun, allResources, force)
			case "image":
				return pruneResource(op, cmd, "image", dryRun, allResources, force)
			case "kernel":
				return pruneResource(op, cmd, "kernel", dryRun, allResources, force)
			case "binary":
				return pruneResource(op, cmd, "binary", dryRun, allResources, force)
			case "misc":
				return pruneMisc(op, cmd, dryRun, force)
			case "":
				if allResources {
					return pruneAll(op, cmd, dryRun, force)
				}
				// Match Python: mvm_cli.error("No resource specified. Use --all to prune all resource types.")
				common.Cli.Error("No resource specified. Use --all to prune all resource types.")
				common.Cli.Info("Valid resources: vm, network, image, kernel, binary, misc")
				common.Cli.Info("Or use: mvm cache prune --all  # Prune all types")
				return fmt.Errorf("no resource specified")
		default:
			// Python: elif resource is None or all_resources: — unknown resource with
			// --all should prune all, matching Python behavior.
			if allResources {
				return pruneAll(op, cmd, dryRun, force)
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
// Matches Python: shutil.which(CLI_NAME) or sys.argv[0]; run_cmd(["sudo", mvm_bin, "cache", "clean"], ...)
func runCacheCleanWithSudo(ctx context.Context) error {
	// Match Python: shutil.which(CLI_NAME) or sys.argv[0]
	mvmBin, err := exec.LookPath(infra.CLIName)
	if err != nil {
		mvmBin, err = os.Executable()
		if err != nil {
			mvmBin = infra.CLIName
		}
	}

	common.Cli.Info("")
	common.Cli.Info("Running cache clean with sudo...")
	result := system.RunCmdCompat(ctx, []string{"sudo", mvmBin, "cache", "clean"}, system.RunCmdOptions{
		Capture: false,
		Check:   false,
	})
	if !result.Success {
		return fmt.Errorf("cache clean with sudo failed (exit %d)", result.ExitCode)
	}
	return nil
}

func newCacheCleanCmd(op *api.Operation) *cobra.Command {
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
			// Match Python: Check privileges early — before the destructive confirmation.
			// Python: HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "clean cache")
			if err := op.CacheCheckPrivileges("/usr/sbin/ip", "clean cache"); err != nil {
				if op.CacheSessionHasGroup() {
					// Group is active but something else is wrong (missing binary, etc.)
					// Match Python: raise — re-raise the original exception without printing,
					// let Cobra's root error handler display it (avoids double-printing).
					return err
				}
				// Session doesn't have the group — offer sudo
				// Match Python: single confirm prompt: "Elevated privileges required. Run with sudo instead?"
				if !promptConfirm("Elevated privileges required. Run with sudo instead?", true) {
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
				if !promptConfirm("Continue?", true) {
					return nil
				}
			}

			opResult := op.CacheClean(cmd.Context(), dryRun)
			if opResult.IsError() {
				common.Cli.Error(opResult.Message)
				return fmt.Errorf("clean failed: %s", opResult.Message)
			}

			// Match Python: result = op_result.item (type-safe from *model.CleanResult)
			// Python uses a typed CleanResult dataclass containing a PruneAllResult sub-object.
			cleanResult, _ := opResult.Item.(*model.CleanResult)
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
					common.Cli.Warning(fmt.Sprintf("Failed to prune %d item(s): %s", len(prune.FailedIDs), strings.Join(prune.FailedIDs, ", ")))
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
			// Match Python: no fallback output when result is nil (no "Cache cleaned" message)

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without actually removing")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompts")
	return cmd
}

// newCacheHelpCmd creates a hidden "help" subcommand for the cache group.
// Matches Python: @cache_app.command(name="help", hidden=True) that prints parent help.
func newCacheHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "help",
		Short:  "Show help for the cache command group",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Parent().Help()
		},
	}
}
