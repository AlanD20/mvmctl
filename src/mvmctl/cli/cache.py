"""Cache management commands."""

from __future__ import annotations

from typing import Optional

import typer

from mvmctl.api.cache_operations import CacheOperation
from mvmctl.utils.console import (
    print_error,
    print_info,
    print_success,
    print_warning,
)

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
def cache_init() -> None:
    """Initialize all cache resources."""
    try:
        result = CacheOperation.init_all()
        print_success("Cache initialized successfully")
        for resource, path in result.items():
            if path:
                print_info(f"  {resource}: {path}")
    except Exception as e:
        print_error(f"Failed to initialize cache: {e}")
        raise typer.Exit(code=1)


@cache_app.command(name="prune")
def cache_prune(
    resource: Optional[str] = typer.Argument(
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
    """Prune cache resources.

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
        try:
            removed = CacheOperation.prune_vms(
                dry_run=dry_run, include_all=all_resources
            )
            if removed:
                print_success(
                    f"Pruned {len(removed)} VM(s): {', '.join(removed)}"
                )
            else:
                print_info("No VMs to prune")
        except Exception as e:
            print_error(f"Failed to prune VMs: {e}")
            raise typer.Exit(code=1)

    elif resource == "network":
        try:
            removed = CacheOperation.prune_networks(
                dry_run=dry_run, include_all=all_resources
            )
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
        except Exception as e:
            print_error(f"Failed to prune networks: {e}")
            raise typer.Exit(code=1)

    elif resource == "image":
        try:
            removed = CacheOperation.prune_images(
                dry_run=dry_run, include_all=all_resources
            )
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
        except Exception as e:
            print_error(f"Failed to prune images: {e}")
            raise typer.Exit(code=1)

    elif resource == "kernel":
        try:
            removed = CacheOperation.prune_kernels(
                dry_run=dry_run, include_all=all_resources
            )
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
        except Exception as e:
            print_error(f"Failed to prune kernels: {e}")
            raise typer.Exit(code=1)

    elif resource == "binary":
        try:
            removed = CacheOperation.prune_binaries(
                dry_run=dry_run, include_all=all_resources
            )
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
        except Exception as e:
            print_error(f"Failed to prune binaries: {e}")
            raise typer.Exit(code=1)

    elif resource == "misc":
        try:
            misc_result = CacheOperation.prune_misc(dry_run=dry_run)
            if misc_result.get("appliance"):
                if dry_run:
                    print_info("[DRY RUN] Would remove appliance folder")
                else:
                    print_success("Removed appliance folder")
            if misc_result.get("warm_images"):
                if dry_run:
                    print_info(
                        "[DRY RUN] Would remove warm images (ready pool)"
                    )
                else:
                    print_success("Removed warm images (ready pool)")
            if not misc_result.get("appliance") and not misc_result.get(
                "warm_images"
            ):
                print_info("No misc cache to prune")
        except Exception as e:
            print_error(f"Failed to prune misc cache: {e}")
            raise typer.Exit(code=1)

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
        elif not force:
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
            if not typer.confirm("Continue?"):
                print_info("Aborted")
                raise typer.Exit()

        try:
            result = CacheOperation.prune_all(dry_run=dry_run, include_all=True)

            vms_result: list[str] | bool = result.get("vms", [])
            if isinstance(vms_result, list) and vms_result:
                if dry_run:
                    print_info(f"[DRY RUN] Would prune {len(vms_result)} VM(s)")
                else:
                    print_success(f"Pruned {len(vms_result)} VM(s)")
            networks_result: list[str] | bool = result.get("networks", [])
            if isinstance(networks_result, list) and networks_result:
                if dry_run:
                    print_info(
                        f"[DRY RUN] Would prune {len(networks_result)} network(s)"
                    )
                else:
                    print_success(f"Pruned {len(networks_result)} network(s)")
            images_result: list[str] | bool = result.get("images", [])
            if isinstance(images_result, list) and images_result:
                if dry_run:
                    print_info(
                        f"[DRY RUN] Would prune {len(images_result)} image(s)"
                    )
                else:
                    print_success(f"Pruned {len(images_result)} image(s)")
            kernels_result: list[str] | bool = result.get("kernels", [])
            if isinstance(kernels_result, list) and kernels_result:
                if dry_run:
                    print_info(
                        f"[DRY RUN] Would prune {len(kernels_result)} kernel(s)"
                    )
                else:
                    print_success(f"Pruned {len(kernels_result)} kernel(s)")
            binaries_result: list[str] | bool = result.get("binaries", [])
            if isinstance(binaries_result, list) and binaries_result:
                if dry_run:
                    print_info(
                        f"[DRY RUN] Would prune {len(binaries_result)} binary(s)"
                    )
                else:
                    print_success(f"Pruned {len(binaries_result)} binary(s)")
            appliance_pruned = result.get("appliance", False)
            if appliance_pruned:
                if dry_run:
                    print_info("[DRY RUN] Would remove appliance folder")
                else:
                    print_success("Removed appliance folder")
            warm_images_pruned = result.get("warm_images", False)
            if warm_images_pruned:
                if dry_run:
                    print_info(
                        "[DRY RUN] Would remove warm images (ready pool)"
                    )
                else:
                    print_success("Removed warm images (ready pool)")

        except Exception as e:
            print_error(f"Failed to prune cache: {e}")
            raise typer.Exit(code=1)

    print_error(f"Unknown resource: {resource}")
    print_info("Valid resources: vm, network, image, kernel, binary")
    print_info(
        "Or use: mvm cache prune --all  # Prune all types including protected"
    )
    raise typer.Exit(code=1)
