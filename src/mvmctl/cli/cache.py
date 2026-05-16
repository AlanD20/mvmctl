"""Cache management commands."""

from __future__ import annotations

from typing import TYPE_CHECKING

import typer
from rich.console import Console

from mvmctl.api import CacheOperation as _CacheOperation

if TYPE_CHECKING:
    from mvmctl.api.cache_operations import CacheOperation
else:
    CacheOperation = _CacheOperation

from mvmctl.models.result import ProgressEvent
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_success,
    print_warning,
)
from mvmctl.utils.cli import handle_errors

cache_app = typer.Typer(
    help="Cache management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@cache_app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the cache command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@cache_app.command(name="init")
@handle_errors
def cache_init() -> None:
    """Initialize all cache resources."""
    console = Console()
    with console.status("", spinner="dots") as status:

        def _on_progress(event: ProgressEvent) -> None:
            if event.message:
                status.update(f"[dim]{event.message}[/dim]")

        operation_result = CacheOperation.init_all(on_progress=_on_progress)
    if operation_result.is_error:
        print_error(operation_result.message)
        raise typer.Exit(code=1)
    print_success(operation_result.message)
    item = operation_result.item or {}
    for resource, path in item.items():
        if path:
            print_info(f"  {resource}: {path}")


@cache_app.command(name="prune")
@handle_errors
def cache_prune(
    resource: str | None = typer.Argument(
        None,
        help=(
            "Resource to prune: vm, network, image, kernel, binary, misc. "
            "Omit to prune all types."
        ),
    ),
    all_resources: bool = typer.Option(
        False,
        "--all",
        "-a",
        help="Remove ALL items including running VMs, default network, protected assets.",
    ),
    dry_run: bool = typer.Option(
        False,
        "--dry-run",
        help="Show what would be removed without actually removing",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Skip confirmation prompts"
    ),
) -> None:
    """
    Prune cache resources.

    Default behavior prunes all items EXCEPT:
    - RUNNING or STARTING VMs
    - Default network and networks referenced by VMs
    - Default image and images used by VMs
    - Default kernel and kernels used by VMs
    - Default Firecracker binary version

    Use ``--all`` to remove everything including protected items.

    Examples:
        mvm cache prune vm                     # Prune non-running VMs
        mvm cache prune vm --all               # Remove ALL VMs including running
        mvm cache prune image --all            # Remove ALL images including protected
        mvm cache prune network                # Prune unused networks only
        mvm cache prune misc                   # Remove appliance + warm images
        mvm cache prune --all                  # Prune ALL resources including protected
        mvm cache prune --all --force          # Prune all without confirmation

    """
    if resource == "vm":
        if not force and not dry_run:
            print_warning("This will remove cached data for all VMs")
            print_info("")
            if not typer.confirm("Continue?", default=True):
                raise typer.Exit()
        result = CacheOperation.prune_vms(
            dry_run=dry_run, include_all=all_resources
        )
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        removed = result.item or []
        if removed:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(removed)} VM(s): {', '.join(removed)}"
                )
            else:
                print_success(
                    f"Pruned {len(removed)} VM(s): {', '.join(removed)}"
                )
        else:
            print_info("No VMs to prune")

    elif resource == "network":
        if not force and not dry_run:
            print_warning("This will remove cached data for all networks")
            print_info("")
            if not typer.confirm("Continue?", default=True):
                raise typer.Exit()
        result = CacheOperation.prune_networks(
            dry_run=dry_run, include_all=all_resources
        )
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        removed = result.item or []
        if removed:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(removed)} network(s): {', '.join(removed)}"
                )
            else:
                print_success(
                    f"Pruned {len(removed)} network(s): {', '.join(removed)}"
                )
        else:
            print_info("No networks to prune")

    elif resource == "image":
        if not force and not dry_run:
            print_warning("This will remove cached data for all images")
            print_info("")
            if not typer.confirm("Continue?", default=True):
                raise typer.Exit()
        result = CacheOperation.prune_images(
            dry_run=dry_run, include_all=all_resources
        )
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        removed = result.item or []
        if removed:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(removed)} image(s): {', '.join(removed)}"
                )
            else:
                print_success(
                    f"Pruned {len(removed)} image(s): {', '.join(removed)}"
                )
        else:
            print_info("No images to prune")

    elif resource == "kernel":
        if not force and not dry_run:
            print_warning("This will remove cached data for all kernels")
            print_info("")
            if not typer.confirm("Continue?", default=True):
                raise typer.Exit()
        result = CacheOperation.prune_kernels(
            dry_run=dry_run, include_all=all_resources
        )
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        removed = result.item or []
        if removed:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(removed)} kernel(s): {', '.join(removed)}"
                )
            else:
                print_success(
                    f"Pruned {len(removed)} kernel(s): {', '.join(removed)}"
                )
        else:
            print_info("No kernels to prune")

    elif resource == "binary":
        if not force and not dry_run:
            print_warning("This will remove cached data for all binaries")
            print_info("")
            if not typer.confirm("Continue?", default=True):
                raise typer.Exit()
        result = CacheOperation.prune_binaries(
            dry_run=dry_run, include_all=all_resources
        )
        if result.is_error:
            print_error(result.message)
            raise typer.Exit(code=1)
        removed = result.item or []
        if removed:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(removed)} binary(s): {', '.join(removed)}"
                )
            else:
                print_success(
                    f"Pruned {len(removed)} binary(s): {', '.join(removed)}"
                )
        else:
            print_info("No binaries to prune")

    elif resource == "misc":
        if not force and not dry_run:
            print_warning(
                "This will remove cached data (appliance folder, warm images)"
            )
            print_info("")
            if not typer.confirm("Continue?", default=True):
                raise typer.Exit()
        misc_op_result = CacheOperation.prune_misc(dry_run=dry_run)
        if misc_op_result.is_error:
            print_error(misc_op_result.message)
            raise typer.Exit(code=1)
        misc_result = misc_op_result.item or {}
        if misc_result.get("appliance"):
            if dry_run:
                print_info("[DRY RUN] Would remove appliance folder")
            else:
                print_success("Removed appliance folder")
        if misc_result.get("warm_images"):
            if dry_run:
                print_info("[DRY RUN] Would remove warm images (ready pool)")
            else:
                print_success("Removed warm images (ready pool)")
        if not misc_result.get("appliance") and not misc_result.get(
            "warm_images"
        ):
            print_info("No misc cache to prune")

    elif resource is None or all_resources:
        if not all_resources:
            print_error(
                "No resource specified. Use --all to prune all resource types."
            )
            print_info(
                "Valid resources: vm, network, image, kernel, binary, misc"
            )
            print_info("Or use: mvm cache prune --all  # Prune all types")
            raise typer.Exit(code=1)

        if dry_run:
            print_info("[DRY RUN] The following would be removed:")
            print_info("  - ALL VMs (including RUNNING and STARTING)")
            print_info("  - ALL networks (including default)")
            print_info("  - ALL images (including default)")
            print_info("  - ALL kernels (including default)")
            print_info("  - ALL binaries (including default)")
            print_info("  - Appliance folder (libguestfs cache)")
            print_info("  - Warm images (tmpfs ready pool)")
        elif not force and not dry_run:
            print_warning(
                "This will remove ALL cache resources INCLUDING protected items:"
            )
            print_info("  - ALL VMs (including RUNNING and STARTING)")
            print_info("  - ALL networks (including default)")
            print_info("  - ALL images (including default)")
            print_info("  - ALL kernels (including default)")
            print_info("  - ALL binaries (including default)")
            print_info("  - Appliance folder (libguestfs cache)")
            print_info("  - Warm images (tmpfs ready pool)")
            print_info("")
            if not typer.confirm("Continue?", default=True):
                print_info("Aborted")
                raise typer.Exit()

        prune_op_result = CacheOperation.prune_all(
            dry_run=dry_run, include_all=True
        )
        if prune_op_result.is_error:
            print_error(prune_op_result.message)
            raise typer.Exit(code=1)

        prune_item = prune_op_result.item
        if prune_item and prune_item.pruned_ids:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(prune_item.pruned_ids)} item(s)"
                )
            else:
                print_success(f"Pruned {len(prune_item.pruned_ids)} item(s)")

        if prune_item and prune_item.failed_ids:
            print_warning(
                f"Failed to prune {len(prune_item.failed_ids)} item(s): "
                f"{', '.join(prune_item.failed_ids)}"
            )

        if prune_item and prune_item.had_running_vms:
            print_info(
                "Note: running or starting VMs were present during prune"
            )

    else:
        print_error(f"Unknown resource: {resource}")
        print_info("Valid resources: vm, network, image, kernel, binary, misc")
        print_info(
            "Or use: mvm cache prune --all  # Prune all types including protected"
        )
        raise typer.Exit(code=1)


@cache_app.command(name="clean")
@handle_errors
def cache_clean(
    dry_run: bool = typer.Option(
        False,
        "--dry-run",
        help="Show what would be removed without actually removing",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Skip confirmation prompts"
    ),
) -> None:
    """Completely clean all cache — prune everything, host clean, remove cache dir.

    This is the "nuclear option" for cache cleanup. It:
    1. Prunes ALL resources (VMs, networks, images, kernels, binaries, misc)
    2. Cleans host networking (TAPs, bridges, iptables chains)
    3. Removes the entire cache directory at ~/.cache/mvmctl

    Examples:
        mvm cache clean                # Clean all cache (with confirmation)
        mvm cache clean --dry-run      # Preview what would be removed
        mvm cache clean --force        # Clean without confirmation
    """
    if dry_run:
        print_info("[DRY RUN] The following would be removed:")
        print_info("  - ALL VMs (including RUNNING and STARTING)")
        print_info("  - ALL networks (including default)")
        print_info("  - ALL images (including default)")
        print_info("  - ALL kernels (including default)")
        print_info("  - ALL binaries (including default)")
        print_info("  - Appliance folder (libguestfs cache)")
        print_info("  - Warm images (tmpfs ready pool)")
        print_info("  - Host networking (TAPs, bridges, iptables chains)")
        print_info("  - Entire cache directory (~/.cache/mvmctl)")
    elif not force and not dry_run:
        print_warning("This will COMPLETELY remove ALL cache data INCLUDING:")
        print_info("  - ALL VMs (including RUNNING and STARTING)")
        print_info("  - ALL networks (including default)")
        print_info("  - ALL images (including default)")
        print_info("  - ALL kernels (including default)")
        print_info("  - ALL binaries (including default)")
        print_info("  - Appliance folder (libguestfs cache)")
        print_info("  - Warm images (tmpfs ready pool)")
        print_info("  - Host networking (TAPs, bridges, iptables chains)")
        print_info("  - Entire cache directory (~/.cache/mvmctl)")
        print_info("")
        if not typer.confirm("Continue?", default=True):
            raise typer.Exit()

    op_result = CacheOperation.clean(dry_run=dry_run)
    if op_result.is_error:
        print_error(op_result.message)
        raise typer.Exit(code=1)

    result = op_result.item
    if result:
        prune = result.prune_result
        if prune.pruned_ids:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would prune {len(prune.pruned_ids)} item(s)"
                )
            else:
                print_success(f"Pruned {len(prune.pruned_ids)} item(s)")

        if prune.failed_ids:
            print_warning(
                f"Failed to prune {len(prune.failed_ids)} item(s): "
                f"{', '.join(prune.failed_ids)}"
            )

        if prune.had_running_vms:
            print_info(
                "Note: running or starting VMs were present during clean"
            )

        if result.cache_dir_removed:
            if dry_run:
                print_info(
                    f"[DRY RUN] Would remove cache directory: {result.cache_dir}"
                )
            else:
                print_success(f"Removed cache directory: {result.cache_dir}")
        else:
            print_info("Cache directory was already empty")


__all__ = ["cache_app"]
